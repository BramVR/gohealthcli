package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// TestRawEndpointGetProfileAutoRefreshesExpiredAccessToken pins the AC
// for PRD #142 slice 6 (issue #179): with an expired access token but
// valid refresh token and oauthClient.kind == "file", `raw endpoint
// getProfile` transparently refreshes the access token, persists the
// new token to the archive's token_metadata_json (exactly one
// UpdateConnectionTokenMetadata call), and writes the upstream body to
// stdout — the same writable-archive + WithAutoRefresh pattern sync and
// irn-profile already use.
func TestRawEndpointGetProfileAutoRefreshesExpiredAccessToken(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	connectAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                connectAt,
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	// Force the stored access-token expires_at into the past so
	// AccessToken must take the auto-refresh path. The profile.readonly
	// scope is part of the base set granted by connect, so no
	// addStoredConnectionScope is needed.
	setConnectionTokenExpiry(t, archivePath, "2026-01-01T00:00:00Z")

	rawNow := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	refreshedExpiresAt := rawNow.Add(time.Hour)
	testRuntime.now = func() time.Time { return rawNow }
	// Count refresh attempts so a regression where retries silently
	// double-rotate the stored token surfaces here as well.
	refreshCalls := 0
	testRuntime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		refreshCalls++
		if refreshToken != "connect-refresh-secret" {
			t.Fatalf("refresh token = %q, want connect-refresh-secret", refreshToken)
		}
		return oauthTokenResponse{
			accessToken:  "rotated-access-secret",
			refreshToken: "connect-refresh-secret",
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    refreshedExpiresAt,
			rawTokenMaterialObject: map[string]any{
				"access_token":  "rotated-access-secret",
				"refresh_token": "connect-refresh-secret",
				"token_type":    "Bearer",
				"expires_in":    float64(3600),
			},
		}, nil
	}

	const providerBody = `{"name":"users/me/profile","displayName":"Rotated User"}`
	rawCalls := 0
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		rawCalls++
		if request.url != googleHealthProfileURL {
			t.Fatalf("raw URL = %q, want profile URL", request.url)
		}
		if accessToken != "rotated-access-secret" {
			t.Fatalf("raw access token = %q, want rotated-access-secret (post-refresh)", accessToken)
		}
		return []byte(providerBody), nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"raw", "endpoint", "getProfile", "--config", configPath, "--db", archivePath}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("raw exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stdout.String() != providerBody {
		t.Fatalf("stdout = %q, want upstream body %q", stdout.String(), providerBody)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshOAuthToken call count = %d, want 1 (no retry loop should double-rotate the stored token)", refreshCalls)
	}
	if rawCalls != 1 {
		t.Fatalf("fetchRawProvider call count = %d, want 1", rawCalls)
	}

	// Refreshed expires_at must have been persisted to the archive's
	// token_metadata_json via UpdateConnectionTokenMetadata. The bare
	// readOnlyArchive handle the pre-#179 code used does not satisfy
	// connectionTokenWriter, so the refresh would persist to the
	// Credential Store but not the archive — pinning the persisted
	// expires_at here guards the writable-mode upgrade.
	gotMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if !strings.Contains(gotMetadata, refreshedExpiresAt.Format(time.RFC3339)) {
		t.Fatalf("archived token_metadata_json = %s, want refreshed expires_at %s", gotMetadata, refreshedExpiresAt.Format(time.RFC3339))
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestProfileCommandFailsFastWhenScopeMissing pins the PRD #142 slice 5
// AC: when the stored Connection's granted scopes do not cover the
// scope the profile verb requires (whatever the catalog says today),
// the command exits non-zero, sets result.Status to
// "profile_scope_missing", names the recovery `gohealthcli connect`
// command in result.Message, and crucially does NOT issue any HTTP
// request to googleHealthProfileURL — proving the scope pre-check
// happens before the upstream call. The test reads the required scope
// from the same googleHealthIdentityEndpointScopes catalog the
// production code uses so a future slice-2 revision of the catalog
// automatically updates what gets stripped from the stored Connection,
// keeping the test honest without manual edits.
func TestProfileCommandFailsFastWhenScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	// Strip every scope the catalog ties to getProfile from the
	// stored Connection so AccessToken's scope pre-check fails. Using
	// the same catalog key the production code reads means this test
	// keeps pinning the right behaviour after slice 2 rewrites the
	// catalog entry.
	required := googleHealthIdentityEndpointScopes["getProfile"]
	requiredSet := make(map[string]struct{}, len(required))
	for _, scope := range required {
		requiredSet[scope] = struct{}{}
	}
	storedScopes := connectionGrantedScopes(t, archivePath)
	filtered := storedScopes[:0]
	for _, scope := range storedScopes {
		if _, drop := requiredSet[scope]; drop {
			continue
		}
		filtered = append(filtered, scope)
	}
	setConnectionTokenScopes(t, archivePath, filtered)

	// fetchProfile MUST NOT be called when the scope is missing —
	// the bare HTTP 403 PRD #142 documents only happens because the
	// pre-check is absent, so guarding the seam is what proves the
	// migration shut that path down.
	originalFetchProfile := fetchProfile
	fetchProfile = func(string) (googleProfile, error) {
		t.Fatal("fetchProfile called despite missing scope")
		return googleProfile{}, nil
	}
	t.Cleanup(func() { fetchProfile = originalFetchProfile })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code == 0 {
		t.Fatalf("profile exit code = %d, want non-zero; stdout=%s", code, stdout.String())
	}
	var result profileResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "profile_scope_missing" {
		t.Fatalf("result.Status = %q, want profile_scope_missing", result.Status)
	}
	// The recovery hint either names `--add-scopes <keyword>` (when the
	// missing scope maps to a connectAddScopeKeywords entry) or names
	// `gohealthcli connect` again (generic fallback). Either way, the
	// message must mention `connect` — that single substring covers
	// both shapes and survives a slice-2 catalog rewrite.
	if !strings.Contains(result.Message, "gohealthcli connect") {
		t.Fatalf("result.Message = %q, want it to name `gohealthcli connect` recovery", result.Message)
	}
	keywords := addScopeKeywordsForScopes(required)
	if len(keywords) == len(required) && len(keywords) > 0 {
		wantHint := "--add-scopes " + strings.Join(keywords, ",")
		if !strings.Contains(result.Message, wantHint) {
			t.Fatalf("result.Message = %q, want it to name %q", result.Message, wantHint)
		}
	}
}

// TestProfileCommandAutoRefreshesExpiredAccessToken pins the AC for
// PRD #142 slice 5: with an expired access token but valid refresh
// token and oauthClient.kind == "file", profile refreshes
// transparently, persists the new token via UpdateConnectionTokenMetadata
// on the archive (the same handle openHealthArchiveConnectionAPI
// already returns), and exits 0 with status "profile_archived" plus
// a new identity_snapshots row whose snapshot_kind = 'profile'.
func TestProfileCommandAutoRefreshesExpiredAccessToken(t *testing.T) {
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
	// Ensure the stored Connection carries every scope the catalog
	// requires for getProfile, so this test still exercises auto-refresh
	// after slice 2 (#176) revises the catalog away from the default-granted
	// scope set.
	for _, scope := range googleHealthIdentityEndpointScopes["getProfile"] {
		addStoredConnectionScope(t, archivePath, scope)
	}
	// Force the stored access-token expires_at into the past so
	// AccessToken must take the auto-refresh path.
	setConnectionTokenExpiry(t, archivePath, "2026-01-01T00:00:00Z")

	profileNow := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	refreshedExpiresAt := profileNow.Add(time.Hour)
	testRuntime.now = func() time.Time { return profileNow }
	// Count refresh attempts so the implicit "refresh once, persist
	// once" contract is guarded against a regression where retries
	// would silently double-rotate the stored token.
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

	// profile reaches the Provider through runtime.fetchProfile (not the
	// package-var seam) so the auto-refresh path's stub MUST live on the
	// runtime adapters; setting the package var alone would leak the real
	// HTTP fetchGoogleProfile into the test.
	var calledWithToken string
	testRuntime.fetchProfile = func(accessToken string) (googleProfile, error) {
		calledWithToken = accessToken
		return googleProfile{
			healthUserID: "111111256096816351",
			rawJSON:      `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}`,
		}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("profile exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}
	if calledWithToken != "rotated-access-secret" {
		t.Fatalf("fetchProfile access token = %q, want rotated-access-secret", calledWithToken)
	}

	var result profileResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "profile_archived" {
		t.Fatalf("result.Status = %q, want profile_archived", result.Status)
	}

	// Refreshed expires_at must have been persisted to the archive's
	// token_metadata_json via UpdateConnectionTokenMetadata.
	gotMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if !strings.Contains(gotMetadata, refreshedExpiresAt.Format(time.RFC3339)) {
		t.Fatalf("archived token_metadata_json = %s, want refreshed expires_at %s", gotMetadata, refreshedExpiresAt.Format(time.RFC3339))
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshOAuthToken call count = %d, want 1 (no retry loop should double-rotate the stored token)", refreshCalls)
	}

	// A new identity_snapshots row with snapshot_kind = 'profile'
	// must exist so the auto-refresh path doesn't silently skip the
	// archive write the AC requires.
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var snapshotCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM identity_snapshots WHERE snapshot_kind = 'profile'`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count profile snapshots: %v", err)
	}
	if snapshotCount != 1 {
		t.Fatalf("profile snapshot count = %d, want 1", snapshotCount)
	}
}

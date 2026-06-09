package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCurrentConnectionAccessTokenValidatesMetadataBeforeCredentialStore(t *testing.T) {
	access := newCurrentConnectionAccess(
		credentialStoreConfig{kind: "bogus"},
		archivedConnection{
			id:                "googlehealth:111",
			tokenMetadataJSON: tokenMetadataJSON(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), []string{googleHealthProfileReadonlyScope}),
		},
		nil,
	)
	access.runtime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	_, err := access.AccessToken([]string{googleHealthProfileReadonlyScope})
	if err == nil || !strings.Contains(err.Error(), "token has expired") {
		t.Fatalf("AccessToken error = %v, want expired token before Credential Store error", err)
	}
}

func TestCurrentConnectionAccessTokenRequiresScopesBeforeCredentialStore(t *testing.T) {
	access := newCurrentConnectionAccess(
		credentialStoreConfig{kind: "bogus"},
		archivedConnection{
			id:                "googlehealth:111",
			tokenMetadataJSON: tokenMetadataJSON(t, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), []string{googleHealthActivityReadonlyScope}),
		},
		nil,
	)
	access.runtime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	_, err := access.AccessToken([]string{googleHealthProfileReadonlyScope})
	if err == nil || !strings.Contains(err.Error(), googleHealthProfileReadonlyScope) {
		t.Fatalf("AccessToken error = %v, want missing scope before Credential Store error", err)
	}
}

// TestRequireConnectionScopesAddScopesHint pins the AC for #104:
// when the required scope is one of the opt-in Tier 2 scopes
// (.ecg.readonly or .irn.readonly), the error message points the
// user at `connect --add-scopes ecg,irn` instead of the generic
// "run `gohealthcli connect` again". The keyword list is sorted so
// the message is deterministic regardless of which missing scope
// surfaces first.
func TestRequireConnectionScopesAddScopesHint(t *testing.T) {
	metadata := tokenMetadataJSON(t, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), []string{googleHealthProfileReadonlyScope})
	tests := []struct {
		name           string
		requiredScopes []string
		wantContains   string
	}{
		{
			name:           "ecg only",
			requiredScopes: []string{googleHealthEcgReadonlyScope},
			wantContains:   "run `gohealthcli connect --add-scopes ecg`",
		},
		{
			name:           "irn only",
			requiredScopes: []string{googleHealthIrnReadonlyScope},
			wantContains:   "run `gohealthcli connect --add-scopes irn`",
		},
		{
			name:           "ecg and irn",
			requiredScopes: []string{googleHealthEcgReadonlyScope, googleHealthIrnReadonlyScope},
			wantContains:   "run `gohealthcli connect --add-scopes ecg,irn`",
		},
		{
			name:           "non opt-in scope keeps generic hint",
			requiredScopes: []string{googleHealthSleepReadonlyScope},
			wantContains:   "run `gohealthcli connect` again",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireConnectionScopes(metadata, tt.requiredScopes)
			if err == nil {
				t.Fatalf("requireConnectionScopes returned nil, want missing-scope error")
			}
			if !strings.Contains(err.Error(), tt.wantContains) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantContains)
			}
		})
	}
}

// TestCurrentConnectionAccessTokenScopeMissingSentinel pins the AC for
// PRD #142 slice 1: AccessToken returns an error matching
// errors.Is(err, errCurrentConnectionScopeMissing) exactly when a
// required scope is absent from the stored Connection. Other failure
// modes (expired token, missing access token, Provider 401-style
// errors) must NOT match the sentinel so callers can switch on it to
// set per-command "<command>_scope_missing" status without false
// positives.
func TestCurrentConnectionAccessTokenScopeMissingSentinel(t *testing.T) {
	expiresFuture := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	expiresPast := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		setup          func(t *testing.T) currentConnectionAccess
		requiredScopes []string
		wantSentinel   bool
	}{
		{
			name: "missing required scope matches sentinel",
			setup: func(t *testing.T) currentConnectionAccess {
				access := newCurrentConnectionAccess(
					credentialStoreConfig{kind: "bogus"},
					archivedConnection{
						id:                "googlehealth:111",
						tokenMetadataJSON: tokenMetadataJSON(t, expiresFuture, []string{googleHealthActivityReadonlyScope}),
					},
					nil,
				)
				access.runtime.now = func() time.Time { return now }
				return access
			},
			requiredScopes: []string{googleHealthIrnReadonlyScope},
			wantSentinel:   true,
		},
		{
			name: "expired token without auto-refresh does not match sentinel",
			setup: func(t *testing.T) currentConnectionAccess {
				access := newCurrentConnectionAccess(
					credentialStoreConfig{kind: "bogus"},
					archivedConnection{
						id:                "googlehealth:111",
						tokenMetadataJSON: tokenMetadataJSON(t, expiresPast, []string{googleHealthIrnReadonlyScope}),
					},
					nil,
				)
				access.runtime.now = func() time.Time { return now }
				return access
			},
			requiredScopes: []string{googleHealthIrnReadonlyScope},
			wantSentinel:   false,
		},
		{
			name: "missing access token in credential store does not match sentinel",
			setup: func(t *testing.T) currentConnectionAccess {
				fixture := setupAutoRefreshFixture(t, map[string]any{
					"refresh_token": "stored-refresh",
					"token_type":    "Bearer",
				})
				access := newCurrentConnectionAccess(
					fixture.credentialStore,
					archivedConnection{
						id:                "googlehealth:111",
						tokenMetadataJSON: tokenMetadataJSON(t, expiresFuture, []string{googleHealthIrnReadonlyScope}),
					},
					nil,
				)
				access.runtime.now = func() time.Time { return now }
				return access
			},
			requiredScopes: []string{googleHealthIrnReadonlyScope},
			wantSentinel:   false,
		},
		{
			// Auto-refresh failure that surfaces an HTTP 401-style error
			// must NOT be wrapped in the scope-missing sentinel — Provider
			// rejections live in a different category and would otherwise
			// flip the per-command status to "<command>_scope_missing"
			// when the user actually needs to re-run `connect` (or the
			// `doctor --online` diagnose path the auto-refresh wrapper
			// names).
			name: "auto-refresh HTTP 401 failure does not match sentinel",
			setup: func(t *testing.T) currentConnectionAccess {
				fixture := setupAutoRefreshFixture(t, map[string]any{
					"access_token":  "stale-access",
					"refresh_token": "stored-refresh",
					"token_type":    "Bearer",
				})
				runtime := runtimeAdapters{}
				runtime.now = func() time.Time { return now }
				runtime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
					return oauthTokenResponse{}, errors.New("Google Health raw request failed with HTTP 401")
				}
				access := newCurrentConnectionAccessWithRuntime(
					fixture.credentialStore,
					archivedConnection{
						id:                "googlehealth:111",
						tokenMetadataJSON: tokenMetadataJSON(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), []string{googleHealthIrnReadonlyScope}),
					},
					nil,
					runtime,
				).WithAutoRefresh(oauthClientSource{kind: "file", path: fixture.oauthClientPath}, &fakeHealthArchiveConnectionAPI{})
				return access
			},
			requiredScopes: []string{googleHealthIrnReadonlyScope},
			wantSentinel:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			access := tt.setup(t)
			_, err := access.AccessToken(tt.requiredScopes)
			if err == nil {
				t.Fatalf("AccessToken returned nil, want error")
			}
			gotSentinel := errors.Is(err, errCurrentConnectionScopeMissing)
			if gotSentinel != tt.wantSentinel {
				t.Fatalf("errors.Is(err, errCurrentConnectionScopeMissing) = %v, want %v (err=%v)", gotSentinel, tt.wantSentinel, err)
			}
		})
	}
}

// TestCurrentConnectionAccessTokenScopeMissingNamesAddScopesRecovery
// pins the AC that the sentinel error's Error() still names the
// precise `connect --add-scopes <keyword>` recovery for Tier-2 scopes
// and falls back to `gohealthcli connect` for non-keyword scopes —
// preserving requireConnectionScopes's existing message verbatim.
func TestCurrentConnectionAccessTokenScopeMissingNamesAddScopesRecovery(t *testing.T) {
	expiresFuture := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		requiredScopes []string
		wantContains   string
	}{
		{
			name:           "tier-2 irn scope names add-scopes keyword",
			requiredScopes: []string{googleHealthIrnReadonlyScope},
			wantContains:   "run `gohealthcli connect --add-scopes irn`",
		},
		{
			name:           "non-keyword scope falls back to generic connect",
			requiredScopes: []string{googleHealthSleepReadonlyScope},
			wantContains:   "run `gohealthcli connect` again",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			access := newCurrentConnectionAccess(
				credentialStoreConfig{kind: "bogus"},
				archivedConnection{
					id:                "googlehealth:111",
					tokenMetadataJSON: tokenMetadataJSON(t, expiresFuture, []string{googleHealthActivityReadonlyScope}),
				},
				nil,
			)
			access.runtime.now = func() time.Time { return now }

			_, err := access.AccessToken(tt.requiredScopes)
			if err == nil {
				t.Fatalf("AccessToken returned nil, want missing-scope error")
			}
			if !errors.Is(err, errCurrentConnectionScopeMissing) {
				t.Fatalf("err = %v, want errCurrentConnectionScopeMissing match", err)
			}
			if !strings.Contains(err.Error(), tt.wantContains) {
				t.Fatalf("err.Error() = %q, want substring %q", err.Error(), tt.wantContains)
			}
		})
	}
}

func TestCurrentConnectionAccessFetchVerifiedIdentityNormalizesUnauthorized(t *testing.T) {
	runtime := productionRuntimeAdapters()
	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		return googleIdentity{}, errors.New("Google Health raw request failed with HTTP 401")
	}
	access := newCurrentConnectionAccessWithRuntime(
		credentialStoreConfig{},
		archivedConnection{googleHealthUserID: "111111256096816351"},
		nil,
		runtime,
	)

	_, err := access.FetchVerifiedIdentity("access-secret")
	if err == nil || err.Error() != "Google Health rejected stored Connection token; run `gohealthcli connect` again" {
		t.Fatalf("FetchVerifiedIdentity error = %v, want normalized unauthorized error", err)
	}
	if !errors.Is(err, errCurrentConnectionProviderUnauthorized) {
		t.Fatalf("FetchVerifiedIdentity error = %v, want current Connection provider unauthorized category", err)
	}
}

func TestCurrentConnectionAccessFetchVerifiedIdentityRejectsMismatch(t *testing.T) {
	runtime := productionRuntimeAdapters()
	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		return googleIdentity{healthUserID: "other"}, nil
	}
	access := newCurrentConnectionAccessWithRuntime(
		credentialStoreConfig{},
		archivedConnection{googleHealthUserID: "111111256096816351"},
		nil,
		runtime,
	)

	_, err := access.FetchVerifiedIdentity("access-secret")
	if err == nil || err.Error() != "Provider returned a different Google Identity; use a new archive path" {
		t.Fatalf("FetchVerifiedIdentity error = %v, want mismatch error", err)
	}
	if !isCurrentConnectionIdentityMismatch(err) {
		t.Fatalf("FetchVerifiedIdentity error = %v, want current Connection identity mismatch category", err)
	}
}

func TestCurrentConnectionAccessTokenMissingCategory(t *testing.T) {
	if !isCurrentConnectionTokenMissing(errCurrentConnectionMissingAccessToken) {
		t.Fatal("missing access token error is not categorized as token_missing")
	}
	if !isCurrentConnectionTokenMissing(errCurrentConnectionMissingRefreshToken) {
		t.Fatal("missing refresh token error is not categorized as token_missing")
	}
	if !isCurrentConnectionTokenMissing(errors.New("Credential Store token material not found; run `gohealthcli connect` first")) {
		t.Fatal("missing stored token material error is not categorized as token_missing")
	}
}

func TestCurrentConnectionAccessTokenAutoRefreshesExpiredToken(t *testing.T) {
	fixture := setupAutoRefreshFixture(t, map[string]any{
		"access_token":  "stale-access",
		"refresh_token": "stored-refresh",
		"token_type":    "Bearer",
	})
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	runtime := runtimeAdapters{}
	runtime.now = func() time.Time { return now }
	runtime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		if client.clientID != "test-client" {
			t.Fatalf("oauth client id = %q, want test-client", client.clientID)
		}
		if refreshToken != "stored-refresh" {
			t.Fatalf("refresh token = %q, want stored-refresh", refreshToken)
		}
		return oauthTokenResponse{
			accessToken:  "fresh-access",
			refreshToken: "stored-refresh",
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    now.Add(time.Hour),
			rawTokenMaterialObject: map[string]any{
				"access_token":  "fresh-access",
				"refresh_token": "stored-refresh",
				"token_type":    "Bearer",
				"expires_in":    float64(3600),
			},
		}, nil
	}
	archive := &fakeHealthArchiveConnectionAPI{}
	access := newCurrentConnectionAccessWithRuntime(
		fixture.credentialStore,
		archivedConnection{
			id:                "googlehealth:111",
			tokenMetadataJSON: tokenMetadataJSON(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), []string{googleHealthProfileReadonlyScope}),
		},
		nil,
		runtime,
	).WithAutoRefresh(oauthClientSource{kind: "file", path: fixture.oauthClientPath}, archive)

	accessToken, err := access.AccessToken([]string{googleHealthProfileReadonlyScope})
	if err != nil {
		t.Fatalf("AccessToken error = %v, want refreshed token", err)
	}
	if accessToken != "fresh-access" {
		t.Fatalf("AccessToken = %q, want fresh-access", accessToken)
	}
}

type autoRefreshFixture struct {
	oauthClientPath string
	credentialStore credentialStoreConfig
}

// setupAutoRefreshFixture writes a minimal OAuth client file and seeds a
// file Credential Store with the supplied token material, returning the
// paths and config needed to drive the auto-refresh path. The credential
// store is keyed by "googlehealth:111" to match the archivedConnection
// fixtures the tests construct.
func setupAutoRefreshFixture(t *testing.T, seedTokenMaterial map[string]any) autoRefreshFixture {
	t.Helper()
	tempDir := t.TempDir()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		t.Fatalf("chmod tempDir: %v", err)
	}
	oauthClientPath := filepath.Join(tempDir, "client_secret.json")
	if err := os.WriteFile(oauthClientPath, []byte(`{"installed":{"client_id":"test-client","client_secret":"test-secret","redirect_uris":["http://localhost"]}}`), 0o600); err != nil {
		t.Fatalf("write oauth client file: %v", err)
	}
	credentialStore := credentialStoreConfig{kind: "file", path: filepath.Join(tempDir, "tokens.json")}
	store, err := newCredentialStore(credentialStore)
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", seedTokenMaterial); err != nil {
		t.Fatalf("seed credential store: %v", err)
	}
	return autoRefreshFixture{oauthClientPath: oauthClientPath, credentialStore: credentialStore}
}

func TestCurrentConnectionAccessTokenAutoRefreshPersistsToCredentialStoreAndArchive(t *testing.T) {
	fixture := setupAutoRefreshFixture(t, map[string]any{
		"access_token":  "stale-access",
		"refresh_token": "stored-refresh",
		"token_type":    "Bearer",
	})
	store, err := newCredentialStore(fixture.credentialStore)
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}

	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	refreshedExpiresAt := now.Add(time.Hour)
	runtime := runtimeAdapters{}
	runtime.now = func() time.Time { return now }
	runtime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		return oauthTokenResponse{
			accessToken:  "fresh-access",
			refreshToken: "rotated-refresh",
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    refreshedExpiresAt,
			rawTokenMaterialObject: map[string]any{
				"access_token":  "fresh-access",
				"refresh_token": "rotated-refresh",
				"token_type":    "Bearer",
				"expires_in":    float64(3600),
			},
		}, nil
	}
	archive := &fakeHealthArchiveConnectionAPI{}
	access := newCurrentConnectionAccessWithRuntime(
		fixture.credentialStore,
		archivedConnection{
			id:                "googlehealth:111",
			tokenMetadataJSON: tokenMetadataJSON(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), []string{googleHealthProfileReadonlyScope}),
		},
		nil,
		runtime,
	).WithAutoRefresh(oauthClientSource{kind: "file", path: fixture.oauthClientPath}, archive)

	if _, err := access.AccessToken([]string{googleHealthProfileReadonlyScope}); err != nil {
		t.Fatalf("AccessToken error = %v, want refreshed token", err)
	}

	stored, err := store.Load("googlehealth:111")
	if err != nil {
		t.Fatalf("load credential store: %v", err)
	}
	if stored["access_token"] != "fresh-access" {
		t.Fatalf("Credential Store access_token = %v, want fresh-access", stored["access_token"])
	}
	if stored["refresh_token"] != "rotated-refresh" {
		t.Fatalf("Credential Store refresh_token = %v, want rotated-refresh", stored["refresh_token"])
	}

	if archive.updateCalls != 1 {
		t.Fatalf("UpdateConnectionTokenMetadata calls = %d, want 1", archive.updateCalls)
	}
	if archive.updateConnectionID != "googlehealth:111" {
		t.Fatalf("UpdateConnectionTokenMetadata connection id = %q, want googlehealth:111", archive.updateConnectionID)
	}
	if !archive.updateToken.expiresAt.Equal(refreshedExpiresAt) {
		t.Fatalf("UpdateConnectionTokenMetadata expiresAt = %v, want %v", archive.updateToken.expiresAt, refreshedExpiresAt)
	}
}

func TestCurrentConnectionAccessTokenAutoRefreshFailureNamesCauseAndPointsAtDoctorOrConnect(t *testing.T) {
	fixture := setupAutoRefreshFixture(t, map[string]any{
		"access_token":  "stale-access",
		"refresh_token": "revoked-refresh",
		"token_type":    "Bearer",
	})

	runtime := runtimeAdapters{}
	runtime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	runtime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		return oauthTokenResponse{}, errors.New("Google rejected refresh token: invalid_grant")
	}
	access := newCurrentConnectionAccessWithRuntime(
		fixture.credentialStore,
		archivedConnection{
			id:                "googlehealth:111",
			tokenMetadataJSON: tokenMetadataJSON(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), []string{googleHealthProfileReadonlyScope}),
		},
		nil,
		runtime,
	).WithAutoRefresh(oauthClientSource{kind: "file", path: fixture.oauthClientPath}, &fakeHealthArchiveConnectionAPI{})

	_, err := access.AccessToken([]string{googleHealthProfileReadonlyScope})
	if err == nil {
		t.Fatal("AccessToken error = nil, want refresh failure")
	}
	message := err.Error()
	if !strings.Contains(message, "invalid_grant") {
		t.Fatalf("AccessToken error = %q, want it to name the refresh cause (invalid_grant)", message)
	}
	if !strings.Contains(message, "doctor --online") || !strings.Contains(message, "connect") {
		t.Fatalf("AccessToken error = %q, want it to point at `doctor --online` and `connect`", message)
	}
}

type fakeHealthArchiveConnectionAPI struct {
	updateConnectionID string
	updateToken        oauthTokenResponse
	updateNow          time.Time
	updateCalls        int
	updateErr          error
}

func (archive *fakeHealthArchiveConnectionAPI) Close() error { return nil }
func (archive *fakeHealthArchiveConnectionAPI) EnsureSameGoogleIdentity(string) error {
	return nil
}
func (archive *fakeHealthArchiveConnectionAPI) CurrentConnection() (archivedConnection, error) {
	return archivedConnection{}, nil
}
func (archive *fakeHealthArchiveConnectionAPI) UpsertConnection(string, googleIdentity, oauthTokenResponse, time.Time) error {
	return nil
}
func (archive *fakeHealthArchiveConnectionAPI) UpdateConnectionTokenMetadata(connectionID string, token oauthTokenResponse, now time.Time) error {
	archive.updateCalls++
	archive.updateConnectionID = connectionID
	archive.updateToken = token
	archive.updateNow = now
	return archive.updateErr
}
func (archive *fakeHealthArchiveConnectionAPI) RefreshConnectionIdentity(archivedConnection, googleIdentity, time.Time) error {
	return nil
}
func (archive *fakeHealthArchiveConnectionAPI) InspectConnectionTokenMetadata() (int, string, error) {
	return 0, "", nil
}

func tokenMetadataJSON(t *testing.T, expiresAt time.Time, scopes []string) string {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"credential_store_key": "googlehealth:111",
		"expires_at":           expiresAt.Format(time.RFC3339),
		"scopes":               scopes,
	})
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	return string(content)
}

package main

import (
	"encoding/json"
	"errors"
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

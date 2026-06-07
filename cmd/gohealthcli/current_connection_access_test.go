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
	access.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

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
	access.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	_, err := access.AccessToken([]string{googleHealthProfileReadonlyScope})
	if err == nil || !strings.Contains(err.Error(), googleHealthProfileReadonlyScope) {
		t.Fatalf("AccessToken error = %v, want missing scope before Credential Store error", err)
	}
}

func TestCurrentConnectionAccessFetchVerifiedIdentityNormalizesUnauthorized(t *testing.T) {
	originalFetchIdentity := fetchIdentity
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		return googleIdentity{}, errors.New("Google Health raw request failed with HTTP 401")
	}
	t.Cleanup(func() { fetchIdentity = originalFetchIdentity })

	access := newCurrentConnectionAccess(
		credentialStoreConfig{},
		archivedConnection{googleHealthUserID: "111111256096816351"},
		nil,
	)

	_, err := access.FetchVerifiedIdentity("access-secret")
	if err == nil || err.Error() != "Google Health rejected stored Connection token; run `gohealthcli connect` again" {
		t.Fatalf("FetchVerifiedIdentity error = %v, want normalized unauthorized error", err)
	}
}

func TestCurrentConnectionAccessFetchVerifiedIdentityRejectsMismatch(t *testing.T) {
	originalFetchIdentity := fetchIdentity
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		return googleIdentity{healthUserID: "other"}, nil
	}
	t.Cleanup(func() { fetchIdentity = originalFetchIdentity })

	access := newCurrentConnectionAccess(
		credentialStoreConfig{},
		archivedConnection{googleHealthUserID: "111111256096816351"},
		nil,
	)

	_, err := access.FetchVerifiedIdentity("access-secret")
	if err == nil || err.Error() != "Provider returned a different Google Identity; use a new archive path" {
		t.Fatalf("FetchVerifiedIdentity error = %v, want mismatch error", err)
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

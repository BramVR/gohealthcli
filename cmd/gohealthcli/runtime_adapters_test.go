package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestConnectSetupUsesRuntimeAdapter(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	now := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	runtime := productionRuntimeAdapters()
	runtime.now = func() time.Time { return now }
	runtime.runOAuthFlow = func(client oauthClientConfig, scopes []string, noInput bool) (oauthTokenResponse, error) {
		if client.clientID != "test-client" || client.clientSecret != "test-secret" {
			t.Fatalf("OAuth client = (%q, %q), want test client", client.clientID, client.clientSecret)
		}
		if len(scopes) == 0 {
			t.Fatal("OAuth scopes empty")
		}
		return oauthTokenResponse{
			accessToken:  "adapter-access-secret",
			refreshToken: "adapter-refresh-secret",
			tokenType:    "Bearer",
			scopes:       scopes,
			expiresAt:    now.Add(time.Hour),
			rawTokenMaterialObject: map[string]any{
				"access_token":  "adapter-access-secret",
				"refresh_token": "adapter-refresh-secret",
				"expires_in":    float64(3600),
				"scope":         strings.Join(scopes, " "),
				"token_type":    "Bearer",
			},
		}, nil
	}
	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if accessToken != "adapter-access-secret" {
			t.Fatalf("identity access token = %q, want adapter token", accessToken)
		}
		return googleIdentity{
			healthUserID:       "111111256096816351",
			legacyFitbitUserID: "A1B2C3",
			rawJSON:            fmt.Sprintf(`{"healthUserId":%q,"legacyUserId":%q}`, "111111256096816351", "A1B2C3"),
		}, nil
	}

	result, err := connectSetupWithRuntime(configPath, archivePath, false, runtime)
	if err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	if result.Status != "connected" || result.GoogleHealthUserID != "111111256096816351" {
		t.Fatalf("connect result = %#v, want connected adapter identity", result)
	}
}

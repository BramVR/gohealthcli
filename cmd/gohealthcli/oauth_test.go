package main

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestParseOAuthClientConfigContentPinsHTTPSAndGoogleHosts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name:    "http auth_uri rejected",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"http://accounts.google.com/o/oauth2/v2/auth"}}`,
			wantErr: true,
		},
		{
			name:    "attacker-host token_uri rejected",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","token_uri":"https://attacker.example.com/token"}}`,
			wantErr: true,
		},
		{
			name:    "empty uris default to Google and accepted",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret"}}`,
			wantErr: false,
		},
		{
			name:    "valid Google https uris accepted",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"https://accounts.google.com/o/oauth2/v2/auth","token_uri":"https://oauth2.googleapis.com/token"}}`,
			wantErr: false,
		},
		{
			name:    "uppercase scheme and host accepted (case-insensitive)",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"HTTPS://Accounts.Google.Com/o/oauth2/v2/auth","token_uri":"HTTPS://OAuth2.GoogleAPIs.Com/token"}}`,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseOAuthClientConfigContent([]byte(tt.content))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseOAuthClientConfigContent error = nil, want https/Google host rejection")
				}
				if !strings.Contains(err.Error(), "https") || !strings.Contains(err.Error(), "Google OAuth host") {
					t.Fatalf("error = %q, want mention of https and Google OAuth host", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOAuthClientConfigContent error = %v, want accepted", err)
			}
		})
	}
}

func TestOAuthScopesUseRecognizedGoogleHealthScopes(t *testing.T) {
	t.Parallel()
	scopes := oauthScopesForDataTypes(googlehealth.DefaultDataTypes())
	wantScopes := []string{
		googlehealth.ScopeActivityReadonly,
		googlehealth.ScopeHealthMetricsReadonly,
		googlehealth.ScopeSleepReadonly,
		googlehealth.ScopeProfileReadonly,
	}
	if !slices.Equal(scopes, wantScopes) {
		t.Fatalf("scopes = %v, want configured Google Health readonly scopes %v", scopes, wantScopes)
	}
	for _, scope := range scopes {
		for _, invalid := range []string{"settings.readonly"} {
			if strings.Contains(scope, invalid) {
				t.Fatalf("scopes include unrecognized Google Health scope %q: %v", invalid, scopes)
			}
		}
	}
}

func TestOAuthScopesForEmptyDataTypesRequestOnlyProfileScope(t *testing.T) {
	t.Parallel()
	for name, dataTypes := range map[string][]string{"nil": nil, "empty": {}} {
		if scopes := oauthScopesForDataTypes(dataTypes); !slices.Equal(scopes, []string{googlehealth.ScopeProfileReadonly}) {
			t.Fatalf("scopes for %s dataTypes = %v, want only the profile scope", name, scopes)
		}
	}
}

func TestListenForOAuthRedirectPreservesEmptyLoopbackPath(t *testing.T) {
	t.Parallel()
	listener, redirectURI, err := listenForOAuthRedirect([]string{"http://localhost"})
	if err != nil {
		t.Fatalf("listen for OAuth redirect: %v", err)
	}
	defer listener.Close()

	parsed, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect URI: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" {
		t.Fatalf("redirect URI = %s, want dynamic loopback with empty path", redirectURI)
	}
}

func TestParseOAuthTokenResponseRequiresRefreshToken(t *testing.T) {
	t.Parallel()
	_, err := parseOAuthTokenResponse([]byte(`{
		"access_token": "access-secret-value",
		"expires_in": 3600,
		"scope": "https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly",
		"token_type": "Bearer"
	}`), time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "refresh token") {
		t.Fatalf("parse token response error = %v, want missing refresh token", err)
	}
}

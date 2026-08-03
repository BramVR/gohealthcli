package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestOAuthAuthorizationURLCarriesCopiedLoopbackContract(t *testing.T) {
	t.Parallel()
	client := oauthClientConfig{
		clientID: "synthetic-client",
		authURI:  "https://accounts.google.com/o/oauth2/v2/auth",
	}
	redirectURI := "http://127.0.0.1:43210/oauth2callback"
	state := "synthetic-state"
	verifier := "synthetic-pkce-verifier-with-enough-length-for-the-test-contract"
	challenge := pkceChallenge(verifier)

	rawURL, err := buildOAuthAuthURL(client, redirectURI, []string{googlehealth.ScopeProfileReadonly}, state, challenge)
	if err != nil {
		t.Fatalf("build OAuth authorization URL: %v", err)
	}
	authURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse OAuth authorization URL: %v", err)
	}
	query := authURL.Query()
	checks := map[string]string{
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
		"redirect_uri":          redirectURI,
		"response_type":         "code",
		"state":                 state,
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Errorf("authorization URL %s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(rawURL, verifier) {
		t.Fatal("authorization URL contains the PKCE verifier")
	}
}

func TestOAuthLoopbackCallbackRequiresStateOnCompleteRedirect(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		state     string
		wantCode  string
		wantError string
	}{
		{name: "matching state", state: "synthetic-state", wantCode: "synthetic-code"},
		{name: "wrong state", state: "wrong-state", wantError: "OAuth state mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, redirectURI, err := listenForOAuthRedirect(nil)
			if err != nil {
				t.Fatalf("listen for OAuth redirect: %v", err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			result := make(chan struct {
				code string
				err  error
			}, 1)
			go func() {
				code, err := waitForOAuthCode(listener, "synthetic-state")
				result <- struct {
					code string
					err  error
				}{code: code, err: err}
			}()

			callbackURL, err := url.Parse(redirectURI)
			if err != nil {
				t.Fatalf("parse redirect URI: %v", err)
			}
			query := callbackURL.Query()
			query.Set("code", "synthetic-code")
			query.Set("state", test.state)
			callbackURL.RawQuery = query.Encode()
			request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, callbackURL.String(), nil)
			if err != nil {
				t.Fatalf("build complete redirected URL request: %v", err)
			}
			response, err := (&http.Client{Timeout: time.Second}).Do(request) // #nosec G107 -- synthetic loopback test server.
			if err != nil {
				t.Fatalf("send complete redirected URL: %v", err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()

			outcome := <-result
			if outcome.code != test.wantCode {
				t.Errorf("callback code = %q, want %q", outcome.code, test.wantCode)
			}
			if test.wantError == "" {
				if outcome.err != nil {
					t.Fatalf("callback error = %v, want nil", outcome.err)
				}
				return
			}
			if outcome.err == nil || outcome.err.Error() != test.wantError {
				t.Fatalf("callback error = %v, want %q", outcome.err, test.wantError)
			}
		})
	}
}

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

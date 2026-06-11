package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRuntimeAdaptersBindSharedTimeoutClientAsHTTPDoer pins the #281
// adapter shape: the production runtime adapters carry the one shared
// Provider HTTP client (#271, providerHTTPTimeout) as their HTTP doer,
// and withDefaults fills a nil doer with that same client — so every
// Provider request routed through the adapters seam keeps its deadline
// and no code path falls back to the process-wide default client.
func TestRuntimeAdaptersBindSharedTimeoutClientAsHTTPDoer(t *testing.T) {
	t.Parallel()
	production := productionRuntimeAdapters()
	client, ok := production.httpDoer.(*http.Client)
	if !ok {
		t.Fatalf("production httpDoer = %T, want the shared *http.Client", production.httpDoer)
	}
	if client != providerHTTPClient {
		t.Fatal("production httpDoer is not the shared Provider HTTP client")
	}
	if client.Timeout != providerHTTPTimeout {
		t.Fatalf("production doer timeout = %v, want providerHTTPTimeout %v", client.Timeout, providerHTTPTimeout)
	}

	defaulted := runtimeAdapters{}.withDefaults()
	if defaulted.httpDoer == nil {
		t.Fatal("withDefaults left httpDoer nil")
	}
	defaultedClient, ok := defaulted.httpDoer.(*http.Client)
	if !ok || defaultedClient != providerHTTPClient {
		t.Fatalf("defaulted httpDoer = %#v, want the shared Provider HTTP client", defaulted.httpDoer)
	}
}

// TestWithDefaultsRoutesNilFetchersThroughInjectedDoer pins the seam
// the doer slice (#281) adds for everything downstream: when a test
// injects only a fake HTTP doer, withDefaults binds the REAL identity,
// profile, and raw Provider fetcher bodies over that doer — so URL
// building, bearer auth, and status mapping run against the fake
// transport without any global being swapped.
func TestWithDefaultsRoutesNilFetchersThroughInjectedDoer(t *testing.T) {
	t.Parallel()
	transport := &stubProviderTransport{status: 200, body: `{"healthUserId":"hu-1","legacyUserId":"fb-1"}`}
	adapters := runtimeAdapters{httpDoer: providerDoer(transport)}.withDefaults()

	identity, err := adapters.fetchIdentity("test-access-token")
	if err != nil {
		t.Fatalf("fetchIdentity through injected doer: %v", err)
	}
	if transport.request == nil {
		t.Fatal("identity fetch bypassed the injected doer")
	}
	if got := transport.request.URL.String(); got != googleHealthIdentityURL {
		t.Fatalf("identity URL = %q, want %q", got, googleHealthIdentityURL)
	}
	if got := transport.request.Header.Get("Authorization"); got != "Bearer test-access-token" {
		t.Fatalf("Authorization = %q, want bearer token", got)
	}
	if identity.healthUserID != "hu-1" {
		t.Fatalf("identity = %+v, want parsed stub payload", identity)
	}

	profileTransport := &stubProviderTransport{status: 200, body: `{"name":"users/hu-1/profile"}`}
	adapters = runtimeAdapters{httpDoer: providerDoer(profileTransport)}.withDefaults()
	profile, err := adapters.fetchProfile("test-access-token")
	if err != nil {
		t.Fatalf("fetchProfile through injected doer: %v", err)
	}
	if profileTransport.request == nil || profile.healthUserID != "hu-1" {
		t.Fatalf("profile fetch did not ride the injected doer: request=%v profile=%+v", profileTransport.request, profile)
	}

	rawTransport := &stubProviderTransport{status: 200, body: `{"raw":true}`}
	adapters = runtimeAdapters{httpDoer: providerDoer(rawTransport)}.withDefaults()
	body, err := adapters.fetchRawProvider(rawProviderRequest{url: googleHealthIdentityURL}, "test-access-token")
	if err != nil {
		t.Fatalf("fetchRawProvider through injected doer: %v", err)
	}
	if rawTransport.request == nil || string(body) != `{"raw":true}` {
		t.Fatalf("raw fetch did not ride the injected doer: request=%v body=%q", rawTransport.request, body)
	}
}

func TestConnectSetupUsesRuntimeAdapter(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	now := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	runtime := runtimeAdapters{}
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

	result, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, runtime)
	if err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	if result.Status != "connected" || result.GoogleHealthUserID != "111111256096816351" {
		t.Fatalf("connect result = %#v, want connected adapter identity", result)
	}
}

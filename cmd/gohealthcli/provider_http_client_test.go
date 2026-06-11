package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// stubProviderTransport answers every request with a canned Provider
// response without touching the network, and records the last request
// so tests can prove a fetcher routed through the shared client.
type stubProviderTransport struct {
	request *http.Request
	status  int
	body    string
}

func (transport *stubProviderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request
	return &http.Response{
		StatusCode: transport.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(transport.body)),
		Request:    request,
	}, nil
}

// providerDoer wraps a stub transport in an http.Client carrying the
// production timeout — a fake HTTP doer tests inject through the
// runtime adapters seam or the Provider GET module value, instead of
// reassigning any package-level client (#281).
func providerDoer(transport http.RoundTripper) httpDoer {
	return &http.Client{Timeout: providerHTTPTimeout, Transport: transport}
}

// startStalledProviderServer returns a Provider stand-in that stalls
// every request until the test finishes, simulating the stalled
// connection that used to hang a Sync Run forever. The stall is capped
// at five seconds so a client without a deadline (the regression this
// file guards against) fails the test instead of deadlocking it.
func startStalledProviderServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	return server
}

// shortTimeoutDoer is a real Provider HTTP client with a shrunken
// deadline, injected as the doer so stalled-request behavior is
// observable without waiting out the production timeout.
func shortTimeoutDoer() httpDoer {
	return newProviderHTTPClient(50 * time.Millisecond)
}

func TestSharedProviderHTTPClientCarriesDocumentedTimeout(t *testing.T) {
	if providerHTTPTimeout <= 0 {
		t.Fatalf("providerHTTPTimeout = %v, want a positive deadline", providerHTTPTimeout)
	}
	if providerHTTPClient.Timeout != providerHTTPTimeout {
		t.Fatalf("shared Provider HTTP client timeout = %v, want the documented providerHTTPTimeout %v", providerHTTPClient.Timeout, providerHTTPTimeout)
	}
	// The timeout exists so a stalled request fails before the
	// abandoned-run fence can fence a Sync Run whose process is
	// still alive; it must therefore sit inside the fence window.
	if providerHTTPTimeout >= syncRunFenceStaleAfter {
		t.Fatalf("providerHTTPTimeout %v must stay inside the abandoned-run fence window %v", providerHTTPTimeout, syncRunFenceStaleAfter)
	}
}

func TestGoogleIdentityFetchRoutesThroughInjectedDoer(t *testing.T) {
	transport := &stubProviderTransport{status: http.StatusOK, body: `{"healthUserId":"hu-123","legacyUserId":"fb-456"}`}

	identity, err := fetchGoogleIdentity(providerGETWithDoer(transport), "test-access-token")
	if err != nil {
		t.Fatalf("fetchGoogleIdentity: %v", err)
	}
	if transport.request == nil {
		t.Fatal("Google identity fetch bypassed the injected HTTP doer")
	}
	if got := transport.request.Header.Get("Authorization"); got != "Bearer test-access-token" {
		t.Fatalf("Authorization header = %q, want bearer token", got)
	}
	if identity.healthUserID != "hu-123" || identity.legacyFitbitUserID != "fb-456" {
		t.Fatalf("identity = %+v, want parsed stub payload", identity)
	}
}

func TestGoogleProfileFetchRoutesThroughInjectedDoer(t *testing.T) {
	transport := &stubProviderTransport{status: http.StatusOK, body: `{"name":"users/hu-789/profile"}`}

	profile, err := fetchGoogleProfile(providerGETWithDoer(transport), "test-access-token")
	if err != nil {
		t.Fatalf("fetchGoogleProfile: %v", err)
	}
	if transport.request == nil {
		t.Fatal("Google profile fetch bypassed the injected HTTP doer")
	}
	if profile.healthUserID != "hu-789" {
		t.Fatalf("profile = %+v, want healthUserID parsed from stub payload", profile)
	}
}

func TestPairedDevicesIdentitySnapshotFetchRoutesThroughInjectedDoer(t *testing.T) {
	transport := &stubProviderTransport{status: http.StatusOK, body: `{"devices":[]}`}

	devices, err := fetchGooglePairedDevices(providerGETWithDoer(transport), "test-access-token")
	if err != nil {
		t.Fatalf("fetchGooglePairedDevices: %v", err)
	}
	if transport.request == nil {
		t.Fatal("paired-devices Identity Snapshot fetch bypassed the injected HTTP doer")
	}
	if devices.rawJSON != `{"devices":[]}` {
		t.Fatalf("devices.rawJSON = %q, want the stub payload", devices.rawJSON)
	}
}

func TestSettingsIdentitySnapshotFetchRoutesThroughInjectedDoer(t *testing.T) {
	transport := &stubProviderTransport{status: http.StatusOK, body: `{"weightUnit":"KILOGRAM"}`}

	settings, err := fetchGoogleSettings(providerGETWithDoer(transport), "test-access-token")
	if err != nil {
		t.Fatalf("fetchGoogleSettings: %v", err)
	}
	if transport.request == nil {
		t.Fatal("settings Identity Snapshot fetch bypassed the injected HTTP doer")
	}
	if settings.rawJSON != `{"weightUnit":"KILOGRAM"}` {
		t.Fatalf("settings.rawJSON = %q, want the stub payload", settings.rawJSON)
	}
}

func TestIRNProfileIdentitySnapshotFetchRoutesThroughInjectedDoer(t *testing.T) {
	transport := &stubProviderTransport{status: http.StatusOK, body: `{"enrolled":true}`}

	irn, err := fetchGoogleIRNProfile(providerGETWithDoer(transport), "test-access-token")
	if err != nil {
		t.Fatalf("fetchGoogleIRNProfile: %v", err)
	}
	if transport.request == nil {
		t.Fatal("irn-profile Identity Snapshot fetch bypassed the injected HTTP doer")
	}
	if irn.rawJSON != `{"enrolled":true}` {
		t.Fatalf("irn.rawJSON = %q, want the stub payload", irn.rawJSON)
	}
}

func TestOAuthCodeExchangeFailsStalledTokenEndpointByDeadline(t *testing.T) {
	server := startStalledProviderServer(t)

	client := oauthClientConfig{clientID: "id", clientSecret: "secret", tokenURI: server.URL}
	_, err := exchangeOAuthCodeWithRuntime(client, "http://127.0.0.1/callback", "code", "verifier", runtimeAdapters{now: currentTime, httpDoer: shortTimeoutDoer()})
	if err == nil {
		t.Fatal("expected a stalled OAuth token exchange to fail by deadline, got success")
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || !urlErr.Timeout() {
		t.Fatalf("expected a timeout error from the stalled token endpoint, got %v", err)
	}
}

func TestOAuthTokenRefreshFailsStalledTokenEndpointByDeadline(t *testing.T) {
	server := startStalledProviderServer(t)

	client := oauthClientConfig{clientID: "id", clientSecret: "secret", tokenURI: server.URL}
	_, err := refreshGoogleOAuthTokenWithRuntime(client, "refresh-token", nil, runtimeAdapters{now: currentTime, httpDoer: shortTimeoutDoer()})
	if err == nil {
		t.Fatal("expected a stalled OAuth token refresh to fail by deadline, got success")
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || !urlErr.Timeout() {
		t.Fatalf("expected a timeout error from the stalled token endpoint, got %v", err)
	}
}

func TestRawProviderFetchFailsStalledProviderByDeadline(t *testing.T) {
	server := startStalledProviderServer(t)

	_, err := fetchGoogleHealthRaw(shortTimeoutDoer(), rawProviderRequest{url: server.URL}, "test-access-token")
	if err == nil {
		t.Fatal("expected a stalled raw Provider fetch to fail by deadline, got success")
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || !urlErr.Timeout() {
		t.Fatalf("expected a timeout error from the stalled Provider, got %v", err)
	}
}

func TestProviderHTTPClientFailsStalledRequestByDeadline(t *testing.T) {
	server := startStalledProviderServer(t)

	client := newProviderHTTPClient(50 * time.Millisecond)
	response, err := client.Get(server.URL)
	if err == nil {
		response.Body.Close()
		t.Fatal("expected a stalled Provider request to fail by deadline, got a response")
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || !urlErr.Timeout() {
		t.Fatalf("expected a timeout error from the stalled Provider request, got %v", err)
	}
}

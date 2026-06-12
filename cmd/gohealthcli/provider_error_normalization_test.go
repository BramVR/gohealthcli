package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// bindFetcherProviderGET rebinds one runtime-adapters fetcher seam so a
// full command run keeps the REAL fetcher body in the path while riding
// the given Provider GET module — a fake doer plus virtual retry sleeps
// — instead of the production module. The seam pointer targets a field
// on the test's local runtimeAdapters value, so no package state is
// touched and no restore is needed.
func bindFetcherProviderGET[Result any](seam *func(string) (Result, error), fetch func(providerGET, string) (Result, error), get providerGET) {
	*seam = func(accessToken string) (Result, error) {
		return fetch(get, accessToken)
	}
}

// connectedProviderFixture initializes config + Health Archive, runs a
// faked connect, and grants any extra scopes the command under test
// requires, so it reaches its Provider fetch. The returned adapters
// carry the connect fakes; tests attach their own fetcher fakes to it
// and dispatch through runWithRuntime.
func connectedProviderFixture(t *testing.T, extraScopes ...string) (string, string, runtimeAdapters) {
	t.Helper()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:  "connect-access-secret",
		refreshToken: "connect-refresh-secret",
		healthUserID: "111111256096816351",
	})
	for _, scope := range extraScopes {
		addStoredConnectionScope(t, archivePath, scope)
	}
	return configPath, archivePath, testRuntime
}

// connectedDevicesFixture is connectedProviderFixture with the
// settings scope pairedDevices (and getSettings) requires.
func connectedDevicesFixture(t *testing.T) (string, string, runtimeAdapters) {
	t.Helper()
	return connectedProviderFixture(t, googleHealthSettingsReadonlyScope)
}

// TestDevicesEmitsProviderUnreachableOnNetworkFailure pins the issue
// #272 behavior change: when the Provider cannot be reached at all
// (dial failure, DNS, timeout — surfaced by net/http as *url.Error),
// the devices JSON envelope carries the documented provider_unreachable
// failure status instead of the generic devices_failed, so JSON
// consumers can distinguish a Provider outage from local
// misconfiguration.
func TestDevicesEmitsProviderUnreachableOnNetworkFailure(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedDevicesFixture(t)
	fetchErr := &url.Error{
		Op:  "Get",
		URL: googleHealthPairedDevicesURL,
		Err: errors.New("dial tcp 127.0.0.1:9: connect: connection refused"),
	}
	testRuntime.fetchPairedDevices = func(string) (googlePairedDevices, error) {
		return googlePairedDevices{}, fetchErr
	}

	stdout := new(bytes.Buffer)
	code := runWithRuntime([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), testRuntime)
	if code != 1 {
		t.Fatalf("devices exit code = %d, want 1\nstdout: %s", code, stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode devices --json: %v\n%s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "provider_unreachable")
}

// TestDevicesEmitsProviderUnreachableOnProviderHTTPFailure drives the
// production pairedDevices fetcher against a Provider answering HTTP
// 503 (a stub transport injected as the Provider GET module's doer).
// The fetcher must surface the typed upstream HTTP error so the
// translation layer classifies the failure as provider_unreachable —
// not the generic devices_failed. Since #280 the fetcher rides the
// shared Provider GET module's bounded retry, so a persistent 503
// exhausts the budget and the message reports the attempt count while
// keeping its endpoint-specific wording inside the wrap.
func TestDevicesEmitsProviderUnreachableOnProviderHTTPFailure(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedDevicesFixture(t)
	var sleeps []time.Duration
	bindFetcherProviderGET(&testRuntime.fetchPairedDevices, fetchGooglePairedDevices,
		providerGETWithRetrySeams(&stubProviderTransport{status: 503, body: `{"error":"unavailable"}`}, &sleeps))

	stdout := new(bytes.Buffer)
	code := runWithRuntime([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), testRuntime)
	if code != 1 {
		t.Fatalf("devices exit code = %d, want 1\nstdout: %s", code, stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode devices --json: %v\n%s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "provider_unreachable")
	assertJSONString(t, got, "message", "Google Health request failed after 5 attempts: Google Health pairedDevices request failed with HTTP 503")
	if len(sleeps) != googleHealthRetryMaxAttempts-1 {
		t.Fatalf("sleeps = %d, want %d (devices rides the shared retry budget)", len(sleeps), googleHealthRetryMaxAttempts-1)
	}
}

// TestFetchVerifiedIdentityPreservesTypedCauseChainOnUnauthorized pins
// the issue #272 chain AC: when the Provider rejects the stored
// Connection token with a typed HTTP 401, the normalized error still
// matches the errCurrentConnectionProviderUnauthorized category AND
// keeps the typed googleHealthHTTPError reachable via errors.As, while
// the user-facing message stays the historical wording verbatim.
func TestFetchVerifiedIdentityPreservesTypedCauseChainOnUnauthorized(t *testing.T) {
	t.Parallel()
	runtime := productionRuntimeAdapters()
	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		return googleIdentity{}, &googleHealthHTTPError{StatusCode: 401, endpoint: "identity"}
	}
	access := newCurrentConnectionAccessWithRuntime(
		credentialStoreConfig{},
		archivedConnection{googleHealthUserID: "111111256096816351"},
		nil,
		runtime,
	)

	_, err := access.FetchVerifiedIdentity("access-secret")
	if err == nil {
		t.Fatal("FetchVerifiedIdentity returned nil, want normalized unauthorized error")
	}
	if !errors.Is(err, errCurrentConnectionProviderUnauthorized) {
		t.Fatalf("err = %v, want errCurrentConnectionProviderUnauthorized category", err)
	}
	var httpErr *googleHealthHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 401 {
		t.Fatalf("err = %v, want typed googleHealthHTTPError with status 401 preserved in the chain", err)
	}
	if err.Error() != errCurrentConnectionProviderUnauthorized.Error() {
		t.Fatalf("err.Error() = %q, want the historical message %q verbatim", err.Error(), errCurrentConnectionProviderUnauthorized.Error())
	}
}

// TestSyncIngestionNormalizesTypedUnauthorizedPreservingChain pins the
// issue #272 chain AC on the Sync Run ingestion path: an upstream
// typed HTTP 401 during pagination surfaces as the shared "run
// `gohealthcli connect` again" category with the typed
// googleHealthHTTPError still reachable via errors.As — instead of the
// historical text-matched errors.New that discarded the cause chain.
func TestSyncIngestionNormalizesTypedUnauthorizedPreservingChain(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{}
	provider := funcIngestionProvider(func(request rawProviderRequest, accessToken string) ([]byte, error) {
		return nil, &googleHealthHTTPError{StatusCode: 401}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)

	_, err := ingestion.Execute(context.Background(), archive, googleHealthIngestionRequest{
		connection:  archivedConnection{id: "googlehealth:111"},
		dataType:    "steps",
		from:        "2026-01-01T00:00:00Z",
		to:          "2026-01-02T00:00:00Z",
		accessToken: "revoked-access",
	})
	if err == nil {
		t.Fatal("Execute returned nil, want normalized unauthorized error")
	}
	if !errors.Is(err, errCurrentConnectionProviderUnauthorized) {
		t.Fatalf("err = %v, want errCurrentConnectionProviderUnauthorized category", err)
	}
	var httpErr *googleHealthHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 401 {
		t.Fatalf("err = %v, want typed googleHealthHTTPError with status 401 preserved in the chain", err)
	}
	if err.Error() != errCurrentConnectionProviderUnauthorized.Error() {
		t.Fatalf("err.Error() = %q, want the historical message %q verbatim", err.Error(), errCurrentConnectionProviderUnauthorized.Error())
	}
}

// failingProviderTransport makes every request through the shared
// Provider HTTP client fail at the transport layer; net/http surfaces
// that as a *url.Error, the same shape a dial-refused, DNS, or
// deadline failure produces against the live Provider.
type failingProviderTransport struct {
	err error
}

func (transport failingProviderTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, transport.err
}

// TestSettingsEmitsProviderUnreachable drives the settings command
// against a Provider that is down — transport-level network failure
// and HTTP 503 — through the production fetcher, and expects the
// provider_unreachable JSON failure status both times (issue #272).
func TestSettingsEmitsProviderUnreachable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		transport http.RoundTripper
	}{
		{name: "network failure", transport: failingProviderTransport{err: errors.New("connect: connection refused")}},
		{name: "provider HTTP 503", transport: &stubProviderTransport{status: 503, body: `{"error":"unavailable"}`}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			configPath, archivePath, testRuntime := connectedDevicesFixture(t)
			// The 503 case exhausts the shared Provider GET retry budget
			// (#280); the module's stubbed seams keep the sleeps virtual.
			bindFetcherProviderGET(&testRuntime.fetchSettings, fetchGoogleSettings,
				providerGETWithRetrySeams(tt.transport, &[]time.Duration{}))

			stdout := new(bytes.Buffer)
			code := runWithRuntime([]string{"settings", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), testRuntime)
			if code != 1 {
				t.Fatalf("settings exit code = %d, want 1\nstdout: %s", code, stdout.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode settings --json: %v\n%s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "provider_unreachable")
		})
	}
}

// TestIRNProfileEmitsProviderUnreachable mirrors the settings case for
// the irn-profile Identity Snapshot command (issue #272).
func TestIRNProfileEmitsProviderUnreachable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		transport http.RoundTripper
	}{
		{name: "network failure", transport: failingProviderTransport{err: errors.New("connect: connection refused")}},
		{name: "provider HTTP 503", transport: &stubProviderTransport{status: 503, body: `{"error":"unavailable"}`}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			configPath, archivePath, testRuntime := connectedProviderFixture(t, googleHealthIrnReadonlyScope)
			// The 503 case exhausts the shared Provider GET retry budget
			// (#280); the module's stubbed seams keep the sleeps virtual.
			bindFetcherProviderGET(&testRuntime.fetchIRNProfile, fetchGoogleIRNProfile,
				providerGETWithRetrySeams(tt.transport, &[]time.Duration{}))

			stdout := new(bytes.Buffer)
			code := runWithRuntime([]string{"irn-profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), testRuntime)
			if code != 1 {
				t.Fatalf("irn-profile exit code = %d, want 1\nstdout: %s", code, stdout.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode irn-profile --json: %v\n%s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "provider_unreachable")
		})
	}
}

// TestProfileEmitsProviderUnreachable mirrors the settings case for
// the profile Identity Snapshot command (issue #272). The profile
// fetcher rides the runtime adapters seam (production binding: the
// fetchProfile package seam), so rebinding that seam over a fake-doer
// module exercises the production fetcher end to end.
func TestProfileEmitsProviderUnreachable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		transport http.RoundTripper
	}{
		{name: "network failure", transport: failingProviderTransport{err: errors.New("connect: connection refused")}},
		{name: "provider HTTP 503", transport: &stubProviderTransport{status: 503, body: `{"error":"unavailable"}`}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			configPath, archivePath, testRuntime := connectedProviderFixture(t)
			// The 503 case exhausts the shared Provider GET retry budget
			// (#280); the module's stubbed seams keep the sleeps virtual.
			bindFetcherProviderGET(&testRuntime.fetchProfile, fetchGoogleProfile,
				providerGETWithRetrySeams(tt.transport, &[]time.Duration{}))

			stdout := new(bytes.Buffer)
			code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), testRuntime)
			if code != 1 {
				t.Fatalf("profile exit code = %d, want 1\nstdout: %s", code, stdout.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode profile --json: %v\n%s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "provider_unreachable")
		})
	}
}

// TestIdentityEmitsProviderUnreachable mirrors the settings case for
// the identity command, whose Provider round trip is the verified
// identity fetch (issue #272). The connect fixture binds a fake
// fetchIdentity on the adapters, so the test rebinds that field to the
// production fetcher over the fake-doer module.
func TestIdentityEmitsProviderUnreachable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		transport http.RoundTripper
	}{
		{name: "network failure", transport: failingProviderTransport{err: errors.New("connect: connection refused")}},
		{name: "provider HTTP 503", transport: &stubProviderTransport{status: 503, body: `{"error":"unavailable"}`}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			configPath, archivePath, testRuntime := connectedProviderFixture(t)
			// The 503 case exhausts the shared Provider GET retry budget
			// (#280); the module's stubbed seams keep the sleeps virtual.
			bindFetcherProviderGET(&testRuntime.fetchIdentity, fetchGoogleIdentity,
				providerGETWithRetrySeams(tt.transport, &[]time.Duration{}))

			stdout := new(bytes.Buffer)
			code := runWithRuntime([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), testRuntime)
			if code != 1 {
				t.Fatalf("identity exit code = %d, want 1\nstdout: %s", code, stdout.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode identity --json: %v\n%s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "provider_unreachable")
		})
	}
}

// TestRawEmitsProviderUnreachableFailureStatus pins the unified
// Failure Reporter side of issue #272: the raw command (the one
// Provider-touching command that fails through the FailureReport
// envelope) classifies non-auth Provider HTTP/network failures under
// the documented StatusProviderUnreachable instead of the catch-all
// operation_failed.
func TestRawEmitsProviderUnreachableFailureStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		transport http.RoundTripper
	}{
		{name: "network failure", transport: failingProviderTransport{err: errors.New("connect: connection refused")}},
		{name: "provider HTTP 503", transport: &stubProviderTransport{status: 503, body: `{"error":"unavailable"}`}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			configPath, archivePath, testRuntime := connectedProviderFixture(t)
			// Rebind the raw Provider seam over the real single-shot
			// fetcher with the stub transport as its doer (#281).
			testRuntime.fetchRawProvider = func(ctx context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
				return fetchGoogleHealthRaw(ctx, providerDoer(tt.transport), request, accessToken)
			}

			stdout := new(bytes.Buffer)
			code := runWithRuntime([]string{"--json", "raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, new(bytes.Buffer), testRuntime)
			if code != 1 {
				t.Fatalf("raw exit code = %d, want 1\nstdout: %s", code, stdout.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode raw failure envelope: %v\n%s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "provider_unreachable")
		})
	}
}

// TestFetchVerifiedIdentityDoesNotMatchOnErrorText pins the issue #272
// no-string-matching AC: an untyped error whose text merely mentions
// "HTTP 401" is NOT a Provider auth rejection — it passes through
// unchanged instead of being rewritten into the connect-again message.
func TestFetchVerifiedIdentityDoesNotMatchOnErrorText(t *testing.T) {
	t.Parallel()
	cause := errors.New("proxy relayed HTTP 401 from an unrelated hop")
	runtime := productionRuntimeAdapters()
	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		return googleIdentity{}, cause
	}
	access := newCurrentConnectionAccessWithRuntime(
		credentialStoreConfig{},
		archivedConnection{googleHealthUserID: "111111256096816351"},
		nil,
		runtime,
	)

	_, err := access.FetchVerifiedIdentity("access-secret")
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the untyped cause passed through unchanged", err)
	}
	if errors.Is(err, errCurrentConnectionProviderUnauthorized) {
		t.Fatalf("err = %v, must not classify as Provider auth rejection on message text alone", err)
	}
}

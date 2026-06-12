package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/BramVR/gohealthcli/internal/archived"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

// These tests pin the issue #272 behavior at the CLI surface: every
// Provider-touching command classifies non-auth Provider HTTP/network
// failures under the documented provider_unreachable JSON failure
// status. They moved out of the Provider package's test file with the
// #287 extraction because they dispatch full command runs.
//
// The retry-budget timing itself (attempt counts, virtual backoff
// sleeps, Retry-After floors) is pinned inside internal/googlehealth's
// own GET-module tests; here the 503 sub-cases inject the typed
// upstream error at the runtime-adapters fetcher seam, and one devices
// test keeps the full real-fetcher retry exhaustion end to end.

// bindFetcherProviderGET rebinds one runtime-adapters fetcher seam so a
// full command run keeps the REAL fetcher body in the path while riding
// the given Provider GET module with a fake doer instead of the
// production module. The seam pointer targets a field on the test's
// local runtimeAdapters value, so no package state is touched and no
// restore is needed.
func bindFetcherProviderGET[Result any](seam *func(string) (Result, error), fetch func(googlehealth.GET, string) (Result, error), get googlehealth.GET) {
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
	return connectedProviderFixture(t, googlehealth.ScopeSettingsReadonly)
}

// failingProviderTransport makes every request through the Provider
// GET module fail at the transport layer; net/http surfaces that as a
// *url.Error, the same shape a dial-refused, DNS, or deadline failure
// produces against the live Provider.
type failingProviderTransport struct {
	err error
}

func (transport failingProviderTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, transport.err
}

// runProviderUnreachableCommand dispatches the command and asserts the
// JSON envelope carries the provider_unreachable status.
func runProviderUnreachableCommand(t *testing.T, configPath, archivePath string, testRuntime runtimeAdapters, command string) map[string]any {
	t.Helper()
	stdout := new(bytes.Buffer)
	code := runWithRuntime([]string{command, "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), testRuntime)
	if code != 1 {
		t.Fatalf("%s exit code = %d, want 1\nstdout: %s", command, code, stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode %s --json: %v\n%s", command, err, stdout.String())
	}
	assertJSONString(t, got, "status", "provider_unreachable")
	return got
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
		URL: googlehealth.PairedDevicesURL,
		Err: errors.New("dial tcp 127.0.0.1:9: connect: connection refused"),
	}
	testRuntime.fetchPairedDevices = func(string) (googlePairedDevices, error) {
		return googlePairedDevices{}, fetchErr
	}

	runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "devices")
}

// TestDevicesEmitsProviderUnreachableOnProviderHTTPFailure drives the
// production pairedDevices fetcher against a Provider answering HTTP
// 503 (a stub transport injected as the Provider GET module's doer).
// The fetcher must surface the typed upstream HTTP error so the
// translation layer classifies the failure as provider_unreachable —
// not the generic devices_failed. Since #280 the fetcher rides the
// shared Provider GET module's bounded retry, so the persistent 503
// exhausts the budget (real backoff sleeps — the test runs parallel)
// and the message reports the attempt count while keeping its
// endpoint-specific wording inside the wrap.
func TestDevicesEmitsProviderUnreachableOnProviderHTTPFailure(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedDevicesFixture(t)
	bindFetcherProviderGET(&testRuntime.fetchPairedDevices, fetchGooglePairedDevices,
		providerGETWithDoer(&stubProviderTransport{status: 503, body: `{"error":"unavailable"}`}))

	got := runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "devices")
	assertJSONString(t, got, "message", "Google Health request failed after 5 attempts: Google Health pairedDevices request failed with HTTP 503")
}

// TestIdentitySnapshotCommandsEmitProviderUnreachable covers the
// remaining Provider-touching Identity Snapshot commands (issue #272):
// a transport-level network failure through the REAL production
// fetcher (single attempt — network failures are not retried) and a
// typed upstream 503 injected at the runtime-adapters fetcher seam
// both classify as provider_unreachable.
func TestIdentitySnapshotCommandsEmitProviderUnreachable(t *testing.T) {
	t.Parallel()
	t.Run("settings", func(t *testing.T) {
		t.Parallel()
		t.Run("network failure", func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, testRuntime := connectedDevicesFixture(t)
			bindFetcherProviderGET(&testRuntime.fetchSettings, fetchGoogleSettings,
				providerGETWithDoer(failingProviderTransport{err: errors.New("connect: connection refused")}))
			runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "settings")
		})
		t.Run("provider HTTP 503", func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, testRuntime := connectedDevicesFixture(t)
			testRuntime.fetchSettings = func(string) (googleSettings, error) {
				return googleSettings{}, &googlehealth.HTTPError{StatusCode: 503, Endpoint: "settings"}
			}
			runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "settings")
		})
	})
	t.Run("irn-profile", func(t *testing.T) {
		t.Parallel()
		t.Run("network failure", func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, testRuntime := connectedProviderFixture(t, googlehealth.ScopeIrnReadonly)
			bindFetcherProviderGET(&testRuntime.fetchIRNProfile, fetchGoogleIRNProfile,
				providerGETWithDoer(failingProviderTransport{err: errors.New("connect: connection refused")}))
			runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "irn-profile")
		})
		t.Run("provider HTTP 503", func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, testRuntime := connectedProviderFixture(t, googlehealth.ScopeIrnReadonly)
			testRuntime.fetchIRNProfile = func(string) (googleIRNProfile, error) {
				return googleIRNProfile{}, &googlehealth.HTTPError{StatusCode: 503, Endpoint: "irnProfile"}
			}
			runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "irn-profile")
		})
	})
	t.Run("profile", func(t *testing.T) {
		t.Parallel()
		t.Run("network failure", func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, testRuntime := connectedProviderFixture(t)
			bindFetcherProviderGET(&testRuntime.fetchProfile, fetchGoogleProfile,
				providerGETWithDoer(failingProviderTransport{err: errors.New("connect: connection refused")}))
			runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "profile")
		})
		t.Run("provider HTTP 503", func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, testRuntime := connectedProviderFixture(t)
			testRuntime.fetchProfile = func(string) (googleProfile, error) {
				return googleProfile{}, &googlehealth.HTTPError{StatusCode: 503, Endpoint: "profile"}
			}
			runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "profile")
		})
	})
	t.Run("identity", func(t *testing.T) {
		t.Parallel()
		t.Run("network failure", func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, testRuntime := connectedProviderFixture(t)
			bindFetcherProviderGET(&testRuntime.fetchIdentity, fetchGoogleIdentity,
				providerGETWithDoer(failingProviderTransport{err: errors.New("connect: connection refused")}))
			runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "identity")
		})
		t.Run("provider HTTP 503", func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, testRuntime := connectedProviderFixture(t)
			testRuntime.fetchIdentity = func(string) (googleIdentity, error) {
				return googleIdentity{}, &googlehealth.HTTPError{StatusCode: 503, Endpoint: "identity"}
			}
			runProviderUnreachableCommand(t, configPath, archivePath, testRuntime, "identity")
		})
	})
}

// TestRawEmitsProviderUnreachableFailureStatus pins the unified
// Failure Reporter side of issue #272: the raw command (the one
// Provider-touching command that fails through the FailureReport
// envelope) classifies non-auth Provider HTTP/network failures under
// the documented StatusProviderUnreachable instead of the catch-all
// operation_failed. The real single-shot fetcher stays in the path
// with the stub transport as its doer (#281); raw performs no retry,
// so both sub-cases are single-attempt.
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
			t.Parallel()
			configPath, archivePath, testRuntime := connectedProviderFixture(t)
			testRuntime.fetchRawProvider = func(ctx context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
				return googlehealth.FetchRaw(ctx, providerDoer(tt.transport), request, accessToken)
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

// TestFetchVerifiedIdentityPreservesTypedCauseChainOnUnauthorized pins
// the issue #272 chain AC: when the Provider rejects the stored
// Connection token with a typed HTTP 401, the normalized error still
// matches the googlehealth.ErrUnauthorized category AND keeps the
// typed HTTPError reachable via errors.As, while the user-facing
// message stays the historical wording verbatim.
func TestFetchVerifiedIdentityPreservesTypedCauseChainOnUnauthorized(t *testing.T) {
	t.Parallel()
	runtime := productionRuntimeAdapters()
	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		return googleIdentity{}, &googlehealth.HTTPError{StatusCode: 401, Endpoint: "identity"}
	}
	access := newCurrentConnectionAccessWithRuntime(
		credentialStoreConfig{},
		archived.Connection{GoogleHealthUserID: "111111256096816351"},
		nil,
		runtime,
	)

	_, err := access.FetchVerifiedIdentity("access-secret")
	if err == nil {
		t.Fatal("FetchVerifiedIdentity returned nil, want normalized unauthorized error")
	}
	if !errors.Is(err, googlehealth.ErrUnauthorized) {
		t.Fatalf("err = %v, want googlehealth.ErrUnauthorized category", err)
	}
	var httpErr *googlehealth.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 401 {
		t.Fatalf("err = %v, want typed HTTPError with status 401 preserved in the chain", err)
	}
	if err.Error() != googlehealth.ErrUnauthorized.Error() {
		t.Fatalf("err.Error() = %q, want the historical message %q verbatim", err.Error(), googlehealth.ErrUnauthorized.Error())
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
		archived.Connection{GoogleHealthUserID: "111111256096816351"},
		nil,
		runtime,
	)

	_, err := access.FetchVerifiedIdentity("access-secret")
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the untyped cause passed through unchanged", err)
	}
	if errors.Is(err, googlehealth.ErrUnauthorized) {
		t.Fatalf("err = %v, must not classify as Provider auth rejection on message text alone", err)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
)

// installDevicesProviderFailure points the paired-devices Identity
// Snapshot fetcher at a Provider that always fails with the given
// error, restoring the production fetcher when the test ends.
func installDevicesProviderFailure(t *testing.T, fetchErr error) {
	t.Helper()
	original := fetchPairedDevices
	fetchPairedDevices = func(string) (googlePairedDevices, error) {
		return googlePairedDevices{}, fetchErr
	}
	t.Cleanup(func() { fetchPairedDevices = original })
}

// connectedDevicesFixture initializes config + Health Archive, runs a
// faked connect, and grants the settings scope pairedDevices requires,
// so devices reaches its Provider fetch.
func connectedDevicesFixture(t *testing.T) (string, string) {
	t.Helper()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:  "connect-access-secret",
		refreshToken: "connect-refresh-secret",
		healthUserID: "111111256096816351",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}
	addStoredConnectionScope(t, archivePath, googleHealthSettingsReadonlyScope)
	return configPath, archivePath
}

// TestDevicesEmitsProviderUnreachableOnNetworkFailure pins the issue
// #272 behavior change: when the Provider cannot be reached at all
// (dial failure, DNS, timeout — surfaced by net/http as *url.Error),
// the devices JSON envelope carries the documented provider_unreachable
// failure status instead of the generic devices_failed, so JSON
// consumers can distinguish a Provider outage from local
// misconfiguration.
func TestDevicesEmitsProviderUnreachableOnNetworkFailure(t *testing.T) {
	configPath, archivePath := connectedDevicesFixture(t)
	installDevicesProviderFailure(t, &url.Error{
		Op:  "Get",
		URL: googleHealthPairedDevicesURL,
		Err: errors.New("dial tcp 127.0.0.1:9: connect: connection refused"),
	})

	stdout := new(bytes.Buffer)
	code := run([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer))
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
// 503 (via the shared Provider HTTP client's stub transport). The
// fetcher must surface the typed upstream HTTP error so the
// translation layer classifies the failure as provider_unreachable —
// not the generic devices_failed — and the message keeps its
// endpoint-specific wording.
func TestDevicesEmitsProviderUnreachableOnProviderHTTPFailure(t *testing.T) {
	configPath, archivePath := connectedDevicesFixture(t)
	swapSharedProviderHTTPClient(t, &stubProviderTransport{status: 503, body: `{"error":"unavailable"}`})

	stdout := new(bytes.Buffer)
	code := run([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer))
	if code != 1 {
		t.Fatalf("devices exit code = %d, want 1\nstdout: %s", code, stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode devices --json: %v\n%s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "provider_unreachable")
	assertJSONString(t, got, "message", "Google Health pairedDevices request failed with HTTP 503")
}

// TestFetchVerifiedIdentityPreservesTypedCauseChainOnUnauthorized pins
// the issue #272 chain AC: when the Provider rejects the stored
// Connection token with a typed HTTP 401, the normalized error still
// matches the errCurrentConnectionProviderUnauthorized category AND
// keeps the typed googleHealthHTTPError reachable via errors.As, while
// the user-facing message stays the historical wording verbatim.
func TestFetchVerifiedIdentityPreservesTypedCauseChainOnUnauthorized(t *testing.T) {
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

// TestFetchVerifiedIdentityDoesNotMatchOnErrorText pins the issue #272
// no-string-matching AC: an untyped error whose text merely mentions
// "HTTP 401" is NOT a Provider auth rejection — it passes through
// unchanged instead of being rewritten into the connect-again message.
func TestFetchVerifiedIdentityDoesNotMatchOnErrorText(t *testing.T) {
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

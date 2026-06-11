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

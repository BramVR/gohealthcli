package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestStatusJSONOmitsFreshnessBlockWhenNoSnapshots pins the
// omitempty contract: a clean archive with no identity_snapshots rows
// produces no `identity_snapshots_freshness` JSON field at all.
func TestStatusJSONOmitsFreshnessBlockWhenNoSnapshots(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "identity_snapshots_freshness") {
		t.Fatalf("--json includes identity_snapshots_freshness on a clean archive; want omitted\n%s", stdout.String())
	}
}

// TestStatusJSONReportsIdentitySnapshotsFreshnessBlock is the tracer
// for the snapshot-freshness slice of #111: status --json carries an
// identity_snapshots_freshness block with the latest fetched_at per
// kind plus paired_device_count from the most recent paired-devices
// snapshot.
func TestStatusJSONReportsIdentitySnapshotsFreshnessBlock(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect: %d", code)
	}

	snapshots, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	connection, err := snapshots.CurrentConnection()
	if err != nil {
		snapshots.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, err := snapshots.Insert(connection, "profile", `{"name":"users/me/profile"}`, "2026-06-01T00:00:00Z"); err != nil {
		snapshots.Close()
		t.Fatalf("Insert profile: %v", err)
	}
	if _, err := snapshots.Insert(connection, "settings", `{"unit":"metric"}`, "2026-06-05T00:00:00Z"); err != nil {
		snapshots.Close()
		t.Fatalf("Insert settings: %v", err)
	}
	if _, err := snapshots.Insert(connection, "paired-devices", `{"devices":[{"model":"Pixel Watch 2"},{"model":"Pixel 8"}]}`, "2026-06-08T00:00:00Z"); err != nil {
		snapshots.Close()
		t.Fatalf("Insert paired-devices: %v", err)
	}
	snapshots.Close()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		IdentitySnapshots struct {
			PairedDeviceCount int               `json:"paired_device_count"`
			LatestFetchedAt   map[string]string `json:"latest_fetched_at"`
		} `json:"identity_snapshots_freshness"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout.String())
	}
	if got.IdentitySnapshots.PairedDeviceCount != 2 {
		t.Fatalf("paired_device_count = %d, want 2", got.IdentitySnapshots.PairedDeviceCount)
	}
	wantKinds := map[string]string{
		"profile":        "2026-06-01T00:00:00Z",
		"settings":       "2026-06-05T00:00:00Z",
		"paired-devices": "2026-06-08T00:00:00Z",
	}
	for kind, want := range wantKinds {
		if got.IdentitySnapshots.LatestFetchedAt[kind] != want {
			t.Errorf("latest_fetched_at[%s] = %q, want %q", kind, got.IdentitySnapshots.LatestFetchedAt[kind], want)
		}
	}
}

// TestStatusPlainReportsSnapshotFreshnessLines pins the plain-mode
// output: `paired_device_count: N` and one
// `identity_snapshot.<kind>.fetched_at: <ts>` line per known kind.
func TestStatusPlainReportsSnapshotFreshnessLines(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect: %d", code)
	}
	snapshots, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	connection, err := snapshots.CurrentConnection()
	if err != nil {
		snapshots.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, err := snapshots.Insert(connection, "paired-devices", `{"devices":[{"model":"Pixel Watch 4"}]}`, "2026-06-08T13:00:00Z"); err != nil {
		snapshots.Close()
		t.Fatalf("Insert: %v", err)
	}
	snapshots.Close()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	for _, line := range []string{
		"paired_device_count: 1",
		"identity_snapshot.paired-devices.fetched_at: 2026-06-08T13:00:00Z",
	} {
		if !strings.Contains(stdout.String(), line) {
			t.Errorf("plain output missing %q\n--- stdout ---\n%s", line, stdout.String())
		}
	}
	// Kinds with no snapshot must be omitted (not shown as empty).
	for _, kind := range []string{"profile", "settings", "irn-profile"} {
		if strings.Contains(stdout.String(), "identity_snapshot."+kind+".fetched_at") {
			t.Errorf("plain output unexpectedly includes %s freshness line:\n%s", kind, stdout.String())
		}
	}
}

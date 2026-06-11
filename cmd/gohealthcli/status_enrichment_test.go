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
	connection, err := readCurrentConnection(snapshots.db)
	if err != nil {
		snapshots.Close()
		t.Fatalf("read current Connection: %v", err)
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

// TestStatusJSONReportsTier2CountsAsZeroWhenScopesNotGranted is the
// red test for the #111 Tier 2 slice: status --json always carries a
// `tier_2` block with `electrocardiogram_event_count` and
// `irregular_rhythm_notification_count`. When the user hasn't run
// `connect --add-scopes ecg,irn`, both counts surface as 0 — never
// as an error or a missing field — matching the AC bullet "defaulting
// to 0 when the scopes are not granted".
func TestStatusJSONReportsTier2CountsAsZeroWhenScopesNotGranted(t *testing.T) {
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

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		Tier2 struct {
			ElectrocardiogramEventCount        *int `json:"electrocardiogram_event_count"`
			IrregularRhythmNotificationCount   *int `json:"irregular_rhythm_notification_count"`
			ElectrocardiogramScopeGranted      bool `json:"electrocardiogram_scope_granted"`
			IrregularRhythmNotificationGranted bool `json:"irregular_rhythm_notification_scope_granted"`
		} `json:"tier_2"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout.String())
	}
	if got.Tier2.ElectrocardiogramEventCount == nil {
		t.Fatalf("tier_2.electrocardiogram_event_count missing (want 0)\n%s", stdout.String())
	}
	if *got.Tier2.ElectrocardiogramEventCount != 0 {
		t.Errorf("tier_2.electrocardiogram_event_count = %d, want 0 (scope not granted)", *got.Tier2.ElectrocardiogramEventCount)
	}
	if got.Tier2.IrregularRhythmNotificationCount == nil {
		t.Fatalf("tier_2.irregular_rhythm_notification_count missing (want 0)\n%s", stdout.String())
	}
	if *got.Tier2.IrregularRhythmNotificationCount != 0 {
		t.Errorf("tier_2.irregular_rhythm_notification_count = %d, want 0 (scope not granted)", *got.Tier2.IrregularRhythmNotificationCount)
	}
	if got.Tier2.ElectrocardiogramScopeGranted {
		t.Errorf("tier_2.electrocardiogram_scope_granted = true, want false")
	}
	if got.Tier2.IrregularRhythmNotificationGranted {
		t.Errorf("tier_2.irregular_rhythm_notification_scope_granted = true, want false")
	}
}

// TestStatusPlainOmitsTier2CountsWhenScopesNotGranted pins the plain
// AC: when the Tier 2 scopes are not granted, the
// `electrocardiogram_event_count` and
// `irregular_rhythm_notification_count` lines are omitted entirely
// (matching the snapshot-freshness omitted-when-missing convention
// from PR #128).
func TestStatusPlainOmitsTier2CountsWhenScopesNotGranted(t *testing.T) {
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

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	for _, line := range []string{
		"electrocardiogram_event_count",
		"irregular_rhythm_notification_count",
	} {
		if strings.Contains(stdout.String(), line) {
			t.Errorf("plain output unexpectedly includes %q (scope not granted):\n%s", line, stdout.String())
		}
	}
}

// TestStatusReportsTier2CountsWhenScopesGranted is the green test for
// the #111 Tier 2 slice: after `connect --add-scopes ecg,irn` and one
// archived row per Data Type, status --plain and --json both report
// the row counts under the Tier 2 fields. Plain emits one line per
// Data Type; JSON keeps both fields under `tier_2`.
func TestStatusReportsTier2CountsWhenScopesGranted(t *testing.T) {
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
	addStoredConnectionScope(t, archivePath, googleHealthEcgReadonlyScope)
	addStoredConnectionScope(t, archivePath, googleHealthIrnReadonlyScope)
	insertTier2DataPoint(t, archivePath, "electrocardiogram", "ecg-1")
	insertTier2DataPoint(t, archivePath, "electrocardiogram", "ecg-2")
	insertTier2DataPoint(t, archivePath, "irregular-rhythm-notification", "irn-1")

	jsonStdout := new(bytes.Buffer)
	jsonStderr := new(bytes.Buffer)
	if code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, jsonStdout, jsonStderr); code != 0 {
		t.Fatalf("status --json exit = %d, stderr=%s", code, jsonStderr.String())
	}
	var got struct {
		Tier2 struct {
			ElectrocardiogramEventCount        int  `json:"electrocardiogram_event_count"`
			IrregularRhythmNotificationCount   int  `json:"irregular_rhythm_notification_count"`
			ElectrocardiogramScopeGranted      bool `json:"electrocardiogram_scope_granted"`
			IrregularRhythmNotificationGranted bool `json:"irregular_rhythm_notification_scope_granted"`
		} `json:"tier_2"`
	}
	if err := json.Unmarshal(jsonStdout.Bytes(), &got); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, jsonStdout.String())
	}
	if got.Tier2.ElectrocardiogramEventCount != 2 {
		t.Errorf("tier_2.electrocardiogram_event_count = %d, want 2", got.Tier2.ElectrocardiogramEventCount)
	}
	if got.Tier2.IrregularRhythmNotificationCount != 1 {
		t.Errorf("tier_2.irregular_rhythm_notification_count = %d, want 1", got.Tier2.IrregularRhythmNotificationCount)
	}
	if !got.Tier2.ElectrocardiogramScopeGranted {
		t.Errorf("tier_2.electrocardiogram_scope_granted = false, want true")
	}
	if !got.Tier2.IrregularRhythmNotificationGranted {
		t.Errorf("tier_2.irregular_rhythm_notification_scope_granted = false, want true")
	}

	plainStdout := new(bytes.Buffer)
	plainStderr := new(bytes.Buffer)
	if code := run([]string{"status", "--config", configPath, "--db", archivePath, "--plain"}, plainStdout, plainStderr); code != 0 {
		t.Fatalf("status --plain exit = %d, stderr=%s", code, plainStderr.String())
	}
	for _, line := range []string{
		"electrocardiogram_event_count: 2",
		"irregular_rhythm_notification_count: 1",
	} {
		if !strings.Contains(plainStdout.String(), line) {
			t.Errorf("plain output missing %q\n--- stdout ---\n%s", line, plainStdout.String())
		}
	}
}

// TestStatusReportsTier2CountsWithPartialScopeGrant pins the
// partial-scope branch (#111 AC: "tests cover ... no Tier 2 scopes",
// extended here to the realistic case where the user has granted one
// of the two): with only the ECG scope granted, JSON keeps both
// fields but flips only `electrocardiogram_scope_granted=true`, and
// `--plain` emits only the ECG line.
func TestStatusReportsTier2CountsWithPartialScopeGrant(t *testing.T) {
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
	// Only grant the ECG scope — IRN stays missing.
	addStoredConnectionScope(t, archivePath, googleHealthEcgReadonlyScope)
	insertTier2DataPoint(t, archivePath, "electrocardiogram", "ecg-1")

	jsonStdout := new(bytes.Buffer)
	jsonStderr := new(bytes.Buffer)
	if code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, jsonStdout, jsonStderr); code != 0 {
		t.Fatalf("status --json exit = %d, stderr=%s", code, jsonStderr.String())
	}
	var got struct {
		Tier2 struct {
			ElectrocardiogramEventCount             int  `json:"electrocardiogram_event_count"`
			IrregularRhythmNotificationCount        int  `json:"irregular_rhythm_notification_count"`
			ElectrocardiogramScopeGranted           bool `json:"electrocardiogram_scope_granted"`
			IrregularRhythmNotificationScopeGranted bool `json:"irregular_rhythm_notification_scope_granted"`
		} `json:"tier_2"`
	}
	if err := json.Unmarshal(jsonStdout.Bytes(), &got); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, jsonStdout.String())
	}
	if got.Tier2.ElectrocardiogramEventCount != 1 {
		t.Errorf("tier_2.electrocardiogram_event_count = %d, want 1", got.Tier2.ElectrocardiogramEventCount)
	}
	if got.Tier2.IrregularRhythmNotificationCount != 0 {
		t.Errorf("tier_2.irregular_rhythm_notification_count = %d, want 0", got.Tier2.IrregularRhythmNotificationCount)
	}
	if !got.Tier2.ElectrocardiogramScopeGranted {
		t.Errorf("tier_2.electrocardiogram_scope_granted = false, want true")
	}
	if got.Tier2.IrregularRhythmNotificationScopeGranted {
		t.Errorf("tier_2.irregular_rhythm_notification_scope_granted = true, want false")
	}

	plainStdout := new(bytes.Buffer)
	plainStderr := new(bytes.Buffer)
	if code := run([]string{"status", "--config", configPath, "--db", archivePath, "--plain"}, plainStdout, plainStderr); code != 0 {
		t.Fatalf("status --plain exit = %d, stderr=%s", code, plainStderr.String())
	}
	if !strings.Contains(plainStdout.String(), "electrocardiogram_event_count: 1") {
		t.Errorf("plain output missing electrocardiogram_event_count: 1\n%s", plainStdout.String())
	}
	if strings.Contains(plainStdout.String(), "irregular_rhythm_notification_count") {
		t.Errorf("plain output unexpectedly includes irregular_rhythm_notification_count (scope not granted)\n%s", plainStdout.String())
	}
}

// insertTier2DataPoint writes one minimal data_points row for a Tier 2
// Data Type so the status reader's count query has something to find.
// The row is intentionally bare — only the fields the count query
// (and the existing readStatusDataTypes UNION) reference are filled.
func insertTier2DataPoint(t *testing.T, archivePath, dataType, resourceID string) {
	t.Helper()
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO data_points (
		provider_name,
		connection_id,
		data_type,
		upstream_resource_name,
		record_kind,
		start_time_utc,
		end_time_utc,
		data_source_json,
		raw_json,
		inserted_at,
		updated_at
	) VALUES ('googlehealth', 'googlehealth:111111256096816351', ?, ?, 'session', '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z', '{}', '{}', '2026-01-01T00:01:00Z', '2026-01-01T00:01:00Z')`,
		dataType, "users/me/dataTypes/"+dataType+"/dataPoints/"+resourceID); err != nil {
		t.Fatalf("insert %s row: %v", dataType, err)
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
	connection, err := readCurrentConnection(snapshots.db)
	if err != nil {
		snapshots.Close()
		t.Fatalf("read current Connection: %v", err)
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

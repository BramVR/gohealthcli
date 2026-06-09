package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestStatusJSONHasTopLevelKnownDataTypes pins PRD #144 slice 9: the
// `--json` output carries a top-level `known_data_types` array
// containing every Data Type name, matching the comma list emitted by
// `--plain`. A consumer who picks `--json` must not lose this field.
func TestStatusJSONHasTopLevelKnownDataTypes(t *testing.T) {
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
	// Two Data Types so the array is non-trivial.
	insertTier2DataPoint(t, archivePath, "steps", "steps-1")
	insertTier2DataPoint(t, archivePath, "heart-rate", "hr-1")

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		KnownDataTypes []string `json:"known_data_types"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout.String())
	}
	if len(got.KnownDataTypes) == 0 {
		t.Fatalf("known_data_types missing or empty in --json output\n%s", stdout.String())
	}
	want := map[string]bool{"steps": true, "heart-rate": true}
	for _, name := range got.KnownDataTypes {
		delete(want, name)
	}
	if len(want) != 0 {
		t.Errorf("known_data_types %v missing entries %v", got.KnownDataTypes, want)
	}

	// Parity with --plain: the comma-list on `known_data_types:` line
	// matches the JSON array (order included).
	plainStdout := new(bytes.Buffer)
	plainStderr := new(bytes.Buffer)
	if code := run([]string{"status", "--config", configPath, "--db", archivePath, "--plain"}, plainStdout, plainStderr); code != 0 {
		t.Fatalf("status --plain exit = %d, stderr=%s", code, plainStderr.String())
	}
	plainList := extractPlainKnownDataTypes(t, plainStdout.String())
	if !equalStringSlices(plainList, got.KnownDataTypes) {
		t.Errorf("known_data_types parity mismatch:\n  plain: %v\n  json:  %v", plainList, got.KnownDataTypes)
	}
}

// TestStatusJSONHasTopLevelPairedDeviceCount pins PRD #144 slice 9:
// `--json` carries `paired_device_count` as a top-level integer,
// matching `--plain`. The existing nested location is preserved.
func TestStatusJSONHasTopLevelPairedDeviceCount(t *testing.T) {
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
	if _, err := snapshots.Insert(connection, "paired-devices", `{"devices":[{"model":"Pixel Watch 2"},{"model":"Pixel 8"},{"model":"Pixel Watch 4"}]}`, "2026-06-08T00:00:00Z"); err != nil {
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
		PairedDeviceCount *int `json:"paired_device_count"`
		IdentitySnapshots struct {
			PairedDeviceCount int `json:"paired_device_count"`
		} `json:"identity_snapshots_freshness"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout.String())
	}
	if got.PairedDeviceCount == nil {
		t.Fatalf("top-level paired_device_count missing in --json output\n%s", stdout.String())
	}
	if *got.PairedDeviceCount != 3 {
		t.Errorf("top-level paired_device_count = %d, want 3", *got.PairedDeviceCount)
	}
	// Back-compat: existing nested location must still carry the count.
	if got.IdentitySnapshots.PairedDeviceCount != 3 {
		t.Errorf("identity_snapshots_freshness.paired_device_count = %d, want 3 (back-compat)", got.IdentitySnapshots.PairedDeviceCount)
	}
}

// TestStatusJSONOmitsPairedDeviceCountWhenZero asserts that the
// top-level field uses omitempty when no paired-devices snapshot is
// archived, so a clean archive does not advertise a misleading
// `paired_device_count: 0`. Mirrors the omitted-when-missing
// convention used by `identity_snapshots_freshness`.
func TestStatusJSONOmitsPairedDeviceCountWhenZero(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	// Decode into a generic map so we can assert presence-or-absence
	// (omitempty contract) rather than value.
	var raw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout.String())
	}
	if _, ok := raw["paired_device_count"]; ok {
		t.Errorf("--json includes paired_device_count on a clean archive; want omitted\n%s", stdout.String())
	}
}

// TestStatusJSONOmitsKnownDataTypesWhenEmpty asserts that the
// top-level `known_data_types` field is omitted on a clean archive
// (mirroring the existing `data_types` omitempty contract and the
// plain writer, which only emits the line when there are Data Types).
func TestStatusJSONOmitsKnownDataTypesWhenEmpty(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout.String())
	}
	if _, ok := raw["known_data_types"]; ok {
		t.Errorf("--json includes known_data_types on a clean archive; want omitted\n%s", stdout.String())
	}
}

// TestStatusPlainOutputPreservedAfterParityChange is the byte-identical
// golden for the plain writer: the same archive that surfaced
// `known_data_types: steps,heart-rate` before slice 9 must still surface
// the exact same line afterwards. Captures every meaningful
// `--plain` line on a populated archive so any drift fails the test.
func TestStatusPlainOutputPreservedAfterParityChange(t *testing.T) {
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
	insertTier2DataPoint(t, archivePath, "steps", "steps-1")
	insertTier2DataPoint(t, archivePath, "heart-rate", "hr-1")
	snapshots, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	connection, err := snapshots.CurrentConnection()
	if err != nil {
		snapshots.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, err := snapshots.Insert(connection, "paired-devices", `{"devices":[{"model":"Pixel Watch 2"}]}`, "2026-06-08T00:00:00Z"); err != nil {
		snapshots.Close()
		t.Fatalf("Insert paired-devices: %v", err)
	}
	snapshots.Close()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	for _, want := range []string{
		"status: ok\n",
		"data_point_count: 2\n",
		"known_data_types: heart-rate,steps\n",
		"data_type.heart-rate.data_point_count: 1\n",
		"data_type.steps.data_point_count: 1\n",
		"paired_device_count: 1\n",
		"identity_snapshot.paired-devices.fetched_at: 2026-06-08T00:00:00Z\n",
		"message: Health Archive status summarized\n",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("plain output missing %q\n--- stdout ---\n%s", want, stdout.String())
		}
	}
}

// TestStatusPlainAndJSONKeyParity is the documented-transformation
// test from PRD #144 slice 9 AC: every plain key has a matching JSON
// key (top-level or under a documented nested path), and every JSON
// key has a matching plain key. Lets future slices add fields with
// confidence that the two modes stay in lockstep.
func TestStatusPlainAndJSONKeyParity(t *testing.T) {
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
	insertTier2DataPoint(t, archivePath, "steps", "steps-1")
	snapshots, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	connection, err := snapshots.CurrentConnection()
	if err != nil {
		snapshots.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, err := snapshots.Insert(connection, "paired-devices", `{"devices":[{"model":"Pixel Watch 2"}]}`, "2026-06-08T00:00:00Z"); err != nil {
		snapshots.Close()
		t.Fatalf("Insert paired-devices: %v", err)
	}
	snapshots.Close()

	plainStdout := new(bytes.Buffer)
	plainStderr := new(bytes.Buffer)
	if code := run([]string{"status", "--config", configPath, "--db", archivePath, "--plain"}, plainStdout, plainStderr); code != 0 {
		t.Fatalf("status --plain exit = %d, stderr=%s", code, plainStderr.String())
	}
	jsonStdout := new(bytes.Buffer)
	jsonStderr := new(bytes.Buffer)
	if code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, jsonStdout, jsonStderr); code != 0 {
		t.Fatalf("status --json exit = %d, stderr=%s", code, jsonStderr.String())
	}

	plainKeys := extractPlainStatusKeys(plainStdout.String())
	var jsonResult map[string]any
	if err := json.Unmarshal(jsonStdout.Bytes(), &jsonResult); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, jsonStdout.String())
	}

	// The documented transformation between plain and JSON: plain
	// keys with these dotted prefixes nest under a structured JSON
	// shape rather than appearing at top level. Both must exist in
	// JSON via the nested path the prefix maps to.
	plainOnlyTopLevel := []string{}
	for _, key := range plainKeys {
		if block, ok := plainKeyNestedBlock(key); ok {
			if _, present := jsonResult[block]; !present {
				t.Errorf("plain key %q maps to nested JSON block %q which is missing\nplain: %s\njson keys: %v", key, block, plainStdout.String(), sortedJSONKeys(jsonResult))
			}
			continue
		}
		plainOnlyTopLevel = append(plainOnlyTopLevel, key)
	}
	for _, key := range plainOnlyTopLevel {
		if _, ok := jsonResult[key]; !ok {
			t.Errorf("plain key %q has no matching top-level JSON key\nplain: %s", key, plainStdout.String())
		}
	}

	// Reverse direction: every top-level JSON key must either appear
	// as a plain key or be a known structured block whose contents
	// are flattened into plain keys (data_types, sync_runs,
	// freshness, tier_2).
	plainKeySet := map[string]bool{}
	for _, key := range plainKeys {
		plainKeySet[key] = true
	}
	for key := range jsonResult {
		if jsonStructuredBlock(key) {
			continue
		}
		if !plainKeySet[key] {
			t.Errorf("JSON top-level key %q has no matching plain key\nplain: %s", key, plainStdout.String())
		}
	}
}

// extractPlainKnownDataTypes pulls the comma list off the
// `known_data_types: ...` line in plain output, or fails the test if
// the line is missing.
func extractPlainKnownDataTypes(t *testing.T, plain string) []string {
	t.Helper()
	for _, line := range strings.Split(plain, "\n") {
		if rest, ok := strings.CutPrefix(line, "known_data_types: "); ok {
			return strings.Split(rest, ",")
		}
	}
	t.Fatalf("plain output missing known_data_types: line\n%s", plain)
	return nil
}

// extractPlainStatusKeys returns the set of top-level plain keys
// (everything left of the first `:` per line, skipping blank lines).
// Dotted keys like `data_type.steps.data_point_count` and
// `identity_snapshot.profile.fetched_at` are returned as-is; the
// parity test classifies them via plainKeyNestedBlock.
func extractPlainStatusKeys(plain string) []string {
	var keys []string
	keyLineRE := regexp.MustCompile(`^([a-z0-9_.-]+):`)
	for _, line := range strings.Split(plain, "\n") {
		match := keyLineRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		keys = append(keys, match[1])
	}
	sort.Strings(keys)
	return keys
}

// plainKeyNestedBlock returns the top-level JSON block a flattened
// plain key maps to, plus ok=true. The parity test uses it to assert
// the corresponding JSON block actually exists rather than silently
// skipping the plain key, which would let nested-shape drift slip
// through unnoticed.
func plainKeyNestedBlock(plainKey string) (string, bool) {
	switch {
	case strings.HasPrefix(plainKey, "data_type."):
		return "data_types", true
	case strings.HasPrefix(plainKey, "identity_snapshot."):
		return "identity_snapshots_freshness", true
	case strings.HasPrefix(plainKey, "latest_successful_sync_run_"):
		return "latest_successful_sync_run", true
	case strings.HasPrefix(plainKey, "latest_failed_sync_run_"):
		return "latest_failed_sync_run", true
	}
	return "", false
}

func sortedJSONKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonStructuredBlock returns true when a JSON top-level key is a
// structured block whose contents are flattened into plain keys by
// the plain writer (data_types -> data_type.<name>.*, etc.) and so is
// not expected to appear as a plain top-level key on its own.
func jsonStructuredBlock(jsonKey string) bool {
	switch jsonKey {
	case "data_types",
		"identity_snapshots_freshness",
		"tier_2",
		"latest_successful_sync_run",
		"latest_failed_sync_run":
		return true
	}
	return false
}

// equalStringSlices is a small helper for ordered comparisons in
// parity assertions; we want the JSON array to match the plain comma
// list exactly, not just as a set.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

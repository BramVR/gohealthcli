package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
	"strings"
	"testing"
	"time"
)

// TestDevicesCommandRendersPerDeviceFieldsInJSONAndPlain pins the
// behaviour that --json and --plain modes emit per-device fields, not
// just an aggregate device_count. The PR's AC requires this so a user
// can read 'gohealthcli devices' output without also running a SQL
// query against paired_devices. The fixtures use the real
// users.pairedDevices.list shape verified against a live archive on
// 2026-06-11 (#298): the list lives under `pairedDevices` and each
// device carries name / deviceType / batteryStatus / batteryLevel /
// deviceVersion.
func TestDevicesCommandRendersPerDeviceFieldsInJSONAndPlain(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	// PRD #142 slice 2 / #176: pairedDevices now requires
	// settings.readonly, so simulate the user having run
	// `connect --add-scopes settings`.
	addStoredConnectionScope(t, archivePath, googlehealth.ScopeSettingsReadonly)

	testRuntime.fetchPairedDevices = func(string) (googlePairedDevices, error) {
		return googlePairedDevices{
			rawJSON: `{"pairedDevices":[
				{"name":"users/111111256096816351/pairedDevices/2978855095","deviceType":"TRACKER","batteryStatus":"Medium","batteryLevel":50,"deviceVersion":"Google Pixel Watch 4"},
				{"name":"users/111111256096816351/pairedDevices/1122334455","deviceType":"SCALE","batteryStatus":"High","deviceVersion":"Withings Body+"}
			]}`,
		}, nil
	}

	jsonOut := new(bytes.Buffer)
	if code := runWithRuntime([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, jsonOut, new(bytes.Buffer), testRuntime); code != 0 {
		t.Fatalf("devices --json exit code = %d, stdout=%s", code, jsonOut.String())
	}
	var jsonResult struct {
		DeviceCount int `json:"device_count"`
		Devices     []struct {
			Name          string `json:"name"`
			DeviceType    string `json:"device_type"`
			DeviceVersion string `json:"device_version"`
			BatteryStatus string `json:"battery_status"`
			BatteryLevel  *int   `json:"battery_level"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &jsonResult); err != nil {
		t.Fatalf("decode devices --json: %v\n%s", err, jsonOut.String())
	}
	if jsonResult.DeviceCount != 2 {
		t.Fatalf("DeviceCount = %d, want 2", jsonResult.DeviceCount)
	}
	if len(jsonResult.Devices) != 2 {
		t.Fatalf("Devices len = %d, want 2 (per-device fields must surface in --json)", len(jsonResult.Devices))
	}
	if jsonResult.Devices[0].DeviceVersion != "Google Pixel Watch 4" {
		t.Fatalf("Devices[0].DeviceVersion = %q, want Google Pixel Watch 4", jsonResult.Devices[0].DeviceVersion)
	}
	if jsonResult.Devices[0].Name != "users/111111256096816351/pairedDevices/2978855095" {
		t.Fatalf("Devices[0].Name = %q, want the upstream resource name", jsonResult.Devices[0].Name)
	}
	if jsonResult.Devices[0].BatteryLevel == nil || *jsonResult.Devices[0].BatteryLevel != 50 {
		t.Fatalf("Devices[0].BatteryLevel missing or wrong; want 50")
	}
	if jsonResult.Devices[1].BatteryLevel != nil {
		t.Fatalf("Devices[1].BatteryLevel = %d, want omitted when upstream has none", *jsonResult.Devices[1].BatteryLevel)
	}

	plainOut := new(bytes.Buffer)
	testRuntime.fetchPairedDevices = func(string) (googlePairedDevices, error) {
		return googlePairedDevices{
			rawJSON: `{"pairedDevices":[{"name":"users/111111256096816351/pairedDevices/2978855095","deviceType":"TRACKER","batteryStatus":"Medium","batteryLevel":50,"deviceVersion":"Google Pixel Watch 4"}]}`,
		}, nil
	}
	if code := runWithRuntime([]string{"devices", "--config", configPath, "--db", archivePath, "--plain"}, plainOut, new(bytes.Buffer), testRuntime); code != 0 {
		t.Fatalf("devices --plain exit code = %d, stdout=%s", code, plainOut.String())
	}
	want := []string{
		"device_count: 1",
		"devices.0.name: users/111111256096816351/pairedDevices/2978855095",
		"devices.0.device_type: TRACKER",
		"devices.0.device_version: Google Pixel Watch 4",
		"devices.0.battery_status: Medium",
		"devices.0.battery_level: 50",
	}
	for _, line := range want {
		if !strings.Contains(plainOut.String(), line) {
			t.Errorf("plain output missing %q\n--- full stdout ---\n%s", line, plainOut.String())
		}
	}
}

// TestPairedDevicesViewExplodesDevicesViaJSONEach is the slice B
// behaviour: the paired_devices view returns one row per device from
// the latest paired-devices snapshot, with the contracted columns.
func TestPairedDevicesViewExplodesDevicesViaJSONEach(t *testing.T) {
	t.Parallel()
	_, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	connection, err := readCurrentConnection(context.Background(), archive.db)
	if err != nil {
		archive.Close()
		t.Fatalf("read current Connection: %v", err)
	}
	payload := `{"pairedDevices":[
		{"name":"users/111111256096816351/pairedDevices/2978855095","deviceType":"TRACKER","batteryStatus":"Medium","batteryLevel":50,"deviceVersion":"Google Pixel Watch 4"},
		{"name":"users/111111256096816351/pairedDevices/1122334455","deviceType":"SCALE","batteryStatus":"High","deviceVersion":"Withings Body+"}
	]}`
	if _, err := archive.Insert(context.Background(), connection, "paired-devices", payload, "2026-06-08T13:00:00Z"); err != nil {
		archive.Close()
		t.Fatalf("Insert: %v", err)
	}
	archive.Close()

	db := openArchiveForTest(t, archivePath)
	rows, err := db.QueryContext(context.Background(), `SELECT name, device_type, device_version, battery_status, battery_level FROM paired_devices ORDER BY device_version`)
	if err != nil {
		t.Fatalf("query paired_devices: %v", err)
	}
	defer rows.Close()
	type devRow struct {
		name, typ, version, batteryStatus string
		batteryLevel                      sql.NullInt64
	}
	var got []devRow
	for rows.Next() {
		var row devRow
		if err := rows.Scan(&row.name, &row.typ, &row.version, &row.batteryStatus, &row.batteryLevel); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, row)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got[0].version != "Google Pixel Watch 4" || got[1].version != "Withings Body+" {
		t.Fatalf("device_versions = (%q, %q), want (Google Pixel Watch 4, Withings Body+)", got[0].version, got[1].version)
	}
	if got[0].name != "users/111111256096816351/pairedDevices/2978855095" {
		t.Fatalf("name = %q, want the upstream resource name", got[0].name)
	}
	if !got[0].batteryLevel.Valid || got[0].batteryLevel.Int64 != 50 {
		t.Fatalf("Pixel Watch 4 battery_level = %v, want 50", got[0].batteryLevel)
	}
	if got[1].batteryLevel.Valid {
		t.Fatalf("Withings Body+ battery_level = %d, want NULL when upstream has none", got[1].batteryLevel.Int64)
	}
	if got[1].batteryStatus != "High" {
		t.Fatalf("Withings Body+ battery_status = %q, want High", got[1].batteryStatus)
	}
}

// TestPairedDevicesViewHandlesEmptyDeviceList pins the edge case where
// the latest paired-devices snapshot has no devices: the view returns
// zero rows, not an error. Covers both an explicit empty list and a
// bare {} payload (the key is omitted upstream when nothing is paired).
func TestPairedDevicesViewHandlesEmptyDeviceList(t *testing.T) {
	t.Parallel()
	_, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	connection, err := readCurrentConnection(context.Background(), archive.db)
	if err != nil {
		archive.Close()
		t.Fatalf("read current Connection: %v", err)
	}
	for _, payload := range []string{`{"pairedDevices":[]}`, `{}`} {
		if _, err := archive.Insert(context.Background(), connection, "paired-devices", payload, "2026-06-08T13:00:00Z"); err != nil {
			archive.Close()
			t.Fatalf("Insert %s: %v", payload, err)
		}
	}
	archive.Close()

	db := openArchiveForTest(t, archivePath)
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM paired_devices`).Scan(&count); err != nil {
		t.Fatalf("query paired_devices: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0 for empty devices list", count)
	}
}

// TestDevicesCommandArchivesSnapshotWithKindPairedDevices is the slice
// A tracer for #98: `gohealthcli devices` calls
// users.pairedDevices.list and archives the payload through the
// Identity Snapshot Archive with kind='paired-devices'.
func TestDevicesCommandArchivesSnapshotWithKindPairedDevices(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	// PRD #142 slice 2 / #176: pairedDevices now requires
	// settings.readonly, so simulate the user having run
	// `connect --add-scopes settings`.
	addStoredConnectionScope(t, archivePath, googlehealth.ScopeSettingsReadonly)

	testRuntime.fetchPairedDevices = func(string) (googlePairedDevices, error) {
		return googlePairedDevices{
			rawJSON: `{"pairedDevices":[{"name":"users/111111256096816351/pairedDevices/2978855095","deviceType":"TRACKER","batteryStatus":"Medium","batteryLevel":50,"deviceVersion":"Google Pixel Watch 4"}]}`,
		}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("devices exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open identity snapshot archive: %v", err)
	}
	defer archive.Close()
	connection, err := readCurrentConnection(context.Background(), archive.db)
	if err != nil {
		t.Fatalf("read current Connection: %v", err)
	}
	latest, found := latestIdentitySnapshotRow(t, archive.db, connection.ID, "paired-devices")
	if !found {
		t.Fatal("latest paired-devices snapshot: not found")
	}
	if latest.Kind != "paired-devices" {
		t.Fatalf("Kind = %q, want paired-devices", latest.Kind)
	}
	if latest.RawJSON == "" {
		t.Fatalf("RawJSON empty; want round-tripped paired-devices payload")
	}
}

// TestDevicesCommandFailsFastWhenScopeMissing pins the PRD #142 slice 3
// AC: when the stored Connection's granted scopes do not cover the
// scope the devices verb requires (whatever the catalog says today),
// the command exits non-zero, sets result.Status to
// "devices_scope_missing", names the recovery `gohealthcli connect`
// command in result.Message, and crucially does NOT issue any HTTP
// request to googlehealth.PairedDevicesURL — proving the scope
// pre-check happens before the upstream call. The test reads the
// required scope from the same googlehealth.IdentityEndpointScopes
// catalog the production code uses so a future slice-2 revision of
// the catalog automatically updates what gets stripped from the
// stored Connection, keeping the test honest without manual edits.
func TestDevicesCommandFailsFastWhenScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	// Strip every scope the catalog ties to pairedDevices from the
	// stored Connection so AccessToken's scope pre-check fails. Using
	// the same catalog key the production code reads means this test
	// keeps pinning the right behaviour after slice 2 rewrites the
	// catalog entry.
	required := googlehealth.IdentityEndpointScopes("pairedDevices")
	requiredSet := make(map[string]struct{}, len(required))
	for _, scope := range required {
		requiredSet[scope] = struct{}{}
	}
	storedScopes := connectionGrantedScopes(t, archivePath)
	filtered := storedScopes[:0]
	for _, scope := range storedScopes {
		if _, drop := requiredSet[scope]; drop {
			continue
		}
		filtered = append(filtered, scope)
	}
	setConnectionTokenScopes(t, archivePath, filtered)

	// fetchPairedDevices MUST NOT be called when the scope is missing —
	// the bare HTTP 403 PRD #142 documents only happens because the
	// pre-check is absent, so guarding the seam is what proves the
	// migration shut that path down.
	testRuntime.fetchPairedDevices = func(string) (googlePairedDevices, error) {
		t.Fatal("fetchPairedDevices called despite missing scope")
		return googlePairedDevices{}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatalf("devices exit code = %d, want non-zero; stdout=%s", code, stdout.String())
	}
	var result devicesResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "devices_scope_missing" {
		t.Fatalf("result.Status = %q, want devices_scope_missing", result.Status)
	}
	// The recovery hint either names `--add-scopes <keyword>` (when the
	// missing scope maps to a connectAddScopeKeywords entry) or names
	// `gohealthcli connect` again (generic fallback). Either way, the
	// message must mention `connect` — that single substring covers
	// both shapes and survives a slice-2 catalog rewrite.
	if !strings.Contains(result.Message, "gohealthcli connect") {
		t.Fatalf("result.Message = %q, want it to name `gohealthcli connect` recovery", result.Message)
	}
	keywords := addScopeKeywordsForScopes(required)
	if len(keywords) == len(required) && len(keywords) > 0 {
		wantHint := "--add-scopes " + strings.Join(keywords, ",")
		if !strings.Contains(result.Message, wantHint) {
			t.Fatalf("result.Message = %q, want it to name %q", result.Message, wantHint)
		}
	}
}

// TestDevicesCommandAutoRefreshesExpiredAccessToken pins the AC for
// PRD #142 slice 3: with an expired access token but valid refresh
// token and oauthClient.kind == "file", devices refreshes
// transparently, persists the new token via UpdateConnectionTokenMetadata
// on the archive (the same handle openHealthArchiveConnectionAPI
// already returns), and exits 0 with status "devices_archived" plus
// a new identity_snapshots row whose snapshot_kind = 'paired-devices'.
func TestDevicesCommandAutoRefreshesExpiredAccessToken(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	connectAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                connectAt,
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnectSetup(t, configPath, archivePath, testRuntime)
	// PRD #142 slice 2 / #176: pairedDevices now requires
	// settings.readonly, so simulate the user having run
	// `connect --add-scopes settings`.
	addStoredConnectionScope(t, archivePath, googlehealth.ScopeSettingsReadonly)
	// Force the stored access-token expires_at into the past so
	// AccessToken must take the auto-refresh path.
	setConnectionTokenExpiry(t, archivePath, "2026-01-01T00:00:00Z")

	devicesNow := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	refreshedExpiresAt := devicesNow.Add(time.Hour)
	testRuntime.now = func() time.Time { return devicesNow }
	// Count refresh attempts so the implicit "refresh once, persist
	// once" contract is guarded against a regression where retries
	// would silently double-rotate the stored token.
	refreshCalls := 0
	bindRefreshOAuthTokenFake(t, &testRuntime, fakeRefreshConfig{
		wantRefreshToken: "connect-refresh-secret",
		accessToken:      "rotated-access-secret",
		expiresAt:        refreshedExpiresAt,
		calls:            &refreshCalls,
	})

	var calledWithToken string
	testRuntime.fetchPairedDevices = func(accessToken string) (googlePairedDevices, error) {
		calledWithToken = accessToken
		return googlePairedDevices{
			rawJSON: `{"pairedDevices":[{"name":"users/111111256096816351/pairedDevices/2978855095","deviceType":"TRACKER","batteryStatus":"Medium","batteryLevel":50,"deviceVersion":"Google Pixel Watch 4"}]}`,
		}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("devices exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}
	if calledWithToken != "rotated-access-secret" {
		t.Fatalf("fetchPairedDevices access token = %q, want rotated-access-secret", calledWithToken)
	}

	var result devicesResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "devices_archived" {
		t.Fatalf("result.Status = %q, want devices_archived", result.Status)
	}

	// Refreshed expires_at must have been persisted to the archive's
	// token_metadata_json via UpdateConnectionTokenMetadata.
	gotMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if !strings.Contains(gotMetadata, refreshedExpiresAt.Format(time.RFC3339)) {
		t.Fatalf("archived token_metadata_json = %s, want refreshed expires_at %s", gotMetadata, refreshedExpiresAt.Format(time.RFC3339))
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshOAuthToken call count = %d, want 1 (no retry loop should double-rotate the stored token)", refreshCalls)
	}

	// A new identity_snapshots row with snapshot_kind = 'paired-devices'
	// must exist so the auto-refresh path doesn't silently skip the
	// archive write the AC requires.
	db := openArchiveForTest(t, archivePath)
	var snapshotCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM identity_snapshots WHERE snapshot_kind = 'paired-devices'`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count paired-devices snapshots: %v", err)
	}
	if snapshotCount != 1 {
		t.Fatalf("paired-devices snapshot count = %d, want 1", snapshotCount)
	}
}

// connectionGrantedScopes reads the stored Connection's token_metadata_json
// scopes array. Used by the scope-missing test to discover what was
// granted at connect time before stripping the catalog-required scope.
func connectionGrantedScopes(t *testing.T, archivePath string) []string {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal([]byte(archivedConnectionTokenMetadata(t, archivePath)), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	raw, ok := metadata["scopes"].([]any)
	if !ok {
		return nil
	}
	scopes := make([]string, 0, len(raw))
	for _, value := range raw {
		if scope, ok := value.(string); ok {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// TestDevicesRejectsNoInputFlag pins issue #171: the dead --no-input flag
// is removed from devices' spec. The command never blocks on browser
// input, so accepting --no-input would imply a behaviour it does not
// have. Passing it now produces the Common Flag Set's targeted
// "--no-input is not supported by devices" rejection and exits non-zero.
func TestDevicesRejectsNoInputFlag(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "devices", "--no-input")
	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	const want = "--no-input is not supported by devices"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want substring %q", stderr.String(), want)
	}
}

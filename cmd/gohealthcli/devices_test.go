package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestDevicesCommandRendersPerDeviceFieldsInJSONAndPlain pins the
// behaviour that --json and --plain modes emit per-device fields, not
// just an aggregate device_count. The PR's AC requires this so a user
// can read 'gohealthcli devices' output without also running a SQL
// query against paired_devices.
func TestDevicesCommandRendersPerDeviceFieldsInJSONAndPlain(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}
	// PRD #142 slice 2 / #176: pairedDevices now requires
	// settings.readonly, so simulate the user having run
	// `connect --add-scopes settings`.
	addStoredConnectionScope(t, archivePath, googleHealthSettingsReadonlyScope)

	originalFetchPairedDevices := fetchPairedDevices
	fetchPairedDevices = func(string) (googlePairedDevices, error) {
		return googlePairedDevices{
			rawJSON: `{"devices":[
				{"deviceType":"WATCH","model":"Pixel Watch 2","manufacturer":"Google","batteryPercentage":76,"lastSyncTime":"2026-06-08T13:00:00Z","features":["HR","GPS"]},
				{"deviceType":"PHONE","model":"Pixel 8","manufacturer":"Google","batteryPercentage":42,"lastSyncTime":"2026-06-08T12:30:00Z","features":["STEPS"]}
			]}`,
		}, nil
	}
	t.Cleanup(func() { fetchPairedDevices = originalFetchPairedDevices })

	jsonOut := new(bytes.Buffer)
	if code := run([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, jsonOut, new(bytes.Buffer)); code != 0 {
		t.Fatalf("devices --json exit code = %d, stdout=%s", code, jsonOut.String())
	}
	var jsonResult struct {
		DeviceCount int `json:"device_count"`
		Devices     []struct {
			DeviceType        string   `json:"device_type"`
			Model             string   `json:"model"`
			Manufacturer      string   `json:"manufacturer"`
			BatteryPercentage *int     `json:"battery_percentage"`
			LastSyncTime      string   `json:"last_sync_time"`
			Features          []string `json:"features"`
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
	if jsonResult.Devices[0].Model != "Pixel Watch 2" {
		t.Fatalf("Devices[0].Model = %q, want Pixel Watch 2", jsonResult.Devices[0].Model)
	}
	if jsonResult.Devices[0].BatteryPercentage == nil || *jsonResult.Devices[0].BatteryPercentage != 76 {
		t.Fatalf("Devices[0].BatteryPercentage missing or wrong; want 76")
	}

	plainOut := new(bytes.Buffer)
	fetchPairedDevices = func(string) (googlePairedDevices, error) {
		return googlePairedDevices{
			rawJSON: `{"devices":[{"deviceType":"WATCH","model":"Pixel Watch 2","manufacturer":"Google","batteryPercentage":76,"lastSyncTime":"2026-06-08T13:00:00Z","features":["HR","GPS"]}]}`,
		}, nil
	}
	if code := run([]string{"devices", "--config", configPath, "--db", archivePath, "--plain"}, plainOut, new(bytes.Buffer)); code != 0 {
		t.Fatalf("devices --plain exit code = %d, stdout=%s", code, plainOut.String())
	}
	want := []string{
		"devices.0.device_type: WATCH",
		"devices.0.model: Pixel Watch 2",
		"devices.0.manufacturer: Google",
		"devices.0.battery_percentage: 76",
		"devices.0.last_sync_time: 2026-06-08T13:00:00Z",
		"devices.0.features: HR,GPS",
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
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	connection, err := archive.CurrentConnection()
	if err != nil {
		archive.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	payload := `{"devices":[
		{"deviceType":"WATCH","model":"Pixel Watch 2","manufacturer":"Google","batteryPercentage":76,"lastSyncTime":"2026-06-08T13:00:00Z","features":["HR","GPS"]},
		{"deviceType":"PHONE","model":"Pixel 8","manufacturer":"Google","batteryPercentage":42,"lastSyncTime":"2026-06-08T12:30:00Z","features":["STEPS"]}
	]}`
	if _, err := archive.Insert(connection, "paired-devices", payload, "2026-06-08T13:00:00Z"); err != nil {
		archive.Close()
		t.Fatalf("Insert: %v", err)
	}
	archive.Close()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT device_type, model, manufacturer, battery_percentage, last_sync_time FROM paired_devices ORDER BY model`)
	if err != nil {
		t.Fatalf("query paired_devices: %v", err)
	}
	defer rows.Close()
	type devRow struct {
		typ, model, manufacturer, lastSync string
		battery                            sql.NullInt64
	}
	var got []devRow
	for rows.Next() {
		var row devRow
		if err := rows.Scan(&row.typ, &row.model, &row.manufacturer, &row.battery, &row.lastSync); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, row)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got[0].model != "Pixel 8" || got[1].model != "Pixel Watch 2" {
		t.Fatalf("models = (%q, %q), want (Pixel 8, Pixel Watch 2)", got[0].model, got[1].model)
	}
	if got[1].battery.Int64 != 76 {
		t.Fatalf("Pixel Watch 2 battery = %d, want 76", got[1].battery.Int64)
	}
}

// TestPairedDevicesViewHandlesEmptyDeviceList pins the edge case where
// the latest paired-devices snapshot has no devices: the view returns
// zero rows, not an error.
func TestPairedDevicesViewHandlesEmptyDeviceList(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	connection, err := archive.CurrentConnection()
	if err != nil {
		archive.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, err := archive.Insert(connection, "paired-devices", `{"devices":[]}`, "2026-06-08T13:00:00Z"); err != nil {
		archive.Close()
		t.Fatalf("Insert: %v", err)
	}
	archive.Close()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM paired_devices`).Scan(&count); err != nil {
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
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}
	// PRD #142 slice 2 / #176: pairedDevices now requires
	// settings.readonly, so simulate the user having run
	// `connect --add-scopes settings`.
	addStoredConnectionScope(t, archivePath, googleHealthSettingsReadonlyScope)

	originalFetchPairedDevices := fetchPairedDevices
	fetchPairedDevices = func(string) (googlePairedDevices, error) {
		return googlePairedDevices{
			rawJSON: `{"devices":[{"deviceType":"WATCH","model":"Pixel Watch 2","manufacturer":"Google","batteryPercentage":76,"lastSyncTime":"2026-06-08T13:00:00Z","features":["HR","GPS"]}]}`,
		}, nil
	}
	t.Cleanup(func() { fetchPairedDevices = originalFetchPairedDevices })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("devices exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open identity snapshot archive: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	latest, found, err := archive.Latest(connection, "paired-devices")
	if err != nil || !found {
		t.Fatalf("Latest(paired-devices): found=%v err=%v", found, err)
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
// request to googleHealthPairedDevicesURL — proving the scope
// pre-check happens before the upstream call. The test reads the
// required scope from the same googleHealthIdentityEndpointScopes
// catalog the production code uses so a future slice-2 revision of
// the catalog automatically updates what gets stripped from the
// stored Connection, keeping the test honest without manual edits.
func TestDevicesCommandFailsFastWhenScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	// Strip every scope the catalog ties to pairedDevices from the
	// stored Connection so AccessToken's scope pre-check fails. Using
	// the same catalog key the production code reads means this test
	// keeps pinning the right behaviour after slice 2 rewrites the
	// catalog entry.
	required := googleHealthIdentityEndpointScopes["pairedDevices"]
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
	originalFetchPairedDevices := fetchPairedDevices
	fetchPairedDevices = func(string) (googlePairedDevices, error) {
		t.Fatal("fetchPairedDevices called despite missing scope")
		return googlePairedDevices{}, nil
	}
	t.Cleanup(func() { fetchPairedDevices = originalFetchPairedDevices })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"devices", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
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
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	// PRD #142 slice 2 / #176: pairedDevices now requires
	// settings.readonly, so simulate the user having run
	// `connect --add-scopes settings`.
	addStoredConnectionScope(t, archivePath, googleHealthSettingsReadonlyScope)
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
	testRuntime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		refreshCalls++
		if refreshToken != "connect-refresh-secret" {
			t.Fatalf("refresh token = %q, want connect-refresh-secret", refreshToken)
		}
		return oauthTokenResponse{
			accessToken:  "rotated-access-secret",
			refreshToken: "connect-refresh-secret",
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    refreshedExpiresAt,
			rawTokenMaterialObject: map[string]any{
				"access_token":  "rotated-access-secret",
				"refresh_token": "connect-refresh-secret",
				"token_type":    "Bearer",
				"expires_in":    float64(3600),
			},
		}, nil
	}

	originalFetchPairedDevices := fetchPairedDevices
	var calledWithToken string
	fetchPairedDevices = func(accessToken string) (googlePairedDevices, error) {
		calledWithToken = accessToken
		return googlePairedDevices{
			rawJSON: `{"devices":[{"deviceType":"WATCH","model":"Pixel Watch 2","manufacturer":"Google","batteryPercentage":76,"lastSyncTime":"2026-06-08T13:00:00Z","features":["HR","GPS"]}]}`,
		}, nil
	}
	t.Cleanup(func() { fetchPairedDevices = originalFetchPairedDevices })

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
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var snapshotCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM identity_snapshots WHERE snapshot_kind = 'paired-devices'`).Scan(&snapshotCount); err != nil {
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
	code, stdout, stderr := runCommand(t, "devices", "--no-input")
	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	const want = "--no-input is not supported by devices"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want substring %q", stderr.String(), want)
	}
}

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
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
			DeviceType        string  `json:"device_type"`
			Model             string  `json:"model"`
			Manufacturer      string  `json:"manufacturer"`
			BatteryPercentage *int    `json:"battery_percentage"`
			LastSyncTime      string  `json:"last_sync_time"`
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

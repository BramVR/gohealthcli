package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCaloriesInHeartRateZoneDailySyncAndExport(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	page := string(readTestFixture(t, "googlehealth_calories_in_heart_rate_zone_daily_rollup.json"))
	testRuntime, requests := withDailyRollupFetchFake(t, testRuntime, "connect-access-secret", "calories-in-heart-rate-zone", map[string]string{
		"2026-01-01/2026-01-03/": page,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "calories-in-heart-rate-zone", "--rollup", "daily",
		"--from", "2026-01-01", "--to", "2026-01-03", "--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var syncResult map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &syncResult); err != nil {
		t.Fatalf("sync stdout is not JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, syncResult, "status", "sync_completed")
	assertJSONString(t, syncResult, "endpoint_family", "dailyRollUp")
	assertJSONNumber(t, syncResult, "rollups_seen", 2)
	assertJSONNumber(t, syncResult, "rollups_new", 2)
	if len(*requests) != 1 {
		t.Fatalf("Provider request count = %d, want 1", len(*requests))
	}
	assertArchiveTableCount(t, archivePath, "rollups", 2)
	assertArchiveTableCount(t, archivePath, "data_points", 0)

	db := openArchiveForTest(t, archivePath)
	var rawJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT raw_json FROM rollups WHERE civil_date = '2026-01-01'`).Scan(&rawJSON); err != nil {
		t.Fatalf("query archived Rollup: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	wantZones := `"caloriesInHeartRateZones":[{"heartRateZone":"VIGOROUS","kcal":25.25},{"heartRateZone":"LIGHT","kcal":120.5},{"heartRateZone":"MODERATE","kcal":50}]`
	if !strings.Contains(rawJSON, wantZones) {
		t.Fatalf("raw_json = %s, want Provider zone order and number tokens %s", rawJSON, wantZones)
	}

	stdout.Reset()
	stderr.Reset()
	code = runWithRuntime([]string{
		"export", "--config", configPath, "--db", archivePath,
		"calories-in-heart-rate-zone-rollups", "--stdout", "--format", "csv",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := "provider_name,connection_id,rollup_kind,window_start_utc,window_end_utc,civil_date,zone_index,heart_rate_zone,kcal\n" +
		"googlehealth,googlehealth:111111256096816351,dailyRollUp,,,2026-01-01,0,VIGOROUS,25.25\n" +
		"googlehealth,googlehealth:111111256096816351,dailyRollUp,,,2026-01-01,1,LIGHT,120.5\n" +
		"googlehealth,googlehealth:111111256096816351,dailyRollUp,,,2026-01-01,2,MODERATE,50\n" +
		"googlehealth,googlehealth:111111256096816351,dailyRollUp,,,2026-01-02,0,PEAK,0\n"
	if stdout.String() != want {
		t.Fatalf("export output =\n%s\nwant:\n%s", stdout.String(), want)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		"2026-01-01/2026-01-05/": page,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "calories-in-heart-rate-zone", "--rollup", "daily",
		"--from", "2026-01-01", "--to", "2026-01-05", "--json",
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
	assertJSONNumber(t, syncResult, "rollups_seen", 4)
	assertJSONNumber(t, syncResult, "rollups_new", 4)
	if len(*requests) != 1 {
		t.Fatalf("Provider request count = %d, want 1", len(*requests))
	}
	assertArchiveTableCount(t, archivePath, "rollups", 4)
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
		"googlehealth,googlehealth:111111256096816351,dailyRollUp,,,2026-01-02,0,PEAK,0\n" +
		"googlehealth,googlehealth:111111256096816351,dailyRollUp,,,2026-01-04,0,FUTURE_ZONE,\n"
	if stdout.String() != want {
		t.Fatalf("export output =\n%s\nwant:\n%s", stdout.String(), want)
	}
}

func TestCaloriesInHeartRateZoneSparseBucketsAndAmounts(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	day := func(dayOfMonth int, union string) string {
		return fmt.Sprintf(`{%s"civilStartTime":{"date":{"year":2026,"month":1,"day":%d}},"civilEndTime":{"date":{"year":2026,"month":1,"day":%d}}}`, union, dayOfMonth, dayOfMonth+1)
	}
	page := `{"rollupDataPoints":[` + strings.Join([]string{
		day(1, ``),
		day(2, `"caloriesInHeartRateZone":null,`),
		day(3, `"caloriesInHeartRateZone":{},`),
		day(4, `"caloriesInHeartRateZone":{"caloriesInHeartRateZones":null},`),
		day(5, `"caloriesInHeartRateZone":{"caloriesInHeartRateZones":[]},`),
		day(6, `"caloriesInHeartRateZone":{"caloriesInHeartRateZones":[`+
			`{"heartRateZone":"LIGHT"},`+
			`{"heartRateZone":"MODERATE","kcal":null},`+
			`{"kcal":0},`+
			`{"heartRateZone":null,"kcal":0.0},`+
			`{"heartRateZone":"HEART_RATE_ZONE_TYPE_UNSPECIFIED","kcal":30.0},`+
			`{"heartRateZone":"FUTURE_ZONE","kcal":120.5},`+
			`{"heartRateZone":"PEAK","kcal":1e2},`+
			`{"heartRateZone":"PEAK","kcal":0.1234567890123456789}`+
			`]},`),
	}, ",") + `]}`
	testRuntime, _ = withDailyRollupFetchFake(t, testRuntime, "connect-access-secret", "calories-in-heart-rate-zone", map[string]string{
		"2026-01-01/2026-01-07/": page,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "calories-in-heart-rate-zone", "--rollup", "daily",
		"--from", "2026-01-01", "--to", "2026-01-07", "--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var syncResult map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &syncResult); err != nil {
		t.Fatalf("sync stdout is not JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, syncResult, "status", "sync_completed")
	assertJSONNumber(t, syncResult, "rollups_seen", 4)
	assertJSONNumber(t, syncResult, "rollups_new", 4)
	assertArchiveTableCount(t, archivePath, "rollups", 4)

	db := openArchiveForTest(t, archivePath)
	var cursor string
	if err := db.QueryRowContext(context.Background(), `SELECT cursor_time FROM sync_cursors WHERE data_type = 'calories-in-heart-rate-zone' AND rollup_kind = 'daily'`).Scan(&cursor); err != nil {
		t.Fatalf("query daily cursor: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	if cursor != "2026-01-07" {
		t.Fatalf("daily cursor = %q, want 2026-01-07", cursor)
	}

	prefix := "googlehealth,googlehealth:111111256096816351,dailyRollUp,,,2026-01-06,"
	wantCSV := "provider_name,connection_id,rollup_kind,window_start_utc,window_end_utc,civil_date,zone_index,heart_rate_zone,kcal\n" +
		prefix + "0,LIGHT,\n" +
		prefix + "1,MODERATE,\n" +
		prefix + "2,,0\n" +
		prefix + "3,,0.0\n" +
		prefix + "4,HEART_RATE_ZONE_TYPE_UNSPECIFIED,30.0\n" +
		prefix + "5,FUTURE_ZONE,120.5\n" +
		prefix + "6,PEAK,100.0\n" +
		prefix + "7,PEAK,0.123456789012346\n"
	jsonPrefix := `{"provider_name":"googlehealth","connection_id":"googlehealth:111111256096816351","rollup_kind":"dailyRollUp","window_start_utc":"","window_end_utc":"","civil_date":"2026-01-06",`
	wantJSONL := jsonPrefix + `"zone_index":0,"heart_rate_zone":"LIGHT","kcal":null}` + "\n" +
		jsonPrefix + `"zone_index":1,"heart_rate_zone":"MODERATE","kcal":null}` + "\n" +
		jsonPrefix + `"zone_index":2,"heart_rate_zone":"","kcal":"0"}` + "\n" +
		jsonPrefix + `"zone_index":3,"heart_rate_zone":"","kcal":"0.0"}` + "\n" +
		jsonPrefix + `"zone_index":4,"heart_rate_zone":"HEART_RATE_ZONE_TYPE_UNSPECIFIED","kcal":"30.0"}` + "\n" +
		jsonPrefix + `"zone_index":5,"heart_rate_zone":"FUTURE_ZONE","kcal":"120.5"}` + "\n" +
		jsonPrefix + `"zone_index":6,"heart_rate_zone":"PEAK","kcal":"100.0"}` + "\n" +
		jsonPrefix + `"zone_index":7,"heart_rate_zone":"PEAK","kcal":"0.123456789012346"}` + "\n"
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "csv", args: []string{"--format", "csv"}, want: wantCSV},
		{name: "plain", args: []string{"--plain"}, want: wantCSV},
		{name: "jsonl", args: []string{"--format", "jsonl"}, want: wantJSONL},
	} {
		stdout.Reset()
		stderr.Reset()
		args := append([]string{"export", "--config", configPath, "--db", archivePath, "calories-in-heart-rate-zone-rollups", "--stdout"}, test.args...)
		if code := runWithRuntime(args, stdout, stderr, testRuntime); code != 0 {
			t.Fatalf("%s export exit code = %d\nstderr: %s", test.name, code, stderr.String())
		}
		if stdout.String() != test.want {
			t.Fatalf("%s export =\n%s\nwant:\n%s", test.name, stdout.String(), test.want)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestSyncArchivesBasalEnergyBurnedIntervalWithRawTruth(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	page := string(readTestFixture(t, "googlehealth_basal_energy_burned_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "basal-energy-burned", map[string]string{"": page})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "basal-energy-burned",
		"--from", "2026-01-01", "--to", "2026-01-02", "--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if len(*requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(*requests))
	}
	wantFilter := `basal_energy_burned.interval.start_time >= "2026-01-01T00:00:00Z" AND basal_energy_burned.interval.start_time < "2026-01-02T00:00:00Z"`
	if gotFilter := mustURLQuery(t, (*requests)[0].URL).Get("filter"); gotFilter != wantFilter {
		t.Fatalf("filter = %q, want %q", gotFilter, wantFilter)
	}
	assertArchivedIntervalDataPoint(
		t,
		archivePath,
		"users/me/dataTypes/basal-energy-burned/dataPoints/basal-2026-01-01-a",
		"basal-energy-burned",
		"2026-01-01T05:00:00Z",
		"2026-01-01T05:15:00Z",
		"2026-01-01T06:00:00",
		"2026-01-01T06:15:00",
		"2026-01-01",
		`{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`,
		`{"platform":"FITBIT","applicationName":"Synthetic Fitness"}`,
		"",
		`"kcal":17.125`,
	)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "basal-energy-burned", "list", 1, 1, 0, "")
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "17.125") {
		t.Fatal("sync output leaked health payload value")
	}
}

func TestSyncReconcilesBasalEnergyBurnedWithIndependentWearableCursor(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	page := `{"dataPoints":[{
		"dataPointName":"users/me/dataTypes/basal-energy-burned/dataPoints/basal-wearable-2026-01-01-a",
		"dataSource":{"platform":"FITBIT","applicationName":"Synthetic Wearable"},
		"basalEnergyBurned":{
			"interval":{
				"startTime":"2026-01-01T07:00:00+01:00","startUtcOffset":"3600s",
				"endTime":"2026-01-01T07:15:00+01:00","endUtcOffset":"3600s",
				"civilStartTime":{"date":{"year":2026,"month":1,"day":1},"time":{"hours":7}},
				"civilEndTime":{"date":{"year":2026,"month":1,"day":1},"time":{"hours":7,"minutes":15}}
			},
			"kcal":18.25
		}
	}]}`
	requests := bindDataPointReconcileFetchFake(t, &testRuntime, "connect-access-secret", "basal-energy-burned", map[string]string{"": page})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "basal-energy-burned", "--source-family", "wearable",
		"--from", "2026-01-01", "--to", "2026-01-02", "--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("reconcile exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_new", 1)
	if len(*requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(*requests))
	}
	wantFilter := `basal_energy_burned.interval.start_time >= "2026-01-01T00:00:00Z" AND basal_energy_burned.interval.start_time < "2026-01-02T00:00:00Z"`
	if gotFilter := mustURLQuery(t, (*requests)[0].URL).Get("filter"); gotFilter != wantFilter {
		t.Fatalf("filter = %q, want %q", gotFilter, wantFilter)
	}
	assertArchivedIntervalDataPoint(
		t,
		archivePath,
		"users/me/dataTypes/basal-energy-burned/dataPoints/basal-wearable-2026-01-01-a",
		"basal-energy-burned",
		"2026-01-01T06:00:00Z",
		"2026-01-01T06:15:00Z",
		"2026-01-01T07:00:00",
		"2026-01-01T07:15:00",
		"2026-01-01",
		`{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`,
		`{"platform":"FITBIT","applicationName":"Synthetic Wearable"}`,
		"wearable",
		`"kcal":18.25`,
	)
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("current Connection: %v", err)
	}
	cursor, found, err := archive.ResolveSyncCursor(context.Background(), syncCursorKey{
		connectionID:       connection.ID,
		dataType:           "basal-energy-burned",
		sourceFamilyFilter: "wearable",
		rollupKind:         syncCursorRollupKindNone,
	})
	if err != nil || !found || cursor != "2026-01-02" {
		t.Fatalf("wearable cursor = (%q, found=%v, err=%v), want 2026-01-02", cursor, found, err)
	}
}

func TestExportBasalEnergyBurnedIntervalsHasDeterministicKcalContract(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	for _, point := range []exportDataPointFixture{
		{
			dataType:     "basal-energy-burned",
			resourceName: "users/me/dataTypes/basal-energy-burned/dataPoints/basal-b",
			recordKind:   "interval",
			startUTC:     "2026-01-01T07:00:00Z",
			endUTC:       "2026-01-01T07:15:00Z",
			startCivil:   "2026-01-01T08:00:00",
			endCivil:     "2026-01-01T08:15:00",
			civilDate:    "2026-01-01",
			dataSource:   `{"platform":"FITBIT"}`,
			rawJSON:      `{"basalEnergyBurned":{"kcal":42.0}}`,
		},
		{
			dataType:     "basal-energy-burned",
			resourceName: "users/me/dataTypes/basal-energy-burned/dataPoints/basal-a",
			recordKind:   "interval",
			startUTC:     "2026-01-01T06:00:00Z",
			endUTC:       "2026-01-01T06:15:00Z",
			startCivil:   "2026-01-01T07:00:00",
			endCivil:     "2026-01-01T07:15:00",
			civilDate:    "2026-01-01",
			dataSource:   `{"platform":"FITBIT"}`,
			sourceFamily: "wearable",
			rawJSON:      `{"basalEnergyBurned":{"kcal":17.125000000000004}}`,
		},
		{
			dataType:     "basal-energy-burned",
			resourceName: "users/me/dataTypes/basal-energy-burned/dataPoints/basal-null",
			recordKind:   "interval",
			startUTC:     "2026-01-01T08:00:00Z",
			endUTC:       "2026-01-01T08:15:00Z",
			dataSource:   `{}`,
			rawJSON:      `{"basalEnergyBurned":{}}`,
		},
	} {
		insertExportDataPoint(t, archivePath, point)
	}

	wantCSV := "provider_name,connection_id,start_time_utc,end_time_utc,start_civil_time,end_civil_time,civil_date,kcal,source_platform,source_family_filter,upstream_resource_name\n" +
		"googlehealth,googlehealth:111111256096816351,2026-01-01T06:00:00Z,2026-01-01T06:15:00Z,2026-01-01T07:00:00,2026-01-01T07:15:00,2026-01-01,17.125,FITBIT,wearable,users/me/dataTypes/basal-energy-burned/dataPoints/basal-a\n" +
		"googlehealth,googlehealth:111111256096816351,2026-01-01T07:00:00Z,2026-01-01T07:15:00Z,2026-01-01T08:00:00,2026-01-01T08:15:00,2026-01-01,42.0,FITBIT,,users/me/dataTypes/basal-energy-burned/dataPoints/basal-b\n" +
		"googlehealth,googlehealth:111111256096816351,2026-01-01T08:00:00Z,2026-01-01T08:15:00Z,,,2026-01-01,,,,users/me/dataTypes/basal-energy-burned/dataPoints/basal-null\n"
	wantJSONL := "{\"provider_name\":\"googlehealth\",\"connection_id\":\"googlehealth:111111256096816351\",\"start_time_utc\":\"2026-01-01T06:00:00Z\",\"end_time_utc\":\"2026-01-01T06:15:00Z\",\"start_civil_time\":\"2026-01-01T07:00:00\",\"end_civil_time\":\"2026-01-01T07:15:00\",\"civil_date\":\"2026-01-01\",\"kcal\":\"17.125\",\"source_platform\":\"FITBIT\",\"source_family_filter\":\"wearable\",\"upstream_resource_name\":\"users/me/dataTypes/basal-energy-burned/dataPoints/basal-a\"}\n" +
		"{\"provider_name\":\"googlehealth\",\"connection_id\":\"googlehealth:111111256096816351\",\"start_time_utc\":\"2026-01-01T07:00:00Z\",\"end_time_utc\":\"2026-01-01T07:15:00Z\",\"start_civil_time\":\"2026-01-01T08:00:00\",\"end_civil_time\":\"2026-01-01T08:15:00\",\"civil_date\":\"2026-01-01\",\"kcal\":\"42.0\",\"source_platform\":\"FITBIT\",\"source_family_filter\":\"\",\"upstream_resource_name\":\"users/me/dataTypes/basal-energy-burned/dataPoints/basal-b\"}\n" +
		"{\"provider_name\":\"googlehealth\",\"connection_id\":\"googlehealth:111111256096816351\",\"start_time_utc\":\"2026-01-01T08:00:00Z\",\"end_time_utc\":\"2026-01-01T08:15:00Z\",\"start_civil_time\":\"\",\"end_civil_time\":\"\",\"civil_date\":\"2026-01-01\",\"kcal\":\"\",\"source_platform\":\"\",\"source_family_filter\":\"\",\"upstream_resource_name\":\"users/me/dataTypes/basal-energy-burned/dataPoints/basal-null\"}\n"

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "csv", args: []string{"--format", "csv"}, want: wantCSV},
		{name: "plain synonym", args: []string{"--plain"}, want: wantCSV},
		{name: "jsonl", args: []string{"--format", "jsonl"}, want: wantJSONL},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			args := []string{"export", "--config", configPath, "--db", archivePath, "basal-energy-burned-intervals", "--stdout"}
			args = append(args, test.args...)
			if code := run(args, stdout, stderr); code != 0 {
				t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
			}
			if stdout.String() != test.want {
				t.Fatalf("output =\n%s\nwant:\n%s", stdout.String(), test.want)
			}
		})
	}

	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "basal-energy-burned",
		resourceName: "users/me/dataTypes/basal-energy-burned/dataPoints/basal-malformed",
		recordKind:   "interval",
		startUTC:     "2026-01-01T09:00:00Z",
		endUTC:       "2026-01-01T09:15:00Z",
		rawJSON:      `{"basalEnergyBurned":{"kcal":"unknown"}}`,
	})
	db := openArchiveForTest(t, archivePath)
	defer db.Close()
	var malformedKcal *string
	if err := db.QueryRowContext(context.Background(), `SELECT kcal FROM basal_energy_burned_intervals WHERE upstream_resource_name LIKE '%/basal-malformed'`).Scan(&malformedKcal); err != nil {
		t.Fatalf("query malformed kcal projection: %v", err)
	}
	if malformedKcal != nil {
		t.Fatalf("malformed kcal = %q, want NULL", *malformedKcal)
	}
}

func TestBasalEnergyBurnedViewMigrationUpgradesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "legacy", "archive.sqlite")
	createLegacyArchive(t, archivePath, 25)
	lifecycle := healthArchiveLifecycle{path: archivePath}
	if err := lifecycle.Migrate(context.Background()); err != nil {
		t.Fatalf("upgrade v25 Health Archive: %v", err)
	}
	if err := lifecycle.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertArchiveUserVersion(t, archivePath, 26)
	db := openArchiveForTest(t, archivePath)
	defer db.Close()
	var viewCount, migrationCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_master WHERE type = 'view' AND name = 'basal_energy_burned_intervals'`).Scan(&viewCount); err != nil {
		t.Fatalf("query basal view: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM schema_migrations WHERE version = 26 AND name = 'add_basal_energy_burned_intervals_view'`).Scan(&migrationCount); err != nil {
		t.Fatalf("query basal migration row: %v", err)
	}
	if viewCount != 1 || migrationCount != 1 {
		t.Fatalf("view/migration counts = (%d, %d), want (1, 1)", viewCount, migrationCount)
	}
}

func TestSyncBasalEnergyBurnedCorrectionPreservesRevision(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	page := string(readTestFixture(t, "googlehealth_basal_energy_burned_list.json"))
	runSync := func(body string) map[string]any {
		bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "basal-energy-burned", map[string]string{"": body})
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		code := runWithRuntime([]string{
			"sync", "--config", configPath, "--db", archivePath,
			"--types", "basal-energy-burned",
			"--from", "2026-01-01", "--to", "2026-01-02", "--json",
		}, stdout, stderr, testRuntime)
		if code != 0 {
			t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
		}
		var result map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("decode sync JSON: %v", err)
		}
		return result
	}
	first := runSync(page)
	assertJSONNumber(t, first, "data_points_new", 1)
	corrected := strings.Replace(page, `"kcal": 17.125`, `"kcal": 17.5`, 1)
	second := runSync(corrected)
	assertJSONNumber(t, second, "data_points_new", 0)
	assertJSONNumber(t, second, "data_points_updated", 1)

	db := openArchiveForTest(t, archivePath)
	defer db.Close()
	var currentRaw, previousRaw, reason string
	if err := db.QueryRowContext(context.Background(), `SELECT raw_json FROM data_points WHERE data_type = 'basal-energy-burned'`).Scan(&currentRaw); err != nil {
		t.Fatalf("query current raw JSON: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT previous_raw_json, replacement_reason FROM data_point_revisions`).Scan(&previousRaw, &reason); err != nil {
		t.Fatalf("query Data Point Revision: %v", err)
	}
	if !strings.Contains(currentRaw, `"kcal":17.5`) || !strings.Contains(previousRaw, `"kcal":17.125`) || reason != "provider_correction" {
		t.Fatalf("current/revision contract = (%s, %s, %q)", currentRaw, previousRaw, reason)
	}
}

func TestSyncBasalEnergyBurnedMissingActivityScopeStopsBeforeProvider(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googlehealth.ScopeProfileReadonly})
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		t.Fatal("Provider fetch ran despite missing activity scope")
		return nil, nil
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "basal-energy-burned", "--from", "2026-01-01", "--json",
	}, stdout, stderr, testRuntime)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("exit/stderr = (%d, %q), want (1, empty)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), googlehealth.ScopeActivityReadonly) || !strings.Contains(stdout.String(), "gohealthcli connect") {
		t.Fatalf("stdout = %q, want activity-scope reconnect remediation", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_failed", "basal-energy-burned", "list", 0, 0, 0, googlehealth.ScopeActivityReadonly)
}

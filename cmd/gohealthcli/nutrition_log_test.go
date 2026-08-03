package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestSyncArchivesNutritionLogSessionWithRawTruth(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googlehealth.ScopeNutritionReadonly})
	page := string(readTestFixture(t, "googlehealth_nutrition_log_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "nutrition-log", map[string]string{"": page})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "nutrition-log",
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
	if len(*requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(*requests))
	}
	if gotScopes := (*requests)[0].RequiredScopes; len(gotScopes) != 1 || gotScopes[0] != googlehealth.ScopeNutritionReadonly {
		t.Fatalf("required scopes = %v, want nutrition.readonly", gotScopes)
	}
	wantFilter := `nutrition_log.interval.civil_start_time >= "2026-01-01" AND nutrition_log.interval.civil_start_time < "2026-01-02"`
	if gotFilter := mustURLQuery(t, (*requests)[0].URL).Get("filter"); gotFilter != wantFilter {
		t.Fatalf("filter = %q, want %q", gotFilter, wantFilter)
	}
	db := openArchiveForTest(t, archivePath)
	var dataType, recordKind, startUTC, endUTC, dataSourceJSON, rawJSON string
	var startCivil, endCivil, civilDate, timezoneMetadata sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		data_type, record_kind, start_time_utc, end_time_utc,
		start_civil_time, end_civil_time, provider_civil_date,
		timezone_metadata, data_source_json, raw_json
	FROM data_points
	WHERE upstream_resource_name = ?`, "users/me/dataTypes/nutrition-log/dataPoints/nutrition-2026-01-01-a").Scan(
		&dataType, &recordKind, &startUTC, &endUTC,
		&startCivil, &endCivil, &civilDate,
		&timezoneMetadata, &dataSourceJSON, &rawJSON,
	); err != nil {
		t.Fatalf("query archived Nutrition Log: %v", err)
	}
	if dataType != "nutrition-log" || recordKind != "session" {
		t.Fatalf("Data Point identity = (%q, %q), want (nutrition-log, session)", dataType, recordKind)
	}
	if startUTC != "2026-01-01T10:30:00Z" || endUTC != "2026-01-01T10:35:00Z" ||
		startCivil.String != "2026-01-01T11:30:00" || endCivil.String != "2026-01-01T11:35:00" ||
		civilDate.String != "2026-01-01" {
		t.Fatalf("Nutrition Log times = (%q, %q, %q, %q, %q)", startUTC, endUTC, startCivil.String, endCivil.String, civilDate.String)
	}
	if timezoneMetadata.String != `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}` ||
		dataSourceJSON != `{"platform":"SYNTHETIC","applicationName":"Fixture Food Logger"}` ||
		!strings.Contains(rawJSON, `"foodDisplayName":"Synthetic oats"`) {
		t.Fatalf("Nutrition Log context/raw mismatch: timezone=%q source=%q raw=%s", timezoneMetadata.String, dataSourceJSON, rawJSON)
	}
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "nutrition-log", "list", 1, 1, 0, "")
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
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
		connectionID: connection.ID,
		dataType:     "nutrition-log",
		rollupKind:   syncCursorRollupKindNone,
	})
	if err != nil || !found || cursor != "2026-01-02" {
		t.Fatalf("list cursor = (%q, found=%v, err=%v), want 2026-01-02", cursor, found, err)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "Synthetic oats") {
		t.Fatal("sync output leaked health payload value")
	}
}

func TestSyncReconcilesAnonymousNutritionLogWithIndependentWearableCursor(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googlehealth.ScopeNutritionReadonly})
	page := `{"dataPoints":[{
		"dataPointName":"users/me/dataTypes/nutrition-log/dataPoints/nutrition-anonymous-a",
		"nutritionLog":{
			"interval":{
				"startTime":"2026-01-01T12:00:00Z","endTime":"2026-01-01T12:01:00Z",
				"civilStartTime":{"date":{"year":2026,"month":1,"day":1},"time":{"hours":13}},
				"civilEndTime":{"date":{"year":2026,"month":1,"day":1},"time":{"hours":13,"minutes":1}}
			},
			"foodDisplayName":"Synthetic anonymous food"
		}
	}]}`
	requests := bindDataPointReconcileFetchFake(t, &testRuntime, "connect-access-secret", "nutrition-log", map[string]string{"": page})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "nutrition-log", "--source-family", "wearable",
		"--from", "2026-01-01", "--to", "2026-01-02", "--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("reconcile exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if len(*requests) != 1 || (*requests)[0].EndpointName != "dataTypes.nutrition-log.reconcile" {
		t.Fatalf("requests = %#v, want one nutrition-log reconcile", *requests)
	}
	if gotScopes := (*requests)[0].RequiredScopes; len(gotScopes) != 1 || gotScopes[0] != googlehealth.ScopeNutritionReadonly {
		t.Fatalf("required scopes = %v, want nutrition.readonly", gotScopes)
	}
	wantFilter := `nutrition_log.interval.civil_start_time >= "2026-01-01" AND nutrition_log.interval.civil_start_time < "2026-01-02"`
	if gotFilter := mustURLQuery(t, (*requests)[0].URL).Get("filter"); gotFilter != wantFilter {
		t.Fatalf("filter = %q, want %q", gotFilter, wantFilter)
	}

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
		dataType:           "nutrition-log",
		sourceFamilyFilter: "wearable",
		rollupKind:         syncCursorRollupKindNone,
	})
	if err != nil || !found || cursor != "2026-01-02" {
		t.Fatalf("wearable cursor = (%q, found=%v, err=%v), want 2026-01-02", cursor, found, err)
	}
}

func TestExportNutritionLogSessionsPreservesFoodsNullsAndStableOrder(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	for _, point := range []exportDataPointFixture{
		{
			dataType:     "nutrition-log",
			resourceName: "users/me/dataTypes/nutrition-log/dataPoints/nutrition-b",
			recordKind:   "session",
			startUTC:     "2026-01-01T10:30:00Z",
			endUTC:       "2026-01-01T10:35:00Z",
			startCivil:   "2026-01-01T11:30:00",
			endCivil:     "2026-01-01T11:35:00",
			civilDate:    "2026-01-01",
			dataSource:   `{"platform":"SYNTHETIC"}`,
			rawJSON: `{"nutritionLog":{
				"food":"users/me/foods/synthetic-oats",
				"foodDisplayName":"Synthetic oats",
				"mealType":"BREAKFAST",
				"serving":{"foodMeasurementUnit":"users/me/foodMeasurementUnits/synthetic-bowl","foodMeasurementUnitDisplayName":"bowl","amount":1.25},
				"energy":{"kcal":321.0},"energyFromFat":{"kcal":81.5},
				"totalCarbohydrate":{"grams":52.25},"totalFat":{"grams":9.125},
				"nutrients":[{"nutrient":"PROTEIN","quantity":{"grams":12.5}}]
			}}`,
		},
		{
			dataType:     "nutrition-log",
			resourceName: "users/me/dataTypes/nutrition-log/dataPoints/nutrition-a",
			recordKind:   "session",
			startUTC:     "2026-01-01T10:30:00Z",
			endUTC:       "2026-01-01T10:31:00Z",
			startCivil:   "2026-01-01T11:30:00",
			endCivil:     "2026-01-01T11:31:00",
			civilDate:    "2026-01-01",
			dataSource:   `{}`,
			rawJSON:      `{"nutritionLog":{"foodDisplayName":"Synthetic anonymous soup"}}`,
		},
	} {
		insertExportDataPoint(t, archivePath, point)
	}

	wantCSV := "provider_name,connection_id,start_time_utc,end_time_utc,start_civil_time,end_civil_time,civil_date,food_resource_name,food_display_name,meal_type,serving_food_measurement_unit,serving_food_measurement_unit_display_name,serving_amount,energy_kcal,energy_from_fat_kcal,total_carbohydrate_grams,total_fat_grams,nutrients_json,source_platform,source_family_filter,upstream_resource_name\n" +
		"googlehealth,googlehealth:111111256096816351,2026-01-01T10:30:00Z,2026-01-01T10:31:00Z,2026-01-01T11:30:00,2026-01-01T11:31:00,2026-01-01,,Synthetic anonymous soup,,,,,,,,,,,,users/me/dataTypes/nutrition-log/dataPoints/nutrition-a\n" +
		"googlehealth,googlehealth:111111256096816351,2026-01-01T10:30:00Z,2026-01-01T10:35:00Z,2026-01-01T11:30:00,2026-01-01T11:35:00,2026-01-01,users/me/foods/synthetic-oats,Synthetic oats,BREAKFAST,users/me/foodMeasurementUnits/synthetic-bowl,bowl,1.25,321.0,81.5,52.25,9.125,\"[{\"\"nutrient\"\":\"\"PROTEIN\"\",\"\"quantity\"\":{\"\"grams\"\":12.5}}]\",SYNTHETIC,,users/me/dataTypes/nutrition-log/dataPoints/nutrition-b\n"
	wantJSONL := "{\"provider_name\":\"googlehealth\",\"connection_id\":\"googlehealth:111111256096816351\",\"start_time_utc\":\"2026-01-01T10:30:00Z\",\"end_time_utc\":\"2026-01-01T10:31:00Z\",\"start_civil_time\":\"2026-01-01T11:30:00\",\"end_civil_time\":\"2026-01-01T11:31:00\",\"civil_date\":\"2026-01-01\",\"food_resource_name\":null,\"food_display_name\":\"Synthetic anonymous soup\",\"meal_type\":null,\"serving_food_measurement_unit\":null,\"serving_food_measurement_unit_display_name\":null,\"serving_amount\":null,\"energy_kcal\":null,\"energy_from_fat_kcal\":null,\"total_carbohydrate_grams\":null,\"total_fat_grams\":null,\"nutrients_json\":null,\"source_platform\":\"\",\"source_family_filter\":\"\",\"upstream_resource_name\":\"users/me/dataTypes/nutrition-log/dataPoints/nutrition-a\"}\n" +
		"{\"provider_name\":\"googlehealth\",\"connection_id\":\"googlehealth:111111256096816351\",\"start_time_utc\":\"2026-01-01T10:30:00Z\",\"end_time_utc\":\"2026-01-01T10:35:00Z\",\"start_civil_time\":\"2026-01-01T11:30:00\",\"end_civil_time\":\"2026-01-01T11:35:00\",\"civil_date\":\"2026-01-01\",\"food_resource_name\":\"users/me/foods/synthetic-oats\",\"food_display_name\":\"Synthetic oats\",\"meal_type\":\"BREAKFAST\",\"serving_food_measurement_unit\":\"users/me/foodMeasurementUnits/synthetic-bowl\",\"serving_food_measurement_unit_display_name\":\"bowl\",\"serving_amount\":\"1.25\",\"energy_kcal\":\"321.0\",\"energy_from_fat_kcal\":\"81.5\",\"total_carbohydrate_grams\":\"52.25\",\"total_fat_grams\":\"9.125\",\"nutrients_json\":[{\"nutrient\":\"PROTEIN\",\"quantity\":{\"grams\":12.5}}],\"source_platform\":\"SYNTHETIC\",\"source_family_filter\":\"\",\"upstream_resource_name\":\"users/me/dataTypes/nutrition-log/dataPoints/nutrition-b\"}\n"

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
			args := []string{"export", "--config", configPath, "--db", archivePath, "nutrition-log-sessions", "--stdout"}
			args = append(args, test.args...)
			if code := run(args, stdout, stderr); code != 0 {
				t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
			}
			if stdout.String() != test.want {
				t.Fatalf("output =\n%s\nwant:\n%s", stdout.String(), test.want)
			}
		})
	}
}

func TestSyncNutritionLogCorrectionPreservesRevision(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googlehealth.ScopeNutritionReadonly})
	page := string(readTestFixture(t, "googlehealth_nutrition_log_list.json"))
	runSync := func(body string) map[string]any {
		bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "nutrition-log", map[string]string{"": body})
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		code := runWithRuntime([]string{
			"sync", "--config", configPath, "--db", archivePath,
			"--types", "nutrition-log", "--from", "2026-01-01", "--to", "2026-01-02", "--json",
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
	corrected := strings.Replace(page, `"foodDisplayName": "Synthetic oats"`, `"foodDisplayName": "Synthetic corrected oats"`, 1)
	second := runSync(corrected)
	assertJSONNumber(t, second, "data_points_new", 0)
	assertJSONNumber(t, second, "data_points_updated", 1)

	db := openArchiveForTest(t, archivePath)
	var currentRaw, previousRaw, reason string
	if err := db.QueryRowContext(context.Background(), `SELECT raw_json FROM data_points WHERE data_type = 'nutrition-log'`).Scan(&currentRaw); err != nil {
		t.Fatalf("query current raw JSON: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT previous_raw_json, replacement_reason FROM data_point_revisions`).Scan(&previousRaw, &reason); err != nil {
		t.Fatalf("query Data Point Revision: %v", err)
	}
	if !strings.Contains(currentRaw, `"foodDisplayName":"Synthetic corrected oats"`) ||
		!strings.Contains(previousRaw, `"foodDisplayName":"Synthetic oats"`) || reason != "provider_correction" {
		t.Fatalf("current/revision contract = (%s, %s, %q)", currentRaw, previousRaw, reason)
	}
}

func TestSyncNutritionLogMissingScopeStopsBeforeProvider(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googlehealth.ScopeProfileReadonly})
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		t.Fatal("Provider fetch ran despite missing nutrition scope")
		return nil, nil
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--config", configPath, "--db", archivePath,
		"--types", "nutrition-log", "--from", "2026-01-01", "--json",
	}, stdout, stderr, testRuntime)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("exit/stderr = (%d, %q), want (1, empty)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), googlehealth.ScopeNutritionReadonly) ||
		!strings.Contains(stdout.String(), "gohealthcli connect --add-scopes nutrition") {
		t.Fatalf("stdout = %q, want nutrition-scope reconnect remediation", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_failed", "nutrition-log", "list", 0, 0, 0, googlehealth.ScopeNutritionReadonly)
}

func TestNutritionLogSessionsViewMigrationUpgradesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "legacy", "archive.sqlite")
	createLegacyArchive(t, archivePath, 27)
	lifecycle := healthArchiveLifecycle{path: archivePath}
	if err := lifecycle.Migrate(context.Background()); err != nil {
		t.Fatalf("upgrade v27 Health Archive: %v", err)
	}
	if err := lifecycle.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertArchiveUserVersion(t, archivePath, 28)
	db := openArchiveForTest(t, archivePath)
	var viewCount, migrationCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_master WHERE type = 'view' AND name = 'nutrition_log_sessions'`).Scan(&viewCount); err != nil {
		t.Fatalf("query Nutrition Log view: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM schema_migrations WHERE version = 28 AND name = 'add_nutrition_log_sessions_view'`).Scan(&migrationCount); err != nil {
		t.Fatalf("query Nutrition Log migration row: %v", err)
	}
	if viewCount != 1 || migrationCount != 1 {
		t.Fatalf("view/migration counts = (%d, %d), want (1, 1)", viewCount, migrationCount)
	}
}

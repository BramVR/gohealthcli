package main

import (
	"context"
	"database/sql"
	"testing"
)

// TestSleepStagesViewExplodesEachStage is the slice A tracer for #105:
// sleep_stages produces one row per stage in the sleep session's
// stages[] array, with the contracted columns.
func TestSleepStagesViewExplodesEachStage(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "sleep",
		resourceName: "users/me/dataTypes/sleep/dataPoints/sleep-2026-06-08",
		recordKind:   "session",
		startUTC:     "2026-06-08T21:30:00Z",
		endUTC:       "2026-06-09T05:45:00Z",
		startCivil:   "2026-06-08T22:30:00",
		endCivil:     "2026-06-09T06:45:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON: `{"sleep":{"stages":[
			{"type":"LIGHT","startTime":"2026-06-08T22:30:00Z","endTime":"2026-06-08T23:30:00Z"},
			{"type":"DEEP","startTime":"2026-06-08T23:30:00Z","endTime":"2026-06-09T00:30:00Z"},
			{"type":"REM","startTime":"2026-06-09T00:30:00Z","endTime":"2026-06-09T01:15:00Z"}
		]}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(context.Background(), `SELECT sleep_stage, duration_seconds, start_time_utc, end_time_utc, civil_date, upstream_resource_name FROM sleep_stages ORDER BY start_time_utc`)
	if err != nil {
		t.Fatalf("query sleep_stages: %v", err)
	}
	defer rows.Close()
	type row struct {
		stage, startUTC, endUTC, civilDate, resource string
		duration                                     sql.NullInt64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.stage, &r.duration, &r.startUTC, &r.endUTC, &r.civilDate, &r.resource); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	if got[0].stage != "LIGHT" || got[1].stage != "DEEP" || got[2].stage != "REM" {
		t.Fatalf("stages = (%q, %q, %q), want (LIGHT, DEEP, REM)", got[0].stage, got[1].stage, got[2].stage)
	}
	// Each stage is 3600s (1h) or 2700s (45m).
	if got[0].duration.Int64 != 3600 || got[1].duration.Int64 != 3600 || got[2].duration.Int64 != 2700 {
		t.Fatalf("durations = (%d, %d, %d), want (3600, 3600, 2700)", got[0].duration.Int64, got[1].duration.Int64, got[2].duration.Int64)
	}
	if got[0].civilDate != "2026-06-08" {
		t.Fatalf("civil_date = %q, want 2026-06-08 (inherited from parent session)", got[0].civilDate)
	}
	if got[0].resource != "users/me/dataTypes/sleep/dataPoints/sleep-2026-06-08" {
		t.Fatalf("upstream_resource_name = %q, want parent session", got[0].resource)
	}
}

// TestExerciseSplitsViewExplodesEachSplit pins the parallel behaviour
// for exercise sessions: one row per split with split_type and
// distance_meters projected from json_each over the splits array.
func TestExerciseSplitsViewExplodesEachSplit(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "exercise",
		resourceName: "users/me/dataTypes/exercise/dataPoints/run-2026-06-08",
		recordKind:   "session",
		startUTC:     "2026-06-08T17:00:00Z",
		endUTC:       "2026-06-08T17:30:00Z",
		startCivil:   "2026-06-08T18:00:00",
		endCivil:     "2026-06-08T18:30:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT"}`,
		// Real Google Health API shape: distanceMillimeters lives under
		// metricsSummary, not at the split root. Live testing in #105
		// caught the earlier wrong-path bug.
		rawJSON: `{"exercise":{"exerciseType":"RUNNING","splits":[
			{"splitType":"DISTANCE","metricsSummary":{"distanceMillimeters":1000000},"startTime":"2026-06-08T17:00:00Z","endTime":"2026-06-08T17:05:00Z"},
			{"splitType":"DISTANCE","metricsSummary":{"distanceMillimeters":1000000},"startTime":"2026-06-08T17:05:00Z","endTime":"2026-06-08T17:10:30Z"},
			{"splitType":"DISTANCE","metricsSummary":{"distanceMillimeters":1000000},"startTime":"2026-06-08T17:10:30Z","endTime":"2026-06-08T17:16:00Z"}
		]}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(context.Background(), `SELECT split_type, distance_meters, start_time_utc, end_time_utc, civil_date, upstream_resource_name FROM exercise_splits ORDER BY start_time_utc`)
	if err != nil {
		t.Fatalf("query exercise_splits: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var splitType, startUTC, endUTC, civilDate, resource string
		var distance sql.NullInt64
		if err := rows.Scan(&splitType, &distance, &startUTC, &endUTC, &civilDate, &resource); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if splitType != "DISTANCE" || distance.Int64 != 1000 {
			t.Errorf("split %d: type=%q distance=%d, want DISTANCE/1000", count, splitType, distance.Int64)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("split rows = %d, want 3", count)
	}
}

// TestSleepStagesViewHandlesSessionWithoutStages pins the edge case
// where a sleep session has no stages array (or it's empty): the view
// must not error and must return zero rows for that session.
func TestSleepStagesViewHandlesSessionWithoutStages(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "sleep",
		resourceName: "users/me/dataTypes/sleep/dataPoints/sleep-no-stages",
		recordKind:   "session",
		startUTC:     "2026-06-08T21:30:00Z",
		endUTC:       "2026-06-09T05:45:00Z",
		startCivil:   "2026-06-08T22:30:00",
		endCivil:     "2026-06-09T06:45:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"sleep":{}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sleep_stages`).Scan(&count); err != nil {
		t.Fatalf("query sleep_stages: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0 (no stages → no rows, not an error)", count)
	}
}

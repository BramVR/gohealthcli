package main

import (
	"database/sql"
	"testing"
)

// Tier 1 Health metrics view tests (#102). One test per Data Type
// pins the documented Google Health REST API JSON shape into a
// fixture, exercises the Normalized View, and asserts the view
// returns the scalar at the contracted column. Five views ship in
// migration 18: body_fat_samples, blood_glucose_samples,
// core_body_temperature_samples, height_samples, current_height.

func TestBodyFatSamplesViewProjectsPercentage(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "body-fat",
		resourceName: "users/me/dataTypes/body-fat/dataPoints/body-fat-2026-06-08",
		recordKind:   "sample",
		startUTC:     "2026-06-08T07:00:00Z",
		endUTC:       "2026-06-08T07:00:00Z",
		startCivil:   "2026-06-08T08:00:00",
		endCivil:     "2026-06-08T08:00:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT"}`,
		// Google Health v4 sample shape — same percentage convention
		// as oxygenSaturation. Stored as TEXT to preserve precision.
		rawJSON: `{"bodyFat":{"sampleTime":{"physicalTime":"2026-06-08T08:00:00+01:00"},"percentage":"23.4"}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	row := db.QueryRow(`SELECT sample_time_utc, percentage, civil_date, upstream_resource_name FROM body_fat_samples`)
	var sampleTime, percentage, civilDate, resource string
	if err := row.Scan(&sampleTime, &percentage, &civilDate, &resource); err != nil {
		t.Fatalf("scan body_fat_samples: %v", err)
	}
	if sampleTime != "2026-06-08T07:00:00Z" {
		t.Fatalf("sample_time_utc = %q, want 2026-06-08T07:00:00Z", sampleTime)
	}
	if percentage != "23.4" {
		t.Fatalf("percentage = %q, want 23.4", percentage)
	}
	if civilDate != "2026-06-08" {
		t.Fatalf("civil_date = %q, want 2026-06-08", civilDate)
	}
	if resource != "users/me/dataTypes/body-fat/dataPoints/body-fat-2026-06-08" {
		t.Fatalf("upstream_resource_name = %q, want body-fat resource", resource)
	}
}

func TestBloodGlucoseSamplesViewProjectsLevel(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "blood-glucose",
		resourceName: "users/me/dataTypes/blood-glucose/dataPoints/glucose-2026-06-08",
		recordKind:   "sample",
		startUTC:     "2026-06-08T07:00:00Z",
		endUTC:       "2026-06-08T07:00:00Z",
		startCivil:   "2026-06-08T08:00:00",
		endCivil:     "2026-06-08T08:00:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT"}`,
		// Google Health v4: bloodGlucoseLevel is the principal
		// scalar in milligramsPerDeciliter (mg/dL).
		rawJSON: `{"bloodGlucose":{"sampleTime":{"physicalTime":"2026-06-08T08:00:00+01:00"},"bloodGlucoseLevel":{"milligramsPerDeciliter":"95.0"}}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	row := db.QueryRow(`SELECT sample_time_utc, milligrams_per_deciliter, civil_date FROM blood_glucose_samples`)
	var sampleTime, mgdl, civilDate string
	if err := row.Scan(&sampleTime, &mgdl, &civilDate); err != nil {
		t.Fatalf("scan blood_glucose_samples: %v", err)
	}
	if sampleTime != "2026-06-08T07:00:00Z" {
		t.Fatalf("sample_time_utc = %q, want 2026-06-08T07:00:00Z", sampleTime)
	}
	if mgdl != "95.0" {
		t.Fatalf("milligrams_per_deciliter = %q, want 95.0", mgdl)
	}
	if civilDate != "2026-06-08" {
		t.Fatalf("civil_date = %q, want 2026-06-08", civilDate)
	}
}

func TestCoreBodyTemperatureSamplesViewProjectsCelsius(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "core-body-temperature",
		resourceName: "users/me/dataTypes/core-body-temperature/dataPoints/temp-2026-06-08",
		recordKind:   "sample",
		startUTC:     "2026-06-08T07:00:00Z",
		endUTC:       "2026-06-08T07:00:00Z",
		startCivil:   "2026-06-08T08:00:00",
		endCivil:     "2026-06-08T08:00:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT"}`,
		// Google Health v4 convention: core body temperature in °C
		// at $.coreBodyTemperature.celsius.
		rawJSON: `{"coreBodyTemperature":{"sampleTime":{"physicalTime":"2026-06-08T08:00:00+01:00"},"celsius":"36.7"}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	row := db.QueryRow(`SELECT sample_time_utc, celsius, civil_date FROM core_body_temperature_samples`)
	var sampleTime, celsius, civilDate string
	if err := row.Scan(&sampleTime, &celsius, &civilDate); err != nil {
		t.Fatalf("scan core_body_temperature_samples: %v", err)
	}
	if sampleTime != "2026-06-08T07:00:00Z" {
		t.Fatalf("sample_time_utc = %q, want 2026-06-08T07:00:00Z", sampleTime)
	}
	if celsius != "36.7" {
		t.Fatalf("celsius = %q, want 36.7", celsius)
	}
	if civilDate != "2026-06-08" {
		t.Fatalf("civil_date = %q, want 2026-06-08", civilDate)
	}
}

func TestHeightSamplesViewProjectsMeters(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "height",
		resourceName: "users/me/dataTypes/height/dataPoints/height-2026-06-08",
		recordKind:   "sample",
		startUTC:     "2026-06-08T07:00:00Z",
		endUTC:       "2026-06-08T07:00:00Z",
		startCivil:   "2026-06-08T08:00:00",
		endCivil:     "2026-06-08T08:00:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT"}`,
		// Google Health v4: height in meters at $.height.heightMeters
		// (mirrors weight.weightGrams convention).
		rawJSON: `{"height":{"sampleTime":{"physicalTime":"2026-06-08T08:00:00+01:00"},"heightMeters":"1.83"}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	row := db.QueryRow(`SELECT sample_time_utc, height_meters, civil_date FROM height_samples`)
	var sampleTime, heightMeters, civilDate string
	if err := row.Scan(&sampleTime, &heightMeters, &civilDate); err != nil {
		t.Fatalf("scan height_samples: %v", err)
	}
	if sampleTime != "2026-06-08T07:00:00Z" {
		t.Fatalf("sample_time_utc = %q, want 2026-06-08T07:00:00Z", sampleTime)
	}
	if heightMeters != "1.83" {
		t.Fatalf("height_meters = %q, want 1.83", heightMeters)
	}
	if civilDate != "2026-06-08" {
		t.Fatalf("civil_date = %q, want 2026-06-08", civilDate)
	}
}

// TestCurrentHeightViewReturnsLatestSample pins the latest-only view
// the issue asked for: an LLM should be able to answer "what's my
// height?" without ordering manually.
func TestCurrentHeightViewReturnsLatestSample(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	// Two height samples — the view must return the later one.
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "height",
		resourceName: "users/me/dataTypes/height/dataPoints/height-old",
		recordKind:   "sample",
		startUTC:     "2025-01-01T07:00:00Z",
		endUTC:       "2025-01-01T07:00:00Z",
		startCivil:   "2025-01-01T07:00:00",
		endCivil:     "2025-01-01T07:00:00",
		civilDate:    "2025-01-01",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"height":{"sampleTime":{"physicalTime":"2025-01-01T07:00:00Z"},"heightMeters":"1.82"}}`,
	})
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "height",
		resourceName: "users/me/dataTypes/height/dataPoints/height-new",
		recordKind:   "sample",
		startUTC:     "2026-06-08T07:00:00Z",
		endUTC:       "2026-06-08T07:00:00Z",
		startCivil:   "2026-06-08T08:00:00",
		endCivil:     "2026-06-08T08:00:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"height":{"sampleTime":{"physicalTime":"2026-06-08T08:00:00+01:00"},"heightMeters":"1.83"}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT sample_time_utc, height_meters FROM current_height`)
	if err != nil {
		t.Fatalf("query current_height: %v", err)
	}
	defer rows.Close()

	count := 0
	var sampleTime, heightMeters sql.NullString
	for rows.Next() {
		count++
		if err := rows.Scan(&sampleTime, &heightMeters); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}
	if count != 1 {
		t.Fatalf("current_height row count = %d, want 1", count)
	}
	if sampleTime.String != "2026-06-08T07:00:00Z" {
		t.Fatalf("sample_time_utc = %q, want 2026-06-08T07:00:00Z (latest)", sampleTime.String)
	}
	if heightMeters.String != "1.83" {
		t.Fatalf("height_meters = %q, want 1.83 (latest)", heightMeters.String)
	}
}

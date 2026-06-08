package main

import (
	"testing"
)

// TestDailyVo2MaxViewProjectsScalars pins the contract for the
// daily_vo2_max view (#103): one row per archived daily Data Point with
// the principal vo2Max scalar (TEXT to preserve precision), the
// cardio-fitness-level enum, the covariance scalar, and civil_date.
// Fixture JSON mirrors the live Google Health API response shape
// observed via `sync --types daily-vo2-max` against the user's archive.
func TestDailyVo2MaxViewProjectsScalars(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "daily-vo2-max",
		resourceName: "users/me/dataTypes/daily-vo2-max/dataPoints/2026-06-07",
		recordKind:   "daily",
		civilDate:    "2026-06-07",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"dailyVo2Max":{"date":{"year":2026,"month":6,"day":7},"vo2Max":52.72084217366443,"cardioFitnessLevel":"VERY_GOOD","vo2MaxCovariance":0.76340626688998781}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	var civilDate, vo2Max, cardioLevel, covariance string
	if err := db.QueryRow(`SELECT civil_date, vo2_max, cardio_fitness_level, vo2_max_covariance FROM daily_vo2_max`).Scan(&civilDate, &vo2Max, &cardioLevel, &covariance); err != nil {
		t.Fatalf("query daily_vo2_max: %v", err)
	}
	if civilDate != "2026-06-07" {
		t.Errorf("civil_date = %q, want 2026-06-07", civilDate)
	}
	// SQLite json_extract → CAST AS TEXT formats doubles with ~15
	// significant digits of precision, which is enough for vo2Max
	// (52.72…) and covariance (0.76…) but does trim the live API's
	// 17-digit doubles. The raw JSON is preserved verbatim in
	// data_points.raw_json for callers who need full precision; the
	// view exposes the value SQLite returns.
	if vo2Max != "52.7208421736644" {
		t.Errorf("vo2_max = %q, want 52.7208421736644 (TEXT preserves 15-digit precision)", vo2Max)
	}
	if cardioLevel != "VERY_GOOD" {
		t.Errorf("cardio_fitness_level = %q, want VERY_GOOD", cardioLevel)
	}
	if covariance != "0.763406266889988" {
		t.Errorf("vo2_max_covariance = %q, want 0.763406266889988", covariance)
	}
}

// TestDailyHeartRateZonesViewExplodesEachZone pins the contract for
// daily_heart_rate_zones: one row per zone in the daily Data Point's
// heartRateZones[] array, exposing the enum + min/max BPM scalars.
// Fixture JSON mirrors the live API response shape.
func TestDailyHeartRateZonesViewExplodesEachZone(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "daily-heart-rate-zones",
		resourceName: "users/me/dataTypes/daily-heart-rate-zones/dataPoints/2026-06-07",
		recordKind:   "daily",
		civilDate:    "2026-06-07",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON: `{"dailyHeartRateZones":{"date":{"year":2026,"month":6,"day":7},"heartRateZones":[
			{"heartRateZoneType":"LIGHT","minBeatsPerMinute":"30","maxBeatsPerMinute":"104"},
			{"heartRateZoneType":"MODERATE","minBeatsPerMinute":"105","maxBeatsPerMinute":"130"},
			{"heartRateZoneType":"VIGOROUS","minBeatsPerMinute":"131","maxBeatsPerMinute":"162"},
			{"heartRateZoneType":"PEAK","minBeatsPerMinute":"163","maxBeatsPerMinute":"220"}
		]}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT civil_date, heart_rate_zone_type, min_beats_per_minute, max_beats_per_minute FROM daily_heart_rate_zones ORDER BY heart_rate_zone_type`)
	if err != nil {
		t.Fatalf("query daily_heart_rate_zones: %v", err)
	}
	defer rows.Close()
	type row struct {
		civilDate, zoneType string
		minBPM, maxBPM      int64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.civilDate, &r.zoneType, &r.minBPM, &r.maxBPM); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("rows = %d, want 4", len(got))
	}
	if got[0].zoneType != "LIGHT" || got[0].minBPM != 30 || got[0].maxBPM != 104 {
		t.Errorf("zone[0] = %+v, want LIGHT/30/104", got[0])
	}
	if got[1].zoneType != "MODERATE" || got[1].minBPM != 105 || got[1].maxBPM != 130 {
		t.Errorf("zone[1] = %+v, want MODERATE/105/130", got[1])
	}
	if got[2].zoneType != "PEAK" || got[2].minBPM != 163 || got[2].maxBPM != 220 {
		t.Errorf("zone[2] = %+v, want PEAK/163/220", got[2])
	}
	if got[3].zoneType != "VIGOROUS" || got[3].minBPM != 131 || got[3].maxBPM != 162 {
		t.Errorf("zone[3] = %+v, want VIGOROUS/131/162", got[3])
	}
	if got[0].civilDate != "2026-06-07" {
		t.Errorf("civil_date = %q, want 2026-06-07 (inherited from parent daily Data Point)", got[0].civilDate)
	}
}

// TestDailySleepTemperatureDerivationsViewProjectsScalars pins the
// contract for daily_sleep_temperature_derivations: one row per
// archived daily Data Point with the nightly temperature, the
// baseline, and the relative stddev, all in Celsius.
func TestDailySleepTemperatureDerivationsViewProjectsScalars(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "daily-sleep-temperature-derivations",
		resourceName: "users/me/dataTypes/daily-sleep-temperature-derivations/dataPoints/2026-06-07",
		recordKind:   "daily",
		civilDate:    "2026-06-07",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"dailySleepTemperatureDerivations":{"date":{"year":2026,"month":6,"day":7},"nightlyTemperatureCelsius":36.4,"baselineTemperatureCelsius":36.7,"relativeNightlyStddev30dCelsius":0.25}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	var civilDate, nightly, baseline, stddev string
	if err := db.QueryRow(`SELECT civil_date, nightly_temperature_celsius, baseline_temperature_celsius, relative_nightly_stddev_30d_celsius FROM daily_sleep_temperature_derivations`).Scan(&civilDate, &nightly, &baseline, &stddev); err != nil {
		t.Fatalf("query daily_sleep_temperature_derivations: %v", err)
	}
	if civilDate != "2026-06-07" {
		t.Errorf("civil_date = %q, want 2026-06-07", civilDate)
	}
	if nightly != "36.4" || baseline != "36.7" || stddev != "0.25" {
		t.Errorf("scalars = (%q, %q, %q), want (36.4, 36.7, 0.25)", nightly, baseline, stddev)
	}
}

// TestRespiratoryRateSleepSummaryViewProjectsPerStageScalars pins the
// contract for respiratory_rate_sleep_summary: one row per archived
// sample Data Point with the principal full-sleep breaths-per-minute
// plus the per-stage scalars (deep/light/REM). All projected as TEXT
// to preserve floating-point precision.
func TestRespiratoryRateSleepSummaryViewProjectsPerStageScalars(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "respiratory-rate-sleep-summary",
		resourceName: "users/me/dataTypes/respiratory-rate-sleep-summary/dataPoints/sleep-2026-06-01",
		recordKind:   "sample",
		startUTC:     "2026-06-01T05:18:30Z",
		startCivil:   "2026-06-01T07:18:30",
		civilDate:    "2026-06-01",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"respiratoryRateSleepSummary":{"sampleTime":{"physicalTime":"2026-06-01T05:18:30Z"},"deepSleepStats":{"breathsPerMinute":13.4},"lightSleepStats":{"breathsPerMinute":13.6},"remSleepStats":{"breathsPerMinute":14.2},"fullSleepStats":{"breathsPerMinute":13.4}}}`,
	})

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	var sampleTime, civilDate, full, deep, light, rem string
	if err := db.QueryRow(`SELECT sample_time_utc, civil_date, full_sleep_breaths_per_minute, deep_sleep_breaths_per_minute, light_sleep_breaths_per_minute, rem_sleep_breaths_per_minute FROM respiratory_rate_sleep_summary`).Scan(&sampleTime, &civilDate, &full, &deep, &light, &rem); err != nil {
		t.Fatalf("query respiratory_rate_sleep_summary: %v", err)
	}
	if sampleTime != "2026-06-01T05:18:30Z" {
		t.Errorf("sample_time_utc = %q, want 2026-06-01T05:18:30Z", sampleTime)
	}
	if civilDate != "2026-06-01" {
		t.Errorf("civil_date = %q, want 2026-06-01", civilDate)
	}
	if full != "13.4" || deep != "13.4" || light != "13.6" || rem != "14.2" {
		t.Errorf("scalars = (full=%q, deep=%q, light=%q, rem=%q), want (13.4, 13.4, 13.6, 14.2)", full, deep, light, rem)
	}
}

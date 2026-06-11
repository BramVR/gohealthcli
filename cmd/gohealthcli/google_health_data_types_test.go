package main

import (
	"slices"
	"testing"
)

func TestGoogleHealthDataTypeCatalogDescribesCurrentBehavior(t *testing.T) {
	tests := []struct {
		dataType              string
		wantScopes            []string
		wantListFilterField   string
		wantSyncDataPoint     bool
		wantReconcile         bool
		wantDailyRollup       bool
		wantParser            string
		wantRecordKind        string
		wantDateRangeDefault  bool
		wantDefaultConfigType bool
	}{
		{
			dataType:              "steps",
			wantScopes:            []string{googleHealthActivityReadonlyScope},
			wantListFilterField:   "steps.interval.start_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantDailyRollup:       true,
			wantParser:            "interval",
			wantRecordKind:        "interval",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "heart-rate",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "heart_rate.sample_time.physical_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-resting-heart-rate",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "daily_resting_heart_rate.date",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "daily",
			wantRecordKind:        "daily",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "heart-rate-variability",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "heart_rate_variability.sample_time.physical_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-heart-rate-variability",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "daily_heart_rate_variability.date",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "daily",
			wantRecordKind:        "daily",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "oxygen-saturation",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "oxygen_saturation.sample_time.physical_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-oxygen-saturation",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "daily_oxygen_saturation.date",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "daily",
			wantRecordKind:        "daily",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-respiratory-rate",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "daily_respiratory_rate.date",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "daily",
			wantRecordKind:        "daily",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "sleep",
			wantScopes:            []string{googleHealthSleepReadonlyScope},
			wantListFilterField:   "sleep.interval.civil_end_time",
			wantSyncDataPoint:     true,
			wantParser:            "session",
			wantRecordKind:        "session",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "exercise",
			wantScopes:            []string{googleHealthActivityReadonlyScope},
			wantListFilterField:   "exercise.interval.civil_start_time",
			wantSyncDataPoint:     true,
			wantParser:            "session",
			wantRecordKind:        "session",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "distance",
			wantScopes:            []string{googleHealthActivityReadonlyScope},
			wantListFilterField:   "distance.interval.start_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "interval",
			wantRecordKind:        "interval",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "total-calories",
			wantScopes:            []string{googleHealthActivityReadonlyScope},
			wantParser:            "",
			wantRecordKind:        "",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "weight",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "weight.sample_time.physical_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		// Tier 1 Health metrics Data Types (#102). Opt-in only via
		// `--types <name>` — none are DefaultConfigType yet.
		{
			dataType:            "body-fat",
			wantScopes:          []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField: "body_fat.sample_time.physical_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "sample",
			wantRecordKind:      "sample",
		},
		{
			dataType:            "blood-glucose",
			wantScopes:          []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField: "blood_glucose.sample_time.physical_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "sample",
			wantRecordKind:      "sample",
		},
		{
			dataType:            "core-body-temperature",
			wantScopes:          []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField: "core_body_temperature.sample_time.physical_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "sample",
			wantRecordKind:      "sample",
		},
		{
			dataType:            "height",
			wantScopes:          []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField: "height.sample_time.physical_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "sample",
			wantRecordKind:      "sample",
		},
		// Tier 1 Daily + hydration Data Types (#103). None are
		// DefaultConfigType yet — users opt in via --types until each
		// has run cleanly against real data over multiple weeks.
		{
			dataType:             "daily-vo2-max",
			wantScopes:           []string{googleHealthActivityReadonlyScope},
			wantListFilterField:  "daily_vo2_max.date",
			wantSyncDataPoint:    true,
			wantReconcile:        true,
			wantParser:           "daily",
			wantRecordKind:       "daily",
			wantDateRangeDefault: true,
		},
		{
			dataType:             "daily-heart-rate-zones",
			wantScopes:           []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:  "daily_heart_rate_zones.date",
			wantSyncDataPoint:    true,
			wantReconcile:        true,
			wantParser:           "daily",
			wantRecordKind:       "daily",
			wantDateRangeDefault: true,
		},
		{
			dataType:             "daily-sleep-temperature-derivations",
			wantScopes:           []string{googleHealthSleepReadonlyScope},
			wantListFilterField:  "daily_sleep_temperature_derivations.date",
			wantSyncDataPoint:    true,
			wantReconcile:        true,
			wantParser:           "daily",
			wantRecordKind:       "daily",
			wantDateRangeDefault: true,
		},
		{
			dataType:            "respiratory-rate-sleep-summary",
			wantScopes:          []string{googleHealthSleepReadonlyScope},
			wantListFilterField: "respiratory_rate_sleep_summary.sample_time.physical_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "sample",
			wantRecordKind:      "sample",
		},
		{
			dataType:             "hydration-log",
			wantScopes:           []string{googleHealthNutritionReadonlyScope},
			wantListFilterField:  "hydration_log.interval.civil_start_time",
			wantSyncDataPoint:    true,
			wantParser:           "session",
			wantRecordKind:       "session",
			wantDateRangeDefault: true,
		},
		// Tier 2 ECG + IRN Data Types (#104). List-only Data Types
		// guarded by opt-in scopes (`connect --add-scopes ecg,irn`).
		// Neither is DefaultConfigType — users opt in via --types
		// once the scope is granted.
		{
			dataType:             "electrocardiogram",
			wantScopes:           []string{googleHealthEcgReadonlyScope},
			wantListFilterField:  "electrocardiogram.interval.civil_start_time",
			wantSyncDataPoint:    true,
			wantParser:           "session",
			wantRecordKind:       "session",
			wantDateRangeDefault: true,
		},
		{
			dataType:             "irregular-rhythm-notification",
			wantScopes:           []string{googleHealthIrnReadonlyScope},
			wantListFilterField:  "irregular_rhythm_notification.interval.civil_start_time",
			wantSyncDataPoint:    true,
			wantParser:           "session",
			wantRecordKind:       "session",
			wantDateRangeDefault: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.dataType, func(t *testing.T) {
			entry, ok := googleHealthDataTypes.Lookup(tt.dataType)
			if !ok {
				t.Fatalf("catalog missing Data Type %q", tt.dataType)
			}
			if !slices.Equal(entry.RequiredScopes, tt.wantScopes) {
				t.Fatalf("RequiredScopes = %v, want %v", entry.RequiredScopes, tt.wantScopes)
			}
			list, hasList := entry.SupportedEndpoints[endpointFamilyList]
			gotFilter := ""
			if hasList {
				gotFilter = list.FilterField
			}
			if gotFilter != tt.wantListFilterField {
				t.Fatalf("ListFilterField = %q, want %q", gotFilter, tt.wantListFilterField)
			}
			gotSyncDataPoint := syncDataPointDataTypeSupported(tt.dataType)
			if gotSyncDataPoint != tt.wantSyncDataPoint {
				t.Fatalf("syncDataPointDataTypeSupported = %v, want %v", gotSyncDataPoint, tt.wantSyncDataPoint)
			}
			gotReconcile := reconcileDataTypeSupported(tt.dataType)
			if gotReconcile != tt.wantReconcile {
				t.Fatalf("reconcileDataTypeSupported = %v, want %v", gotReconcile, tt.wantReconcile)
			}
			gotDailyRollup := dailyRollupDataTypeSupported(tt.dataType)
			if gotDailyRollup != tt.wantDailyRollup {
				t.Fatalf("dailyRollupDataTypeSupported = %v, want %v", gotDailyRollup, tt.wantDailyRollup)
			}
			if entry.Parser != tt.wantParser {
				t.Fatalf("Parser = %q, want %q", entry.Parser, tt.wantParser)
			}
			if entry.RecordKind != tt.wantRecordKind {
				t.Fatalf("RecordKind = %q, want %q", entry.RecordKind, tt.wantRecordKind)
			}
			if entry.UsesDateRangeDefault != tt.wantDateRangeDefault {
				t.Fatalf("UsesDateRangeDefault = %v, want %v", entry.UsesDateRangeDefault, tt.wantDateRangeDefault)
			}
			if entry.DefaultConfigType != tt.wantDefaultConfigType {
				t.Fatalf("DefaultConfigType = %v, want %v", entry.DefaultConfigType, tt.wantDefaultConfigType)
			}
		})
	}
}

func TestGoogleHealthDataTypeCatalogDescribesSourceFamilyFilters(t *testing.T) {
	filter, err := googleHealthSourceFamilyFilterName("steps", "wearable")
	if err != nil {
		t.Fatalf("source family filter: %v", err)
	}
	if filter != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("source family filter = %q, want google-wearables", filter)
	}
}

func TestGoogleHealthDataTypeCatalogDefaultDataTypes(t *testing.T) {
	want := []string{
		"steps",
		"heart-rate",
		"daily-resting-heart-rate",
		"heart-rate-variability",
		"daily-heart-rate-variability",
		"oxygen-saturation",
		"daily-oxygen-saturation",
		"daily-respiratory-rate",
		"sleep",
		"exercise",
		"distance",
		"total-calories",
		"weight",
	}
	if !slices.Equal(defaultDataTypes, want) {
		t.Fatalf("defaultDataTypes = %v, want %v", defaultDataTypes, want)
	}
	if !slices.Equal(googleHealthDataTypes.DefaultDataTypes(), want) {
		t.Fatalf("catalog defaults = %v, want %v", googleHealthDataTypes.DefaultDataTypes(), want)
	}
}

func TestGoogleHealthScopesForDataTypeReturnsCopy(t *testing.T) {
	scopes := googleHealthScopesForDataType("steps")
	if len(scopes) != 1 {
		t.Fatalf("scopes = %v, want one scope", scopes)
	}
	scopes[0] = "mutated"
	got := googleHealthScopesForDataType("steps")
	if !slices.Equal(got, []string{googleHealthActivityReadonlyScope}) {
		t.Fatalf("scopes after mutation = %v, want original scope", got)
	}
}

func TestGoogleHealthDataTypeCatalogRejectsUnknownDataType(t *testing.T) {
	if _, ok := googleHealthDataTypes.Lookup("bogus"); ok {
		t.Fatal("catalog contains bogus Data Type")
	}
}

func TestGoogleHealthDataTypeCatalogRejectsInvalidEntries(t *testing.T) {
	assertPanic := func(t *testing.T, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		fn()
	}

	t.Run("empty Data Type", func(t *testing.T) {
		assertPanic(t, func() {
			newGoogleHealthDataTypeCatalog([]googleHealthDataTypeCatalogEntry{{}})
		})
	})
	t.Run("duplicate Data Type", func(t *testing.T) {
		assertPanic(t, func() {
			newGoogleHealthDataTypeCatalog([]googleHealthDataTypeCatalogEntry{
				{DataType: "steps"},
				{DataType: "steps"},
			})
		})
	})
}

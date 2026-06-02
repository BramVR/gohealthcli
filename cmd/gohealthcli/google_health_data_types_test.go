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
			wantDailyRollup:       true,
			wantParser:            "steps",
			wantRecordKind:        "interval",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "heart-rate",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "heart_rate.sample_time.physical_time",
			wantSyncDataPoint:     true,
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-resting-heart-rate",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "daily_resting_heart_rate.date",
			wantSyncDataPoint:     true,
			wantParser:            "daily",
			wantRecordKind:        "daily",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "heart-rate-variability",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "heart_rate_variability.sample_time.physical_time",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-heart-rate-variability",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "daily_heart_rate_variability.date",
			wantSyncDataPoint:     true,
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
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-oxygen-saturation",
			wantScopes:            []string{googleHealthHealthMetricsReadonlyScope},
			wantListFilterField:   "daily_oxygen_saturation.date",
			wantSyncDataPoint:     true,
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
			wantParser:            "daily",
			wantRecordKind:        "daily",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "sleep",
			wantScopes:            []string{googleHealthSleepReadonlyScope},
			wantListFilterField:   "sleep.interval.end_time",
			wantParser:            "",
			wantRecordKind:        "",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "exercise",
			wantScopes:            []string{googleHealthActivityReadonlyScope},
			wantListFilterField:   "exercise.interval.civil_start_time",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "distance",
			wantScopes:            []string{googleHealthActivityReadonlyScope},
			wantListFilterField:   "distance.interval.start_time",
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
			wantDefaultConfigType: true,
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
			if entry.ListFilterField != tt.wantListFilterField {
				t.Fatalf("ListFilterField = %q, want %q", entry.ListFilterField, tt.wantListFilterField)
			}
			if entry.SupportsSyncDataPoint != tt.wantSyncDataPoint {
				t.Fatalf("SupportsSyncDataPoint = %v, want %v", entry.SupportsSyncDataPoint, tt.wantSyncDataPoint)
			}
			if entry.SupportsDailyRollup != tt.wantDailyRollup {
				t.Fatalf("SupportsDailyRollup = %v, want %v", entry.SupportsDailyRollup, tt.wantDailyRollup)
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

func TestGoogleHealthDataTypeCatalogDefaultDataTypes(t *testing.T) {
	if !slices.Equal(defaultDataTypes, googleHealthDataTypes.DefaultDataTypes()) {
		t.Fatalf("defaultDataTypes = %v, want catalog defaults %v", defaultDataTypes, googleHealthDataTypes.DefaultDataTypes())
	}
}

func TestGoogleHealthDataTypeCatalogRejectsUnknownDataType(t *testing.T) {
	if _, ok := googleHealthDataTypes.Lookup("bogus"); ok {
		t.Fatal("catalog contains bogus Data Type")
	}
}

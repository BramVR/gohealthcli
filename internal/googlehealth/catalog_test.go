package googlehealth

import (
	"slices"
	"sort"
	"testing"
)

func TestGoogleHealthDataTypeCatalogDescribesCurrentBehavior(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dataType              string
		wantScopes            []string
		wantListFilterField   string
		wantLowerBoundOnly    bool
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
			wantScopes:            []string{ScopeActivityReadonly},
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
			wantScopes:            []string{ScopeHealthMetricsReadonly},
			wantListFilterField:   "heart_rate.sample_time.physical_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantDailyRollup:       true,
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-resting-heart-rate",
			wantScopes:            []string{ScopeHealthMetricsReadonly},
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
			wantScopes:            []string{ScopeHealthMetricsReadonly},
			wantListFilterField:   "heart_rate_variability.sample_time.physical_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-heart-rate-variability",
			wantScopes:            []string{ScopeHealthMetricsReadonly},
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
			wantScopes:            []string{ScopeHealthMetricsReadonly},
			wantListFilterField:   "oxygen_saturation.sample_time.physical_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "daily-oxygen-saturation",
			wantScopes:            []string{ScopeHealthMetricsReadonly},
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
			wantScopes:            []string{ScopeHealthMetricsReadonly},
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
			wantScopes:            []string{ScopeSleepReadonly},
			wantListFilterField:   "sleep.interval.civil_end_time",
			wantSyncDataPoint:     true,
			wantParser:            "session",
			wantRecordKind:        "session",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "exercise",
			wantScopes:            []string{ScopeActivityReadonly},
			wantListFilterField:   "exercise.interval.civil_start_time",
			wantSyncDataPoint:     true,
			wantParser:            "session",
			wantRecordKind:        "session",
			wantDateRangeDefault:  true,
			wantDefaultConfigType: true,
		},
		{
			dataType:              "distance",
			wantScopes:            []string{ScopeActivityReadonly},
			wantListFilterField:   "distance.interval.start_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "interval",
			wantRecordKind:        "interval",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "total-calories",
			wantScopes:            []string{ScopeActivityReadonly},
			wantDailyRollup:       true,
			wantParser:            "",
			wantRecordKind:        "",
			wantDefaultConfigType: true,
		},
		{
			dataType:              "weight",
			wantScopes:            []string{ScopeHealthMetricsReadonly},
			wantListFilterField:   "weight.sample_time.physical_time",
			wantSyncDataPoint:     true,
			wantReconcile:         true,
			wantParser:            "sample",
			wantRecordKind:        "sample",
			wantDefaultConfigType: true,
		},
		{
			dataType:          "floors",
			wantScopes:        []string{ScopeActivityReadonly},
			wantSyncDataPoint: true,
			wantReconcile:     true,
			wantDailyRollup:   true,
			wantParser:        "interval",
			wantRecordKind:    "interval",
		},
		{
			dataType:            "basal-energy-burned",
			wantScopes:          []string{ScopeActivityReadonly},
			wantListFilterField: "basal_energy_burned.interval.start_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "interval",
			wantRecordKind:      "interval",
		},
		{
			dataType:          "calories-in-heart-rate-zone",
			wantScopes:        []string{ScopeActivityReadonly},
			wantSyncDataPoint: false,
			wantReconcile:     false,
			wantDailyRollup:   false,
			wantParser:        "interval",
			wantRecordKind:    "interval",
		},
		// Tier 1 Health metrics Data Types (#102). Opt-in only via
		// `--types <name>` — none are DefaultConfigType yet.
		{
			dataType:            "body-fat",
			wantScopes:          []string{ScopeHealthMetricsReadonly},
			wantListFilterField: "body_fat.sample_time.physical_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "sample",
			wantRecordKind:      "sample",
		},
		{
			dataType:            "blood-glucose",
			wantScopes:          []string{ScopeHealthMetricsReadonly},
			wantListFilterField: "blood_glucose.sample_time.physical_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "sample",
			wantRecordKind:      "sample",
		},
		{
			dataType:            "core-body-temperature",
			wantScopes:          []string{ScopeHealthMetricsReadonly},
			wantListFilterField: "core_body_temperature.sample_time.physical_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "sample",
			wantRecordKind:      "sample",
		},
		{
			dataType:            "height",
			wantScopes:          []string{ScopeHealthMetricsReadonly},
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
			wantScopes:           []string{ScopeActivityReadonly},
			wantListFilterField:  "daily_vo2_max.date",
			wantSyncDataPoint:    true,
			wantReconcile:        true,
			wantParser:           "daily",
			wantRecordKind:       "daily",
			wantDateRangeDefault: true,
		},
		{
			dataType:             "daily-heart-rate-zones",
			wantScopes:           []string{ScopeHealthMetricsReadonly},
			wantListFilterField:  "daily_heart_rate_zones.date",
			wantSyncDataPoint:    true,
			wantReconcile:        true,
			wantParser:           "daily",
			wantRecordKind:       "daily",
			wantDateRangeDefault: true,
		},
		{
			dataType:             "daily-sleep-temperature-derivations",
			wantScopes:           []string{ScopeSleepReadonly},
			wantListFilterField:  "daily_sleep_temperature_derivations.date",
			wantSyncDataPoint:    true,
			wantReconcile:        true,
			wantParser:           "daily",
			wantRecordKind:       "daily",
			wantDateRangeDefault: true,
		},
		{
			dataType:            "respiratory-rate-sleep-summary",
			wantScopes:          []string{ScopeHealthMetricsReadonly},
			wantListFilterField: "respiratory_rate_sleep_summary.sample_time.physical_time",
			wantSyncDataPoint:   true,
			wantReconcile:       true,
			wantParser:          "sample",
			wantRecordKind:      "sample",
		},
		{
			dataType:             "hydration-log",
			wantScopes:           []string{ScopeNutritionReadonly},
			wantListFilterField:  "hydration_log.interval.civil_start_time",
			wantSyncDataPoint:    true,
			wantParser:           "session",
			wantRecordKind:       "session",
			wantDateRangeDefault: true,
		},
		{
			dataType:             "nutrition-log",
			wantScopes:           []string{ScopeNutritionReadonly},
			wantListFilterField:  "nutrition_log.interval.civil_start_time",
			wantSyncDataPoint:    true,
			wantReconcile:        true,
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
			wantScopes:           []string{ScopeEcgReadonly},
			wantListFilterField:  "electrocardiogram.interval.start_time",
			wantLowerBoundOnly:   true,
			wantSyncDataPoint:    true,
			wantParser:           "session",
			wantRecordKind:       "session",
			wantDateRangeDefault: true,
		},
		{
			dataType:             "irregular-rhythm-notification",
			wantScopes:           []string{ScopeIrnReadonly},
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
			if hasList && list.LowerBoundOnly != tt.wantLowerBoundOnly {
				t.Fatalf("List LowerBoundOnly = %v, want %v", list.LowerBoundOnly, tt.wantLowerBoundOnly)
			}
			gotSyncDataPoint := SupportsSyncDataPoints(tt.dataType)
			if gotSyncDataPoint != tt.wantSyncDataPoint {
				t.Fatalf("SupportsSyncDataPoints = %v, want %v", gotSyncDataPoint, tt.wantSyncDataPoint)
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
	t.Parallel()
	filter, err := SourceFamilyFilterName("steps", "wearable")
	if err != nil {
		t.Fatalf("source family filter: %v", err)
	}
	if filter != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("source family filter = %q, want google-wearables", filter)
	}
}

func TestGoogleHealthDataTypeCatalogDefaultDataTypes(t *testing.T) {
	t.Parallel()
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

func TestGoogleHealthDataTypeCatalogCompletionViews(t *testing.T) {
	t.Parallel()

	var wantSyncable []string
	var wantListable []string
	for _, dataType := range googleHealthDataTypes.order {
		entry, ok := googleHealthDataTypes.Lookup(dataType)
		if !ok {
			t.Fatalf("catalog order contains missing Data Type %q", dataType)
		}
		if _, ok := entry.SupportedEndpoints[endpointFamilyList]; ok {
			wantListable = append(wantListable, dataType)
		}
		if _, listable := entry.SupportedEndpoints[endpointFamilyList]; listable {
			wantSyncable = append(wantSyncable, dataType)
			continue
		}
		if _, reconcilable := entry.SupportedEndpoints[endpointFamilyReconcile]; reconcilable {
			wantSyncable = append(wantSyncable, dataType)
		}
	}
	sort.Strings(wantSyncable)
	sort.Strings(wantListable)

	if got := SyncableDataTypes(); !slices.Equal(got, wantSyncable) {
		t.Fatalf("SyncableDataTypes = %v, want catalog projection %v", got, wantSyncable)
	}
	if got := ListableDataTypes(); !slices.Equal(got, wantListable) {
		t.Fatalf("ListableDataTypes = %v, want catalog projection %v", got, wantListable)
	}

	got := SyncableDataTypes()
	got[0] = "mutated"
	if fresh := SyncableDataTypes(); fresh[0] == "mutated" {
		t.Fatal("SyncableDataTypes returned shared catalog state")
	}
}

func TestCatalogBrowseProjectionsCoverCanonicalFacts(t *testing.T) {
	t.Parallel()

	dataTypes := CatalogDataTypes()
	if len(dataTypes) != len(googleHealthDataTypes.order) {
		t.Fatalf("CatalogDataTypes returned %d rows, want %d canonical rows", len(dataTypes), len(googleHealthDataTypes.order))
	}
	wantScopeMembers := make(map[string][]string)
	for index, dataType := range googleHealthDataTypes.order {
		entry, ok := googleHealthDataTypes.Lookup(dataType)
		if !ok {
			t.Fatalf("catalog order contains missing Data Type %q", dataType)
		}
		got := dataTypes[index]
		if got.DataType != dataType {
			t.Errorf("CatalogDataTypes()[%d].DataType = %q, want %q", index, got.DataType, dataType)
		}
		wantSelection := "opt_in"
		if entry.DefaultConfigType {
			wantSelection = "default"
		}
		if got.Selection != wantSelection {
			t.Errorf("CatalogDataTypes()[%d].Selection = %q, want %q", index, got.Selection, wantSelection)
		}
		wantRaw := "unsupported"
		if SupportsSyncDataPoints(dataType) {
			wantRaw = "supported"
		}
		if got.RawDataPoints != wantRaw {
			t.Errorf("CatalogDataTypes()[%d].RawDataPoints = %q, want %q", index, got.RawDataPoints, wantRaw)
		}
		if !slices.Equal(got.RequiredScopes, entry.RequiredScopes) {
			t.Errorf("CatalogDataTypes()[%d].RequiredScopes = %v, want %v", index, got.RequiredScopes, entry.RequiredScopes)
		}
		for _, scope := range entry.RequiredScopes {
			wantScopeMembers[scope] = append(wantScopeMembers[scope], dataType)
		}
	}

	scopes := CatalogScopes()
	if len(scopes) != len(wantScopeMembers) {
		t.Fatalf("CatalogScopes returned %d rows, want %d exact scopes", len(scopes), len(wantScopeMembers))
	}
	if !sort.SliceIsSorted(scopes, func(i, j int) bool { return scopes[i].Scope < scopes[j].Scope }) {
		t.Errorf("CatalogScopes order is not lexical: %v", scopes)
	}
	for _, scope := range scopes {
		want, ok := wantScopeMembers[scope.Scope]
		if !ok {
			t.Errorf("CatalogScopes returned unknown scope %q", scope.Scope)
			continue
		}
		if !slices.Equal(scope.DataTypes, want) {
			t.Errorf("CatalogScopes membership for %q = %v, want catalog order %v", scope.Scope, scope.DataTypes, want)
		}
		delete(wantScopeMembers, scope.Scope)
	}
	if len(wantScopeMembers) != 0 {
		t.Errorf("CatalogScopes omitted required scopes: %v", wantScopeMembers)
	}

	dataTypes[0].RequiredScopes[0] = "mutated"
	if CatalogDataTypes()[0].RequiredScopes[0] == "mutated" {
		t.Fatal("CatalogDataTypes returned shared scope state")
	}
	scopes[0].DataTypes[0] = "mutated"
	if CatalogScopes()[0].DataTypes[0] == "mutated" {
		t.Fatal("CatalogScopes returned shared membership state")
	}
}

func TestGoogleHealthSourceFamilyCompletionView(t *testing.T) {
	t.Parallel()

	got := SupportedSourceFamilies()
	if !slices.Equal(got, []string{"wearable"}) {
		t.Fatalf("SupportedSourceFamilies = %v, want [wearable]", got)
	}
	got[0] = "mutated"
	if fresh := SupportedSourceFamilies(); !slices.Equal(fresh, []string{"wearable"}) {
		t.Fatalf("SupportedSourceFamilies returned shared state: %v", fresh)
	}
}

func TestGoogleHealthScopesForDataTypeReturnsCopy(t *testing.T) {
	t.Parallel()
	scopes := ScopesForDataType("steps")
	if len(scopes) != 1 {
		t.Fatalf("scopes = %v, want one scope", scopes)
	}
	scopes[0] = "mutated"
	got := ScopesForDataType("steps")
	if !slices.Equal(got, []string{ScopeActivityReadonly}) {
		t.Fatalf("scopes after mutation = %v, want original scope", got)
	}
}

func TestGoogleHealthDataTypeCatalogRejectsUnknownDataType(t *testing.T) {
	t.Parallel()
	if _, ok := googleHealthDataTypes.Lookup("bogus"); ok {
		t.Fatal("catalog contains bogus Data Type")
	}
}

func TestGoogleHealthDataTypeCatalogRejectsInvalidEntries(t *testing.T) {
	t.Parallel()
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

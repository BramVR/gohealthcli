package main

import "fmt"

type googleHealthDataTypeCatalogEntry struct {
	DataType              string
	RequiredScopes        []string
	ListFilterField       string
	SupportsSyncDataPoint bool
	SupportsReconcile     bool
	SupportsDailyRollup   bool
	Parser                string
	JSONField             string
	RecordKind            string
	UsesDateRangeDefault  bool
	DefaultConfigType     bool
}

type googleHealthDataTypeCatalog struct {
	entries map[string]googleHealthDataTypeCatalogEntry
	order   []string
}

var googleHealthDataTypes = newGoogleHealthDataTypeCatalog([]googleHealthDataTypeCatalogEntry{
	{
		DataType:              "steps",
		RequiredScopes:        []string{googleHealthActivityReadonlyScope},
		ListFilterField:       "steps.interval.start_time",
		SupportsSyncDataPoint: true,
		SupportsReconcile:     true,
		SupportsDailyRollup:   true,
		Parser:                "steps",
		RecordKind:            "interval",
		DefaultConfigType:     true,
	},
	{
		DataType:              "heart-rate",
		RequiredScopes:        []string{googleHealthHealthMetricsReadonlyScope},
		ListFilterField:       "heart_rate.sample_time.physical_time",
		SupportsSyncDataPoint: true,
		SupportsReconcile:     true,
		Parser:                "sample",
		JSONField:             "heartRate",
		RecordKind:            "sample",
		DefaultConfigType:     true,
	},
	{
		DataType:              "daily-resting-heart-rate",
		RequiredScopes:        []string{googleHealthHealthMetricsReadonlyScope},
		ListFilterField:       "daily_resting_heart_rate.date",
		SupportsSyncDataPoint: true,
		SupportsReconcile:     true,
		Parser:                "daily",
		JSONField:             "dailyRestingHeartRate",
		RecordKind:            "daily",
		UsesDateRangeDefault:  true,
		DefaultConfigType:     true,
	},
	{
		DataType:          "heart-rate-variability",
		RequiredScopes:    []string{googleHealthHealthMetricsReadonlyScope},
		ListFilterField:   "heart_rate_variability.sample_time.physical_time",
		DefaultConfigType: true,
	},
	{
		DataType:              "daily-heart-rate-variability",
		RequiredScopes:        []string{googleHealthHealthMetricsReadonlyScope},
		ListFilterField:       "daily_heart_rate_variability.date",
		SupportsSyncDataPoint: true,
		SupportsReconcile:     true,
		Parser:                "daily",
		JSONField:             "dailyHeartRateVariability",
		RecordKind:            "daily",
		UsesDateRangeDefault:  true,
		DefaultConfigType:     true,
	},
	{
		DataType:              "oxygen-saturation",
		RequiredScopes:        []string{googleHealthHealthMetricsReadonlyScope},
		ListFilterField:       "oxygen_saturation.sample_time.physical_time",
		SupportsSyncDataPoint: true,
		SupportsReconcile:     true,
		Parser:                "sample",
		JSONField:             "oxygenSaturation",
		RecordKind:            "sample",
		DefaultConfigType:     true,
	},
	{
		DataType:              "daily-oxygen-saturation",
		RequiredScopes:        []string{googleHealthHealthMetricsReadonlyScope},
		ListFilterField:       "daily_oxygen_saturation.date",
		SupportsSyncDataPoint: true,
		SupportsReconcile:     true,
		Parser:                "daily",
		JSONField:             "dailyOxygenSaturation",
		RecordKind:            "daily",
		UsesDateRangeDefault:  true,
		DefaultConfigType:     true,
	},
	{
		DataType:              "daily-respiratory-rate",
		RequiredScopes:        []string{googleHealthHealthMetricsReadonlyScope},
		ListFilterField:       "daily_respiratory_rate.date",
		SupportsSyncDataPoint: true,
		SupportsReconcile:     true,
		Parser:                "daily",
		JSONField:             "dailyRespiratoryRate",
		RecordKind:            "daily",
		UsesDateRangeDefault:  true,
		DefaultConfigType:     true,
	},
	{
		DataType:              "sleep",
		RequiredScopes:        []string{googleHealthSleepReadonlyScope},
		ListFilterField:       "sleep.interval.civil_end_time",
		SupportsSyncDataPoint: true,
		Parser:                "session",
		JSONField:             "sleep",
		RecordKind:            "session",
		UsesDateRangeDefault:  true,
		DefaultConfigType:     true,
	},
	{
		DataType:              "exercise",
		RequiredScopes:        []string{googleHealthActivityReadonlyScope},
		ListFilterField:       "exercise.interval.civil_start_time",
		SupportsSyncDataPoint: true,
		Parser:                "session",
		JSONField:             "exercise",
		RecordKind:            "session",
		UsesDateRangeDefault:  true,
		DefaultConfigType:     true,
	},
	{
		DataType:          "distance",
		RequiredScopes:    []string{googleHealthActivityReadonlyScope},
		ListFilterField:   "distance.interval.start_time",
		DefaultConfigType: true,
	},
	{
		DataType:          "total-calories",
		RequiredScopes:    []string{googleHealthActivityReadonlyScope},
		DefaultConfigType: true,
	},
	{
		DataType:          "weight",
		RequiredScopes:    []string{googleHealthHealthMetricsReadonlyScope},
		ListFilterField:   "weight.sample_time.physical_time",
		DefaultConfigType: true,
	},
})

var defaultDataTypes = googleHealthDataTypes.DefaultDataTypes()

const googleHealthWearableSourceFamilyFilterName = "users/me/dataSourceFamilies/google-wearables"

func newGoogleHealthDataTypeCatalog(entries []googleHealthDataTypeCatalogEntry) googleHealthDataTypeCatalog {
	catalog := googleHealthDataTypeCatalog{
		entries: make(map[string]googleHealthDataTypeCatalogEntry, len(entries)),
		order:   make([]string, 0, len(entries)),
	}
	for _, entry := range entries {
		if entry.DataType == "" {
			panic("Google Health Data Type catalog contains empty DataType")
		}
		if _, ok := catalog.entries[entry.DataType]; ok {
			panic(fmt.Sprintf("Google Health Data Type catalog contains duplicate DataType %q", entry.DataType))
		}
		catalog.entries[entry.DataType] = entry
		catalog.order = append(catalog.order, entry.DataType)
	}
	return catalog
}

func (catalog googleHealthDataTypeCatalog) Lookup(dataType string) (googleHealthDataTypeCatalogEntry, bool) {
	entry, ok := catalog.entries[dataType]
	if !ok {
		return googleHealthDataTypeCatalogEntry{}, false
	}
	return entry, true
}

func (catalog googleHealthDataTypeCatalog) DefaultDataTypes() []string {
	dataTypes := make([]string, 0, len(catalog.order))
	for _, dataType := range catalog.order {
		entry, ok := catalog.entries[dataType]
		if ok && entry.DefaultConfigType {
			dataTypes = append(dataTypes, dataType)
		}
	}
	return dataTypes
}

func googleHealthScopesForDataType(dataType string) []string {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok {
		return nil
	}
	return append([]string(nil), entry.RequiredScopes...)
}

func googleHealthDataTypeListFilterField(dataType string) (string, error) {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if ok && entry.ListFilterField != "" {
		return entry.ListFilterField, nil
	}
	return "", fmt.Errorf("raw Data Type %q is not supported by dataPoints.list", dataType)
}

func googleHealthSampleDataPointJSONField(dataType string) string {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok || entry.Parser != "sample" {
		return ""
	}
	return entry.JSONField
}

type googleHealthDailyDataPointShape struct {
	jsonField   string
	filterField string
}

func googleHealthDailyDataPointShapeForDataType(dataType string) (googleHealthDailyDataPointShape, bool) {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok || entry.Parser != "daily" {
		return googleHealthDailyDataPointShape{}, false
	}
	return googleHealthDailyDataPointShape{jsonField: entry.JSONField, filterField: entry.ListFilterField}, true
}

func googleHealthDailyDataPointJSONField(dataType string) string {
	if shape, ok := googleHealthDailyDataPointShapeForDataType(dataType); ok {
		return shape.jsonField
	}
	return ""
}

func googleHealthSessionDataPointJSONField(dataType string) string {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok || entry.Parser != "session" {
		return ""
	}
	return entry.JSONField
}

func syncDataPointDataTypeSupported(dataType string) bool {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	return ok && entry.SupportsSyncDataPoint
}

func reconcileDataTypeSupported(dataType string) bool {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	return ok && entry.SupportsReconcile
}

func googleHealthSourceFamilyFilterName(dataType, sourceFamily string) (string, error) {
	if !reconcileDataTypeSupported(dataType) {
		return "", fmt.Errorf("sync --source-family is not supported for Data Type %s", dataType)
	}
	switch sourceFamily {
	case "wearable":
		return googleHealthWearableSourceFamilyFilterName, nil
	default:
		return "", fmt.Errorf("sync --source-family currently supports only wearable")
	}
}

func dailyRollupDataTypeSupported(dataType string) bool {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	return ok && entry.SupportsDailyRollup
}

func syncDataPointUsesDateRange(dataType string) bool {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	return ok && entry.UsesDateRangeDefault
}

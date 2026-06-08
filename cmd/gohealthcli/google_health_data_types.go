package main

import "fmt"

// endpointFamily identifies the Google Health API endpoint family a
// Data Type catalog entry supports. Per the architecture review on
// PRD #93, parallel boolean fields are gone — the SupportedEndpoints
// map is the single source of truth for "which endpoints this Data
// Type exposes".
type endpointFamily string

const (
	endpointFamilyList        endpointFamily = "list"
	endpointFamilyReconcile   endpointFamily = "reconcile"
	endpointFamilyRollUp      endpointFamily = "rollUp"
	endpointFamilyDailyRollUp endpointFamily = "dailyRollUp"
)

// endpointSupport carries the per-family metadata callers need (filter
// field for list/reconcile, rollup value type for rollUp/dailyRollUp).
// Adding a new endpoint family for a Data Type is one map entry — no
// new struct field.
type endpointSupport struct {
	FilterField         string   // for list / reconcile — e.g. "steps.interval.start_time"
	RollupValueType     string   // for rollUp / dailyRollUp — drives the generic rollup parser
	WindowGranularities []string // for rollUp — e.g. ["1h","1d","7d"]; nil for fixed-window families
}

type googleHealthDataTypeCatalogEntry struct {
	DataType             string
	RequiredScopes       []string
	Parser               string
	JSONField            string
	RecordKind           string
	UsesDateRangeDefault bool
	DefaultConfigType    bool
	SupportedEndpoints   map[endpointFamily]endpointSupport
}

type googleHealthDataTypeCatalog struct {
	entries map[string]googleHealthDataTypeCatalogEntry
	order   []string
}

// listEndpoints / listReconcile constructors keep entry definitions
// terse. The previous parallel-boolean layout had ~7 fields per entry;
// using these helpers preserves that brevity while reading from one
// canonical source.
func listEndpoint(filterField string) map[endpointFamily]endpointSupport {
	return map[endpointFamily]endpointSupport{
		endpointFamilyList: {FilterField: filterField},
	}
}

func listReconcileEndpoints(filterField string) map[endpointFamily]endpointSupport {
	return map[endpointFamily]endpointSupport{
		endpointFamilyList:      {FilterField: filterField},
		endpointFamilyReconcile: {FilterField: filterField},
	}
}

func listReconcileDailyRollupEndpoints(filterField, rollupValueType string) map[endpointFamily]endpointSupport {
	return map[endpointFamily]endpointSupport{
		endpointFamilyList:        {FilterField: filterField},
		endpointFamilyReconcile:   {FilterField: filterField},
		endpointFamilyDailyRollUp: {RollupValueType: rollupValueType},
	}
}

// listReconcileAllRollupEndpoints adds the windowed rollUp family
// alongside list/reconcile/dailyRollUp. Used by Data Types whose
// upstream supports both daily aggregates and arbitrary-window
// aggregates (steps, floors, …) so `sync --rollup hourly|weekly|window=…`
// dispatches through the same catalog row.
func listReconcileAllRollupEndpoints(filterField, dailyValueType, windowValueType string, windowGranularities []string) map[endpointFamily]endpointSupport {
	return map[endpointFamily]endpointSupport{
		endpointFamilyList:        {FilterField: filterField},
		endpointFamilyReconcile:   {FilterField: filterField},
		endpointFamilyDailyRollUp: {RollupValueType: dailyValueType},
		endpointFamilyRollUp:      {RollupValueType: windowValueType, WindowGranularities: windowGranularities},
	}
}

// listReconcileWithRollupEndpoints adds only the windowed rollUp
// family alongside list/reconcile. Used by Data Types whose upstream
// returns sample shapes per Data Point but supports aggregated rollUp
// (heart-rate hourly averages, etc.) and does not expose a separate
// dailyRollUp endpoint.
func listReconcileWithRollupEndpoints(filterField, rollupValueType string, windowGranularities []string) map[endpointFamily]endpointSupport {
	return map[endpointFamily]endpointSupport{
		endpointFamilyList:      {FilterField: filterField},
		endpointFamilyReconcile: {FilterField: filterField},
		endpointFamilyRollUp:    {RollupValueType: rollupValueType, WindowGranularities: windowGranularities},
	}
}

var googleHealthDataTypes = newGoogleHealthDataTypeCatalog([]googleHealthDataTypeCatalogEntry{
	{
		DataType:          "steps",
		RequiredScopes:    []string{googleHealthActivityReadonlyScope},
		Parser:            "steps",
		RecordKind:        "interval",
		DefaultConfigType: true,
		SupportedEndpoints: listReconcileAllRollupEndpoints(
			"steps.interval.start_time",
			"stepsCount",
			"stepsCount",
			[]string{"1h", "1d", "7d"},
		),
	},
	{
		DataType:          "heart-rate",
		RequiredScopes:    []string{googleHealthHealthMetricsReadonlyScope},
		Parser:            "sample",
		JSONField:         "heartRate",
		RecordKind:        "sample",
		DefaultConfigType: true,
		SupportedEndpoints: listReconcileWithRollupEndpoints(
			"heart_rate.sample_time.physical_time",
			"heartRate",
			[]string{"1h", "1d", "7d"},
		),
	},
	{
		DataType:             "daily-resting-heart-rate",
		RequiredScopes:       []string{googleHealthHealthMetricsReadonlyScope},
		Parser:               "daily",
		JSONField:            "dailyRestingHeartRate",
		RecordKind:           "daily",
		UsesDateRangeDefault: true,
		DefaultConfigType:    true,
		SupportedEndpoints:   listReconcileEndpoints("daily_resting_heart_rate.date"),
	},
	{
		DataType:           "heart-rate-variability",
		RequiredScopes:     []string{googleHealthHealthMetricsReadonlyScope},
		Parser:             "sample",
		JSONField:          "heartRateVariability",
		RecordKind:         "sample",
		DefaultConfigType:  true,
		SupportedEndpoints: listReconcileEndpoints("heart_rate_variability.sample_time.physical_time"),
	},
	{
		DataType:             "daily-heart-rate-variability",
		RequiredScopes:       []string{googleHealthHealthMetricsReadonlyScope},
		Parser:               "daily",
		JSONField:            "dailyHeartRateVariability",
		RecordKind:           "daily",
		UsesDateRangeDefault: true,
		DefaultConfigType:    true,
		SupportedEndpoints:   listReconcileEndpoints("daily_heart_rate_variability.date"),
	},
	{
		DataType:           "oxygen-saturation",
		RequiredScopes:     []string{googleHealthHealthMetricsReadonlyScope},
		Parser:             "sample",
		JSONField:          "oxygenSaturation",
		RecordKind:         "sample",
		DefaultConfigType:  true,
		SupportedEndpoints: listReconcileEndpoints("oxygen_saturation.sample_time.physical_time"),
	},
	{
		DataType:             "daily-oxygen-saturation",
		RequiredScopes:       []string{googleHealthHealthMetricsReadonlyScope},
		Parser:               "daily",
		JSONField:            "dailyOxygenSaturation",
		RecordKind:           "daily",
		UsesDateRangeDefault: true,
		DefaultConfigType:    true,
		SupportedEndpoints:   listReconcileEndpoints("daily_oxygen_saturation.date"),
	},
	{
		DataType:             "daily-respiratory-rate",
		RequiredScopes:       []string{googleHealthHealthMetricsReadonlyScope},
		Parser:               "daily",
		JSONField:            "dailyRespiratoryRate",
		RecordKind:           "daily",
		UsesDateRangeDefault: true,
		DefaultConfigType:    true,
		SupportedEndpoints:   listReconcileEndpoints("daily_respiratory_rate.date"),
	},
	{
		DataType:             "sleep",
		RequiredScopes:       []string{googleHealthSleepReadonlyScope},
		Parser:               "session",
		JSONField:            "sleep",
		RecordKind:           "session",
		UsesDateRangeDefault: true,
		DefaultConfigType:    true,
		SupportedEndpoints:   listEndpoint("sleep.interval.civil_end_time"),
	},
	{
		DataType:             "exercise",
		RequiredScopes:       []string{googleHealthActivityReadonlyScope},
		Parser:               "session",
		JSONField:            "exercise",
		RecordKind:           "session",
		UsesDateRangeDefault: true,
		DefaultConfigType:    true,
		SupportedEndpoints:   listEndpoint("exercise.interval.civil_start_time"),
	},
	{
		DataType:           "distance",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "distance",
		RecordKind:         "interval",
		DefaultConfigType:  true,
		SupportedEndpoints: listReconcileEndpoints("distance.interval.start_time"),
	},
	{
		DataType:          "total-calories",
		RequiredScopes:    []string{googleHealthActivityReadonlyScope},
		DefaultConfigType: true,
		// total-calories has no parser shape yet — reserved Tier 1 entry.
		// SupportedEndpoints stays nil; sync would error 'not supported'.
	},
	{
		DataType:           "weight",
		RequiredScopes:     []string{googleHealthHealthMetricsReadonlyScope},
		Parser:             "sample",
		JSONField:          "weight",
		RecordKind:         "sample",
		DefaultConfigType:  true,
		SupportedEndpoints: listReconcileEndpoints("weight.sample_time.physical_time"),
	},
	{
		// floors is the first Tier 1 Data Type to land via the new
		// SupportedEndpoints shape (#100). Interval-shaped, same
		// endpoint surface as steps (list + reconcile + dailyRollUp).
		//
		// Not yet a DefaultConfigType: the live API returns HTTP 400
		// against the assumed filter field, so including it in
		// `sync --all` would break that command for users. Add the
		// flag once the upstream endpoint shape is confirmed and the
		// floors sync runs cleanly end-to-end.
		DataType:       "floors",
		RequiredScopes: []string{googleHealthActivityReadonlyScope},
		Parser:         "interval",
		JSONField:      "floors",
		RecordKind:     "interval",
		SupportedEndpoints: listReconcileAllRollupEndpoints(
			"floors.interval.start_time",
			"floorsCount",
			"floorsCount",
			[]string{"1h", "1d", "7d"},
		),
	},
	// Tier 1 Activity & fitness Data Types (#101). Each carries a
	// hopeful filter field based on the existing pattern (snake-case
	// data-type prefix + .interval.start_time for interval or
	// .sample_time.physical_time for sample); the live probe step
	// flips DefaultConfigType once the response shape is confirmed.
	{
		DataType:           "active-energy-burned",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "activeEnergyBurned",
		RecordKind:         "interval",
		SupportedEndpoints: listReconcileEndpoints("active_energy_burned.interval.start_time"),
	},
	{
		DataType:           "active-minutes",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "activeMinutes",
		RecordKind:         "interval",
		SupportedEndpoints: listReconcileEndpoints("active_minutes.interval.start_time"),
	},
	{
		DataType:           "active-zone-minutes",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "activeZoneMinutes",
		RecordKind:         "interval",
		SupportedEndpoints: listReconcileEndpoints("active_zone_minutes.interval.start_time"),
	},
	{
		DataType:           "altitude",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "altitude",
		RecordKind:         "interval",
		SupportedEndpoints: listReconcileEndpoints("altitude.interval.start_time"),
	},
	{
		DataType:           "sedentary-period",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "sedentaryPeriod",
		RecordKind:         "interval",
		SupportedEndpoints: listReconcileEndpoints("sedentary_period.interval.start_time"),
	},
	{
		// calories-in-heart-rate-zone: live API returns HTTP 400 against
		// the assumed filter field; deferred until the upstream shape
		// is confirmed. Catalog row stays for future debugging.
		DataType:           "calories-in-heart-rate-zone",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "caloriesInHeartRateZone",
		RecordKind:         "interval",
		SupportedEndpoints: listReconcileEndpoints("calories_in_heart_rate_zone.interval.start_time"),
	},
	{
		DataType:           "time-in-heart-rate-zone",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "timeInHeartRateZone",
		RecordKind:         "interval",
		SupportedEndpoints: listReconcileEndpoints("time_in_heart_rate_zone.interval.start_time"),
	},
	{
		DataType:           "activity-level",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "activityLevel",
		RecordKind:         "interval",
		SupportedEndpoints: listReconcileEndpoints("activity_level.interval.start_time"),
	},
	{
		DataType:           "vo2-max",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "sample",
		JSONField:          "vo2Max",
		RecordKind:         "sample",
		SupportedEndpoints: listReconcileEndpoints("vo2_max.sample_time.physical_time"),
	},
	{
		DataType:           "run-vo2-max",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "sample",
		JSONField:          "runVo2Max",
		RecordKind:         "sample",
		SupportedEndpoints: listReconcileEndpoints("run_vo2_max.sample_time.physical_time"),
	},
	{
		DataType:           "swim-lengths-data",
		RequiredScopes:     []string{googleHealthActivityReadonlyScope},
		Parser:             "interval",
		JSONField:          "swimLengthsData",
		RecordKind:         "interval",
		SupportedEndpoints: listReconcileEndpoints("swim_lengths_data.interval.start_time"),
	},
	// Tier 1 Health metrics Data Types (#102). Same hopeful-filter
	// pattern as the Activity Tier 1 entries (#101): snake-case
	// data-type prefix + .sample_time.physical_time for sample-shaped
	// types. None flipped to DefaultConfigType until the upstream
	// shape is confirmed across multiple weeks of real data.
	{
		DataType:           "body-fat",
		RequiredScopes:     []string{googleHealthHealthMetricsReadonlyScope},
		Parser:             "sample",
		JSONField:          "bodyFat",
		RecordKind:         "sample",
		SupportedEndpoints: listReconcileEndpoints("body_fat.sample_time.physical_time"),
	},
	{
		DataType:           "blood-glucose",
		RequiredScopes:     []string{googleHealthHealthMetricsReadonlyScope},
		Parser:             "sample",
		JSONField:          "bloodGlucose",
		RecordKind:         "sample",
		SupportedEndpoints: listReconcileEndpoints("blood_glucose.sample_time.physical_time"),
	},
	{
		DataType:           "core-body-temperature",
		RequiredScopes:     []string{googleHealthHealthMetricsReadonlyScope},
		Parser:             "sample",
		JSONField:          "coreBodyTemperature",
		RecordKind:         "sample",
		SupportedEndpoints: listReconcileEndpoints("core_body_temperature.sample_time.physical_time"),
	},
	{
		DataType:           "height",
		RequiredScopes:     []string{googleHealthHealthMetricsReadonlyScope},
		Parser:             "sample",
		JSONField:          "height",
		RecordKind:         "sample",
		SupportedEndpoints: listReconcileEndpoints("height.sample_time.physical_time"),
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
	if !ok {
		return "", fmt.Errorf("raw Data Type %q is not in the catalog", dataType)
	}
	if list, ok := entry.SupportedEndpoints[endpointFamilyList]; ok && list.FilterField != "" {
		return list.FilterField, nil
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

func googleHealthIntervalDataPointJSONField(dataType string) string {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok || entry.Parser != "interval" {
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
	list := entry.SupportedEndpoints[endpointFamilyList]
	return googleHealthDailyDataPointShape{jsonField: entry.JSONField, filterField: list.FilterField}, true
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

// syncDataPointDataTypeSupported returns true if the catalog has at
// least one list/reconcile endpoint for the Data Type. Replaces the
// previous parallel-boolean field SupportsSyncDataPoint.
func syncDataPointDataTypeSupported(dataType string) bool {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok {
		return false
	}
	_, hasList := entry.SupportedEndpoints[endpointFamilyList]
	_, hasReconcile := entry.SupportedEndpoints[endpointFamilyReconcile]
	return hasList || hasReconcile
}

func reconcileDataTypeSupported(dataType string) bool {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok {
		return false
	}
	_, hasReconcile := entry.SupportedEndpoints[endpointFamilyReconcile]
	return hasReconcile
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
	if !ok {
		return false
	}
	_, hasDaily := entry.SupportedEndpoints[endpointFamilyDailyRollUp]
	return hasDaily
}

// windowRollupDataTypeSupported reports whether the Data Type's
// catalog entry carries the windowed rollUp endpoint family. Used by
// the sync planner to gate `--rollup hourly|weekly|window=<dur>`.
func windowRollupDataTypeSupported(dataType string) bool {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok {
		return false
	}
	_, hasRollUp := entry.SupportedEndpoints[endpointFamilyRollUp]
	return hasRollUp
}

func syncDataPointUsesDateRange(dataType string) bool {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	return ok && entry.UsesDateRangeDefault
}

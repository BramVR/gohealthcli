package googlehealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/archived"
	"sort"
	"strings"
)

var errRollupValueAbsent = errors.New("Google Health Rollup value union member is absent")

// parseGoogleHealthRollup is the generic Rollup parser introduced in
// #106, which replaced the steps-daily-only parser (deleted with the
// dead command-wrapper layer, #270). Its output for the steps-daily
// shape stays byte-identical to the legacy parser's — the #106 AC
// regression guard in google_health_rollup_test.go pins it.
//
// Errors include the Data Type's actual SupportedEndpoints when the
// rollup kind is unsupported — the #106 AC requires this quote.
func parseGoogleHealthRollup(connection archived.Connection, dataType, rollupKind string, rawRollup json.RawMessage) (archived.Rollup, error) {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok {
		return archived.Rollup{}, fmt.Errorf("Google Health %s Rollup: Data Type not in catalog", dataType)
	}
	valueType, family, err := rollupValueTypeForKind(entry, rollupKind)
	if err != nil {
		return archived.Rollup{}, err
	}
	canonicalRaw, err := compactJSONString(rawRollup)
	if err != nil {
		return archived.Rollup{}, fmt.Errorf("Google Health %s %s Rollup is not valid JSON", dataType, rollupKind)
	}
	rollup := archived.Rollup{
		ProviderName: connection.ProviderName,
		ConnectionID: connection.ID,
		DataType:     dataType,
		RollupKind:   rollupKind,
		RawJSON:      canonicalRaw,
	}
	if err := validateRollupValue(valueType, family, rawRollup, rollup); err != nil {
		return archived.Rollup{}, err
	}
	if family == endpointFamilyDailyRollUp {
		err = parseGoogleHealthDailyRollupCivilShape(rawRollup, &rollup)
	} else {
		err = parseGoogleHealthWindowRollupShape(rawRollup, &rollup)
	}
	if err != nil {
		return archived.Rollup{}, err
	}
	return rollup, nil
}

// rollupValueTypeForKind returns the RollupValueType that the catalog
// assigns to (dataType, rollupKind). dailyRollUp reads from the
// dailyRollUp endpoint entry; every non-daily kind reads from the
// rollUp entry. Returns an error whose message lists the actual
// SupportedEndpoints — the #106 AC requires this verbatim.
func rollupValueTypeForKind(entry googleHealthDataTypeCatalogEntry, rollupKind string) (string, endpointFamily, error) {
	family := endpointFamilyRollUp
	if rollupKind == "dailyRollUp" || rollupKind == "daily" {
		family = endpointFamilyDailyRollUp
	}
	support, ok := entry.SupportedEndpoints[family]
	if !ok || support.RollupValueType == "" {
		return "", family, fmt.Errorf("Google Health %s does not support %s Rollups; SupportedEndpoints=%s",
			entry.DataType, rollupKind, formatSupportedEndpoints(entry.SupportedEndpoints))
	}
	return support.RollupValueType, family, nil
}

// formatSupportedEndpoints renders the catalog map keys in stable
// sorted order so the error message is deterministic across runs.
func formatSupportedEndpoints(endpoints map[endpointFamily]endpointSupport) string {
	if len(endpoints) == 0 {
		return "[]"
	}
	keys := make([]string, 0, len(endpoints))
	for family := range endpoints {
		keys = append(keys, string(family))
	}
	sort.Strings(keys)
	return "[" + strings.Join(keys, ", ") + "]"
}

func parseGoogleHealthDailyRollupCivilShape(rawRollup json.RawMessage, rollup *archived.Rollup) error {
	var raw struct {
		CivilStartTime json.RawMessage `json:"civilStartTime"`
		CivilEndTime   json.RawMessage `json:"civilEndTime"`
	}
	if err := json.Unmarshal(rawRollup, &raw); err != nil {
		return fmt.Errorf("Google Health %s daily Rollup is not valid JSON", rollup.DataType)
	}
	_, civilDate, err := googleCivilDateTimeText(raw.CivilStartTime)
	if err != nil {
		return fmt.Errorf("Google Health %s daily Rollup civilStartTime: %w", rollup.DataType, err)
	}
	if civilDate == "" {
		return fmt.Errorf("Google Health %s daily Rollup missing civilStartTime", rollup.DataType)
	}
	if _, endCivilDate, err := googleCivilDateTimeText(raw.CivilEndTime); err != nil {
		return fmt.Errorf("Google Health %s daily Rollup civilEndTime: %w", rollup.DataType, err)
	} else if endCivilDate == "" {
		return fmt.Errorf("Google Health %s daily Rollup missing civilEndTime", rollup.DataType)
	}
	timezoneMetadata, err := googleDailyRollupTimeMetadataJSON(raw.CivilStartTime, raw.CivilEndTime)
	if err != nil {
		return fmt.Errorf("Google Health %s %w", rollup.DataType, err)
	}
	rollup.CivilDate = civilDate
	rollup.TimezoneMetadataJSON = timezoneMetadata
	return nil
}

// parseGoogleHealthWindowRollupShape parses the startTime / endTime
// shape used by rollUp responses (hourly, weekly, custom-window).
// Times arrive as RFC3339 strings rather than the
// civilStartTime/civilEndTime objects dailyRollUp returns.
func parseGoogleHealthWindowRollupShape(rawRollup json.RawMessage, rollup *archived.Rollup) error {
	var raw struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	}
	if err := json.Unmarshal(rawRollup, &raw); err != nil {
		return fmt.Errorf("Google Health %s %s Rollup is not valid JSON", rollup.DataType, rollup.RollupKind)
	}
	if raw.StartTime == "" {
		return fmt.Errorf("Google Health %s %s Rollup missing startTime", rollup.DataType, rollup.RollupKind)
	}
	if raw.EndTime == "" {
		return fmt.Errorf("Google Health %s %s Rollup missing endTime", rollup.DataType, rollup.RollupKind)
	}
	startUTC, err := normalizeGoogleTimestamp(raw.StartTime)
	if err != nil {
		return fmt.Errorf("Google Health %s %s Rollup startTime: %w", rollup.DataType, rollup.RollupKind, err)
	}
	endUTC, err := normalizeGoogleTimestamp(raw.EndTime)
	if err != nil {
		return fmt.Errorf("Google Health %s %s Rollup endTime: %w", rollup.DataType, rollup.RollupKind, err)
	}
	rollup.WindowStartUTC = startUTC
	rollup.WindowEndUTC = endUTC
	return nil
}

type rollupValueContract struct {
	jsonField         string
	absentMeansNoData bool
	validate          func(value json.RawMessage, dataType string) error
}

func rollupValueContractForType(valueType string) (rollupValueContract, error) {
	switch valueType {
	case "stepsCount":
		return rollupValueContract{jsonField: "steps"}, nil
	case "floorsCount":
		return rollupValueContract{jsonField: "floors"}, nil
	case "heartRate":
		return rollupValueContract{jsonField: "heartRate"}, nil
	case "totalCalories":
		return rollupValueContract{jsonField: "totalCalories", absentMeansNoData: true, validate: validateTotalCaloriesRollupValue}, nil
	case "caloriesInHeartRateZone":
		return rollupValueContract{jsonField: "caloriesInHeartRateZone", absentMeansNoData: true, validate: validateCaloriesInHeartRateZoneRollupValue}, nil
	}
	return rollupValueContract{}, fmt.Errorf("Google Health Rollup value type %q has no parser shape", valueType)
}

func validateRollupValue(valueType string, family endpointFamily, rawRollup json.RawMessage, rollup archived.Rollup) error {
	contract, err := rollupValueContractForType(valueType)
	if err != nil {
		return err
	}
	rollupLabel := rollup.DataType + " " + rollup.RollupKind
	if family == endpointFamilyDailyRollUp {
		rollupLabel = rollup.DataType + " daily"
	}
	value, err := rollupFieldRawValue(rawRollup, contract.jsonField)
	if err != nil {
		return fmt.Errorf("Google Health %s Rollup is not valid JSON", rollupLabel)
	}
	if value == nil {
		if contract.absentMeansNoData {
			return fmt.Errorf("%w: %s", errRollupValueAbsent, contract.jsonField)
		}
		return fmt.Errorf("Google Health %s Rollup missing %s value", rollupLabel, contract.jsonField)
	}
	if contract.validate == nil {
		return nil
	}
	return contract.validate(value, rollup.DataType)
}

func validateTotalCaloriesRollupValue(value json.RawMessage, dataType string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return fmt.Errorf("Google Health %s Rollup totalCalories is not an object", dataType)
	}
	rawKcal, ok := object["kcalSum"]
	if !ok || len(rawKcal) == 0 || string(rawKcal) == "null" {
		return fmt.Errorf("Google Health %s Rollup missing kcalSum", dataType)
	}
	var kcal float64
	if err := json.Unmarshal(rawKcal, &kcal); err != nil {
		return fmt.Errorf("Google Health %s Rollup kcalSum is not a number", dataType)
	}
	return nil
}

func validateCaloriesInHeartRateZoneRollupValue(value json.RawMessage, dataType string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return fmt.Errorf("Google Health %s Rollup caloriesInHeartRateZone is not an object", dataType)
	}
	var zones []json.RawMessage
	if rawZones, ok := object["caloriesInHeartRateZones"]; ok {
		if err := json.Unmarshal(rawZones, &zones); err != nil {
			return fmt.Errorf("Google Health %s Rollup caloriesInHeartRateZone.caloriesInHeartRateZones is not an array", dataType)
		}
	}
	for index, rawZone := range zones {
		var zone map[string]json.RawMessage
		if err := json.Unmarshal(rawZone, &zone); err != nil || zone == nil {
			return fmt.Errorf("Google Health %s Rollup caloriesInHeartRateZone.caloriesInHeartRateZones[%d] is not an object", dataType, index)
		}
		if label, ok := zone["heartRateZone"]; ok {
			var text string
			if err := json.Unmarshal(label, &text); err != nil {
				return fmt.Errorf("Google Health %s Rollup caloriesInHeartRateZone.caloriesInHeartRateZones[%d].heartRateZone is not a string", dataType, index)
			}
		}
		if kcal, ok := zone["kcal"]; ok {
			var number float64
			if err := json.Unmarshal(kcal, &number); err != nil {
				return fmt.Errorf("Google Health %s Rollup caloriesInHeartRateZone.caloriesInHeartRateZones[%d].kcal is not a number", dataType, index)
			}
		}
	}
	return nil
}

// rollupFieldRawValue extracts the raw JSON value of fieldName from
// rawRollup without forcing it into a Go shape — the parser only
// needs to know whether the field is present, not its scalar value
// (the canonical raw_json carries the bytes).
func rollupFieldRawValue(rawRollup json.RawMessage, fieldName string) (json.RawMessage, error) {
	var lookup map[string]json.RawMessage
	if err := json.Unmarshal(rawRollup, &lookup); err != nil {
		return nil, err
	}
	value, ok := lookup[fieldName]
	if !ok {
		return nil, nil
	}
	if len(value) == 0 || string(value) == "null" {
		return nil, nil
	}
	return value, nil
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/archived"
	"sort"
	"strings"
)

// parseGoogleHealthRollup is the generic Rollup parser introduced in
// #106, which replaced the steps-daily-only parser (deleted with the
// dead command-wrapper layer, #270). Its output for the steps-daily
// shape stays byte-identical to the legacy parser's — the #106 AC
// regression guard in google_health_rollup_test.go pins it.
//
// The parser looks up the Data Type's endpointSupport.RollupValueType
// for the active rollup family. RollupValueType drives the JSON-field
// dispatch and the time-shape dispatch:
//
//   - "stepsCount", "floorsCount", … carry the Google
//     civilStartTime/civilEndTime shape used by dailyRollUp.
//   - "heartRate", … carry the RFC3339 startTime/endTime shape used by
//     hourly / weekly / custom-window rollUp.
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
	if err := rollupValueTypeDispatch(valueType, family, rawRollup, &rollup); err != nil {
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

// rollupValueTypeDispatch unmarshals the rollup payload into the
// scalar shape named by valueType. The civil-time shape (dailyRollUp,
// "*Count" / *Sum / Daily…) populates civilDate + timezone metadata;
// the RFC3339 shape (rollUp hourly/weekly/window) populates
// windowStartUTC / windowEndUTC.
func rollupValueTypeDispatch(valueType string, family endpointFamily, rawRollup json.RawMessage, rollup *archived.Rollup) error {
	if family == endpointFamilyDailyRollUp {
		return parseGoogleHealthDailyRollupCivilShape(valueType, rawRollup, rollup)
	}
	return parseGoogleHealthWindowRollupShape(valueType, rawRollup, rollup)
}

// parseGoogleHealthDailyRollupCivilShape parses the civilStartTime /
// civilEndTime shape used by dailyRollUp responses. Today's catalog
// names the value-type by the JSON field it carries ("stepsCount" →
// "steps", "floorsCount" → "floors") so the dispatch is mechanical.
func parseGoogleHealthDailyRollupCivilShape(valueType string, rawRollup json.RawMessage, rollup *archived.Rollup) error {
	jsonField, err := rollupJSONFieldForValueType(valueType)
	if err != nil {
		return err
	}
	var raw struct {
		CivilStartTime json.RawMessage `json:"civilStartTime"`
		CivilEndTime   json.RawMessage `json:"civilEndTime"`
	}
	if err := json.Unmarshal(rawRollup, &raw); err != nil {
		return fmt.Errorf("Google Health %s daily Rollup is not valid JSON", rollup.DataType)
	}
	value, err := rollupFieldRawValue(rawRollup, jsonField)
	if err != nil {
		return err
	}
	if value == nil {
		return fmt.Errorf("Google Health %s daily Rollup missing %s value", rollup.DataType, jsonField)
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
func parseGoogleHealthWindowRollupShape(valueType string, rawRollup json.RawMessage, rollup *archived.Rollup) error {
	jsonField, err := rollupJSONFieldForValueType(valueType)
	if err != nil {
		return err
	}
	var raw struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	}
	if err := json.Unmarshal(rawRollup, &raw); err != nil {
		return fmt.Errorf("Google Health %s %s Rollup is not valid JSON", rollup.DataType, rollup.RollupKind)
	}
	value, err := rollupFieldRawValue(rawRollup, jsonField)
	if err != nil {
		return err
	}
	if value == nil {
		return fmt.Errorf("Google Health %s %s Rollup missing %s value", rollup.DataType, rollup.RollupKind, jsonField)
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

// rollupJSONFieldForValueType maps the catalog's RollupValueType
// scalar name to the JSON field key carrying the aggregate payload.
// "stepsCount" → "steps", "floorsCount" → "floors", "heartRate" →
// "heartRate". The dispatch table here is explicit so adding a new
// shape is one case, not a regex.
func rollupJSONFieldForValueType(valueType string) (string, error) {
	switch valueType {
	case "stepsCount":
		return "steps", nil
	case "floorsCount":
		return "floors", nil
	case "heartRate":
		return "heartRate", nil
	}
	return "", fmt.Errorf("Google Health Rollup value type %q has no parser shape", valueType)
}

// rollupFieldRawValue extracts the raw JSON value of fieldName from
// rawRollup without forcing it into a Go shape — the parser only
// needs to know whether the field is present, not its scalar value
// (the canonical raw_json carries the bytes).
func rollupFieldRawValue(rawRollup json.RawMessage, fieldName string) (json.RawMessage, error) {
	var lookup map[string]json.RawMessage
	if err := json.Unmarshal(rawRollup, &lookup); err != nil {
		return nil, errors.New("Google Health Rollup is not a JSON object")
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

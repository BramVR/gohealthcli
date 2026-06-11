package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGenericRollupParserDispatchSteps pins #106 Slice 1: the generic
// rollup parser reads endpointSupport.RollupValueType and unmarshals
// the stepsCount-shaped payload, producing the same archivedRollup the
// legacy steps-only parser produced.
func TestGenericRollupParserDispatchSteps(t *testing.T) {
	t.Parallel()
	conn := archivedConnection{
		providerName: "googlehealth",
		id:           "googlehealth:111111256096816351",
	}
	rawRollup := json.RawMessage(`{
		"steps": {"countSum": "1234"},
		"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
		"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
	}`)

	rollup, err := parseGoogleHealthRollup(conn, "steps", "dailyRollUp", rawRollup)
	if err != nil {
		t.Fatalf("parseGoogleHealthRollup steps: %v", err)
	}
	if rollup.dataType != "steps" {
		t.Errorf("dataType = %q, want steps", rollup.dataType)
	}
	if rollup.rollupKind != "dailyRollUp" {
		t.Errorf("rollupKind = %q, want dailyRollUp", rollup.rollupKind)
	}
	if rollup.civilDate != "2026-01-01" {
		t.Errorf("civilDate = %q, want 2026-01-01", rollup.civilDate)
	}
}

// TestGenericRollupParserDispatchHeartRate pins #106 Slice 1: the
// generic parser handles the heart-rate rollUp payload shape (bpmAvg /
// bpmMin / bpmMax) that lives behind RollupValueType="heartRate".
func TestGenericRollupParserDispatchHeartRate(t *testing.T) {
	t.Parallel()
	conn := archivedConnection{
		providerName: "googlehealth",
		id:           "googlehealth:111111256096816351",
	}
	rawRollup := json.RawMessage(`{
		"heartRate": {"bpmAvg": 72.5, "bpmMin": 55.0, "bpmMax": 110.0},
		"startTime": "2026-01-01T08:00:00Z",
		"endTime": "2026-01-01T09:00:00Z"
	}`)

	rollup, err := parseGoogleHealthRollup(conn, "heart-rate", "hourly", rawRollup)
	if err != nil {
		t.Fatalf("parseGoogleHealthRollup heart-rate: %v", err)
	}
	if rollup.dataType != "heart-rate" {
		t.Errorf("dataType = %q, want heart-rate", rollup.dataType)
	}
	if rollup.rollupKind != "hourly" {
		t.Errorf("rollupKind = %q, want hourly", rollup.rollupKind)
	}
	if rollup.windowStartUTC != "2026-01-01T08:00:00Z" {
		t.Errorf("windowStartUTC = %q, want 2026-01-01T08:00:00Z", rollup.windowStartUTC)
	}
	if rollup.windowEndUTC != "2026-01-01T09:00:00Z" {
		t.Errorf("windowEndUTC = %q, want 2026-01-01T09:00:00Z", rollup.windowEndUTC)
	}
}

// TestGenericRollupParserDispatchFloors pins the third value-type
// dispatch the #106 AC names ("parser dispatch for at least three
// rollup value types"). Floors carries RollupValueType="floorsCount".
func TestGenericRollupParserDispatchFloors(t *testing.T) {
	t.Parallel()
	conn := archivedConnection{
		providerName: "googlehealth",
		id:           "googlehealth:111111256096816351",
	}
	rawRollup := json.RawMessage(`{
		"floors": {"countSum": "12"},
		"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
		"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
	}`)

	rollup, err := parseGoogleHealthRollup(conn, "floors", "dailyRollUp", rawRollup)
	if err != nil {
		t.Fatalf("parseGoogleHealthRollup floors: %v", err)
	}
	if rollup.dataType != "floors" {
		t.Errorf("dataType = %q, want floors", rollup.dataType)
	}
	if rollup.civilDate != "2026-01-01" {
		t.Errorf("civilDate = %q, want 2026-01-01", rollup.civilDate)
	}
}

// TestGenericRollupParserRejectsUnknownDataType returns a typed error
// when the catalog has no rollup endpoint for the Data Type.
func TestGenericRollupParserRejectsUnknownDataType(t *testing.T) {
	t.Parallel()
	conn := archivedConnection{providerName: "googlehealth", id: "x"}
	_, err := parseGoogleHealthRollup(conn, "sleep", "dailyRollUp", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("parseGoogleHealthRollup sleep: want error, got nil")
	}
	if !strings.Contains(err.Error(), "sleep") {
		t.Errorf("err = %q, want it to mention sleep", err.Error())
	}
}

// TestStepsDailyRollupParserStillProducesByteIdenticalRow pins the
// #106 AC: the steps-daily byte-identical guard. The legacy
// steps-only parser was deleted with the dead command-wrapper layer
// (#270), so the guard pins the generic parser's output for the
// steps-daily shape to the exact archivedRollup row the legacy parser
// produced.
func TestStepsDailyRollupParserStillProducesByteIdenticalRow(t *testing.T) {
	t.Parallel()
	conn := archivedConnection{
		providerName: "googlehealth",
		id:           "googlehealth:111111256096816351",
	}
	rawRollup := json.RawMessage(`{
		"steps": {"countSum": "1234"},
		"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
		"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
	}`)

	generic, err := parseGoogleHealthRollup(conn, "steps", "dailyRollUp", rawRollup)
	if err != nil {
		t.Fatalf("generic parser: %v", err)
	}
	want := archivedRollup{
		providerName:         "googlehealth",
		connectionID:         "googlehealth:111111256096816351",
		dataType:             "steps",
		rollupKind:           "dailyRollUp",
		civilDate:            "2026-01-01",
		timezoneMetadataJSON: `{"civil_end_time":{"date":{"year":2026,"month":1,"day":2}},"civil_start_time":{"date":{"year":2026,"month":1,"day":1}}}`,
		rawJSON:              `{"steps":{"countSum":"1234"},"civilStartTime":{"date":{"year":2026,"month":1,"day":1}},"civilEndTime":{"date":{"year":2026,"month":1,"day":2}}}`,
	}
	if generic != want {
		t.Errorf("generic parser drift:\n   want=%#v\ngeneric=%#v", want, generic)
	}
}

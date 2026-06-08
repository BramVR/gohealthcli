package main

import (
	"strings"
	"testing"
	"time"
)

// TestSyncRollupSpecParseDaily pins the legacy --rollup=daily shape
// (the byte-identical AC). Parsing yields the dailyRollUp endpoint
// family with a 1-day windowSize.
func TestSyncRollupSpecParseDaily(t *testing.T) {
	spec, err := parseSyncRollupSpec("daily")
	if err != nil {
		t.Fatalf("parseSyncRollupSpec daily: %v", err)
	}
	if spec.cursorKind != "daily" {
		t.Errorf("cursorKind = %q, want daily", spec.cursorKind)
	}
	if spec.endpointFamily != endpointFamilyDailyRollUp {
		t.Errorf("endpointFamily = %q, want dailyRollUp", spec.endpointFamily)
	}
	if spec.windowSize != 24*time.Hour {
		t.Errorf("windowSize = %v, want 24h", spec.windowSize)
	}
}

// TestSyncRollupSpecParseHourly verifies that --rollup=hourly maps to
// the windowed rollUp family with 1h windowSize and a distinct cursor
// kind from daily.
func TestSyncRollupSpecParseHourly(t *testing.T) {
	spec, err := parseSyncRollupSpec("hourly")
	if err != nil {
		t.Fatalf("parseSyncRollupSpec hourly: %v", err)
	}
	if spec.cursorKind != "hourly" {
		t.Errorf("cursorKind = %q, want hourly", spec.cursorKind)
	}
	if spec.endpointFamily != endpointFamilyRollUp {
		t.Errorf("endpointFamily = %q, want rollUp", spec.endpointFamily)
	}
	if spec.windowSize != time.Hour {
		t.Errorf("windowSize = %v, want 1h", spec.windowSize)
	}
}

// TestSyncRollupSpecParseWeekly verifies the weekly window math: 7-day
// windowSize, windowed rollUp family.
func TestSyncRollupSpecParseWeekly(t *testing.T) {
	spec, err := parseSyncRollupSpec("weekly")
	if err != nil {
		t.Fatalf("parseSyncRollupSpec weekly: %v", err)
	}
	if spec.cursorKind != "weekly" {
		t.Errorf("cursorKind = %q, want weekly", spec.cursorKind)
	}
	if spec.endpointFamily != endpointFamilyRollUp {
		t.Errorf("endpointFamily = %q, want rollUp", spec.endpointFamily)
	}
	if spec.windowSize != 7*24*time.Hour {
		t.Errorf("windowSize = %v, want 168h", spec.windowSize)
	}
}

// TestSyncRollupSpecParseCustomWindow exercises the AC's window=Nh
// shape — the operator supplies the windowSize directly.
func TestSyncRollupSpecParseCustomWindow(t *testing.T) {
	spec, err := parseSyncRollupSpec("window=6h")
	if err != nil {
		t.Fatalf("parseSyncRollupSpec window=6h: %v", err)
	}
	if spec.cursorKind != "window=6h" {
		t.Errorf("cursorKind = %q, want window=6h", spec.cursorKind)
	}
	if spec.endpointFamily != endpointFamilyRollUp {
		t.Errorf("endpointFamily = %q, want rollUp", spec.endpointFamily)
	}
	if spec.windowSize != 6*time.Hour {
		t.Errorf("windowSize = %v, want 6h", spec.windowSize)
	}
}

// TestSyncRollupSpecParseCustomWindowMinutes covers a sub-hour custom
// window so the duration parser is genuinely time.ParseDuration and
// not just an Nh regex.
func TestSyncRollupSpecParseCustomWindowMinutes(t *testing.T) {
	spec, err := parseSyncRollupSpec("window=30m")
	if err != nil {
		t.Fatalf("parseSyncRollupSpec window=30m: %v", err)
	}
	if spec.windowSize != 30*time.Minute {
		t.Errorf("windowSize = %v, want 30m", spec.windowSize)
	}
}

// TestSyncRollupSpecParseRejectsUnknownKind rejects an unknown literal
// with a message that lists the supported kinds.
func TestSyncRollupSpecParseRejectsUnknownKind(t *testing.T) {
	_, err := parseSyncRollupSpec("monthly")
	if err == nil {
		t.Fatal("parseSyncRollupSpec monthly: want error, got nil")
	}
	if !strings.Contains(err.Error(), "daily") || !strings.Contains(err.Error(), "weekly") {
		t.Errorf("err = %q, want it to list supported kinds", err.Error())
	}
}

// TestSyncRollupSpecParseRejectsBadWindow rejects malformed window=…
// values via time.ParseDuration semantics.
func TestSyncRollupSpecParseRejectsBadWindow(t *testing.T) {
	_, err := parseSyncRollupSpec("window=notADuration")
	if err == nil {
		t.Fatal("parseSyncRollupSpec window=notADuration: want error, got nil")
	}
	if !strings.Contains(err.Error(), "window") {
		t.Errorf("err = %q, want it to mention window", err.Error())
	}
}

// TestSyncRollupSpecValidateAgainstDataTypeQuotesSupportedEndpoints
// pins the AC's "Unsupported combinations error with the Data Type's
// actual SupportedEndpoints quoted in the error message".
func TestSyncRollupSpecValidateAgainstDataTypeQuotesSupportedEndpoints(t *testing.T) {
	spec, err := parseSyncRollupSpec("hourly")
	if err != nil {
		t.Fatalf("parseSyncRollupSpec hourly: %v", err)
	}
	// `sleep` has only `list` in SupportedEndpoints today — no rollup
	// of any kind. The validation must call out hourly is unsupported
	// AND list the actual SupportedEndpoints map keys.
	err = validateSyncRollupAgainstDataType(spec, "sleep")
	if err == nil {
		t.Fatal("validateSyncRollupAgainstDataType sleep+hourly: want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "sleep") {
		t.Errorf("err = %q, want it to mention sleep", msg)
	}
	if !strings.Contains(msg, "hourly") {
		t.Errorf("err = %q, want it to mention hourly", msg)
	}
	if !strings.Contains(msg, "SupportedEndpoints") {
		t.Errorf("err = %q, want it to mention SupportedEndpoints", msg)
	}
	if !strings.Contains(msg, "list") {
		t.Errorf("err = %q, want it to quote sleep's actual SupportedEndpoints (list)", msg)
	}
}

// TestSyncRollupSpecValidateAgainstDataTypeAcceptsHeartRateHourly
// pins the happy path: heart-rate carries rollUp, so hourly is OK.
func TestSyncRollupSpecValidateAgainstDataTypeAcceptsHeartRateHourly(t *testing.T) {
	spec, err := parseSyncRollupSpec("hourly")
	if err != nil {
		t.Fatalf("parseSyncRollupSpec hourly: %v", err)
	}
	if err := validateSyncRollupAgainstDataType(spec, "heart-rate"); err != nil {
		t.Errorf("validateSyncRollupAgainstDataType heart-rate+hourly: %v", err)
	}
}

// TestSyncRollupSpecValidateAgainstDataTypeAcceptsStepsDaily pins the
// regression guard: steps + daily must still validate. The pre-#106
// behaviour and this widened validator agree on the canonical case.
func TestSyncRollupSpecValidateAgainstDataTypeAcceptsStepsDaily(t *testing.T) {
	spec, err := parseSyncRollupSpec("daily")
	if err != nil {
		t.Fatalf("parseSyncRollupSpec daily: %v", err)
	}
	if err := validateSyncRollupAgainstDataType(spec, "steps"); err != nil {
		t.Errorf("validateSyncRollupAgainstDataType steps+daily: %v", err)
	}
}

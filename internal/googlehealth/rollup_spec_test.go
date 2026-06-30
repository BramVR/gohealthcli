package googlehealth

import (
	"strings"
	"testing"
	"time"
)

// TestSyncRollupSpecParseDaily pins the legacy --rollup=daily shape
// (the byte-identical AC). Parsing yields the dailyRollUp endpoint
// family with a 1-day windowSize.
func TestSyncRollupSpecParseDaily(t *testing.T) {
	t.Parallel()
	spec, err := ParseRollupSpec("daily")
	if err != nil {
		t.Fatalf("ParseRollupSpec daily: %v", err)
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
	t.Parallel()
	spec, err := ParseRollupSpec("hourly")
	if err != nil {
		t.Fatalf("ParseRollupSpec hourly: %v", err)
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
	t.Parallel()
	spec, err := ParseRollupSpec("weekly")
	if err != nil {
		t.Fatalf("ParseRollupSpec weekly: %v", err)
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
	t.Parallel()
	spec, err := ParseRollupSpec("window=6h")
	if err != nil {
		t.Fatalf("ParseRollupSpec window=6h: %v", err)
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
	t.Parallel()
	spec, err := ParseRollupSpec("window=30m")
	if err != nil {
		t.Fatalf("ParseRollupSpec window=30m: %v", err)
	}
	if spec.windowSize != 30*time.Minute {
		t.Errorf("windowSize = %v, want 30m", spec.windowSize)
	}
}

// TestSyncRollupSpecParseRejectsUnknownKind rejects an unknown literal
// with a message that lists the supported kinds.
func TestSyncRollupSpecParseRejectsUnknownKind(t *testing.T) {
	t.Parallel()
	_, err := ParseRollupSpec("monthly")
	if err == nil {
		t.Fatal("ParseRollupSpec monthly: want error, got nil")
	}
	if !strings.Contains(err.Error(), "daily") || !strings.Contains(err.Error(), "weekly") {
		t.Errorf("err = %q, want it to list supported kinds", err.Error())
	}
}

// TestSyncRollupSpecParseRejectsBadWindow rejects malformed window=…
// values via time.ParseDuration semantics.
func TestSyncRollupSpecParseRejectsBadWindow(t *testing.T) {
	t.Parallel()
	_, err := ParseRollupSpec("window=notADuration")
	if err == nil {
		t.Fatal("ParseRollupSpec window=notADuration: want error, got nil")
	}
	if !strings.Contains(err.Error(), "window") {
		t.Errorf("err = %q, want it to mention window", err.Error())
	}
}

// TestSyncRollupSpecValidateAgainstDataTypeQuotesSupportedEndpoints
// pins the AC's "Unsupported combinations error with the Data Type's
// actual SupportedEndpoints quoted in the error message".
func TestSyncRollupSpecValidateAgainstDataTypeQuotesSupportedEndpoints(t *testing.T) {
	t.Parallel()
	spec, err := ParseRollupSpec("hourly")
	if err != nil {
		t.Fatalf("ParseRollupSpec hourly: %v", err)
	}
	// `sleep` has only `list` in SupportedEndpoints today — no rollup
	// of any kind. The validation must call out hourly is unsupported
	// AND list the actual SupportedEndpoints map keys.
	err = ValidateRollupAgainstDataType(spec, "sleep")
	if err == nil {
		t.Fatal("ValidateRollupAgainstDataType sleep+hourly: want error, got nil")
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
	t.Parallel()
	spec, err := ParseRollupSpec("hourly")
	if err != nil {
		t.Fatalf("ParseRollupSpec hourly: %v", err)
	}
	if err := ValidateRollupAgainstDataType(spec, "heart-rate"); err != nil {
		t.Errorf("ValidateRollupAgainstDataType heart-rate+hourly: %v", err)
	}
}

// TestSyncRollupSpecValidateAgainstDataTypeAcceptsHeartRateDaily pins
// issue #356: heart-rate supports the dailyRollUp endpoint family as a
// fast daily summary-history path distinct from raw heart-rate samples.
func TestSyncRollupSpecValidateAgainstDataTypeAcceptsHeartRateDaily(t *testing.T) {
	t.Parallel()
	spec, err := ParseRollupSpec("daily")
	if err != nil {
		t.Fatalf("ParseRollupSpec daily: %v", err)
	}
	if err := ValidateRollupAgainstDataType(spec, "heart-rate"); err != nil {
		t.Errorf("ValidateRollupAgainstDataType heart-rate+daily: %v", err)
	}
}

func TestSyncRollupSpecValidateAgainstDataTypeRejectsSleepDailyWithSupportedEndpoints(t *testing.T) {
	t.Parallel()
	spec, err := ParseRollupSpec("daily")
	if err != nil {
		t.Fatalf("ParseRollupSpec daily: %v", err)
	}
	err = ValidateRollupAgainstDataType(spec, "sleep")
	if err == nil {
		t.Fatal("ValidateRollupAgainstDataType sleep+daily: want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "sleep") || !strings.Contains(msg, "daily") {
		t.Errorf("err = %q, want sleep and daily mentions", msg)
	}
	if !strings.Contains(msg, "SupportedEndpoints=[list]") {
		t.Errorf("err = %q, want sleep's supported endpoint families", msg)
	}
}

// TestSyncRollupSpecValidateAgainstDataTypeAcceptsStepsDaily pins the
// regression guard: steps + daily must still validate. The pre-#106
// behaviour and this widened validator agree on the canonical case.
func TestSyncRollupSpecValidateAgainstDataTypeAcceptsStepsDaily(t *testing.T) {
	t.Parallel()
	spec, err := ParseRollupSpec("daily")
	if err != nil {
		t.Fatalf("ParseRollupSpec daily: %v", err)
	}
	if err := ValidateRollupAgainstDataType(spec, "steps"); err != nil {
		t.Errorf("ValidateRollupAgainstDataType steps+daily: %v", err)
	}
}

// TestSyncRollupSpecNormalizeRange pins PRD #141 slice 3: civil-vs-RFC3339
// is owned by RollupSpec. The matrix is (input shape) × (rollup kind)
// → normalized output / error. The planner downstream consumes only the
// normalized values, so this table is the single source of truth for the
// shape contract.
//
// Rules per rollup kind:
//   - daily: accepts civil AND RFC3339 → emits civil (YYYY-MM-DD).
//   - hourly / weekly / window=<dur>: accepts civil (interpreted as
//     start-of-UTC-day) AND RFC3339 → emits RFC3339.
func TestSyncRollupSpecNormalizeRange(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		rollup   string
		from, to string
		wantFrom string
		wantTo   string
		wantErr  string
	}{
		{
			name:     "daily civil-civil normalizes to civil",
			rollup:   "daily",
			from:     "2026-06-07",
			to:       "2026-06-08",
			wantFrom: "2026-06-07",
			wantTo:   "2026-06-08",
		},
		{
			name:     "daily RFC3339-RFC3339 normalizes to civil (UTC day)",
			rollup:   "daily",
			from:     "2026-06-07T00:00:00Z",
			to:       "2026-06-08T00:00:00Z",
			wantFrom: "2026-06-07",
			wantTo:   "2026-06-08",
		},
		{
			name:     "daily mixed civil-RFC3339 normalizes to civil",
			rollup:   "daily",
			from:     "2026-06-07",
			to:       "2026-06-08T00:00:00Z",
			wantFrom: "2026-06-07",
			wantTo:   "2026-06-08",
		},
		{
			name:     "hourly civil-civil normalizes to RFC3339 start-of-UTC-day",
			rollup:   "hourly",
			from:     "2026-06-07",
			to:       "2026-06-08",
			wantFrom: "2026-06-07T00:00:00Z",
			wantTo:   "2026-06-08T00:00:00Z",
		},
		{
			name:     "hourly RFC3339-RFC3339 passes through",
			rollup:   "hourly",
			from:     "2026-06-07T03:30:00Z",
			to:       "2026-06-07T04:30:00Z",
			wantFrom: "2026-06-07T03:30:00Z",
			wantTo:   "2026-06-07T04:30:00Z",
		},
		{
			name:     "hourly mixed civil-RFC3339 normalizes to RFC3339",
			rollup:   "hourly",
			from:     "2026-06-07",
			to:       "2026-06-08T06:00:00Z",
			wantFrom: "2026-06-07T00:00:00Z",
			wantTo:   "2026-06-08T06:00:00Z",
		},
		{
			name:     "weekly civil-civil normalizes to RFC3339",
			rollup:   "weekly",
			from:     "2026-06-01",
			to:       "2026-06-08",
			wantFrom: "2026-06-01T00:00:00Z",
			wantTo:   "2026-06-08T00:00:00Z",
		},
		{
			name:     "weekly RFC3339-RFC3339 passes through",
			rollup:   "weekly",
			from:     "2026-06-01T00:00:00Z",
			to:       "2026-06-08T00:00:00Z",
			wantFrom: "2026-06-01T00:00:00Z",
			wantTo:   "2026-06-08T00:00:00Z",
		},
		{
			name:     "window=6h civil-civil normalizes to RFC3339",
			rollup:   "window=6h",
			from:     "2026-06-07",
			to:       "2026-06-08",
			wantFrom: "2026-06-07T00:00:00Z",
			wantTo:   "2026-06-08T00:00:00Z",
		},
		{
			name:     "window=6h RFC3339-RFC3339 passes through",
			rollup:   "window=6h",
			from:     "2026-06-07T00:00:00Z",
			to:       "2026-06-07T12:00:00Z",
			wantFrom: "2026-06-07T00:00:00Z",
			wantTo:   "2026-06-07T12:00:00Z",
		},
		{
			name:    "hourly rejects unparseable from with supported-shapes message",
			rollup:  "hourly",
			from:    "not-a-date",
			to:      "2026-06-08T00:00:00Z",
			wantErr: "hourly",
		},
		{
			name:    "daily rejects unparseable to with supported-shapes message",
			rollup:  "daily",
			from:    "2026-06-07",
			to:      "definitely-bad",
			wantErr: "daily",
		},
		{
			name:    "window rejects unparseable from",
			rollup:  "window=6h",
			from:    "garbage",
			to:      "2026-06-08T00:00:00Z",
			wantErr: "window=6h",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseRollupSpec(tc.rollup)
			if err != nil {
				t.Fatalf("ParseRollupSpec %q: %v", tc.rollup, err)
			}
			gotFrom, gotTo, err := spec.NormalizeRange(tc.from, tc.to, now)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("NormalizeRange(%q, %q) error = nil, want substring %q", tc.from, tc.to, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
				}
				// The supported-shapes message must name BOTH supported shapes
				// so the operator sees what's acceptable for this rollup kind.
				if !strings.Contains(err.Error(), "YYYY-MM-DD") || !strings.Contains(err.Error(), "RFC3339") {
					t.Errorf("error = %q, want supported shapes (YYYY-MM-DD, RFC3339) named", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeRange(%q, %q): %v", tc.from, tc.to, err)
			}
			if gotFrom != tc.wantFrom {
				t.Errorf("from = %q, want %q", gotFrom, tc.wantFrom)
			}
			if gotTo != tc.wantTo {
				t.Errorf("to = %q, want %q", gotTo, tc.wantTo)
			}
		})
	}
}

// TestSyncRollupSpecNormalizeRangePassesThroughEmpty pins the cursor-resume
// case: --from "" is the lifecycle's resume signal, the gate must not
// reject on an empty input and the normalizer must pass it through so the
// downstream cursor lookup still fires. Empty --to is similarly a default
// signal — the gate fills it before calling NormalizeRange in production,
// but the normalizer treats an empty string as a pass-through too so the
// helper is safe to call before defaulting (defensive: callers that
// forget to default get an empty-out, not a parse error).
func TestSyncRollupSpecNormalizeRangePassesThroughEmpty(t *testing.T) {
	t.Parallel()
	spec, err := ParseRollupSpec("hourly")
	if err != nil {
		t.Fatalf("ParseRollupSpec hourly: %v", err)
	}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	gotFrom, gotTo, err := spec.NormalizeRange("", "2026-06-08T00:00:00Z", now)
	if err != nil {
		t.Fatalf("NormalizeRange empty-from: %v", err)
	}
	if gotFrom != "" {
		t.Errorf("from = %q, want empty pass-through", gotFrom)
	}
	if gotTo != "2026-06-08T00:00:00Z" {
		t.Errorf("to = %q, want passthrough RFC3339", gotTo)
	}
}

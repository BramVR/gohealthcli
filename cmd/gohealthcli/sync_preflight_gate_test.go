package main

import (
	"errors"
	"github.com/BramVR/gohealthcli/internal/archived"
	"strings"
	"testing"
	"time"
)

// fakeSyncPreflightContext lets the gate's unit table-test run without
// touching the real archive: an in-memory catalog adapter, an in-memory
// source-family rule, a fixed clock, and a stub current-connection
// lookup. Re-uses the production rules via thin closures so the gate's
// behaviour stays in lockstep with the catalog as it grows.
func fakeSyncPreflightContext(now time.Time, connection archived.Connection) syncPreflightContext {
	return syncPreflightContext{
		now: func() time.Time { return now },
		dataTypeSupported: func(dataType string) bool {
			switch dataType {
			case "steps", "heart-rate", "weight", "sleep", "active-energy-burned":
				return true
			}
			return false
		},
		dataTypeUsesDateRange: func(dataType string) bool {
			return dataType == "weight"
		},
		sourceFamilyFilter: func(dataType, sourceFamily string) (string, error) {
			if dataType != "steps" && dataType != "heart-rate" {
				return "", errors.New("sync --source-family is not supported for Data Type " + dataType)
			}
			if sourceFamily != "wearable" {
				return "", errors.New("sync --source-family currently supports only wearable")
			}
			return "users/me/dataSourceFamilies/google-wearables", nil
		},
		defaultAllDataTypes: func() []string {
			return []string{"steps", "heart-rate", "sleep"}
		},
		currentConnection: func() (archived.Connection, error) {
			return connection, nil
		},
		rollupCatalogValidator: func(spec syncRollupSpec, dataType string) error {
			// Only `steps` carries `daily` in the fake catalog; everything
			// else returns the same shape the production validator emits.
			if spec.cursorKind == "daily" && dataType != "steps" {
				return errors.New("sync --rollup daily: Data Type " + dataType + " does not support daily Rollups")
			}
			return nil
		},
	}
}

func TestSyncPreflightGateRulesTable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	conn := archived.Connection{ID: "googlehealth:111", ProviderName: "Google Health"}
	ctx := fakeSyncPreflightContext(now, conn)
	gate := syncPreflightGate{ctx: ctx}

	cases := []struct {
		name       string
		options    syncCommandOptions
		wantRule   string
		wantErrSub string
	}{
		{
			name:       "no types and no all flag is missing-types",
			options:    syncCommandOptions{},
			wantRule:   preflightRuleMissingDataTypes,
			wantErrSub: "sync requires --types or --all",
		},
		{
			name:       "all combined with types is mutually exclusive",
			options:    syncCommandOptions{allTypes: true, dataTypes: []string{"steps"}},
			wantRule:   preflightRuleAllVsTypesConflict,
			wantErrSub: "--all cannot be combined with --types",
		},
		{
			name:       "duplicate --types entries rejected",
			options:    syncCommandOptions{dataTypes: []string{"steps", "steps"}},
			wantRule:   preflightRuleDuplicateDataType,
			wantErrSub: `"steps" more than once`,
		},
		{
			name:       "unsupported Data Type rejected",
			options:    syncCommandOptions{dataTypes: []string{"unsupported-type"}},
			wantRule:   preflightRuleUnsupportedDataType,
			wantErrSub: `"unsupported-type" is not supported yet`,
		},
		{
			name:       "rollup parse failure",
			options:    syncCommandOptions{dataTypes: []string{"steps"}, rollup: "weird"},
			wantRule:   preflightRuleRollupParse,
			wantErrSub: `"weird" is not supported`,
		},
		{
			name:       "rollup catalog mismatch",
			options:    syncCommandOptions{dataTypes: []string{"heart-rate"}, rollup: "daily"},
			wantRule:   preflightRuleRollupCatalog,
			wantErrSub: "does not support daily Rollups",
		},
		{
			name:       "source-family rejected for unsupported Data Type",
			options:    syncCommandOptions{dataTypes: []string{"sleep"}, sourceFamily: "wearable"},
			wantRule:   preflightRuleSourceFamily,
			wantErrSub: "is not supported for Data Type sleep",
		},
		{
			name:       "source-family + rollup mutually exclusive",
			options:    syncCommandOptions{dataTypes: []string{"steps"}, rollup: "daily", sourceFamily: "wearable"},
			wantRule:   preflightRuleRollupSourceFamilyConflict,
			wantErrSub: "--source-family cannot be combined with --rollup",
		},
		{
			// Mutual-exclusion must fire before per-type source-family /
			// rollup-catalog checks so an operator who sets both flags
			// hears about the conflict itself, not a downstream rejection
			// from one of the flags they aren't actually allowed to use
			// in combination.
			name:       "rollup + source-family conflict reported even when source-family unsupported for the type",
			options:    syncCommandOptions{dataTypes: []string{"sleep"}, rollup: "daily", sourceFamily: "wearable"},
			wantRule:   preflightRuleRollupSourceFamilyConflict,
			wantErrSub: "--source-family cannot be combined with --rollup",
		},
		{
			name:       "from later than to rejected (civil dates)",
			options:    syncCommandOptions{dataTypes: []string{"steps"}, from: "2026-06-08", to: "2026-06-01"},
			wantRule:   preflightRuleRangeOrderInverted,
			wantErrSub: "from must be earlier than to",
		},
		{
			name:       "from later than to rejected (RFC3339 vs civil-date mix)",
			options:    syncCommandOptions{dataTypes: []string{"steps"}, from: "2026-06-08T00:00:00Z", to: "2026-06-01"},
			wantRule:   preflightRuleRangeOrderInverted,
			wantErrSub: "from must be earlier than to",
		},
		{
			name:       "from equal to to rejected as zero-width window (civil dates)",
			options:    syncCommandOptions{dataTypes: []string{"steps"}, from: "2026-06-01", to: "2026-06-01"},
			wantRule:   preflightRuleRangeZeroWidth,
			wantErrSub: "zero-width sync window",
		},
		{
			name:       "from equal to to rejected as zero-width window (RFC3339 vs civil-date mix)",
			options:    syncCommandOptions{dataTypes: []string{"steps"}, from: "2026-06-01T00:00:00Z", to: "2026-06-01"},
			wantRule:   preflightRuleRangeZeroWidth,
			wantErrSub: "zero-width sync window",
		},
		{
			// Cross-shape collision: civil from + start-of-UTC-day RFC3339 to
			// normalize to the same instant. Earlier drafts of slice 2 missed
			// this because the table only covered same-shape and one
			// asymmetric mix. The error message names both inputs verbatim
			// so the user sees why two visually-different strings collided.
			name:       "civil from collides with start-of-UTC-day RFC3339 to as zero-width",
			options:    syncCommandOptions{dataTypes: []string{"steps"}, from: "2026-06-01", to: "2026-06-01T00:00:00Z"},
			wantRule:   preflightRuleRangeZeroWidth,
			wantErrSub: "zero-width sync window",
		},
		{
			// Regression for the empty-to bypass: when --to is omitted, the
			// gate defaults it to now() and validates the resolved value
			// against --from. A future --from with no --to therefore still
			// trips inverted-range instead of silently producing a
			// from>to plan that downstream then fails opaquely.
			// Uses the fake clock's "now" (2026-01-05) — anything later than
			// that as --from must be rejected here.
			name:       "future from with empty to rejected against defaulted now",
			options:    syncCommandOptions{dataTypes: []string{"steps"}, from: "2099-01-01"},
			wantRule:   preflightRuleRangeOrderInverted,
			wantErrSub: "from must be earlier than to",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gate.Validate(tc.options)
			if err == nil {
				t.Fatalf("Validate(%+v) error = nil, want rule=%s", tc.options, tc.wantRule)
			}
			var failure *preflightFailure
			if !errors.As(err, &failure) {
				t.Fatalf("error = %v (%T), want *preflightFailure", err, err)
			}
			if failure.Rule() != tc.wantRule {
				t.Errorf("rule = %q, want %q", failure.Rule(), tc.wantRule)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}

func TestSyncPreflightGateAllExpandsToCatalogList(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	conn := archived.Connection{ID: "googlehealth:111", ProviderName: "Google Health"}
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(now, conn)}

	plan, err := gate.Validate(syncCommandOptions{allTypes: true, from: "2026-01-01", to: "2026-01-02T00:00:00Z"})
	if err != nil {
		t.Fatalf("Validate(--all): %v", err)
	}
	want := []string{"steps", "heart-rate", "sleep"}
	if len(plan.dataTypes) != len(want) {
		t.Fatalf("plan.dataTypes = %v, want %v", plan.dataTypes, want)
	}
	for i, dt := range want {
		if plan.dataTypes[i] != dt {
			t.Errorf("plan.dataTypes[%d] = %q, want %q", i, plan.dataTypes[i], dt)
		}
	}
	if plan.connection.ID != conn.ID {
		t.Errorf("plan.connection.id = %q, want %q", plan.connection.ID, conn.ID)
	}
}

func TestSyncPreflightGateDefaultsToWhenEmpty(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	conn := archived.Connection{ID: "googlehealth:111"}
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(now, conn)}

	plan, err := gate.Validate(syncCommandOptions{dataTypes: []string{"steps"}, from: "2026-01-01"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// steps + non-rollup defaults to RFC3339 (date-range only fires for
	// catalog entries with UsesDateRangeDefault=true, or for --rollup daily).
	if want := now.UTC().Format(time.RFC3339); plan.to != want {
		t.Errorf("plan.to = %q, want %q", plan.to, want)
	}
}

func TestSyncPreflightGateDefaultsToWhenDailyRollup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	conn := archived.Connection{ID: "googlehealth:111"}
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(now, conn)}

	plan, err := gate.Validate(syncCommandOptions{dataTypes: []string{"steps"}, rollup: "daily", from: "2026-01-01"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if want := now.UTC().Format("2006-01-02"); plan.to != want {
		t.Errorf("plan.to = %q, want %q (daily rollup defaults to civil date)", plan.to, want)
	}
}

// TestSyncPreflightGateNormalizesRangePerRollupKind pins PRD #141 slice 3:
// the gate owns the civil-vs-RFC3339 contract by routing both --from and
// --to through syncRollupSpec.NormalizeRange. The planner downstream
// consumes the normalized plan.from / plan.to without re-parsing.
func TestSyncPreflightGateNormalizesRangePerRollupKind(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	conn := archived.Connection{ID: "googlehealth:111"}
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(now, conn)}

	cases := []struct {
		name     string
		rollup   string
		dataType string
		from, to string
		wantFrom string
		wantTo   string
	}{
		{
			name:     "daily civil pass-through",
			rollup:   "daily",
			dataType: "steps",
			from:     "2026-06-07",
			to:       "2026-06-08",
			wantFrom: "2026-06-07",
			wantTo:   "2026-06-08",
		},
		{
			name:     "daily RFC3339 normalized to civil",
			rollup:   "daily",
			dataType: "steps",
			from:     "2026-06-07T03:00:00Z",
			to:       "2026-06-08T00:00:00Z",
			wantFrom: "2026-06-07",
			wantTo:   "2026-06-08",
		},
		{
			name:     "hourly civil normalized to RFC3339 start-of-UTC-day",
			rollup:   "hourly",
			dataType: "heart-rate",
			from:     "2026-06-07",
			to:       "2026-06-08",
			wantFrom: "2026-06-07T00:00:00Z",
			wantTo:   "2026-06-08T00:00:00Z",
		},
		{
			name:     "weekly civil normalized to RFC3339",
			rollup:   "weekly",
			dataType: "steps",
			from:     "2026-06-01",
			to:       "2026-06-08",
			wantFrom: "2026-06-01T00:00:00Z",
			wantTo:   "2026-06-08T00:00:00Z",
		},
		{
			name:     "window=6h civil normalized to RFC3339",
			rollup:   "window=6h",
			dataType: "steps",
			from:     "2026-06-07",
			to:       "2026-06-08",
			wantFrom: "2026-06-07T00:00:00Z",
			wantTo:   "2026-06-08T00:00:00Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := gate.Validate(syncCommandOptions{
				dataTypes: []string{tc.dataType},
				rollup:    tc.rollup,
				from:      tc.from,
				to:        tc.to,
			})
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if plan.from != tc.wantFrom {
				t.Errorf("plan.from = %q, want %q", plan.from, tc.wantFrom)
			}
			if plan.to != tc.wantTo {
				t.Errorf("plan.to = %q, want %q", plan.to, tc.wantTo)
			}
		})
	}
}

// TestSyncPreflightGateRangeParseDistinctFromRollupParse pins the
// rule-discriminator contract: a malformed --from is a range-shape
// failure, NOT a rollup-literal failure. Consumers route on
// preflightFailure.Rule(); collapsing both into preflightRuleRollupParse
// makes downstream JSON envelopes, logging, and exit-code routing unable
// to tell "bad --rollup value" from "bad --from boundary" apart.
func TestSyncPreflightGateRangeParseDistinctFromRollupParse(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	conn := archived.Connection{ID: "googlehealth:111"}
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(now, conn)}

	_, err := gate.Validate(syncCommandOptions{
		dataTypes: []string{"steps"},
		rollup:    "hourly",
		from:      "garbage",
		to:        "2026-06-08T00:00:00Z",
	})
	if err == nil {
		t.Fatalf("Validate: want error for garbage --from, got nil")
	}
	var failure *preflightFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v (%T), want *preflightFailure", err, err)
	}
	if failure.Rule() != preflightRuleRangeParse {
		t.Errorf("rule = %q, want %q", failure.Rule(), preflightRuleRangeParse)
	}
	if failure.Rule() == preflightRuleRollupParse {
		t.Errorf("rule = %q must NOT collapse range-parse into rollup-parse", failure.Rule())
	}
}

// TestSyncPreflightGateRejectsBadShapeWithLocalMessage pins AC 4: civil
// date on --rollup hourly|weekly|window=<dur> no longer surfaces as an
// opaque upstream HTTP 400. The gate names the supported shapes for
// the rollup kind in its rejection. Parse failures are now gate failures,
// not downstream HTTP failures.
func TestSyncPreflightGateRejectsBadShapeWithLocalMessage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	conn := archived.Connection{ID: "googlehealth:111"}
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(now, conn)}

	cases := []struct {
		name   string
		rollup string
	}{
		{"hourly", "hourly"},
		{"weekly", "weekly"},
		{"window=6h", "window=6h"},
		{"daily", "daily"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gate.Validate(syncCommandOptions{
				dataTypes: []string{"steps"},
				rollup:    tc.rollup,
				from:      "not-a-date",
				to:        "2026-06-08T00:00:00Z",
			})
			if err == nil {
				t.Fatalf("Validate: want error for unparseable --from, got nil")
			}
			msg := err.Error()
			if !strings.Contains(msg, "YYYY-MM-DD") || !strings.Contains(msg, "RFC3339") {
				t.Errorf("error = %q, want supported shapes (YYYY-MM-DD, RFC3339) named", msg)
			}
			if !strings.Contains(msg, tc.rollup) {
				t.Errorf("error = %q, want rollup kind %q named", msg, tc.rollup)
			}
		})
	}
}

func TestSyncPreflightGateSkipsRangeOrderCheckOnCursorResume(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	conn := archived.Connection{ID: "googlehealth:111"}
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(now, conn)}

	// --from empty means "resume from Sync Cursor"; the lifecycle resolves
	// --from later, so the gate must NOT reject on range-ordering here.
	// --to alone is fine.
	if _, err := gate.Validate(syncCommandOptions{
		dataTypes: []string{"steps"},
		to:        "2026-01-02T00:00:00Z",
	}); err != nil {
		t.Fatalf("Validate(cursor-resume): %v", err)
	}
}

func TestSyncPreflightGateProducesCursorKeyPerDataType(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	conn := archived.Connection{ID: "googlehealth:abc"}
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(now, conn)}

	plan, err := gate.Validate(syncCommandOptions{
		dataTypes: []string{"steps", "heart-rate"},
		from:      "2026-01-01",
		to:        "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(plan.cursorKeys) != 2 {
		t.Fatalf("plan.cursorKeys len = %d, want 2", len(plan.cursorKeys))
	}
	if plan.cursorKeys[0].dataType != "steps" || plan.cursorKeys[1].dataType != "heart-rate" {
		t.Errorf("cursor keys = %+v, want one per Data Type", plan.cursorKeys)
	}
	for i, key := range plan.cursorKeys {
		if key.connectionID != conn.ID {
			t.Errorf("cursorKeys[%d].connectionID = %q, want %q", i, key.connectionID, conn.ID)
		}
		if key.rollupKind != syncCursorRollupKindNone {
			t.Errorf("cursorKeys[%d].rollupKind = %q, want none", i, key.rollupKind)
		}
	}
}

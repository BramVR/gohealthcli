package main

import (
	"fmt"
	"strings"
	"time"
)

// syncRollupSpec is the parsed form of `--rollup` (`daily | weekly |
// hourly | window=<duration>`). It carries the windowSize used by the
// upstream Google Health rollup endpoint, the endpoint family the
// planner will dispatch to, and the cursor-kind discriminator used by
// the Sync Cursor (so each (Data Type, source-family, rollup-kind)
// triple has its own durable highwater).
type syncRollupSpec struct {
	cursorKind     string         // "daily" | "hourly" | "weekly" | "window=<duration>"
	endpointFamily endpointFamily // dailyRollUp for daily; rollUp for hourly/weekly/window
	windowSize     time.Duration  // 1h / 24h / 168h / parsed-duration
}

const syncRollupWindowPrefix = "window="

// supportedSyncRollupKinds lists the literal --rollup values users
// can pass (the AC names "daily | weekly | hourly | window=<dur>").
// The order here is the order the error message prints.
var supportedSyncRollupKinds = []string{"daily", "hourly", "weekly", "window=<duration>"}

// parseSyncRollupSpec parses the operator-facing `--rollup` value
// into a syncRollupSpec. Returns a typed error for unknown literals
// and for malformed window=… durations.
func parseSyncRollupSpec(value string) (syncRollupSpec, error) {
	switch value {
	case "daily":
		return syncRollupSpec{
			cursorKind:     "daily",
			endpointFamily: endpointFamilyDailyRollUp,
			windowSize:     24 * time.Hour,
		}, nil
	case "hourly":
		return syncRollupSpec{
			cursorKind:     "hourly",
			endpointFamily: endpointFamilyRollUp,
			windowSize:     time.Hour,
		}, nil
	case "weekly":
		return syncRollupSpec{
			cursorKind:     "weekly",
			endpointFamily: endpointFamilyRollUp,
			windowSize:     7 * 24 * time.Hour,
		}, nil
	}
	if strings.HasPrefix(value, syncRollupWindowPrefix) {
		raw := strings.TrimPrefix(value, syncRollupWindowPrefix)
		dur, err := time.ParseDuration(raw)
		if err != nil {
			return syncRollupSpec{}, fmt.Errorf("sync --rollup window=%s: %w", raw, err)
		}
		if dur <= 0 {
			return syncRollupSpec{}, fmt.Errorf("sync --rollup window=%s: duration must be positive", raw)
		}
		return syncRollupSpec{
			cursorKind:     value,
			endpointFamily: endpointFamilyRollUp,
			windowSize:     dur,
		}, nil
	}
	return syncRollupSpec{}, fmt.Errorf("sync --rollup %q is not supported; expected one of %s",
		value, strings.Join(supportedSyncRollupKinds, " | "))
}

// NormalizeRange owns the civil-vs-RFC3339 input-shape rule per
// rollup kind (PRD #141 slice 3). The planner downstream consumes
// only the normalized values, so the catalog's SupportedEndpoints
// data stays authoritative — civil-vs-RFC3339 is purely an input
// ergonomic decision concentrated here.
//
// Acceptance per rollup kind:
//   - daily: civil dates AND RFC3339; emits civil (YYYY-MM-DD).
//     RFC3339 inputs are projected to their UTC calendar day so the
//     downstream dailyRollUp call body receives the catalog-required
//     civil-time interval.
//   - hourly / weekly / window=<dur>: civil dates (interpreted as
//     start-of-UTC-day) AND RFC3339; emits RFC3339 so the windowed
//     rollUp call body carries the upstream-required RFC3339 range.
//
// Empty inputs pass through: --from "" is the cursor-resume signal
// the lifecycle resolves later, and --to "" is the gate-defaulting
// signal; the gate normalises a resolved --to before calling this
// helper, but treating empty as pass-through keeps the contract
// composable for callers that have not yet defaulted.
//
// Parse failures surface a local message naming both supported
// shapes for this rollup kind so the operator no longer sees an
// opaque upstream HTTP 400 for civil-on-hourly etc.
func (spec syncRollupSpec) NormalizeRange(from, to string, now time.Time) (normFrom string, normTo string, err error) {
	_ = now // now is reserved for future relative-input ergonomics (e.g. "yesterday").
	// Local names use the generic "norm" prefix because the emitted shape
	// is per-rollup-kind: daily emits civil dates (YYYY-MM-DD), the
	// windowed family emits RFC3339. Naming the locals rfcFrom/rfcTo
	// (an earlier draft) misleadingly implied RFC3339 was always the
	// output, hiding the daily-civil branch from readers.
	normFrom, err = spec.normalizeBoundary(from, "--from")
	if err != nil {
		return "", "", err
	}
	normTo, err = spec.normalizeBoundary(to, "--to")
	if err != nil {
		return "", "", err
	}
	return normFrom, normTo, nil
}

// normalizeBoundary handles one end of the range. The shape it accepts
// is the same for both ends; the per-rollup choice is what it EMITS
// (civil for daily, RFC3339 for the windowed family).
func (spec syncRollupSpec) normalizeBoundary(value, flag string) (string, error) {
	if value == "" {
		return "", nil
	}
	parsed, ok := parseSyncRangeBoundary(value)
	if !ok {
		return "", fmt.Errorf(
			"sync %s %q for --rollup %s: expected YYYY-MM-DD or RFC3339 (e.g. 2026-01-02T00:00:00Z)",
			flag, value, spec.cursorKind,
		)
	}
	if spec.endpointFamily == endpointFamilyDailyRollUp {
		return parsed.UTC().Format("2006-01-02"), nil
	}
	return parsed.UTC().Format(time.RFC3339), nil
}

// parseSyncRangeBoundary accepts either civil-date (YYYY-MM-DD,
// interpreted as start-of-UTC-day) or RFC3339. Both shapes are
// supported by every rollup kind as an input ergonomic, even when
// the emitted shape is restricted by the upstream endpoint.
func parseSyncRangeBoundary(value string) (time.Time, bool) {
	if parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

// validateSyncRollupAgainstDataType checks whether the rollup kind
// the operator asked for is wired into the Data Type's catalog row.
// Failure quotes the actual SupportedEndpoints map keys — the #106
// AC requires this verbatim so operators can see what alternatives
// the Data Type does support.
func validateSyncRollupAgainstDataType(spec syncRollupSpec, dataType string) error {
	entry, ok := googleHealthDataTypes.Lookup(dataType)
	if !ok {
		return fmt.Errorf("sync --rollup %s: Data Type %q is not in the catalog", spec.cursorKind, dataType)
	}
	support, ok := entry.SupportedEndpoints[spec.endpointFamily]
	if !ok || support.RollupValueType == "" {
		return fmt.Errorf("sync --rollup %s: Data Type %q does not support %s Rollups; SupportedEndpoints=%s",
			spec.cursorKind, dataType, spec.cursorKind, formatSupportedEndpoints(entry.SupportedEndpoints))
	}
	return nil
}

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

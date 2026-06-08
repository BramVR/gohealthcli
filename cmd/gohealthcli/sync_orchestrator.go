package main

import (
	"errors"
	"fmt"
)

// syncOrchestrator multiplexes one user-facing `sync` invocation into one
// Sync Run per Data Type. Per-type failures stay isolated (a failed steps
// run does not poison heart-rate), and a single SIGINT closes cancelCh,
// which the in-flight executor catches between pagination pages and
// finalises as sync_canceled. Subsequent Data Types are skipped on
// cancellation so the user sees a clean stop rather than a poisoned chain.
type syncOrchestrator struct {
	executor syncRunExecutor
	cancelCh <-chan struct{}
}

func newSyncOrchestrator(runtime runtimeAdapters, cancelCh <-chan struct{}) syncOrchestrator {
	return syncOrchestrator{
		executor: syncRunExecutor{runtime: runtime},
		cancelCh: cancelCh,
	}
}

// Sync runs every requested Data Type in order, returning one syncResult
// per Data Type plus an aggregate. The error return is non-nil only when
// the orchestration itself could not run any Data Type (e.g. expansion
// failed); per-type failures are reported inside the result slice and the
// returned error is nil so the caller can render every outcome.
func (orchestrator syncOrchestrator) Sync(options syncCommandOptions) ([]syncResult, error) {
	dataTypes, err := orchestrator.expandDataTypes(options)
	if err != nil {
		return nil, err
	}
	if len(dataTypes) == 0 {
		return nil, errors.New("sync requires at least one Data Type")
	}
	results := make([]syncResult, 0, len(dataTypes))
	for _, dataType := range dataTypes {
		if ingestionCanceled(orchestrator.cancelCh) {
			// Orchestrator received SIGINT after a prior Data Type finished
			// and before this one started. Stop the loop cleanly rather
			// than emitting skipped-result rows that would inflate the
			// summary's "N attempted" count; the in-flight type (if any)
			// already wrote its own sync_canceled result.
			break
		}
		perType := options
		perType.dataTypes = []string{dataType}
		perType.cancelCh = orchestrator.cancelCh
		result, execErr := orchestrator.executor.Execute(perType)
		if execErr != nil && result.Message == "" {
			result.Message = execErr.Error()
		}
		results = append(results, result)
	}
	return results, nil
}

// expandDataTypes resolves --all / --types into the concrete ordered list
// the orchestrator iterates. --all expands to the catalog's
// DefaultConfigType=true Data Types that actually support a sync endpoint
// (SupportsSyncDataPoint=true); Tier-1 entries reserved in the catalog
// without a parser shape are skipped so `sync --all` never produces
// guaranteed-failing rows. --types is taken as-given so an explicit
// `--types unsupported` still surfaces the per-type error.
func (orchestrator syncOrchestrator) expandDataTypes(options syncCommandOptions) ([]string, error) {
	if options.allTypes {
		if len(options.dataTypes) != 0 {
			return nil, errors.New("sync --all cannot be combined with --types")
		}
		var syncable []string
		for _, dataType := range defaultDataTypes {
			if syncDataPointDataTypeSupported(dataType) {
				syncable = append(syncable, dataType)
			}
		}
		return syncable, nil
	}
	if len(options.dataTypes) == 0 {
		return nil, errors.New("sync requires --types or --all")
	}
	seen := make(map[string]struct{}, len(options.dataTypes))
	resolved := make([]string, 0, len(options.dataTypes))
	for _, dataType := range options.dataTypes {
		if _, ok := seen[dataType]; ok {
			return nil, fmt.Errorf("sync --types lists %q more than once", dataType)
		}
		seen[dataType] = struct{}{}
		resolved = append(resolved, dataType)
	}
	return resolved, nil
}

// syncFanOutSummary is the aggregate envelope rendered for multi-Data-Type
// invocations. Status lives on the wrapping syncFanOutResult, not here,
// so downstream tooling reads one canonical status field. Counts sum
// across the whole fan-out; DataTypes lists only the runs the executor
// actually attempted (cancellation between Data Types omits skipped
// types from the result slice, so they do not appear here either).
type syncFanOutSummary struct {
	DataTypes         []string `json:"data_types,omitempty"`
	From              string   `json:"from,omitempty"`
	To                string   `json:"to,omitempty"`
	DataPointsSeen    int      `json:"data_points_seen"`
	DataPointsNew     int      `json:"data_points_new"`
	DataPointsUpdated int      `json:"data_points_updated"`
	RollupsSeen       int      `json:"rollups_seen"`
	RollupsNew        int      `json:"rollups_new"`
	RollupsUpdated    int      `json:"rollups_updated"`
}

// fanOutStatus returns the worst outcome present across results
// (failed > canceled > completed). An empty result set is reported as
// canceled — the orchestrator only produces zero results when SIGINT
// arrived before the first Data Type started.
func fanOutStatus(results []syncResult) string {
	if len(results) == 0 {
		return "sync_canceled"
	}
	status := "sync_completed"
	for _, result := range results {
		switch result.Status {
		case "sync_failed":
			return "sync_failed"
		case "sync_canceled":
			status = "sync_canceled"
		}
	}
	return status
}

func summarizeSyncFanOut(results []syncResult, requestedFrom, requestedTo string) syncFanOutSummary {
	summary := syncFanOutSummary{
		DataTypes: make([]string, 0, len(results)),
		From:      requestedFrom,
		To:        requestedTo,
	}
	for _, result := range results {
		summary.DataPointsSeen += result.DataPointsSeen
		summary.DataPointsNew += result.DataPointsNew
		summary.DataPointsUpdated += result.DataPointsUpdated
		summary.RollupsSeen += result.RollupsSeen
		summary.RollupsNew += result.RollupsNew
		summary.RollupsUpdated += result.RollupsUpdated
		if len(result.DataTypes) > 0 {
			summary.DataTypes = append(summary.DataTypes, result.DataTypes[0])
		}
	}
	return summary
}

func fanOutMessage(status string, attempted int) string {
	switch status {
	case "sync_failed":
		return fmt.Sprintf("Sync Run summary: %d Data Types attempted, at least one failed", attempted)
	case "sync_canceled":
		return fmt.Sprintf("Sync Run summary: %d Data Types completed before cancellation", attempted)
	default:
		return fmt.Sprintf("Sync Run summary: %d Data Types archived", attempted)
	}
}

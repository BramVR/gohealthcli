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
			// and before this one started. Record a skipped-via-cancel
			// result so the user can see which types didn't run; no
			// sync_runs row is created because the executor never ran.
			results = append(results, syncResult{
				Status:    "sync_canceled",
				DataTypes: []string{dataType},
				Message:   "Sync Run skipped: cancellation received before this Data Type started",
			})
			continue
		}
		perType := options
		perType.dataTypes = []string{dataType}
		perType.cancelCh = orchestrator.cancelCh
		result, execErr := orchestrator.executor.Execute(perType)
		if execErr != nil && result.Message == "" {
			result.Message = execErr.Error()
		}
		if execErr != nil && result.Status == "" {
			result.Status = "sync_failed"
		}
		results = append(results, result)
	}
	return results, nil
}

// expandDataTypes resolves --all / --types into the concrete ordered list
// the orchestrator iterates. --all wins over --types (the CLI rejects
// passing both before this point); when --all is set the catalog's
// DefaultConfigType=true Data Types are used.
func (orchestrator syncOrchestrator) expandDataTypes(options syncCommandOptions) ([]string, error) {
	if options.allTypes {
		if len(options.dataTypes) != 0 {
			return nil, errors.New("sync --all cannot be combined with --types")
		}
		return append([]string(nil), defaultDataTypes...), nil
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

// aggregateSyncResults reduces a fan-out's per-type results to one
// summary row. Status is the worst outcome present (failed > canceled >
// completed); counts sum across the whole fan-out.
type syncFanOutSummary struct {
	Status            string `json:"status"`
	DataTypes         []string `json:"data_types,omitempty"`
	From              string `json:"from,omitempty"`
	To                string `json:"to,omitempty"`
	DataPointsSeen    int    `json:"data_points_seen"`
	DataPointsNew     int    `json:"data_points_new"`
	DataPointsUpdated int    `json:"data_points_updated"`
	RollupsSeen       int    `json:"rollups_seen"`
	RollupsNew        int    `json:"rollups_new"`
	RollupsUpdated    int    `json:"rollups_updated"`
	Message           string `json:"message"`
}

func summarizeSyncFanOut(results []syncResult, requestedFrom, requestedTo string) syncFanOutSummary {
	summary := syncFanOutSummary{
		Status:    "sync_completed",
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
		switch result.Status {
		case "sync_failed":
			summary.Status = "sync_failed"
		case "sync_canceled":
			if summary.Status != "sync_failed" {
				summary.Status = "sync_canceled"
			}
		}
	}
	switch summary.Status {
	case "sync_failed":
		summary.Message = fmt.Sprintf("Sync Run summary: %d Data Types attempted, at least one failed", len(results))
	case "sync_canceled":
		summary.Message = fmt.Sprintf("Sync Run summary: %d Data Types attempted, canceled mid-run", len(results))
	default:
		summary.Message = fmt.Sprintf("Sync Run summary: %d Data Types archived", len(results))
	}
	return summary
}

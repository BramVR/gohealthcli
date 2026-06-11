package main

import (
	"context"
	"fmt"
)

// syncOrchestrator multiplexes one user-facing `sync` invocation into one
// Sync Run per Data Type. Per-type failures stay isolated (a failed steps
// run does not poison heart-rate), and a single SIGINT cancels the run's
// context, which aborts the executor's in-flight Provider request (#284)
// and finalises the run as sync_canceled. Subsequent Data Types are
// skipped on cancellation so the user sees a clean stop rather than a
// poisoned chain.
type syncOrchestrator struct {
	executor syncRunExecutor
}

func newSyncOrchestrator(runtime runtimeAdapters) syncOrchestrator {
	return syncOrchestrator{
		executor: syncRunExecutor{runtime: runtime},
	}
}

// Sync runs every requested Data Type in order and returns one
// syncResult per Data Type. Aggregation across the fan-out (totals,
// overall status, summary line) is the caller's job — summarizeSyncFanOut,
// fanOutStatus, and fanOutMessage take this slice and derive each piece
// of the rendered output. The error return is non-nil only when the
// orchestration itself could not start any run (e.g. --all + --types
// mutual-exclusion); per-type failures live inside the result slice and
// the returned error is nil so the caller can render every outcome.
//
// Empty slice + nil error is the canonical "SIGINT arrived before the
// first Data Type started" signal — every other reason for zero
// progress (no Data Types requested, --all/--types conflict) returns
// nil + a non-nil error instead. The CLI translates the empty case
// into a sync_canceled result via fanOutStatus.
func (orchestrator syncOrchestrator) Sync(ctx context.Context, options syncCommandOptions) ([]syncResult, error) {
	dataTypes, err := orchestrator.expandDataTypes(options)
	if err != nil {
		return nil, err
	}
	if len(dataTypes) == 0 {
		// The gate's missing-types rule fires before we get here for the
		// no-flags path; an empty post-expansion list now only happens if
		// --all expanded to zero catalog entries (degenerate catalog).
		// Use a dedicated discriminator so downstream Rule()-based routing
		// can tell "no flags provided" apart from "the catalog has no
		// syncable Data Types right now".
		return nil, newPreflightFailure(
			preflightRuleAllExpandedEmpty,
			fmt.Errorf("sync --all expanded to zero supported Data Types; catalog has no syncable entries"),
		)
	}
	results := make([]syncResult, 0, len(dataTypes))
	for _, dataType := range dataTypes {
		if ctx.Err() != nil {
			// Orchestrator received SIGINT between Data Types: a prior
			// type either completed or had no time to start. Stop the
			// loop cleanly rather than emitting skipped-result rows for
			// the types that never started — those would inflate the
			// summary's "N attempted" count without any audit row to
			// back them up. The skipped types are simply absent from
			// the results slice; fanOutStatus folds an entirely empty
			// fan-out (cancel before the first type started) to
			// sync_canceled, and a partial fan-out (some completed,
			// rest skipped) currently surfaces as sync_completed —
			// callers that need the "incomplete vs complete" signal
			// must compare the requested type list against the
			// results slice themselves.
			break
		}
		perType := perTypeSyncOptions(options, dataType)
		result, execErr := orchestrator.executor.Execute(ctx, perType)
		if execErr != nil && result.Message == "" {
			result.Message = execErr.Error()
		}
		results = append(results, result)
	}
	return results, nil
}

// perTypeSyncOptions narrows a fan-out invocation's options down to a
// single Data Type for one Execute call. allTypes is cleared because the
// orchestrator already resolved the fan-out list — leaving allTypes=true
// alongside a single-element dataTypes slice would trigger the gate's
// preflightRuleAllVsTypesConflict and reject every per-type call,
// breaking `sync --all` end-to-end.
func perTypeSyncOptions(options syncCommandOptions, dataType string) syncCommandOptions {
	perType := options
	perType.allTypes = false
	perType.dataTypes = []string{dataType}
	return perType
}

// expandDataTypes resolves --all / --types into the concrete ordered list
// the orchestrator iterates. Delegates to syncPreflightGate so the
// --all / --types mutual-exclusion + duplicate-detection rules live in
// ONE place across the codebase; this method survives as a thin shim so
// existing orchestrator tests continue to exercise the fan-out rules
// without reaching into the gate's internals. The gate context is built
// from options carried by the orchestrator path; expandDataTypes never
// invokes the I/O adapters (currentConnection, sourceFamilyFilter,
// rollupCatalogValidator), so passing the production context here is
// safe and avoids a separate partial-context constructor with nil
// adapters that any future expansion-time rule could nil-deref.
func (orchestrator syncOrchestrator) expandDataTypes(options syncCommandOptions) ([]string, error) {
	gate := syncPreflightGate{ctx: productionSyncPreflightContext(options, orchestrator.executor.runtime)}
	return gate.expandDataTypes(options)
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

// fanOutMessage formats the one-line summary the CLI prints after a
// fan-out finishes. Per-status counts are derived from `results` rather
// than passed in, so the canceled-status arm can report "Data Types
// that actually completed" instead of len(results) — the canceled
// in-flight run sits in the slice too and would otherwise inflate the
// count by one.
func fanOutMessage(status string, results []syncResult) string {
	attempted := len(results)
	switch status {
	case "sync_failed":
		return fmt.Sprintf("Sync Run summary: %d Data Types attempted, at least one failed", attempted)
	case "sync_canceled":
		completed := 0
		for _, result := range results {
			if result.Status == "sync_completed" {
				completed++
			}
		}
		return fmt.Sprintf("Sync Run summary: %d Data Types completed before cancellation", completed)
	default:
		return fmt.Sprintf("Sync Run summary: %d Data Types archived", attempted)
	}
}

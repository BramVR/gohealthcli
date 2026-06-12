package main

import "context"

type syncRunExecutor struct {
	runtime runtimeAdapters
}

// preflightFailureResult shapes the syncResult returned when the gate
// rejects an invocation. Status is always sync_failed (never the empty
// string, per the JSON wire-shape AC); SyncRunID is unset so the JSON
// envelope omits sync_run_id (matching the no-audit-row contract).
// DataTypes mirrors what the operator passed so the failure envelope
// still names the run the user thought they were starting. From/To come
// straight from options because the gate returns a zero preflightPlan on
// every error path, so plan.from/plan.to would always be empty here.
func preflightFailureResult(options syncCommandOptions, err error) syncResult {
	return syncResult{
		Status:    "sync_failed",
		DataTypes: options.dataTypes,
		From:      options.from,
		To:        options.to,
		Message:   err.Error(),
	}
}

// Execute is the executor's per-Data-Type entry. It routes EVERY preflight
// rule through syncPreflightGate.Validate so this function holds no
// flag-shape, rollup-parse, source-family or connection-presence checks
// of its own — the gate owns the no-audit-row contract: when Validate
// fails, no sync_runs row has been written and the early return preserves
// that invariant. After Validate, the post-preflight path lives in
// syncRunLifecycle.Run (PRD #141 slice 4 AC #1): this method is a thin
// caller composing the gate and the lifecycle.
func (executor syncRunExecutor) Execute(ctx context.Context, options syncCommandOptions) (syncResult, error) {
	runtime := executor.runtime.withDefaults()
	gate := syncPreflightGate{ctx: productionSyncPreflightContext(ctx, options, runtime)}
	plan, err := gate.Validate(options)
	if err != nil {
		return preflightFailureResult(options, err), err
	}
	return syncRunLifecycle{options: options, plan: plan, runtime: runtime}.Run(ctx)
}

func applyGoogleHealthIngestionCounts(result *syncResult, ingestionResult googleHealthIngestionResult) {
	result.DataPointsSeen = ingestionResult.dataPointsSeen
	result.DataPointsNew = ingestionResult.dataPointsNew
	result.DataPointsUpdated = ingestionResult.dataPointsUpdated
	result.RollupsSeen = ingestionResult.rollupsSeen
	result.RollupsNew = ingestionResult.rollupsNew
	result.RollupsUpdated = ingestionResult.rollupsUpdated
}

func syncResultTotalCounts(result syncResult) (int, int, int) {
	return result.DataPointsSeen + result.RollupsSeen,
		result.DataPointsNew + result.RollupsNew,
		result.DataPointsUpdated + result.RollupsUpdated
}

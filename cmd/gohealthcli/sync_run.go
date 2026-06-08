package main

import (
	"errors"
	"fmt"
	"time"
)

type syncRunExecutor struct {
	runtime runtimeAdapters
}

// healthArchiveWriterOpenerForTest allows tests to inject a wrapped writer
// (for example, one that forces CommitSyncCursor to fail) without rewriting
// the whole sync pipeline. Production always uses openHealthArchiveWriter.
var healthArchiveWriterOpenerForTest = openHealthArchiveWriter

func syncSetup(options syncCommandOptions) (syncResult, error) {
	return syncSetupWithRuntime(options, productionRuntimeAdapters())
}

func syncSetupWithRuntime(options syncCommandOptions, runtime runtimeAdapters) (syncResult, error) {
	return (syncRunExecutor{runtime: runtime}).Execute(options)
}

// preflightFailureResult shapes the syncResult returned when the gate
// rejects an invocation. Status is always sync_failed (never the empty
// string, per the JSON wire-shape AC); SyncRunID is unset so the JSON
// envelope omits sync_run_id (matching the no-audit-row contract).
// DataTypes mirrors what the operator passed so the failure envelope
// still names the run the user thought they were starting.
func preflightFailureResult(options syncCommandOptions, plan preflightPlan, err error) syncResult {
	result := syncResult{Status: "sync_failed", DataTypes: options.dataTypes, Message: err.Error()}
	if plan.from != "" {
		result.From = plan.from
	} else {
		result.From = options.from
	}
	if plan.to != "" {
		result.To = plan.to
	} else {
		result.To = options.to
	}
	return result
}

// Execute is the executor's per-Data-Type entry. It routes EVERY preflight
// rule through syncPreflightGate.Validate so this function holds no
// flag-shape, rollup-parse, source-family or connection-presence checks
// of its own — the gate owns the no-audit-row contract: when Validate
// fails, no sync_runs row has been written and the early return preserves
// that invariant. Resume-from-cursor lookup stays here because it depends
// on archive state, not flag shape; the rest of the body assumes every
// option is already validated.
func (executor syncRunExecutor) Execute(options syncCommandOptions) (syncResult, error) {
	runtime := executor.runtime.withDefaults()
	gate := syncPreflightGate{ctx: productionSyncPreflightContext(options, runtime)}
	plan, err := gate.Validate(options)
	if err != nil {
		return preflightFailureResult(options, plan, err), err
	}
	if len(plan.dataTypes) != 1 {
		return syncResult{Status: "sync_failed", DataTypes: plan.dataTypes}, errors.New("sync currently supports one Data Type per run")
	}
	dataType := plan.dataTypes[0]
	options.to = plan.to
	connection := plan.connection
	archive, err := healthArchiveWriterOpenerForTest(options.archivePath)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: plan.dataTypes, From: options.from, To: options.to}, err
	}
	defer archive.Close()
	config, err := inspectIdentityConfig(options.configPath, options.archivePath)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: plan.dataTypes, From: options.from, To: options.to}, fmt.Errorf("config check failed: %w", err)
	}
	cursorKey := plan.cursorKeys[0]
	resumedFromCursor := false
	if options.from == "" {
		cursorTime, found, err := archive.ResolveSyncCursor(cursorKey)
		if err != nil {
			return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, To: options.to}, fmt.Errorf("resolve Sync Cursor: %w", err)
		}
		if !found {
			return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, To: options.to}, errors.New("sync has no Sync Cursor for this Data Type yet; set --from for the initial backfill")
		}
		options.from = cursorTime
		resumedFromCursor = true
	}
	ingestion := newGoogleHealthIngestionWithRuntime(runtime)
	// grantedScopes feeds the optional-scope gate inside the TCX
	// archival hook (#140). connectionTokenExpiryAndScopes already
	// validates the metadata blob earlier in the call chain (via
	// requireConnectionScopes / AccessToken), so an error here would
	// be surprising; surface it so the Sync Run fails loudly rather
	// than silently de-gating the hook.
	_, grantedScopes, err := connectionTokenExpiryAndScopes(connection.tokenMetadataJSON)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, err
	}
	ingestionRequest := googleHealthIngestionRequest{
		connection:    connection,
		dataType:      dataType,
		from:          options.from,
		to:            options.to,
		rollup:        options.rollup,
		sourceFamily:  options.sourceFamily,
		grantedScopes: grantedScopes,
		cancelCh:      options.cancelCh,
	}
	ingestionPlan, err := ingestion.Plan(ingestionRequest)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, err
	}
	result := syncResult{
		ConnectionID:      connection.id,
		ProviderName:      connection.providerName,
		DataTypes:         options.dataTypes,
		From:              options.from,
		To:                options.to,
		EndpointFamily:    ingestionPlan.endpointFamily,
		SourceFamily:      options.sourceFamily,
		ResumedFromCursor: resumedFromCursor,
	}
	startedAt := runtime.now().UTC().Format(time.RFC3339)
	syncRunID, err := archive.StartSyncRun(connection, options.dataTypes, options.from, options.to, result.EndpointFamily, result.SourceFamily, startedAt)
	if err != nil {
		return result, err
	}
	result.SyncRunID = syncRunID
	finalize := func(outcome syncRunOutcome, cause error) (syncResult, error) {
		result.Status = string(outcome)
		errorSummary := ""
		if cause != nil {
			result.Message = cause.Error()
			errorSummary = result.Message
		}
		now := runtime.now().UTC().Format(time.RFC3339)
		seen, newCount, updated := syncResultTotalCounts(result)
		if finalizeErr := archive.FinalizeSyncRun(syncRunFinalize{
			SyncRunID:      syncRunID,
			Outcome:        outcome,
			SeenCount:      seen,
			NewCount:       newCount,
			UpdatedCount:   updated,
			FinishedAt:     now,
			ErrorSummary:   errorSummary,
			CursorKey:      cursorKey,
			CursorTo:       options.to,
			CursorAdvanced: now,
		}); finalizeErr != nil {
			// The atomic write rolled back: the sync_run row is still in its
			// StartSyncRun state and no cursor advance happened. For an
			// originally sync_completed outcome, write a separate sync_failed
			// row so the audit trail does not show a perpetually-running
			// attempt; for outcomes that were already failure-shaped
			// (sync_failed / sync_canceled), the legacy single-UPDATE path
			// would have left the row as sync_running on the same write
			// failure, so do not silently change a canceled run into a
			// failed one. Surface both errors when the recovery write also
			// fails — there is no in-process action that can repair an
			// unreachable database.
			result.Status = "sync_failed"
			result.Message = finalizeErr.Error()
			finalErr := fmt.Errorf("record %s Sync Run: %w", outcome, finalizeErr)
			if cause != nil {
				finalErr = fmt.Errorf("%w; %v", cause, finalErr)
			}
			if outcome.AdvancesCursor() {
				if recoveryErr := archive.FinishSyncRun(syncRunID, "sync_failed", seen, newCount, updated, now, result.Message); recoveryErr != nil {
					finalErr = fmt.Errorf("%v; mark Sync Run failed: %w", finalErr, recoveryErr)
				}
			}
			return result, finalErr
		}
		return result, cause
	}
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{options.configPath, options.archivePath}, runtime)
	if config.oauthClient.kind == "file" {
		// Only the file OAuth client source can drive a refresh today;
		// for other sources, keep the historical fail-on-expired error
		// shape instead of wrapping a "needs file source" message.
		connectionAccess = connectionAccess.WithAutoRefresh(config.oauthClient, archive)
	}
	accessToken, err := connectionAccess.AccessToken(googleHealthScopesForDataType(dataType))
	if err != nil {
		return finalize(syncRunOutcomeFailed, err)
	}
	if _, err := connectionAccess.FetchVerifiedIdentity(accessToken); err != nil {
		return finalize(syncRunOutcomeFailed, err)
	}
	ingestionRequest.accessToken = accessToken
	ingestionResult, err := ingestion.Execute(archive, ingestionRequest)
	applyGoogleHealthIngestionCounts(&result, ingestionResult)
	if err != nil {
		if errors.Is(err, errSyncCanceled) {
			return finalize(syncRunOutcomeCanceled, err)
		}
		return finalize(syncRunOutcomeFailed, err)
	}
	if options.rollup != "" {
		result.Message = fmt.Sprintf("Sync Run archived %s %s Rollups", dataType, options.rollup)
	} else if options.sourceFamily != "" {
		result.Message = fmt.Sprintf("Sync Run archived %s Data Points with source-family filter", dataType)
	} else {
		result.Message = fmt.Sprintf("Sync Run archived %s Data Points", dataType)
	}
	return finalize(syncRunOutcomeCompleted, nil)
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

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

func (executor syncRunExecutor) Execute(options syncCommandOptions) (syncResult, error) {
	runtime := executor.runtime.withDefaults()
	if len(options.dataTypes) == 0 {
		return syncResult{Status: "sync_failed"}, errors.New("sync requires at least one Data Type")
	}
	if len(options.dataTypes) != 1 {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, errors.New("sync currently supports one Data Type per run")
	}
	dataType := options.dataTypes[0]
	if !syncDataPointDataTypeSupported(dataType) {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, fmt.Errorf("sync Data Type %q is not supported yet", dataType)
	}
	if options.rollup != "" && options.rollup != "daily" {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, errors.New("sync --rollup currently supports only daily")
	}
	if options.rollup != "" && !dailyRollupDataTypeSupported(dataType) {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, errors.New("sync --rollup currently supports only Data Type steps")
	}
	if options.sourceFamily != "" {
		if _, err := googleHealthSourceFamilyFilterName(dataType, options.sourceFamily); err != nil {
			return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, err
		}
	}
	if options.rollup != "" && options.sourceFamily != "" {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, errors.New("sync --source-family cannot be combined with --rollup")
	}
	if options.to == "" {
		if options.rollup == "daily" || syncDataPointUsesDateRange(dataType) {
			options.to = runtime.now().UTC().Format("2006-01-02")
		} else {
			options.to = runtime.now().UTC().Format(time.RFC3339)
		}
	}

	config, err := inspectFullConfig(options.configPath, options.archivePath)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, fmt.Errorf("config check failed: %w", err)
	}
	archive, err := healthArchiveWriterOpenerForTest(options.archivePath)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, err
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, err
	}
	cursorKey := syncCursorKey{
		connectionID:       connection.id,
		dataType:           dataType,
		sourceFamilyFilter: options.sourceFamily,
		rollupKind:         rollupKindForSync(options.rollup),
	}
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
	ingestionRequest := googleHealthIngestionRequest{
		connection:   connection,
		dataType:     dataType,
		from:         options.from,
		to:           options.to,
		rollup:       options.rollup,
		sourceFamily: options.sourceFamily,
		cancelCh:     options.cancelCh,
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
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{options.configPath, options.archivePath}, runtime).
		WithAutoRefresh(config.oauthClient, archive)
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
	if options.rollup == "daily" {
		result.Message = "Sync Run archived steps daily Rollups"
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

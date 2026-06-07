package main

import (
	"errors"
	"fmt"
	"time"
)

type syncRunExecutor struct {
	runtime runtimeAdapters
}

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
	if options.from == "" {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, errors.New("sync requires --from")
	}
	if options.to == "" {
		if options.rollup == "daily" || syncDataPointUsesDateRange(dataType) {
			options.to = runtime.now().UTC().Format("2006-01-02")
		} else {
			options.to = runtime.now().UTC().Format(time.RFC3339)
		}
	}

	config, err := inspectIdentityConfig(options.configPath, options.archivePath)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, fmt.Errorf("config check failed: %w", err)
	}
	archive, err := openHealthArchiveWriter(options.archivePath)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, err
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, err
	}
	ingestion := newGoogleHealthIngestionWithRuntime(runtime)
	ingestionRequest := googleHealthIngestionRequest{
		connection:   connection,
		dataType:     dataType,
		from:         options.from,
		to:           options.to,
		rollup:       options.rollup,
		sourceFamily: options.sourceFamily,
	}
	ingestionPlan, err := ingestion.Plan(ingestionRequest)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, err
	}
	result := syncResult{
		ConnectionID:   connection.id,
		ProviderName:   connection.providerName,
		DataTypes:      options.dataTypes,
		From:           options.from,
		To:             options.to,
		EndpointFamily: ingestionPlan.endpointFamily,
		SourceFamily:   options.sourceFamily,
	}
	startedAt := runtime.now().UTC().Format(time.RFC3339)
	syncRunID, err := archive.StartSyncRun(connection, options.dataTypes, options.from, options.to, result.EndpointFamily, result.SourceFamily, startedAt)
	if err != nil {
		return result, err
	}
	result.SyncRunID = syncRunID
	fail := func(cause error) (syncResult, error) {
		result.Status = "sync_failed"
		result.Message = cause.Error()
		seen, newCount, updated := syncResultTotalCounts(result)
		if updateErr := archive.FinishSyncRun(syncRunID, result.Status, seen, newCount, updated, runtime.now().UTC().Format(time.RFC3339), result.Message); updateErr != nil {
			return result, fmt.Errorf("%w; record failed Sync Run: %v", cause, updateErr)
		}
		return result, cause
	}
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{options.configPath, options.archivePath}, runtime)
	accessToken, err := connectionAccess.AccessToken(googleHealthScopesForDataType(dataType))
	if err != nil {
		return fail(err)
	}
	if _, err := connectionAccess.FetchVerifiedIdentity(accessToken); err != nil {
		return fail(err)
	}
	ingestionRequest.accessToken = accessToken
	ingestionResult, err := ingestion.Execute(archive, ingestionRequest)
	applyGoogleHealthIngestionCounts(&result, ingestionResult)
	if err != nil {
		return fail(err)
	}
	seen, newCount, updated := syncResultTotalCounts(result)
	if err := archive.FinishSyncRun(syncRunID, "sync_completed", seen, newCount, updated, runtime.now().UTC().Format(time.RFC3339), ""); err != nil {
		result.Status = "sync_failed"
		result.Message = err.Error()
		return result, err
	}
	result.Status = "sync_completed"
	if options.rollup == "daily" {
		result.Message = "Sync Run archived steps daily Rollups"
	} else if options.sourceFamily != "" {
		result.Message = fmt.Sprintf("Sync Run archived %s Data Points with source-family filter", dataType)
	} else {
		result.Message = fmt.Sprintf("Sync Run archived %s Data Points", dataType)
	}
	return result, nil
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

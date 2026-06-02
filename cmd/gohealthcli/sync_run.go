package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type syncRunExecutor struct{}

func syncSetup(options syncCommandOptions) (syncResult, error) {
	return (syncRunExecutor{}).Execute(options)
}

func (syncRunExecutor) Execute(options syncCommandOptions) (syncResult, error) {
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
	if options.rollup != "" && dataType != "steps" {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, errors.New("sync --rollup currently supports only Data Type steps")
	}
	if options.sourceFamily != "" && options.sourceFamily != "wearable" {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, errors.New("sync --source-family currently supports only wearable")
	}
	if options.rollup != "" && options.sourceFamily != "" {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, errors.New("sync --source-family cannot be combined with --rollup")
	}
	if options.from == "" {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes}, errors.New("sync requires --from")
	}
	if options.to == "" {
		if options.rollup == "daily" || syncDataPointUsesDateRange(dataType) {
			options.to = currentTime().UTC().Format("2006-01-02")
		} else {
			options.to = currentTime().UTC().Format(time.RFC3339)
		}
	}

	config, err := inspectIdentityConfig(options.configPath, options.archivePath)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, fmt.Errorf("config check failed: %w", err)
	}
	if err := migrateArchiveIfNeeded(options.archivePath); err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, fmt.Errorf("Health Archive migration failed: %w", err)
	}
	if _, err := inspectArchive(options.archivePath, false); err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, fmt.Errorf("Health Archive check failed: %w", err)
	}
	db, err := openArchive(options.archivePath)
	if err != nil {
		return syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}, err
	}
	defer db.Close()
	connection, err := readCurrentConnection(db)
	if err != nil {
		result := syncResult{Status: "sync_failed", DataTypes: options.dataTypes, From: options.from, To: options.to}
		if errors.Is(err, sql.ErrNoRows) {
			return result, errors.New("no Connection found; run `gohealthcli connect` first")
		}
		return result, err
	}
	endpointFamily := "list"
	if options.rollup == "daily" {
		endpointFamily = "dailyRollUp"
	} else if options.sourceFamily != "" {
		endpointFamily = "reconcile"
	}
	result := syncResult{
		ConnectionID:   connection.id,
		ProviderName:   connection.providerName,
		DataTypes:      options.dataTypes,
		From:           options.from,
		To:             options.to,
		EndpointFamily: endpointFamily,
		SourceFamily:   options.sourceFamily,
	}
	startedAt := currentTime().UTC().Format(time.RFC3339)
	syncRunID, err := insertSyncRun(db, connection, options.dataTypes, options.from, options.to, result.EndpointFamily, result.SourceFamily, startedAt)
	if err != nil {
		return result, err
	}
	result.SyncRunID = syncRunID
	fail := func(cause error) (syncResult, error) {
		result.Status = "sync_failed"
		result.Message = cause.Error()
		seen, newCount, updated := syncResultTotalCounts(result)
		if updateErr := finishSyncRunRecord(db, syncRunID, result.Status, seen, newCount, updated, currentTime().UTC().Format(time.RFC3339), result.Message); updateErr != nil {
			return result, fmt.Errorf("%w; record failed Sync Run: %v", cause, updateErr)
		}
		return result, cause
	}
	if err := requireUsableConnectionAccessToken(connection.tokenMetadataJSON, currentTime()); err != nil {
		return fail(err)
	}
	if err := requireConnectionScopes(connection.tokenMetadataJSON, googleHealthScopesForDataType(dataType)); err != nil {
		return fail(err)
	}
	accessToken, err := loadAccessTokenForConnection(config.credentialStore, connection, []string{options.configPath, options.archivePath})
	if err != nil {
		return fail(err)
	}
	identity, err := fetchIdentity(accessToken)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 401") {
			return fail(errors.New("Google Health rejected stored Connection token; run `gohealthcli connect` again"))
		}
		return fail(err)
	}
	if identity.healthUserID != connection.googleHealthUserID {
		return fail(errors.New("Provider returned a different Google Identity; use a new archive path"))
	}
	if options.rollup == "daily" {
		windows, err := googleHealthDailyRollupDateWindows(options.from, options.to)
		if err != nil {
			return fail(err)
		}
		for _, window := range windows {
			seenPageTokens := map[string]struct{}{}
			for pageToken := ""; ; {
				request, err := buildGoogleHealthDailyRollupRawRequest(dataType, window.from, window.to, 0, pageToken)
				if err != nil {
					return fail(err)
				}
				body, err := fetchRawProvider(request, accessToken)
				if err != nil {
					if strings.Contains(err.Error(), "HTTP 401") {
						return fail(errors.New("Google Health rejected stored Connection token; run `gohealthcli connect` again"))
					}
					return fail(err)
				}
				page, err := parseGoogleHealthRollupList(body)
				if err != nil {
					return fail(err)
				}
				for _, rawRollup := range page.rollups {
					rollup, err := parseGoogleHealthStepsDailyRollup(connection, rawRollup)
					if err != nil {
						return fail(err)
					}
					status, err := upsertRollup(db, rollup, currentTime().UTC().Format(time.RFC3339))
					if err != nil {
						return fail(err)
					}
					result.RollupsSeen++
					switch status {
					case "new":
						result.RollupsNew++
					case "updated":
						result.RollupsUpdated++
					}
				}
				if page.nextPageToken == "" {
					break
				}
				if _, ok := seenPageTokens[page.nextPageToken]; ok {
					return fail(errors.New("Google Health steps dailyRollUp returned a repeated page token"))
				}
				seenPageTokens[page.nextPageToken] = struct{}{}
				pageToken = page.nextPageToken
			}
		}
	} else {
		seenPageTokens := map[string]struct{}{}
		for pageToken := ""; ; {
			request, err := buildGoogleHealthSyncDataPointRawRequest(dataType, options.from, options.to, options.sourceFamily, 0, pageToken)
			if err != nil {
				return fail(err)
			}
			body, err := fetchRawProvider(request, accessToken)
			if err != nil {
				if strings.Contains(err.Error(), "HTTP 401") {
					return fail(errors.New("Google Health rejected stored Connection token; run `gohealthcli connect` again"))
				}
				return fail(err)
			}
			page, err := parseGoogleHealthDataPointList(body)
			if err != nil {
				return fail(err)
			}
			for _, rawPoint := range page.dataPoints {
				point, err := parseGoogleHealthDataPoint(connection, dataType, rawPoint, options.sourceFamily)
				if err != nil {
					return fail(err)
				}
				status, err := upsertDataPoint(db, point, currentTime().UTC().Format(time.RFC3339))
				if err != nil {
					return fail(err)
				}
				result.DataPointsSeen++
				switch status {
				case "new":
					result.DataPointsNew++
				case "updated":
					result.DataPointsUpdated++
				}
			}
			if page.nextPageToken == "" {
				break
			}
			if _, ok := seenPageTokens[page.nextPageToken]; ok {
				return fail(fmt.Errorf("Google Health %s %s returned a repeated page token", dataType, endpointFamily))
			}
			seenPageTokens[page.nextPageToken] = struct{}{}
			pageToken = page.nextPageToken
		}
	}
	seen, newCount, updated := syncResultTotalCounts(result)
	if err := finishSyncRunRecord(db, syncRunID, "sync_completed", seen, newCount, updated, currentTime().UTC().Format(time.RFC3339), ""); err != nil {
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

func syncDataPointDataTypeSupported(dataType string) bool {
	_, dailySupported := googleHealthDailyDataPointShapeForDataType(dataType)
	return dataType == "steps" || googleHealthSampleDataPointJSONField(dataType) != "" || dailySupported
}

func syncDataPointUsesDateRange(dataType string) bool {
	_, ok := googleHealthDailyDataPointShapeForDataType(dataType)
	return ok
}

func syncResultTotalCounts(result syncResult) (int, int, int) {
	return result.DataPointsSeen + result.RollupsSeen,
		result.DataPointsNew + result.RollupsNew,
		result.DataPointsUpdated + result.RollupsUpdated
}

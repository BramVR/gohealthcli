package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type syncRunExecutor struct{}

type googleHealthDateRange struct {
	from string
	to   string
}

func syncSetup(options syncCommandOptions) (syncResult, error) {
	return (syncRunExecutor{}).Execute(options)
}

func (executor syncRunExecutor) Execute(options syncCommandOptions) (syncResult, error) {
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
		if err := executor.executeDailyRollupPages(db, connection, dataType, options, accessToken, &result); err != nil {
			return fail(err)
		}
	} else {
		if err := executor.executeDataPointPages(db, connection, dataType, options, accessToken, &result); err != nil {
			return fail(err)
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

func (syncRunExecutor) executeDailyRollupPages(db *sql.DB, connection archivedConnection, dataType string, options syncCommandOptions, accessToken string, result *syncResult) error {
	windows, err := googleHealthDailyRollupDateWindows(options.from, options.to)
	if err != nil {
		return err
	}
	for _, window := range windows {
		seenPageTokens := map[string]struct{}{}
		for pageToken := ""; ; {
			request, err := buildGoogleHealthDailyRollupRawRequest(dataType, window.from, window.to, 0, pageToken)
			if err != nil {
				return err
			}
			body, err := fetchRawProvider(request, accessToken)
			if err != nil {
				return syncProviderRequestError(err)
			}
			page, err := parseGoogleHealthRollupList(body)
			if err != nil {
				return err
			}
			for _, rawRollup := range page.rollups {
				rollup, err := parseGoogleHealthStepsDailyRollup(connection, rawRollup)
				if err != nil {
					return err
				}
				status, err := upsertRollup(db, rollup, currentTime().UTC().Format(time.RFC3339))
				if err != nil {
					return err
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
				return errors.New("Google Health steps dailyRollUp returned a repeated page token")
			}
			seenPageTokens[page.nextPageToken] = struct{}{}
			pageToken = page.nextPageToken
		}
	}
	return nil
}

func buildGoogleHealthDailyRollupRawRequest(dataType, from, to string, pageSize int64, pageToken string) (rawProviderRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return rawProviderRequest{}, err
	}
	if !dailyRollupDataTypeSupported(dataType) {
		return rawProviderRequest{}, errors.New("daily Rollup sync currently supports only Data Type steps")
	}
	if from == "" {
		return rawProviderRequest{}, errors.New("daily Rollup calls require --from")
	}
	rangeJSON, err := googleHealthCivilTimeIntervalJSON(from, to)
	if err != nil {
		return rawProviderRequest{}, err
	}
	body := struct {
		Range          json.RawMessage `json:"range"`
		WindowSizeDays int             `json:"windowSizeDays"`
		PageSize       int64           `json:"pageSize,omitempty"`
		PageToken      string          `json:"pageToken,omitempty"`
	}{
		Range:          rangeJSON,
		WindowSizeDays: 1,
		PageSize:       pageSize,
		PageToken:      pageToken,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return rawProviderRequest{}, err
	}
	return rawProviderRequest{
		endpointName:   "dataTypes." + dataType + ".dailyRollUp",
		dataType:       dataType,
		method:         http.MethodPost,
		url:            googleHealthBaseURL + "/users/me/dataTypes/" + url.PathEscape(dataType) + "/dataPoints:dailyRollUp",
		body:           bodyJSON,
		requiredScopes: googleHealthScopesForDataType(dataType),
	}, nil
}

func googleHealthCivilTimeIntervalJSON(from, to string) (json.RawMessage, error) {
	if to == "" {
		return nil, errors.New("daily Rollup calls require --to")
	}
	start, err := googleHealthCivilDateJSON(from)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	end, err := googleHealthCivilDateJSON(to)
	if err != nil {
		return nil, fmt.Errorf("--to: %w", err)
	}
	content, err := json.Marshal(struct {
		Start json.RawMessage `json:"start"`
		End   json.RawMessage `json:"end"`
	}{
		Start: start,
		End:   end,
	})
	if err != nil {
		return nil, err
	}
	return content, nil
}

func googleHealthDailyRollupDateWindows(from, to string) ([]googleHealthDateRange, error) {
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil, fmt.Errorf("--from: expected YYYY-MM-DD")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return nil, fmt.Errorf("--to: expected YYYY-MM-DD")
	}
	if !end.After(start) {
		return nil, errors.New("--to must be after --from for daily Rollup sync")
	}
	var windows []googleHealthDateRange
	for current := start; current.Before(end); {
		next := current.AddDate(0, 0, 90)
		if next.After(end) {
			next = end
		}
		windows = append(windows, googleHealthDateRange{
			from: current.Format("2006-01-02"),
			to:   next.Format("2006-01-02"),
		})
		current = next
	}
	return windows, nil
}

func googleHealthCivilDateJSON(value string) (json.RawMessage, error) {
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		date := struct {
			Year  int `json:"year"`
			Month int `json:"month"`
			Day   int `json:"day"`
		}{
			Year:  parsed.Year(),
			Month: int(parsed.Month()),
			Day:   parsed.Day(),
		}
		return json.Marshal(struct {
			Date any `json:"date"`
		}{Date: date})
	}
	return nil, errors.New("expected YYYY-MM-DD")
}

func (syncRunExecutor) executeDataPointPages(db *sql.DB, connection archivedConnection, dataType string, options syncCommandOptions, accessToken string, result *syncResult) error {
	seenPageTokens := map[string]struct{}{}
	for pageToken := ""; ; {
		request, err := buildGoogleHealthSyncDataPointRawRequest(dataType, options.from, options.to, options.sourceFamily, 0, pageToken)
		if err != nil {
			return err
		}
		body, err := fetchRawProvider(request, accessToken)
		if err != nil {
			return syncProviderRequestError(err)
		}
		page, err := parseGoogleHealthDataPointList(body)
		if err != nil {
			return err
		}
		for _, rawPoint := range page.dataPoints {
			point, err := parseGoogleHealthDataPoint(connection, dataType, rawPoint, options.sourceFamily)
			if err != nil {
				return err
			}
			status, err := upsertDataPoint(db, point, currentTime().UTC().Format(time.RFC3339))
			if err != nil {
				return err
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
			return fmt.Errorf("Google Health %s %s returned a repeated page token", dataType, result.EndpointFamily)
		}
		seenPageTokens[page.nextPageToken] = struct{}{}
		pageToken = page.nextPageToken
	}
	return nil
}

func buildGoogleHealthSyncDataPointRawRequest(dataType, from, to, sourceFamily string, pageSize int64, pageToken string) (rawProviderRequest, error) {
	if sourceFamily == "" {
		return buildGoogleHealthDataTypeListRawRequest(dataType, from, to, pageSize, pageToken)
	}
	return buildGoogleHealthDataTypeReconcileRawRequest(dataType, from, to, sourceFamily, pageSize, pageToken)
}

func buildGoogleHealthDataTypeReconcileRawRequest(dataType, from, to, sourceFamily string, pageSize int64, pageToken string) (rawProviderRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return rawProviderRequest{}, err
	}
	if from == "" {
		return rawProviderRequest{}, errors.New("Data Type reconcile raw calls require --from")
	}
	dataSourceFamily, err := googleHealthSourceFamilyFilterName(dataType, sourceFamily)
	if err != nil {
		return rawProviderRequest{}, err
	}
	query := url.Values{}
	filter, err := googleHealthDataTypeListFilter(dataType, from, to)
	if err != nil {
		return rawProviderRequest{}, err
	}
	query.Set("filter", filter)
	query.Set("dataSourceFamily", dataSourceFamily)
	if pageSize > 0 {
		query.Set("pageSize", strconv.FormatInt(pageSize, 10))
	}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	requestURL := googleHealthBaseURL + "/users/me/dataTypes/" + url.PathEscape(dataType) + "/dataPoints:reconcile"
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}
	return rawProviderRequest{
		endpointName:       "dataTypes." + dataType + ".reconcile",
		dataType:           dataType,
		method:             http.MethodGet,
		url:                requestURL,
		requiredScopes:     googleHealthScopesForDataType(dataType),
		sourceFamilyFilter: sourceFamily,
	}, nil
}

func syncProviderRequestError(err error) error {
	if strings.Contains(err.Error(), "HTTP 401") {
		return errors.New("Google Health rejected stored Connection token; run `gohealthcli connect` again")
	}
	return err
}

func syncResultTotalCounts(result syncResult) (int, int, int) {
	return result.DataPointsSeen + result.RollupsSeen,
		result.DataPointsNew + result.RollupsNew,
		result.DataPointsUpdated + result.RollupsUpdated
}

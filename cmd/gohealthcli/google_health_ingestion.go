package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type googleHealthIngestionArchive interface {
	UpsertDataPoint(point archivedDataPoint, now string) (string, error)
	UpsertRollup(rollup archivedRollup, now string) (string, error)
}

type googleHealthIngestionProvider interface {
	// Fetch issues one request against the upstream provider. cancelCh,
	// when closed, asks the implementation to short-circuit any in-flight
	// retry backoff and surface errSyncCanceled at the next opportunity.
	// nil disables cancellation.
	Fetch(request rawProviderRequest, accessToken string, cancelCh <-chan struct{}) ([]byte, error)
}

type googleHealthIngestion struct {
	provider googleHealthIngestionProvider
	now      func() time.Time
}

type googleHealthIngestionRequest struct {
	connection   archivedConnection
	dataType     string
	from         string
	to           string
	rollup       string
	sourceFamily string
	accessToken  string
	// cancelCh, when closed, asks pagination to stop cleanly between pages.
	// Returns errSyncCanceled. The in-flight Fetch (if any) is not aborted —
	// SIGINT during a sync stops at the next page boundary, not mid-HTTP.
	// nil disables cancellation (used by single-type syncs without SIGINT
	// instrumentation).
	cancelCh <-chan struct{}
}

// errSyncCanceled is the sentinel returned by ingestion when the cancel
// channel was closed between pages. sync_run.go translates it into the
// syncRunOutcomeCanceled outcome, which leaves the Sync Cursor un-advanced
// (ADR-0008).
var errSyncCanceled = errors.New("Sync Run canceled")

// ingestionCanceled is a non-blocking check for a closed cancel channel.
// nil channel disables cancellation, matching the single-type CLI path
// before SIGINT instrumentation was added.
func ingestionCanceled(cancelCh <-chan struct{}) bool {
	if cancelCh == nil {
		return false
	}
	select {
	case <-cancelCh:
		return true
	default:
		return false
	}
}

type googleHealthIngestionPlan struct {
	endpointFamily string
}

type googleHealthIngestionResult struct {
	endpointFamily    string
	dataPointsSeen    int
	dataPointsNew     int
	dataPointsUpdated int
	rollupsSeen       int
	rollupsNew        int
	rollupsUpdated    int
}

type googleHealthDateRange struct {
	from string
	to   string
}

type googleHealthRollupList struct {
	rollups       []json.RawMessage
	nextPageToken string
}

func newGoogleHealthIngestion() googleHealthIngestion {
	return newGoogleHealthIngestionWithRuntime(productionRuntimeAdapters())
}

func newGoogleHealthIngestionWithRuntime(runtime runtimeAdapters) googleHealthIngestion {
	runtime = runtime.withDefaults()
	return googleHealthIngestion{
		provider: runtimeGoogleHealthIngestionProvider{runtime: runtime},
		now:      runtime.now,
	}
}

type runtimeGoogleHealthIngestionProvider struct {
	runtime runtimeAdapters
	// sleeper and jitter are test seams. Production leaves them nil and
	// fetchWithRetry falls back to time.Sleep + defaultRetryJitter.
	sleeper googleHealthRetrySleeper
	jitter  func(time.Duration) time.Duration
}

func (provider runtimeGoogleHealthIngestionProvider) Fetch(request rawProviderRequest, accessToken string, cancelCh <-chan struct{}) ([]byte, error) {
	return fetchWithRetry(provider.runtime.fetchRawProvider, provider.sleeper, provider.jitter, request, accessToken, cancelCh)
}

func (ingestion googleHealthIngestion) Plan(request googleHealthIngestionRequest) (googleHealthIngestionPlan, error) {
	entry, ok := googleHealthDataTypes.Lookup(request.dataType)
	if !ok {
		return googleHealthIngestionPlan{}, fmt.Errorf("sync Data Type %q is not supported yet", request.dataType)
	}
	_, hasList := entry.SupportedEndpoints[endpointFamilyList]
	_, hasReconcile := entry.SupportedEndpoints[endpointFamilyReconcile]
	_, hasDailyRollup := entry.SupportedEndpoints[endpointFamilyDailyRollUp]
	if !hasList && !hasReconcile {
		return googleHealthIngestionPlan{}, fmt.Errorf("sync Data Type %q is not supported yet", request.dataType)
	}
	if request.rollup == "daily" {
		// The daily-rollup parser still hard-codes steps shapes
		// (parseGoogleHealthStepsDailyRollup). Any Data Type whose
		// SupportedEndpoints map carries dailyRollUp would otherwise
		// route through that steps-specific parser and mis-archive.
		// #106 introduces the generic rollup parser; until then,
		// gate daily-rollup support on the actual implementation.
		if !hasDailyRollup || request.dataType != "steps" {
			return googleHealthIngestionPlan{}, errors.New("sync --rollup currently supports only Data Type steps")
		}
		return googleHealthIngestionPlan{endpointFamily: "dailyRollUp"}, nil
	}
	if request.sourceFamily != "" {
		if !hasReconcile {
			return googleHealthIngestionPlan{}, fmt.Errorf("sync Data Type %q does not support source-family filtering", request.dataType)
		}
		return googleHealthIngestionPlan{endpointFamily: "reconcile"}, nil
	}
	return googleHealthIngestionPlan{endpointFamily: "list"}, nil
}

func (ingestion googleHealthIngestion) Execute(archive googleHealthIngestionArchive, request googleHealthIngestionRequest) (googleHealthIngestionResult, error) {
	plan, err := ingestion.Plan(request)
	if err != nil {
		return googleHealthIngestionResult{}, err
	}
	result := googleHealthIngestionResult{endpointFamily: plan.endpointFamily}
	if plan.endpointFamily == "dailyRollUp" {
		err = ingestion.executeDailyRollupPages(archive, request, &result)
	} else {
		err = ingestion.executeDataPointPages(archive, request, &result)
	}
	return result, err
}

func (ingestion googleHealthIngestion) executeDailyRollupPages(archive googleHealthIngestionArchive, request googleHealthIngestionRequest, result *googleHealthIngestionResult) error {
	windows, err := googleHealthDailyRollupDateWindows(request.from, request.to)
	if err != nil {
		return err
	}
	for _, window := range windows {
		seenPageTokens := map[string]struct{}{}
		for pageToken := ""; ; {
			if ingestionCanceled(request.cancelCh) {
				return errSyncCanceled
			}
			rawRequest, err := buildGoogleHealthDailyRollupRawRequest(request.dataType, window.from, window.to, 0, pageToken)
			if err != nil {
				return err
			}
			body, err := ingestion.provider.Fetch(rawRequest, request.accessToken, request.cancelCh)
			if err != nil {
				return syncProviderRequestError(err)
			}
			page, err := parseGoogleHealthRollupList(body)
			if err != nil {
				return err
			}
			for _, rawRollup := range page.rollups {
				rollup, err := parseGoogleHealthStepsDailyRollup(request.connection, rawRollup)
				if err != nil {
					return err
				}
				status, err := archive.UpsertRollup(rollup, ingestion.now().UTC().Format(time.RFC3339))
				if err != nil {
					return err
				}
				result.rollupsSeen++
				switch status {
				case "new":
					result.rollupsNew++
				case "updated":
					result.rollupsUpdated++
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

func (ingestion googleHealthIngestion) executeDataPointPages(archive googleHealthIngestionArchive, request googleHealthIngestionRequest, result *googleHealthIngestionResult) error {
	seenPageTokens := map[string]struct{}{}
	for pageToken := ""; ; {
		if ingestionCanceled(request.cancelCh) {
			return errSyncCanceled
		}
		rawRequest, err := buildGoogleHealthSyncDataPointRawRequest(request.dataType, request.from, request.to, request.sourceFamily, 0, pageToken)
		if err != nil {
			return err
		}
		body, err := ingestion.provider.Fetch(rawRequest, request.accessToken, request.cancelCh)
		if err != nil {
			return syncProviderRequestError(err)
		}
		page, err := parseGoogleHealthDataPointList(body)
		if err != nil {
			return err
		}
		for _, rawPoint := range page.dataPoints {
			point, err := parseGoogleHealthDataPoint(request.connection, request.dataType, rawPoint, request.sourceFamily)
			if err != nil {
				return err
			}
			status, err := archive.UpsertDataPoint(point, ingestion.now().UTC().Format(time.RFC3339))
			if err != nil {
				return err
			}
			result.dataPointsSeen++
			switch status {
			case "new":
				result.dataPointsNew++
			case "updated":
				result.dataPointsUpdated++
			}
		}
		if page.nextPageToken == "" {
			break
		}
		if _, ok := seenPageTokens[page.nextPageToken]; ok {
			return fmt.Errorf("Google Health %s %s returned a repeated page token", request.dataType, result.endpointFamily)
		}
		seenPageTokens[page.nextPageToken] = struct{}{}
		pageToken = page.nextPageToken
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

func parseGoogleHealthRollupList(body []byte) (googleHealthRollupList, error) {
	var raw struct {
		Rollups       []json.RawMessage `json:"rollupDataPoints"`
		NextPageToken string            `json:"nextPageToken"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return googleHealthRollupList{}, errors.New("Google Health Rollup response is not valid JSON")
	}
	return googleHealthRollupList{rollups: raw.Rollups, nextPageToken: raw.NextPageToken}, nil
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

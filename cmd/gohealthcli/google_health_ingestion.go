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
	// StoreAttachment is invoked by the TCX-ingestion hook for #107
	// slice D: after upserting an exercise Data Point, the ingestion
	// calls this with the just-upserted point + the bytes returned by
	// `users.dataTypes.dataPoints.exportExerciseTcx`. The archive impl
	// resolves the data_point row id from the point's identity columns
	// and writes the sidecar via the Attachment Store (ADR-0009).
	StoreAttachment(point archivedDataPoint, kind string, payload []byte, fetchedAt string) error
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
	rollupSpec     syncRollupSpec
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
	if !hasList && !hasReconcile {
		return googleHealthIngestionPlan{}, fmt.Errorf("sync Data Type %q is not supported yet", request.dataType)
	}
	if request.rollup != "" {
		spec, err := parseSyncRollupSpec(request.rollup)
		if err != nil {
			return googleHealthIngestionPlan{}, err
		}
		if err := validateSyncRollupAgainstDataType(spec, request.dataType); err != nil {
			return googleHealthIngestionPlan{}, err
		}
		return googleHealthIngestionPlan{endpointFamily: string(spec.endpointFamily), rollupSpec: spec}, nil
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
	switch plan.endpointFamily {
	case "dailyRollUp":
		err = ingestion.executeDailyRollupPages(archive, request, &result)
	case "rollUp":
		err = ingestion.executeWindowRollupPages(archive, request, plan.rollupSpec, &result)
	default:
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
				rollup, err := parseGoogleHealthRollup(request.connection, request.dataType, "dailyRollUp", rawRollup)
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
				return fmt.Errorf("Google Health %s dailyRollUp returned a repeated page token", request.dataType)
			}
			seenPageTokens[page.nextPageToken] = struct{}{}
			pageToken = page.nextPageToken
		}
	}
	return nil
}

// executeWindowRollupPages drives the windowed rollUp endpoint
// (hourly / weekly / window=<duration>). Unlike dailyRollUp the
// upstream takes an RFC3339 range and a windowSize Duration string,
// returns rollupDataPoints with RFC3339 startTime/endTime, and does
// not need the 90-day client-side window split that dailyRollUp does.
func (ingestion googleHealthIngestion) executeWindowRollupPages(archive googleHealthIngestionArchive, request googleHealthIngestionRequest, spec syncRollupSpec, result *googleHealthIngestionResult) error {
	windowSize := fmt.Sprintf("%ds", int64(spec.windowSize.Seconds()))
	seenPageTokens := map[string]struct{}{}
	for pageToken := ""; ; {
		if ingestionCanceled(request.cancelCh) {
			return errSyncCanceled
		}
		rawRequest, err := buildGoogleHealthRollupRawRequest(request.dataType, request.from, request.to, windowSize, 0, pageToken)
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
			rollup, err := parseGoogleHealthRollup(request.connection, request.dataType, spec.cursorKind, rawRollup)
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
			return fmt.Errorf("Google Health %s rollUp returned a repeated page token", request.dataType)
		}
		seenPageTokens[page.nextPageToken] = struct{}{}
		pageToken = page.nextPageToken
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
			now := ingestion.now().UTC().Format(time.RFC3339)
			status, err := archive.UpsertDataPoint(point, now)
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
			if err := ingestion.attachExerciseTcxIfAvailable(archive, request, point, now); err != nil {
				return err
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

// attachExerciseTcxIfAvailable is the #107 slice D hook: after the
// exercise Data Point is upserted, attempt the byte-shaped
// `users.dataTypes.dataPoints.exportExerciseTcx` export and Store the
// bytes as a `tcx`-kind Attachment (ADR-0009).
//
// Skipped cases — sync stays successful:
//   - data type is not "exercise" (TCX is exercise-only today).
//   - point.upstreamResourceName is empty (no `name` field on the list
//     page; nothing to address the export endpoint at).
//   - Upstream returned HTTP 404 (no TCX route for this Data Point — the
//     exercise might be manually entered or lack GPS/route data).
//   - Upstream returned HTTP 200 with an empty body (no bytes to archive).
//
// All other errors (5xx, transport failure, 401) are propagated so the
// Sync Cursor stays put and the user can retry.
func (ingestion googleHealthIngestion) attachExerciseTcxIfAvailable(archive googleHealthIngestionArchive, request googleHealthIngestionRequest, point archivedDataPoint, fetchedAt string) error {
	if request.dataType != "exercise" {
		return nil
	}
	if point.upstreamResourceName == "" {
		return nil
	}
	tcxRequest, err := buildGoogleHealthExportExerciseTcxRawRequest(point.upstreamResourceName)
	if err != nil {
		return err
	}
	body, err := ingestion.provider.Fetch(tcxRequest, request.accessToken, request.cancelCh)
	if err != nil {
		var httpErr *googleHealthHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			// No TCX available for this exercise — graceful skip.
			return nil
		}
		return syncProviderRequestError(err)
	}
	if len(body) == 0 {
		return nil
	}
	return archive.StoreAttachment(point, "tcx", body, fetchedAt)
}

// buildGoogleHealthRollupRawRequest builds the POST body for the
// windowed rollUp endpoint (hourly / weekly / window=<duration>).
// Google's rollUp endpoint takes an RFC3339 range plus a windowSize
// Duration string (e.g. "3600s") and returns rollupDataPoints with
// RFC3339 startTime/endTime. dailyRollUp is a separate endpoint
// (buildGoogleHealthDailyRollupRawRequest) because Google's API
// distinguishes them — they take different range shapes.
func buildGoogleHealthRollupRawRequest(dataType, from, to, windowSize string, pageSize int64, pageToken string) (rawProviderRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return rawProviderRequest{}, err
	}
	if from == "" {
		return rawProviderRequest{}, errors.New("windowed Rollup calls require --from")
	}
	if to == "" {
		return rawProviderRequest{}, errors.New("windowed Rollup calls require --to")
	}
	if windowSize == "" {
		return rawProviderRequest{}, errors.New("windowed Rollup calls require a windowSize")
	}
	rangeJSON, err := json.Marshal(struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	}{StartTime: from, EndTime: to})
	if err != nil {
		return rawProviderRequest{}, err
	}
	body := struct {
		Range      json.RawMessage `json:"range"`
		WindowSize string          `json:"windowSize"`
		PageSize   int64           `json:"pageSize,omitempty"`
		PageToken  string          `json:"pageToken,omitempty"`
	}{
		Range:      rangeJSON,
		WindowSize: windowSize,
		PageSize:   pageSize,
		PageToken:  pageToken,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return rawProviderRequest{}, err
	}
	return rawProviderRequest{
		endpointName:   "dataTypes." + dataType + ".rollUp",
		dataType:       dataType,
		method:         http.MethodPost,
		url:            googleHealthBaseURL + "/users/me/dataTypes/" + url.PathEscape(dataType) + "/dataPoints:rollUp",
		body:           bodyJSON,
		requiredScopes: googleHealthScopesForDataType(dataType),
	}, nil
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

// buildGoogleHealthExportExerciseTcxRawRequest constructs the GET
// request for `users.dataTypes.dataPoints.exportExerciseTcx` (ADR-0009,
// #107 slice D). Google's REST shape is `<base>/<name>:exportExerciseTcx`
// where `name` is the exercise Data Point's `upstream_resource_name`
// (e.g. `users/me/dataTypes/exercise/dataPoints/<id>`). The endpoint
// returns raw TCX XML bytes on success and HTTP 404 when no route is
// associated with the Data Point — both shapes are handled by the
// caller, which Stores success bytes as a `tcx`-kind Attachment and
// silently skips 404s.
func buildGoogleHealthExportExerciseTcxRawRequest(dataPointName string) (rawProviderRequest, error) {
	if dataPointName == "" {
		return rawProviderRequest{}, errors.New("exportExerciseTcx requires a non-empty exercise Data Point name")
	}
	// The data point resource name must match
	// `users/<user>/dataTypes/exercise/dataPoints/<id>` exactly. Reject
	// anything else so a steps or sleep name (or a malformed name with
	// extra path segments) can't accidentally be routed to the TCX
	// export endpoint.
	parts := strings.Split(dataPointName, "/")
	if len(parts) != 6 ||
		parts[0] != "users" || parts[1] == "" ||
		parts[2] != "dataTypes" || parts[3] != "exercise" ||
		parts[4] != "dataPoints" || parts[5] == "" {
		return rawProviderRequest{}, fmt.Errorf("exportExerciseTcx requires an exercise Data Point name, got %q", dataPointName)
	}
	return rawProviderRequest{
		endpointName:   "dataTypes.exercise.exportExerciseTcx",
		dataType:       "exercise",
		method:         http.MethodGet,
		url:            googleHealthBaseURL + "/" + dataPointName + ":exportExerciseTcx",
		requiredScopes: googleHealthScopesForDataType("exercise"),
	}, nil
}

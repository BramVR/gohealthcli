package googlehealth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/archived"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Archive interface {
	// The archive writes carry a context (#305), but the pagination
	// drivers deliberately pass a WithoutCancel-derived one: a page that
	// was already fetched is archived in full before cancellation lands
	// at the next loop boundary, so SIGINT never discards paid-for
	// upstream data (TestSyncOrchestratorCancelsActiveDataTypeMidPagination
	// pins this; upsert dedupe absorbs the overlap on resume).
	UpsertDataPoint(ctx context.Context, point archived.DataPoint, now string) (string, error)
	UpsertRollup(ctx context.Context, rollup archived.Rollup, now string) (string, error)
	// StoreAttachment is invoked by the TCX-ingestion hook for #107
	// slice D: after upserting an exercise Data Point, the ingestion
	// calls this with the just-upserted point + the bytes returned by
	// `users.dataTypes.dataPoints.exportExerciseTcx`. The archive impl
	// resolves the data_point row id from the point's identity columns
	// and writes the sidecar via the Attachment Store (ADR-0009).
	StoreAttachment(ctx context.Context, point archived.DataPoint, kind string, payload []byte, fetchedAt string) error
}

type ingestionProvider interface {
	// Fetch issues one request against the upstream provider. ctx scopes
	// the HTTP request itself (#284): a SIGINT-canceled context aborts
	// the in-flight call and short-circuits any retry backoff, surfacing
	// ErrSyncCanceled. context.Background() disables cancellation.
	Fetch(ctx context.Context, request RawRequest, accessToken string) ([]byte, error)
}

type Ingestion struct {
	provider ingestionProvider
	now      func() time.Time
}

// IngestionRequest carries one Sync Run's ingestion parameters from
// main's Sync Run lifecycle into Execute. Fields are exported because
// main populates them.
type IngestionRequest struct {
	Connection   archived.Connection
	DataType     string
	From         string
	To           string
	Rollup       string
	SourceFamily string
	AccessToken  string
	// grantedScopes mirrors the granted scope set on the stored
	// Connection token. Sync wires it from
	// `connectionTokenExpiryAndScopes(connection.tokenMetadataJSON)`
	// before calling Execute. Optional ingestion hooks (today: the
	// TCX archival in attachExerciseTcxIfAvailable, #140) gate on
	// this to skip endpoints whose scope was not granted, avoiding
	// a guaranteed-403 round-trip. The gate fails closed: a nil or
	// empty slice means the optional hook does NOT fire. Tests that
	// want to exercise the hook must inject the granting scope
	// explicitly.
	GrantedScopes []string
	// RefreshAccessToken, when set, lets a Sync Run survive access-token
	// expiry mid-run. Google access tokens live about an hour; a long
	// backfill's pagination can outlive one. When an upstream call
	// returns HTTP 401, ingestion calls this hook — sync wires it to the
	// same refresh-and-persist path the pre-run auto-refresh uses — and
	// retries the failed request once with the returned token. Later
	// requests in the same run keep using the refreshed token. nil
	// preserves the historical behavior: the first 401 fails the run.
	RefreshAccessToken func() (string, error)
	// Progress, when non-nil, is invoked at the TOP of every page
	// iteration — before the fetch — with the counts archived so far,
	// so the caller can persist a heartbeat on the sync_runs row
	// (#236). Heartbeating before the fetch (rather than after the
	// page's upserts) means a slow first page — large backfill, 429
	// retry backoff — still shows a live heartbeat from second zero,
	// so the abandoned-run fence cannot mis-flag a run that is merely
	// waiting on upstream. The callback owns its own error policy —
	// ingestion never fails a Sync Run because a progress write
	// misfired, which is why the hook takes no error return. nil
	// disables heartbeats (raw fetch paths and tests that predate #236).
	Progress func(result IngestionResult)
}

// reportIngestionProgress fires the optional pre-fetch progress hook.
// Shared by all three pagination drivers so the heartbeat semantics
// ("before every page fetch, carrying the counts so far") cannot
// drift between endpoint families.
func reportIngestionProgress(request IngestionRequest, result *IngestionResult) {
	if request.Progress == nil {
		return
	}
	request.Progress(*result)
}

// ErrSyncCanceled is the sentinel returned by ingestion when the run's
// context was canceled — between pages or mid-fetch (#284). Main's
// Sync Run lifecycle translates it into the canceled outcome, which
// leaves the Sync Cursor un-advanced (ADR-0008).
var ErrSyncCanceled = errors.New("Sync Run canceled")

// IngestionPlan names the endpoint family a request dispatches to.
// EndpointFamily is exported because main's Sync Run lifecycle records
// it on the sync_runs audit row and in the result envelope.
type IngestionPlan struct {
	EndpointFamily string
	rollupSpec     RollupSpec
}

// IngestionResult carries the per-run counts back to main, which
// folds them into the sync result envelope and the per-page heartbeat.
type IngestionResult struct {
	EndpointFamily    string
	DataPointsSeen    int
	DataPointsNew     int
	DataPointsUpdated int
	RollupsSeen       int
	RollupsNew        int
	RollupsUpdated    int
}

type googleHealthDateRange struct {
	from string
	to   string
}

type googleHealthRollupList struct {
	rollups       []json.RawMessage
	nextPageToken string
}

// NewIngestion builds the production-shaped ingestion over a
// single-attempt fetch and a clock. Main's runtime adapters bind fetch
// to their fetchRawProvider seam (production: FetchRaw over the shared
// timeout client; tests: a fake), and the package wraps it in the
// bounded retry middleware (retry.go) exactly as before the
// extraction. now must be non-nil; main passes the adapters' clock.
func NewIngestion(fetch func(ctx context.Context, request RawRequest, accessToken string) ([]byte, error), now func() time.Time) Ingestion {
	return Ingestion{
		provider: retryFetchProvider{fetch: fetch},
		now:      now,
	}
}

type retryFetchProvider struct {
	fetch googleHealthRetryFetcher
	// sleeper and jitter are test seams. Production leaves them nil and
	// fetchWithRetry falls back to sleepWithCancel + defaultRetryJitter.
	sleeper googleHealthRetrySleeper
	jitter  func(time.Duration) time.Duration
}

func (provider retryFetchProvider) Fetch(ctx context.Context, request RawRequest, accessToken string) ([]byte, error) {
	return fetchWithRetry(ctx, provider.fetch, provider.sleeper, provider.jitter, request, accessToken)
}

// midRunRefreshIngestionProvider wraps the ingestion provider when the
// request carries a refreshAccessToken hook (Execute installs it). On
// HTTP 401 it refreshes the access token once and retries the same
// request; the refreshed token then supersedes the request-captured
// token for every later Fetch in the run, because the pagination loops
// keep passing the token they captured before the refresh happened.
// Each Fetch gets at most one refresh+retry, so a Connection whose
// refreshed token is still rejected (revoked grant) fails the run on
// the retry's 401 instead of looping. A run long enough to outlive two
// access tokens simply refreshes again on the next 401.
type midRunRefreshIngestionProvider struct {
	inner          ingestionProvider
	refresh        func() (string, error)
	refreshedToken string
}

func (provider *midRunRefreshIngestionProvider) Fetch(ctx context.Context, request RawRequest, accessToken string) ([]byte, error) {
	if provider.refreshedToken != "" {
		accessToken = provider.refreshedToken
	}
	body, err := provider.inner.Fetch(ctx, request, accessToken)
	if err == nil || !isUnauthorizedHTTPError(err) {
		return body, err
	}
	// A signal that landed while the failing request was in flight wins
	// over the refresh: the next clean boundary is here, before spending
	// a token refresh + retry the user no longer wants.
	if ctx.Err() != nil {
		return nil, ErrSyncCanceled
	}
	refreshed, refreshErr := provider.refresh()
	if refreshErr != nil {
		return nil, refreshErr
	}
	provider.refreshedToken = refreshed
	return provider.inner.Fetch(ctx, request, refreshed)
}

func (ingestion Ingestion) Plan(request IngestionRequest) (IngestionPlan, error) {
	entry, ok := googleHealthDataTypes.Lookup(request.DataType)
	if !ok {
		return IngestionPlan{}, fmt.Errorf("sync Data Type %q is not supported yet", request.DataType)
	}
	_, hasList := entry.SupportedEndpoints[endpointFamilyList]
	_, hasReconcile := entry.SupportedEndpoints[endpointFamilyReconcile]
	if request.Rollup != "" {
		spec, err := ParseRollupSpec(request.Rollup)
		if err != nil {
			return IngestionPlan{}, err
		}
		if err := ValidateRollupAgainstDataType(spec, request.DataType); err != nil {
			return IngestionPlan{}, err
		}
		return IngestionPlan{EndpointFamily: string(spec.endpointFamily), rollupSpec: spec}, nil
	}
	if !hasList && !hasReconcile {
		return IngestionPlan{}, fmt.Errorf("sync Data Type %q is not supported yet", request.DataType)
	}
	if request.SourceFamily != "" {
		if !hasReconcile {
			return IngestionPlan{}, fmt.Errorf("sync Data Type %q does not support source-family filtering", request.DataType)
		}
		return IngestionPlan{EndpointFamily: "reconcile"}, nil
	}
	if !hasList {
		return IngestionPlan{}, fmt.Errorf("sync Data Type %q does not support default dataPoints.list sync; choose a supported mode; SupportedEndpoints=%s",
			request.DataType, formatSupportedEndpoints(entry.SupportedEndpoints))
	}
	return IngestionPlan{EndpointFamily: "list"}, nil
}

func (ingestion Ingestion) Execute(ctx context.Context, archive Archive, request IngestionRequest) (IngestionResult, error) {
	plan, err := ingestion.Plan(request)
	if err != nil {
		return IngestionResult{}, err
	}
	if request.RefreshAccessToken != nil {
		ingestion.provider = &midRunRefreshIngestionProvider{
			inner:   ingestion.provider,
			refresh: request.RefreshAccessToken,
		}
	}
	result := IngestionResult{EndpointFamily: plan.EndpointFamily}
	switch plan.EndpointFamily {
	case "dailyRollUp":
		err = ingestion.executeDailyRollupPages(ctx, archive, request, &result)
	case "rollUp":
		err = ingestion.executeWindowRollupPages(ctx, archive, request, plan.rollupSpec, &result)
	default:
		err = ingestion.executeDataPointPages(ctx, archive, request, &result)
	}
	return result, err
}

func (ingestion Ingestion) executeDailyRollupPages(ctx context.Context, archive Archive, request IngestionRequest, result *IngestionResult) error {
	windows, err := googleHealthDailyRollupDateWindows(request.From, request.To)
	if err != nil {
		return err
	}
	// archiveCtx: see Archive — page archiving is
	// not a cancellation point.
	archiveCtx := context.WithoutCancel(ctx)
	for _, window := range windows {
		seenPageTokens := map[string]struct{}{}
		for pageToken := ""; ; {
			if ctx.Err() != nil {
				return ErrSyncCanceled
			}
			reportIngestionProgress(request, result)
			rawRequest, err := buildGoogleHealthDailyRollupRawRequest(request.DataType, window.from, window.to, 0, pageToken)
			if err != nil {
				return err
			}
			body, err := ingestion.provider.Fetch(ctx, rawRequest, request.AccessToken)
			if err != nil {
				if ctx.Err() != nil {
					return ErrSyncCanceled
				}
				return NormalizeError(err)
			}
			page, err := parseGoogleHealthRollupList(body)
			if err != nil {
				return err
			}
			for _, rawRollup := range page.rollups {
				rollup, err := parseGoogleHealthRollup(request.Connection, request.DataType, "dailyRollUp", rawRollup)
				if err != nil {
					return err
				}
				status, err := archive.UpsertRollup(archiveCtx, rollup, ingestion.now().UTC().Format(time.RFC3339))
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
				return fmt.Errorf("Google Health %s dailyRollUp returned a repeated page token", request.DataType)
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
func (ingestion Ingestion) executeWindowRollupPages(ctx context.Context, archive Archive, request IngestionRequest, spec RollupSpec, result *IngestionResult) error {
	windowSize := fmt.Sprintf("%ds", int64(spec.windowSize.Seconds()))
	// archiveCtx: see Archive — page archiving is
	// not a cancellation point.
	archiveCtx := context.WithoutCancel(ctx)
	seenPageTokens := map[string]struct{}{}
	for pageToken := ""; ; {
		if ctx.Err() != nil {
			return ErrSyncCanceled
		}
		reportIngestionProgress(request, result)
		rawRequest, err := buildGoogleHealthRollupRawRequest(request.DataType, request.From, request.To, windowSize, 0, pageToken)
		if err != nil {
			return err
		}
		body, err := ingestion.provider.Fetch(ctx, rawRequest, request.AccessToken)
		if err != nil {
			if ctx.Err() != nil {
				return ErrSyncCanceled
			}
			return NormalizeError(err)
		}
		page, err := parseGoogleHealthRollupList(body)
		if err != nil {
			return err
		}
		for _, rawRollup := range page.rollups {
			rollup, err := parseGoogleHealthRollup(request.Connection, request.DataType, spec.cursorKind, rawRollup)
			if err != nil {
				return err
			}
			status, err := archive.UpsertRollup(archiveCtx, rollup, ingestion.now().UTC().Format(time.RFC3339))
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
			return fmt.Errorf("Google Health %s rollUp returned a repeated page token", request.DataType)
		}
		seenPageTokens[page.nextPageToken] = struct{}{}
		pageToken = page.nextPageToken
	}
	return nil
}

func (ingestion Ingestion) executeDataPointPages(ctx context.Context, archive Archive, request IngestionRequest, result *IngestionResult) error {
	// archiveCtx: see Archive — page archiving is
	// not a cancellation point.
	archiveCtx := context.WithoutCancel(ctx)
	seenPageTokens := map[string]struct{}{}
	for pageToken := ""; ; {
		if ctx.Err() != nil {
			return ErrSyncCanceled
		}
		reportIngestionProgress(request, result)
		rawRequest, err := buildGoogleHealthSyncDataPointRawRequest(request.DataType, request.From, request.To, request.SourceFamily, 0, pageToken)
		if err != nil {
			return err
		}
		body, err := ingestion.provider.Fetch(ctx, rawRequest, request.AccessToken)
		if err != nil {
			if ctx.Err() != nil {
				return ErrSyncCanceled
			}
			return NormalizeError(err)
		}
		page, err := parseGoogleHealthDataPointList(body)
		if err != nil {
			return err
		}
		for _, rawPoint := range page.dataPoints {
			point, err := parseGoogleHealthDataPoint(request.Connection, request.DataType, rawPoint, request.SourceFamily)
			if err != nil {
				return err
			}
			now := ingestion.now().UTC().Format(time.RFC3339)
			status, err := archive.UpsertDataPoint(archiveCtx, point, now)
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
			if err := ingestion.attachExerciseTcxIfAvailable(ctx, archive, request, point, now); err != nil {
				return err
			}
		}
		if page.nextPageToken == "" {
			break
		}
		if _, ok := seenPageTokens[page.nextPageToken]; ok {
			return fmt.Errorf("Google Health %s %s returned a repeated page token", request.DataType, result.EndpointFamily)
		}
		seenPageTokens[page.nextPageToken] = struct{}{}
		pageToken = page.nextPageToken
	}
	return nil
}

// grantedScopesAuthoriseTcxExport returns true when the stored
// Connection token grants `googlehealth.location.readonly` (#140).
// Google's `exportExerciseTcx` endpoint requires both
// `activity_and_fitness.readonly` (already granted by anyone hitting
// exercise sync — `requireConnectionScopes` enforces that earlier in
// the call path) and `location.readonly`. The TCX hook gates on the
// second so we don't burn an HTTP round-trip per exercise Data Point
// against an endpoint guaranteed to 403. An empty/nil scope slice
// returns false: the gate must fail closed, otherwise users without
// the scope would still see every exercise round-trip 403.
func grantedScopesAuthoriseTcxExport(grantedScopes []string) bool {
	for _, scope := range grantedScopes {
		if scope == ScopeLocationReadonly {
			return true
		}
	}
	return false
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
//   - Stored Connection token does not include the
//     `googlehealth.location.readonly` scope (#140). Google requires
//     both `activity_and_fitness.readonly` AND `location.readonly` on
//     the access token for `exportExerciseTcx` to succeed — without
//     the second scope every call returns 403. Users opt in via
//     `gohealthcli connect --add-scopes tcx`; until then skip the
//     hook entirely so sync doesn't waste an HTTP round-trip per
//     exercise Data Point. The gate fails closed: a nil/empty
//     grantedScopes slice means the hook is skipped (no TCX call),
//     so test fakes that don't care about TCX simply omit the field.
//   - Upstream returned HTTP 404 (no TCX route for this Data Point — the
//     exercise might be manually entered or lack GPS/route data).
//   - Upstream returned HTTP 403 (belt-and-suspenders against the
//     scope-gate above: the access token did not authorise TCX
//     export). Live-observed on accounts whose
//     `activity_and_fitness.readonly` scope alone does not extend to
//     exportExerciseTcx. The exercise Data Point itself is already
//     archived; tanking the whole sync because the optional TCX
//     sidecar is forbidden would be wrong.
//   - Upstream returned HTTP 200 with an empty body (no bytes to archive).
//
// All other errors (5xx, transport failure, 401) are propagated so the
// Sync Cursor stays put and the user can retry.
func (ingestion Ingestion) attachExerciseTcxIfAvailable(ctx context.Context, archive Archive, request IngestionRequest, point archived.DataPoint, fetchedAt string) error {
	if request.DataType != "exercise" {
		return nil
	}
	if point.UpstreamResourceName == "" {
		return nil
	}
	if !grantedScopesAuthoriseTcxExport(request.GrantedScopes) {
		return nil
	}
	tcxRequest, err := buildGoogleHealthExportExerciseTcxRawRequest(point.UpstreamResourceName)
	if err != nil {
		return err
	}
	body, err := ingestion.provider.Fetch(ctx, tcxRequest, request.AccessToken)
	if err != nil {
		if ctx.Err() != nil {
			return ErrSyncCanceled
		}
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusForbidden) {
			// 404: no TCX route for this exercise.
			// 403: granted scope does not authorize TCX export.
			// Either way the exercise Data Point itself is already
			// archived; the missing sidecar should not fail the sync.
			return nil
		}
		return NormalizeError(err)
	}
	if len(body) == 0 {
		return nil
	}
	// #187: Google's exportExerciseTcx wraps the XML in a JSON envelope
	// `{"tcxData": "<xml>"}`. Store the raw XML — opening a `.tcx`
	// sidecar in a TCX viewer must Just Work. If the response shape is
	// unexpected (not JSON, or no `tcxData` string field), fall back to
	// storing the bytes verbatim so a future shape change does not
	// silently drop the sidecar. The SHA is computed against whichever
	// bytes are passed to StoreAttachment, so a re-sync after the
	// envelope-era build produces a fresh row at a new content-addressed
	// path. Sync stays append-only — the old envelope row + sidecar are
	// left in place for `doctor`-driven cleanup (ADR-0009).
	payload, _ := unwrapExerciseTcxResponse(body)
	if len(payload) == 0 {
		// Envelope present but its `tcxData` was empty — re-fire the
		// empty-body guard against the unwrapped bytes so we don't
		// archive a zero-byte sidecar.
		return nil
	}
	// WithoutCancel: the sidecar write belongs to the already-fetched
	// page — see Archive.
	return archive.StoreAttachment(context.WithoutCancel(ctx), point, "tcx", payload, fetchedAt)
}

// unwrapExerciseTcxResponse extracts the raw TCX XML from Google's
// `exportExerciseTcx` JSON envelope. Returns (xml, true) — including
// an empty xml slice — when the body is a JSON object with a `tcxData`
// string field present (any value, empty or not). The caller treats
// the empty-tcxData case as a no-op via its empty-body guard. Returns
// (body, false) for any other shape — raw XML, an empty object, or a
// JSON value with `tcxData` of the wrong type — so the caller falls
// back to storing the response verbatim if Google's shape changes.
func unwrapExerciseTcxResponse(body []byte) ([]byte, bool) {
	var envelope struct {
		TcxData *string `json:"tcxData"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body, false
	}
	if envelope.TcxData == nil {
		return body, false
	}
	return []byte(*envelope.TcxData), true
}

// buildGoogleHealthRollupRawRequest builds the POST body for the
// windowed rollUp endpoint (hourly / weekly / window=<duration>).
// Google's rollUp endpoint takes an RFC3339 range plus a windowSize
// Duration string (e.g. "3600s") and returns rollupDataPoints with
// RFC3339 startTime/endTime. dailyRollUp is a separate endpoint
// (buildGoogleHealthDailyRollupRawRequest) because Google's API
// distinguishes them — they take different range shapes.
func buildGoogleHealthRollupRawRequest(dataType, from, to, windowSize string, pageSize int64, pageToken string) (RawRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return RawRequest{}, err
	}
	if from == "" {
		return RawRequest{}, errors.New("windowed Rollup calls require --from")
	}
	if to == "" {
		return RawRequest{}, errors.New("windowed Rollup calls require --to")
	}
	if windowSize == "" {
		return RawRequest{}, errors.New("windowed Rollup calls require a windowSize")
	}
	rangeJSON, err := json.Marshal(struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	}{StartTime: from, EndTime: to})
	if err != nil {
		return RawRequest{}, err
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
		return RawRequest{}, err
	}
	return RawRequest{
		EndpointName:   "dataTypes." + dataType + ".rollUp",
		DataType:       dataType,
		Method:         http.MethodPost,
		URL:            googleHealthBaseURL + "/users/me/dataTypes/" + url.PathEscape(dataType) + "/dataPoints:rollUp",
		Body:           bodyJSON,
		RequiredScopes: ScopesForDataType(dataType),
	}, nil
}

func buildGoogleHealthDailyRollupRawRequest(dataType, from, to string, pageSize int64, pageToken string) (RawRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return RawRequest{}, err
	}
	if !dailyRollupDataTypeSupported(dataType) {
		return RawRequest{}, fmt.Errorf("daily Rollup sync currently supports only Data Types %s", strings.Join(dailyRollupSupportedDataTypes(), ", "))
	}
	if from == "" {
		return RawRequest{}, errors.New("daily Rollup calls require --from")
	}
	rangeJSON, err := googleHealthCivilTimeIntervalJSON(from, to)
	if err != nil {
		return RawRequest{}, err
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
		return RawRequest{}, err
	}
	return RawRequest{
		EndpointName:   "dataTypes." + dataType + ".dailyRollUp",
		DataType:       dataType,
		Method:         http.MethodPost,
		URL:            googleHealthBaseURL + "/users/me/dataTypes/" + url.PathEscape(dataType) + "/dataPoints:dailyRollUp",
		Body:           bodyJSON,
		RequiredScopes: ScopesForDataType(dataType),
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

func buildGoogleHealthSyncDataPointRawRequest(dataType, from, to, sourceFamily string, pageSize int64, pageToken string) (RawRequest, error) {
	if sourceFamily == "" {
		return buildGoogleHealthDataTypeListRawRequest(dataType, from, to, pageSize, pageToken)
	}
	return buildGoogleHealthDataTypeReconcileRawRequest(dataType, from, to, sourceFamily, pageSize, pageToken)
}

func buildGoogleHealthDataTypeReconcileRawRequest(dataType, from, to, sourceFamily string, pageSize int64, pageToken string) (RawRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return RawRequest{}, err
	}
	if from == "" {
		return RawRequest{}, errors.New("Data Type reconcile raw calls require --from")
	}
	dataSourceFamily, err := SourceFamilyFilterName(dataType, sourceFamily)
	if err != nil {
		return RawRequest{}, err
	}
	query := url.Values{}
	filter, err := googleHealthDataTypeFilter(dataType, endpointFamilyReconcile, from, to)
	if err != nil {
		return RawRequest{}, err
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
	return RawRequest{
		EndpointName:       "dataTypes." + dataType + ".reconcile",
		DataType:           dataType,
		Method:             http.MethodGet,
		URL:                requestURL,
		RequiredScopes:     ScopesForDataType(dataType),
		SourceFamilyFilter: sourceFamily,
	}, nil
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
func buildGoogleHealthExportExerciseTcxRawRequest(dataPointName string) (RawRequest, error) {
	if dataPointName == "" {
		return RawRequest{}, errors.New("exportExerciseTcx requires a non-empty exercise Data Point name")
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
		return RawRequest{}, fmt.Errorf("exportExerciseTcx requires an exercise Data Point name, got %q", dataPointName)
	}
	// Rebuild the path from its validated segments, percent-encoding the
	// two provider-controlled free segments (the `<user>` and data point
	// `<id>`). The name comes from the upstream JSON `name` field, so a
	// crafted `?`/`#`/`%` would otherwise inject a query string, fragment,
	// or decoded `/` and shift the `:exportExerciseTcx` custom-method
	// suffix off the path. This mirrors the `url.PathEscape` convention
	// already used by the rollUp/dailyRollUp/reconcile builders.
	escapedName := strings.Join([]string{
		parts[0],
		url.PathEscape(parts[1]),
		parts[2],
		parts[3],
		parts[4],
		url.PathEscape(parts[5]),
	}, "/")
	return RawRequest{
		EndpointName:   "dataTypes.exercise.exportExerciseTcx",
		DataType:       "exercise",
		Method:         http.MethodGet,
		URL:            googleHealthBaseURL + "/" + escapedName + ":exportExerciseTcx",
		RequiredScopes: ScopesForDataType("exercise"),
	}, nil
}

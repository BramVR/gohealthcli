package googlehealth

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/archived"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGoogleHealthIngestionArchivesDataPointListFromProviderPages(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new", "updated"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/one",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:15:00Z"
					}
				}
			}],
			"nextPageToken": "next"
		}`,
		"next": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/two",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T09:00:00Z",
						"endTime": "2026-01-01T09:15:00Z"
					}
				}
			}]
		}`,
	})
	ingestion := fakeGoogleHealthIngestion(provider)

	result, err := ingestion.Execute(context.Background(), archive, fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		From:     "2026-01-01",
		To:       "2026-01-02T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("ingest Data Points: %v", err)
	}

	if result.EndpointFamily != "list" {
		t.Fatalf("endpoint family = %q, want list", result.EndpointFamily)
	}
	if result.DataPointsSeen != 2 || result.DataPointsNew != 1 || result.DataPointsUpdated != 1 {
		t.Fatalf("Data Point counts = (%d, %d, %d), want (2, 1, 1)", result.DataPointsSeen, result.DataPointsNew, result.DataPointsUpdated)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(provider.requests))
	}
	if provider.requests[0].EndpointName != "dataTypes.steps.list" || provider.requests[1].EndpointName != "dataTypes.steps.list" {
		t.Fatalf("requests = %#v, want steps list requests", provider.requests)
	}
	if archive.dataPoints[0].UpstreamResourceName != "users/me/dataTypes/steps/dataPoints/one" || archive.dataPoints[1].UpstreamResourceName != "users/me/dataTypes/steps/dataPoints/two" {
		t.Fatalf("archived resource names = (%q, %q), want fixture pages", archive.dataPoints[0].UpstreamResourceName, archive.dataPoints[1].UpstreamResourceName)
	}
}

func TestGoogleHealthIngestionChoosesReconcileFromSourceFamily(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/wearable",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:15:00Z"
					}
				}
			}]
		}`,
	})
	ingestion := fakeGoogleHealthIngestion(provider)

	result, err := ingestion.Execute(context.Background(), archive, fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType:     "steps",
		SourceFamily: "wearable",
		From:         "2026-01-01",
		To:           "2026-01-02T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("ingest reconcile Data Points: %v", err)
	}

	if result.EndpointFamily != "reconcile" {
		t.Fatalf("endpoint family = %q, want reconcile", result.EndpointFamily)
	}
	if len(provider.requests) != 1 || provider.requests[0].EndpointName != "dataTypes.steps.reconcile" {
		t.Fatalf("requests = %#v, want one reconcile request", provider.requests)
	}
	if got := mustURLQuery(t, provider.requests[0].URL).Get("dataSourceFamily"); got != googleHealthWearableSourceFamilyFilterName {
		t.Fatalf("dataSourceFamily = %q, want wearable family", got)
	}
	if archive.dataPoints[0].SourceFamilyFilter != "wearable" {
		t.Fatalf("archived source family = %q, want wearable", archive.dataPoints[0].SourceFamilyFilter)
	}
}

func TestGoogleHealthIngestionHeartbeatsDuringRetryStorm(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{}
	attempts := 0
	fetcher := func(context.Context, RawRequest, string) ([]byte, error) {
		attempts++
		if attempts < 3 {
			return nil, &HTTPError{StatusCode: 429, RetryAfter: 6 * time.Minute}
		}
		return []byte(`{"dataPoints":[]}`), nil
	}
	var sleeps []time.Duration
	ingestion := Ingestion{
		provider: retryFetchProvider{
			fetch:   fetcher,
			sleeper: recordingSleeper(&sleeps),
			jitter:  noopRetryJitter,
		},
		now: func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}
	progressCalls := 0
	request := fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		From:     "2026-01-01",
		To:       "2026-01-02T00:00:00Z",
		Progress: func(IngestionResult) {
			progressCalls++
		},
	})

	if _, err := ingestion.Execute(context.Background(), archive, request); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(sleeps) != 12 {
		t.Fatalf("sleeps = %v, want twelve 1m Retry-After chunks", sleeps)
	}
	for _, sleep := range sleeps {
		if sleep != time.Minute {
			t.Fatalf("sleeps = %v, want all chunks to be 1m", sleeps)
		}
	}
	if progressCalls != 15 {
		t.Fatalf("progress calls = %d, want 15 (page boundary, per-minute retry sleep heartbeats, before each retry attempt)", progressCalls)
	}
}

func TestGoogleHealthIngestionArchivesDailyRollups(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{rollupStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"2026-01-01/2026-01-02/": `{
			"rollupDataPoints": [{
				"steps": {"countSum": "1234"},
				"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
				"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
			}]
		}`,
	})
	ingestion := fakeGoogleHealthIngestion(provider)

	result, err := ingestion.Execute(context.Background(), archive, fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		Rollup:   "daily",
		From:     "2026-01-01",
		To:       "2026-01-02",
	}))
	if err != nil {
		t.Fatalf("ingest daily Rollups: %v", err)
	}

	if result.EndpointFamily != "dailyRollUp" {
		t.Fatalf("endpoint family = %q, want dailyRollUp", result.EndpointFamily)
	}
	if result.RollupsSeen != 1 || result.RollupsNew != 1 || result.RollupsUpdated != 0 {
		t.Fatalf("Rollup counts = (%d, %d, %d), want (1, 1, 0)", result.RollupsSeen, result.RollupsNew, result.RollupsUpdated)
	}
	if len(provider.requests) != 1 || provider.requests[0].Method != http.MethodPost || provider.requests[0].EndpointName != "dataTypes.steps.dailyRollUp" {
		t.Fatalf("requests = %#v, want one dailyRollUp POST", provider.requests)
	}
	if archive.rollups[0].RollupKind != "dailyRollUp" || archive.rollups[0].CivilDate != "2026-01-01" {
		t.Fatalf("archived Rollup = (%q, %q), want daily 2026-01-01", archive.rollups[0].RollupKind, archive.rollups[0].CivilDate)
	}
}

// TestDailyRollupGuardNamesCatalogSupportedDataTypes pins the
// unsupported-Data-Type guard on the dailyRollUp request builder to
// the catalog (#318): the error must name every Data Type whose
// catalog row grants the dailyRollUp endpoint family (today: steps,
// floors), so the message cannot drift when the catalog gains or
// loses a dailyRollUp Data Type. The guard is normally shadowed by
// the ValidateRollupAgainstDataType preflight in Plan, so the builder
// is exercised directly — the narrowest surface that reaches it.
func TestDailyRollupGuardNamesCatalogSupportedDataTypes(t *testing.T) {
	t.Parallel()
	var supported []string
	for _, dataType := range googleHealthDataTypes.order {
		if dailyRollupDataTypeSupported(dataType) {
			supported = append(supported, dataType)
		}
	}
	// Non-vacuity: the catalog grants dailyRollUp to more than one
	// Data Type, so a message naming a single Data Type must fail.
	if len(supported) < 2 {
		t.Fatalf("catalog dailyRollUp Data Types = %v, want at least steps and floors", supported)
	}

	_, err := buildGoogleHealthDailyRollupRawRequest("sleep", "2026-01-01", "2026-01-02", 0, "")
	if err == nil {
		t.Fatal("dailyRollUp request for sleep: want unsupported-Data-Type error, got nil")
	}
	for _, dataType := range supported {
		if !strings.Contains(err.Error(), dataType) {
			t.Errorf("daily Rollup guard error %q does not name catalog-supported Data Type %q", err.Error(), dataType)
		}
	}
}

func TestGoogleHealthIngestionRejectsRepeatedPageToken(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"":     `{"dataPoints":[],"nextPageToken":"same-token"}`,
		"same": `{"dataPoints":[]}`,
	})
	provider.pages["same-token"] = `{"dataPoints":[],"nextPageToken":"same-token"}`
	ingestion := fakeGoogleHealthIngestion(provider)

	_, err := ingestion.Execute(context.Background(), archive, fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		From:     "2026-01-01",
		To:       "2026-01-02T00:00:00Z",
	}))
	if err == nil || !strings.Contains(err.Error(), "Google Health steps list returned a repeated page token") {
		t.Fatalf("ingest error = %v, want repeated page token", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(provider.requests))
	}
}

type fakeGoogleHealthIngestionArchive struct {
	dataPointStatuses []string
	rollupStatuses    []string
	dataPoints        []archived.DataPoint
	rollups           []archived.Rollup
	attachments       []fakeIngestionAttachment
}

// fakeIngestionAttachment captures one StoreAttachment call so tests
// can assert the (point, kind, bytes) shape the ingestion handed off.
type fakeIngestionAttachment struct {
	point     archived.DataPoint
	kind      string
	payload   []byte
	fetchedAt string
}

func (archive *fakeGoogleHealthIngestionArchive) UpsertDataPoint(ctx context.Context, point archived.DataPoint, now string) (string, error) {
	archive.dataPoints = append(archive.dataPoints, point)
	if len(archive.dataPointStatuses) == 0 {
		return "new", nil
	}
	status := archive.dataPointStatuses[0]
	archive.dataPointStatuses = archive.dataPointStatuses[1:]
	return status, nil
}

func (archive *fakeGoogleHealthIngestionArchive) UpsertRollup(ctx context.Context, rollup archived.Rollup, now string) (string, error) {
	archive.rollups = append(archive.rollups, rollup)
	if len(archive.rollupStatuses) == 0 {
		return "new", nil
	}
	status := archive.rollupStatuses[0]
	archive.rollupStatuses = archive.rollupStatuses[1:]
	return status, nil
}

// StoreAttachment records the call so TCX-ingestion tests can assert
// the wiring: which Data Point the bytes attach to, what kind, the
// payload, and the fetchedAt stamp.
func (archive *fakeGoogleHealthIngestionArchive) StoreAttachment(ctx context.Context, point archived.DataPoint, kind string, payload []byte, fetchedAt string) error {
	archive.attachments = append(archive.attachments, fakeIngestionAttachment{
		point:     point,
		kind:      kind,
		payload:   append([]byte(nil), payload...),
		fetchedAt: fetchedAt,
	})
	return nil
}

type fakeGoogleHealthIngestionProvider struct {
	t               *testing.T
	wantAccessToken string
	pages           map[string]string
	requests        []RawRequest
	// errorByPageKey overrides the page body with an error response keyed
	// by the same page key the fixture map uses. Lets TCX tests force a
	// 404 or transport error on a specific resource without leaking error
	// shape into unrelated tests.
	errorByPageKey map[string]error
}

func newFakeGoogleHealthIngestionProvider(t *testing.T, wantAccessToken string, pages map[string]string) *fakeGoogleHealthIngestionProvider {
	t.Helper()
	return &fakeGoogleHealthIngestionProvider{t: t, wantAccessToken: wantAccessToken, pages: pages}
}

func (provider *fakeGoogleHealthIngestionProvider) Fetch(_ context.Context, request RawRequest, accessToken string) ([]byte, error) {
	provider.t.Helper()
	if accessToken != provider.wantAccessToken {
		provider.t.Fatalf("access token = %q, want fixture token", accessToken)
	}
	provider.requests = append(provider.requests, request)
	key := provider.pageKey(request)
	if err, ok := provider.errorByPageKey[key]; ok {
		return nil, err
	}
	body, ok := provider.pages[key]
	if !ok {
		provider.t.Fatalf("no fake provider page for key %q", key)
	}
	return []byte(body), nil
}

func (provider *fakeGoogleHealthIngestionProvider) pageKey(request RawRequest) string {
	provider.t.Helper()
	if strings.HasSuffix(request.EndpointName, ".dailyRollUp") {
		var body struct {
			Range struct {
				Start struct {
					Date struct {
						Year  int `json:"year"`
						Month int `json:"month"`
						Day   int `json:"day"`
					} `json:"date"`
				} `json:"start"`
				End struct {
					Date struct {
						Year  int `json:"year"`
						Month int `json:"month"`
						Day   int `json:"day"`
					} `json:"date"`
				} `json:"end"`
			} `json:"range"`
			PageToken string `json:"pageToken"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			provider.t.Fatalf("rollup body JSON: %v", err)
		}
		return fmt.Sprintf("%04d-%02d-%02d/%04d-%02d-%02d/%s",
			body.Range.Start.Date.Year,
			body.Range.Start.Date.Month,
			body.Range.Start.Date.Day,
			body.Range.End.Date.Year,
			body.Range.End.Date.Month,
			body.Range.End.Date.Day,
			body.PageToken,
		)
	}
	if strings.HasSuffix(request.EndpointName, ".rollUp") {
		var body struct {
			Range struct {
				StartTime string `json:"startTime"`
				EndTime   string `json:"endTime"`
			} `json:"range"`
			WindowSize string `json:"windowSize"`
			PageToken  string `json:"pageToken"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			provider.t.Fatalf("rollUp body JSON: %v", err)
		}
		return fmt.Sprintf("%s/%s/%s/%s",
			body.Range.StartTime,
			body.Range.EndTime,
			body.WindowSize,
			body.PageToken,
		)
	}
	if strings.HasSuffix(request.EndpointName, ".exportExerciseTcx") {
		// TCX export keys on the data point resource path (the suffix of
		// the URL after the base prefix). e.g.
		// "users/me/dataTypes/exercise/dataPoints/run-1:exportExerciseTcx".
		return strings.TrimPrefix(request.URL, googleHealthBaseURL+"/")
	}
	parsedURL, err := url.Parse(request.URL)
	if err != nil {
		provider.t.Fatalf("request URL: %v", err)
	}
	return parsedURL.Query().Get("pageToken")
}

func fakeGoogleHealthIngestion(provider *fakeGoogleHealthIngestionProvider) Ingestion {
	return Ingestion{
		provider: provider,
		now:      func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}
}

func fakeGoogleHealthIngestionRequest(request IngestionRequest) IngestionRequest {
	request.Connection = archived.Connection{
		ID:                 "conn_123",
		ProviderName:       "google_health",
		GoogleHealthUserID: "111111256096816351",
	}
	request.AccessToken = "access-secret"
	return request
}

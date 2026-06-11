package main

import (
	"encoding/json"
	"fmt"
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

	result, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		from:     "2026-01-01",
		to:       "2026-01-02T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("ingest Data Points: %v", err)
	}

	if result.endpointFamily != "list" {
		t.Fatalf("endpoint family = %q, want list", result.endpointFamily)
	}
	if result.dataPointsSeen != 2 || result.dataPointsNew != 1 || result.dataPointsUpdated != 1 {
		t.Fatalf("Data Point counts = (%d, %d, %d), want (2, 1, 1)", result.dataPointsSeen, result.dataPointsNew, result.dataPointsUpdated)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(provider.requests))
	}
	if provider.requests[0].endpointName != "dataTypes.steps.list" || provider.requests[1].endpointName != "dataTypes.steps.list" {
		t.Fatalf("requests = %#v, want steps list requests", provider.requests)
	}
	if archive.dataPoints[0].upstreamResourceName != "users/me/dataTypes/steps/dataPoints/one" || archive.dataPoints[1].upstreamResourceName != "users/me/dataTypes/steps/dataPoints/two" {
		t.Fatalf("archived resource names = (%q, %q), want fixture pages", archive.dataPoints[0].upstreamResourceName, archive.dataPoints[1].upstreamResourceName)
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

	result, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType:     "steps",
		sourceFamily: "wearable",
		from:         "2026-01-01",
		to:           "2026-01-02T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("ingest reconcile Data Points: %v", err)
	}

	if result.endpointFamily != "reconcile" {
		t.Fatalf("endpoint family = %q, want reconcile", result.endpointFamily)
	}
	if len(provider.requests) != 1 || provider.requests[0].endpointName != "dataTypes.steps.reconcile" {
		t.Fatalf("requests = %#v, want one reconcile request", provider.requests)
	}
	if got := mustURLQuery(t, provider.requests[0].url).Get("dataSourceFamily"); got != googleHealthWearableSourceFamilyFilterName {
		t.Fatalf("dataSourceFamily = %q, want wearable family", got)
	}
	if archive.dataPoints[0].sourceFamilyFilter != "wearable" {
		t.Fatalf("archived source family = %q, want wearable", archive.dataPoints[0].sourceFamilyFilter)
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

	result, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		rollup:   "daily",
		from:     "2026-01-01",
		to:       "2026-01-02",
	}))
	if err != nil {
		t.Fatalf("ingest daily Rollups: %v", err)
	}

	if result.endpointFamily != "dailyRollUp" {
		t.Fatalf("endpoint family = %q, want dailyRollUp", result.endpointFamily)
	}
	if result.rollupsSeen != 1 || result.rollupsNew != 1 || result.rollupsUpdated != 0 {
		t.Fatalf("Rollup counts = (%d, %d, %d), want (1, 1, 0)", result.rollupsSeen, result.rollupsNew, result.rollupsUpdated)
	}
	if len(provider.requests) != 1 || provider.requests[0].method != http.MethodPost || provider.requests[0].endpointName != "dataTypes.steps.dailyRollUp" {
		t.Fatalf("requests = %#v, want one dailyRollUp POST", provider.requests)
	}
	if archive.rollups[0].rollupKind != "dailyRollUp" || archive.rollups[0].civilDate != "2026-01-01" {
		t.Fatalf("archived Rollup = (%q, %q), want daily 2026-01-01", archive.rollups[0].rollupKind, archive.rollups[0].civilDate)
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

	_, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		from:     "2026-01-01",
		to:       "2026-01-02T00:00:00Z",
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
	dataPoints        []archivedDataPoint
	rollups           []archivedRollup
	attachments       []fakeIngestionAttachment
}

// fakeIngestionAttachment captures one StoreAttachment call so tests
// can assert the (point, kind, bytes) shape the ingestion handed off.
type fakeIngestionAttachment struct {
	point     archivedDataPoint
	kind      string
	payload   []byte
	fetchedAt string
}

func (archive *fakeGoogleHealthIngestionArchive) UpsertDataPoint(point archivedDataPoint, now string) (string, error) {
	archive.dataPoints = append(archive.dataPoints, point)
	if len(archive.dataPointStatuses) == 0 {
		return "new", nil
	}
	status := archive.dataPointStatuses[0]
	archive.dataPointStatuses = archive.dataPointStatuses[1:]
	return status, nil
}

func (archive *fakeGoogleHealthIngestionArchive) UpsertRollup(rollup archivedRollup, now string) (string, error) {
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
func (archive *fakeGoogleHealthIngestionArchive) StoreAttachment(point archivedDataPoint, kind string, payload []byte, fetchedAt string) error {
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
	requests        []rawProviderRequest
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

func (provider *fakeGoogleHealthIngestionProvider) Fetch(request rawProviderRequest, accessToken string, _ <-chan struct{}) ([]byte, error) {
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

func (provider *fakeGoogleHealthIngestionProvider) pageKey(request rawProviderRequest) string {
	provider.t.Helper()
	if strings.HasSuffix(request.endpointName, ".dailyRollUp") {
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
		if err := json.Unmarshal(request.body, &body); err != nil {
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
	if strings.HasSuffix(request.endpointName, ".rollUp") {
		var body struct {
			Range struct {
				StartTime string `json:"startTime"`
				EndTime   string `json:"endTime"`
			} `json:"range"`
			WindowSize string `json:"windowSize"`
			PageToken  string `json:"pageToken"`
		}
		if err := json.Unmarshal(request.body, &body); err != nil {
			provider.t.Fatalf("rollUp body JSON: %v", err)
		}
		return fmt.Sprintf("%s/%s/%s/%s",
			body.Range.StartTime,
			body.Range.EndTime,
			body.WindowSize,
			body.PageToken,
		)
	}
	if strings.HasSuffix(request.endpointName, ".exportExerciseTcx") {
		// TCX export keys on the data point resource path (the suffix of
		// the URL after the base prefix). e.g.
		// "users/me/dataTypes/exercise/dataPoints/run-1:exportExerciseTcx".
		return strings.TrimPrefix(request.url, googleHealthBaseURL+"/")
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		provider.t.Fatalf("request URL: %v", err)
	}
	return parsedURL.Query().Get("pageToken")
}

func fakeGoogleHealthIngestion(provider *fakeGoogleHealthIngestionProvider) googleHealthIngestion {
	return googleHealthIngestion{
		provider: provider,
		now:      func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}
}

func fakeGoogleHealthIngestionRequest(request googleHealthIngestionRequest) googleHealthIngestionRequest {
	request.connection = archivedConnection{
		id:                 "conn_123",
		providerName:       "google_health",
		googleHealthUserID: "111111256096816351",
	}
	request.accessToken = "access-secret"
	return request
}

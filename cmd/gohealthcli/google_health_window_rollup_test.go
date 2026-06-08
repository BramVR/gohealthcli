package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestGoogleHealthIngestionHourlyHeartRateRollup pins #106 Slice 3:
// the ingestion executor dispatches `sync --types heart-rate --rollup
// hourly` to the windowed rollUp endpoint, posts the right body, and
// archives one row per upstream rollupDataPoint.
func TestGoogleHealthIngestionHourlyHeartRateRollup(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{rollupStatuses: []string{"new", "new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"2026-01-01T00:00:00Z/2026-01-01T02:00:00Z/3600s/": `{
			"rollupDataPoints": [{
				"heartRate": {"bpmAvg": 72.5, "bpmMin": 55.0, "bpmMax": 110.0},
				"startTime": "2026-01-01T00:00:00Z",
				"endTime": "2026-01-01T01:00:00Z"
			}, {
				"heartRate": {"bpmAvg": 65.0, "bpmMin": 50.0, "bpmMax": 90.0},
				"startTime": "2026-01-01T01:00:00Z",
				"endTime": "2026-01-01T02:00:00Z"
			}]
		}`,
	})
	ingestion := fakeGoogleHealthIngestion(provider)

	result, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "heart-rate",
		rollup:   "hourly",
		from:     "2026-01-01T00:00:00Z",
		to:       "2026-01-01T02:00:00Z",
	}))
	if err != nil {
		t.Fatalf("ingest hourly heart-rate Rollups: %v", err)
	}
	if result.endpointFamily != "rollUp" {
		t.Errorf("endpoint family = %q, want rollUp", result.endpointFamily)
	}
	if result.rollupsSeen != 2 || result.rollupsNew != 2 {
		t.Errorf("Rollup counts = (%d new of %d seen), want (2, 2)", result.rollupsNew, result.rollupsSeen)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(provider.requests))
	}
	if provider.requests[0].endpointName != "dataTypes.heart-rate.rollUp" {
		t.Errorf("endpointName = %q, want dataTypes.heart-rate.rollUp", provider.requests[0].endpointName)
	}
	if provider.requests[0].method != http.MethodPost {
		t.Errorf("method = %q, want POST", provider.requests[0].method)
	}
	if archive.rollups[0].rollupKind != "hourly" {
		t.Errorf("rollupKind = %q, want hourly", archive.rollups[0].rollupKind)
	}
	if archive.rollups[0].windowStartUTC != "2026-01-01T00:00:00Z" {
		t.Errorf("windowStartUTC = %q, want 2026-01-01T00:00:00Z", archive.rollups[0].windowStartUTC)
	}
}

// TestGoogleHealthIngestionWeeklyStepsRollup verifies #106's "weekly
// window math" tests. The body carries windowSize=604800s and the
// archived rows reflect the heartRate-shape catalog dispatch (in this
// case stepsCount → windowStartUTC/windowEndUTC for the windowed
// rollUp shape).
func TestGoogleHealthIngestionWeeklyStepsRollup(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{rollupStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"2026-01-01T00:00:00Z/2026-01-15T00:00:00Z/604800s/": `{
			"rollupDataPoints": [{
				"steps": {"countSum": "70000"},
				"startTime": "2026-01-01T00:00:00Z",
				"endTime": "2026-01-08T00:00:00Z"
			}]
		}`,
	})
	ingestion := fakeGoogleHealthIngestion(provider)

	result, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		rollup:   "weekly",
		from:     "2026-01-01T00:00:00Z",
		to:       "2026-01-15T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("ingest weekly steps Rollups: %v", err)
	}
	if result.endpointFamily != "rollUp" {
		t.Errorf("endpoint family = %q, want rollUp", result.endpointFamily)
	}
	if result.rollupsSeen != 1 || result.rollupsNew != 1 {
		t.Errorf("Rollup counts = (%d new of %d seen), want (1, 1)", result.rollupsNew, result.rollupsSeen)
	}
	if archive.rollups[0].rollupKind != "weekly" {
		t.Errorf("rollupKind = %q, want weekly", archive.rollups[0].rollupKind)
	}
}

// TestGoogleHealthIngestionWindowCustomRollup pins the "custom
// `window=Nh`" AC. windowSize is set straight from the spec parser
// output and the cursor kind survives end-to-end onto the row.
func TestGoogleHealthIngestionWindowCustomRollup(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{rollupStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"2026-01-01T00:00:00Z/2026-01-01T12:00:00Z/21600s/": `{
			"rollupDataPoints": [{
				"steps": {"countSum": "8500"},
				"startTime": "2026-01-01T00:00:00Z",
				"endTime": "2026-01-01T06:00:00Z"
			}]
		}`,
	})
	ingestion := fakeGoogleHealthIngestion(provider)

	result, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		rollup:   "window=6h",
		from:     "2026-01-01T00:00:00Z",
		to:       "2026-01-01T12:00:00Z",
	}))
	if err != nil {
		t.Fatalf("ingest window=6h steps Rollups: %v", err)
	}
	if result.endpointFamily != "rollUp" {
		t.Errorf("endpoint family = %q, want rollUp", result.endpointFamily)
	}
	if archive.rollups[0].rollupKind != "window=6h" {
		t.Errorf("rollupKind = %q, want window=6h", archive.rollups[0].rollupKind)
	}
}

// TestGoogleHealthIngestionRollupRejectsUnsupportedDataType pins the
// "Unsupported combinations error with the Data Type's actual
// SupportedEndpoints quoted in the error message" AC at the planner
// seam: sleep has no rollUp family and the error must quote
// SupportedEndpoints verbatim.
func TestGoogleHealthIngestionRollupRejectsUnsupportedDataType(t *testing.T) {
	ingestion := fakeGoogleHealthIngestion(newFakeGoogleHealthIngestionProvider(t, "access-secret", nil))
	_, err := ingestion.Plan(googleHealthIngestionRequest{
		dataType: "sleep",
		rollup:   "hourly",
		from:     "2026-01-01T00:00:00Z",
		to:       "2026-01-02T00:00:00Z",
	})
	if err == nil {
		t.Fatal("Plan sleep+hourly: want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "sleep") || !strings.Contains(msg, "hourly") {
		t.Errorf("err = %q, want sleep and hourly mentions", msg)
	}
	if !strings.Contains(msg, "SupportedEndpoints") || !strings.Contains(msg, "list") {
		t.Errorf("err = %q, want SupportedEndpoints + 'list' (sleep's actual family)", msg)
	}
}

// pageKey is extended on the fake provider to recognise rollUp
// requests; this helper proves the dispatch by re-deriving the same
// shape the executor sends.
func TestGoogleHealthRollupRequestBodyCarriesWindowSize(t *testing.T) {
	request, err := buildGoogleHealthRollupRawRequest("heart-rate", "2026-01-01T00:00:00Z", "2026-01-01T02:00:00Z", "3600s", 0, "")
	if err != nil {
		t.Fatalf("buildGoogleHealthRollupRawRequest: %v", err)
	}
	if request.endpointName != "dataTypes.heart-rate.rollUp" {
		t.Errorf("endpointName = %q, want dataTypes.heart-rate.rollUp", request.endpointName)
	}
	parsed, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	wantPath := "/v4/users/me/dataTypes/heart-rate/dataPoints:rollUp"
	if parsed.Path != wantPath {
		t.Errorf("path = %q, want %q", parsed.Path, wantPath)
	}
	var body struct {
		Range struct {
			StartTime string `json:"startTime"`
			EndTime   string `json:"endTime"`
		} `json:"range"`
		WindowSize string `json:"windowSize"`
	}
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatalf("body unmarshal: %v\nbody: %s", err, string(request.body))
	}
	if body.WindowSize != "3600s" {
		t.Errorf("windowSize = %q, want 3600s", body.WindowSize)
	}
	if body.Range.StartTime != "2026-01-01T00:00:00Z" {
		t.Errorf("range.startTime = %q, want 2026-01-01T00:00:00Z", body.Range.StartTime)
	}
	if body.Range.EndTime != "2026-01-01T02:00:00Z" {
		t.Errorf("range.endTime = %q, want 2026-01-01T02:00:00Z", body.Range.EndTime)
	}
}


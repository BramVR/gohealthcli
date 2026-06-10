package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// funcIngestionProvider adapts a bare function to the
// googleHealthIngestionProvider interface so mid-run-refresh tests can
// script per-call behavior (different bodies/errors per access token)
// without growing the shared fixture provider.
type funcIngestionProvider func(request rawProviderRequest, accessToken string) ([]byte, error)

func (fetch funcIngestionProvider) Fetch(request rawProviderRequest, accessToken string, _ <-chan struct{}) ([]byte, error) {
	return fetch(request, accessToken)
}

func midRunRefreshTestIngestion(t *testing.T, provider googleHealthIngestionProvider) googleHealthIngestion {
	t.Helper()
	return googleHealthIngestion{
		provider: provider,
		now:      func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}
}

func TestGoogleHealthIngestionRefreshesAccessTokenMidRunOn401(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{}
	pages := map[string]string{
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
			"nextPageToken": "page-2"
		}`,
		"page-2": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/two",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T09:00:00Z",
						"endTime": "2026-01-01T09:15:00Z"
					}
				}
			}],
			"nextPageToken": "page-3"
		}`,
		"page-3": `{"dataPoints":[]}`,
	}
	var fetches []string
	provider := funcIngestionProvider(func(request rawProviderRequest, accessToken string) ([]byte, error) {
		pageToken := mustURLQuery(t, request.url).Get("pageToken")
		fetches = append(fetches, pageToken+":"+accessToken)
		switch accessToken {
		case "stale-access":
			if pageToken == "" {
				return []byte(pages[""]), nil
			}
			// The token expired between page 1 and page 2.
			return nil, &googleHealthHTTPError{StatusCode: 401}
		case "fresh-access":
			body, ok := pages[pageToken]
			if !ok {
				t.Fatalf("no fake page for pageToken %q", pageToken)
			}
			return []byte(body), nil
		default:
			t.Fatalf("unexpected access token %q", accessToken)
			return nil, nil
		}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)
	request := fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		from:     "2026-01-01",
		to:       "2026-01-02T00:00:00Z",
	})
	request.accessToken = "stale-access"
	refreshCalls := 0
	request.refreshAccessToken = func() (string, error) {
		refreshCalls++
		return "fresh-access", nil
	}

	result, err := ingestion.Execute(archive, request)
	if err != nil {
		t.Fatalf("ingest Data Points across token expiry: %v", err)
	}

	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	if result.dataPointsSeen != 2 || result.dataPointsNew != 2 {
		t.Fatalf("Data Point counts = (%d, %d), want (2, 2)", result.dataPointsSeen, result.dataPointsNew)
	}
	// Page 2 is fetched twice (401 then retry); pages after the refresh
	// must keep using the refreshed token without re-fetching page 1.
	wantFetches := []string{":stale-access", "page-2:stale-access", "page-2:fresh-access", "page-3:fresh-access"}
	if strings.Join(fetches, ",") != strings.Join(wantFetches, ",") {
		t.Fatalf("fetches = %v, want %v", fetches, wantFetches)
	}
}

func TestGoogleHealthIngestionFailsWhenRefreshedTokenStillUnauthorized(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{}
	fetchCalls := 0
	provider := funcIngestionProvider(func(request rawProviderRequest, accessToken string) ([]byte, error) {
		fetchCalls++
		return nil, &googleHealthHTTPError{StatusCode: 401}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)
	request := fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		from:     "2026-01-01",
		to:       "2026-01-02T00:00:00Z",
	})
	refreshCalls := 0
	request.refreshAccessToken = func() (string, error) {
		refreshCalls++
		return "fresh-access", nil
	}

	_, err := ingestion.Execute(archive, request)
	if err == nil || !strings.Contains(err.Error(), "Google Health rejected stored Connection token") {
		t.Fatalf("ingest error = %v, want rejected-token failure", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want exactly 1 (no refresh loop)", refreshCalls)
	}
	if fetchCalls != 2 {
		t.Fatalf("fetch calls = %d, want 2 (original + one retry)", fetchCalls)
	}
}

func TestGoogleHealthIngestionSurfacesMidRunRefreshFailure(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{}
	fetchCalls := 0
	provider := funcIngestionProvider(func(request rawProviderRequest, accessToken string) ([]byte, error) {
		fetchCalls++
		return nil, &googleHealthHTTPError{StatusCode: 401}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)
	request := fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		from:     "2026-01-01",
		to:       "2026-01-02T00:00:00Z",
	})
	refreshErr := errors.New("auto-refresh of Connection access token failed: refresh token revoked")
	request.refreshAccessToken = func() (string, error) {
		return "", refreshErr
	}

	_, err := ingestion.Execute(archive, request)
	if !errors.Is(err, refreshErr) {
		t.Fatalf("ingest error = %v, want the refresh failure", err)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetch calls = %d, want 1 (no retry after failed refresh)", fetchCalls)
	}
}

func TestGoogleHealthIngestionWithoutRefreshHookSurfaces401(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{}
	fetchCalls := 0
	provider := funcIngestionProvider(func(request rawProviderRequest, accessToken string) ([]byte, error) {
		fetchCalls++
		return nil, &googleHealthHTTPError{StatusCode: 401}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)
	request := fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		from:     "2026-01-01",
		to:       "2026-01-02T00:00:00Z",
	})

	_, err := ingestion.Execute(archive, request)
	if err == nil || !strings.Contains(err.Error(), "Google Health rejected stored Connection token") {
		t.Fatalf("ingest error = %v, want rejected-token failure", err)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetch calls = %d, want 1 (no refresh hook, no retry)", fetchCalls)
	}
}

func TestGoogleHealthIngestionRefreshesAccessTokenMidRunForRollups(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{}
	var fetchTokens []string
	provider := funcIngestionProvider(func(request rawProviderRequest, accessToken string) ([]byte, error) {
		fetchTokens = append(fetchTokens, accessToken)
		if accessToken != "fresh-access" {
			return nil, &googleHealthHTTPError{StatusCode: 401}
		}
		return []byte(`{
			"rollupDataPoints": [{
				"steps": {"countSum": "1234"},
				"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
				"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
			}]
		}`), nil
	})
	ingestion := midRunRefreshTestIngestion(t, provider)
	request := fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		rollup:   "daily",
		from:     "2026-01-01",
		to:       "2026-01-02",
	})
	request.accessToken = "stale-access"
	request.refreshAccessToken = func() (string, error) {
		return "fresh-access", nil
	}

	result, err := ingestion.Execute(archive, request)
	if err != nil {
		t.Fatalf("ingest daily Rollups across token expiry: %v", err)
	}
	if result.rollupsSeen != 1 || result.rollupsNew != 1 {
		t.Fatalf("Rollup counts = (%d, %d), want (1, 1)", result.rollupsSeen, result.rollupsNew)
	}
	if strings.Join(fetchTokens, ",") != "stale-access,fresh-access" {
		t.Fatalf("fetch tokens = %v, want stale then fresh", fetchTokens)
	}
}

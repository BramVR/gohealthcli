package googlehealth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// funcIngestionProvider adapts a bare function to the
// ingestionProvider interface so mid-run-refresh tests can
// script per-call behavior (different bodies/errors per access token)
// without growing the shared fixture provider.
type funcIngestionProvider func(request RawRequest, accessToken string) ([]byte, error)

func (fetch funcIngestionProvider) Fetch(_ context.Context, request RawRequest, accessToken string) ([]byte, error) {
	return fetch(request, accessToken)
}

func midRunRefreshTestIngestion(t *testing.T, provider ingestionProvider) Ingestion {
	t.Helper()
	return Ingestion{
		provider: provider,
		now:      func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}
}

func TestGoogleHealthIngestionRefreshesAccessTokenMidRunOn401(t *testing.T) {
	t.Parallel()
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
	provider := funcIngestionProvider(func(request RawRequest, accessToken string) ([]byte, error) {
		pageToken := mustURLQuery(t, request.URL).Get("pageToken")
		fetches = append(fetches, pageToken+":"+accessToken)
		switch accessToken {
		case "stale-access":
			if pageToken == "" {
				return []byte(pages[""]), nil
			}
			// The token expired between page 1 and page 2.
			return nil, &HTTPError{StatusCode: 401}
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
	request := fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		From:     "2026-01-01",
		To:       "2026-01-02T00:00:00Z",
	})
	request.AccessToken = "stale-access"
	refreshCalls := 0
	request.RefreshAccessToken = func() (string, error) {
		refreshCalls++
		return "fresh-access", nil
	}

	result, err := ingestion.Execute(context.Background(), archive, request)
	if err != nil {
		t.Fatalf("ingest Data Points across token expiry: %v", err)
	}

	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	if result.DataPointsSeen != 2 || result.DataPointsNew != 2 {
		t.Fatalf("Data Point counts = (%d, %d), want (2, 2)", result.DataPointsSeen, result.DataPointsNew)
	}
	// Page 2 is fetched twice (401 then retry); pages after the refresh
	// must keep using the refreshed token without re-fetching page 1.
	wantFetches := []string{":stale-access", "page-2:stale-access", "page-2:fresh-access", "page-3:fresh-access"}
	if strings.Join(fetches, ",") != strings.Join(wantFetches, ",") {
		t.Fatalf("fetches = %v, want %v", fetches, wantFetches)
	}
}

func TestGoogleHealthIngestionFailsWhenRefreshedTokenStillUnauthorized(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{}
	fetchCalls := 0
	provider := funcIngestionProvider(func(request RawRequest, accessToken string) ([]byte, error) {
		fetchCalls++
		return nil, &HTTPError{StatusCode: 401}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)
	request := fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		From:     "2026-01-01",
		To:       "2026-01-02T00:00:00Z",
	})
	refreshCalls := 0
	request.RefreshAccessToken = func() (string, error) {
		refreshCalls++
		return "fresh-access", nil
	}

	_, err := ingestion.Execute(context.Background(), archive, request)
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
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{}
	fetchCalls := 0
	provider := funcIngestionProvider(func(request RawRequest, accessToken string) ([]byte, error) {
		fetchCalls++
		return nil, &HTTPError{StatusCode: 401}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)
	request := fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		From:     "2026-01-01",
		To:       "2026-01-02T00:00:00Z",
	})
	refreshErr := errors.New("auto-refresh of Connection access token failed: refresh token revoked")
	request.RefreshAccessToken = func() (string, error) {
		return "", refreshErr
	}

	_, err := ingestion.Execute(context.Background(), archive, request)
	if !errors.Is(err, refreshErr) {
		t.Fatalf("ingest error = %v, want the refresh failure", err)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetch calls = %d, want 1 (no retry after failed refresh)", fetchCalls)
	}
}

// TestGoogleHealthIngestionDoesNotRefreshAfter401WhenCanceled pins the
// cancellation contract on the refresh path: when SIGINT cancels the
// run's context while the 401-failing request is in flight, the next
// boundary is BEFORE the token refresh — ingestion must surface
// ErrSyncCanceled instead of spending a refresh + retry the user no
// longer wants.
func TestGoogleHealthIngestionDoesNotRefreshAfter401WhenCanceled(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := false
	provider := funcIngestionProvider(func(request RawRequest, accessToken string) ([]byte, error) {
		// Simulate the signal landing while the failing request is in
		// flight: cancel the context, then surface the 401.
		if !canceled {
			cancel()
			canceled = true
		}
		return nil, &HTTPError{StatusCode: 401}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)
	request := fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		From:     "2026-01-01",
		To:       "2026-01-02T00:00:00Z",
	})
	refreshCalls := 0
	request.RefreshAccessToken = func() (string, error) {
		refreshCalls++
		return "fresh-access", nil
	}

	_, err := ingestion.Execute(ctx, archive, request)
	if !errors.Is(err, ErrSyncCanceled) {
		t.Fatalf("ingest error = %v, want ErrSyncCanceled", err)
	}
	if refreshCalls != 0 {
		t.Fatalf("refresh calls = %d, want 0 after cancellation", refreshCalls)
	}
}

func TestGoogleHealthIngestionWithoutRefreshHookSurfaces401(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{}
	fetchCalls := 0
	provider := funcIngestionProvider(func(request RawRequest, accessToken string) ([]byte, error) {
		fetchCalls++
		return nil, &HTTPError{StatusCode: 401}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)
	request := fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		From:     "2026-01-01",
		To:       "2026-01-02T00:00:00Z",
	})

	_, err := ingestion.Execute(context.Background(), archive, request)
	if err == nil || !strings.Contains(err.Error(), "Google Health rejected stored Connection token") {
		t.Fatalf("ingest error = %v, want rejected-token failure", err)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetch calls = %d, want 1 (no refresh hook, no retry)", fetchCalls)
	}
}

func TestGoogleHealthIngestionRefreshesAccessTokenMidRunForRollups(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{}
	var fetchTokens []string
	provider := funcIngestionProvider(func(request RawRequest, accessToken string) ([]byte, error) {
		fetchTokens = append(fetchTokens, accessToken)
		if accessToken != "fresh-access" {
			return nil, &HTTPError{StatusCode: 401}
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
	request := fakeGoogleHealthIngestionRequest(IngestionRequest{
		DataType: "steps",
		Rollup:   "daily",
		From:     "2026-01-01",
		To:       "2026-01-02",
	})
	request.AccessToken = "stale-access"
	request.RefreshAccessToken = func() (string, error) {
		return "fresh-access", nil
	}

	result, err := ingestion.Execute(context.Background(), archive, request)
	if err != nil {
		t.Fatalf("ingest daily Rollups across token expiry: %v", err)
	}
	if result.RollupsSeen != 1 || result.RollupsNew != 1 {
		t.Fatalf("Rollup counts = (%d, %d), want (1, 1)", result.RollupsSeen, result.RollupsNew)
	}
	if strings.Join(fetchTokens, ",") != "stale-access,fresh-access" {
		t.Fatalf("fetch tokens = %v, want stale then fresh", fetchTokens)
	}
}

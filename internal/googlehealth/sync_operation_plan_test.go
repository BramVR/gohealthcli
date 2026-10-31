package googlehealth

import (
	"encoding/json"
	"net/url"
	"reflect"
	"testing"
)

func TestDescribePlanUsesExecutionRequestAndPagePolicy(t *testing.T) {
	t.Parallel()
	ingestion := Ingestion{}
	tests := []struct {
		name               string
		request            IngestionRequest
		wantFamily         string
		wantMethod         string
		wantPath           string
		wantPageSize       int64
		wantRangeDays      int
		wantRangeWindows   int
		wantWindowSize     string
		wantSourceFamily   string
		wantConditionalTCX bool
	}{
		{
			name: "list",
			request: IngestionRequest{
				DataType: "steps",
				From:     "2026-01-01T00:00:00Z",
				To:       "2026-01-02T00:00:00Z",
			},
			wantFamily:       "list",
			wantMethod:       "GET",
			wantPath:         "/v4/users/me/dataTypes/steps/dataPoints",
			wantPageSize:     googleHealthMaxDataPointPageSize,
			wantRangeWindows: 1,
		},
		{
			name: "reconcile",
			request: IngestionRequest{
				DataType:     "steps",
				From:         "2026-01-01T00:00:00Z",
				To:           "2026-01-02T00:00:00Z",
				SourceFamily: "wearable",
			},
			wantFamily:       "reconcile",
			wantMethod:       "GET",
			wantPath:         "/v4/users/me/dataTypes/steps/dataPoints:reconcile",
			wantPageSize:     googleHealthMaxDataPointPageSize,
			wantRangeWindows: 1,
			wantSourceFamily: "users/me/dataSourceFamilies/google-wearables",
		},
		{
			name: "daily Rollup",
			request: IngestionRequest{
				DataType: "steps",
				From:     "2026-01-01",
				To:       "2026-04-10",
				Rollup:   "daily",
			},
			wantFamily:       "dailyRollUp",
			wantMethod:       "POST",
			wantPath:         "/v4/users/me/dataTypes/steps/dataPoints:dailyRollUp",
			wantRangeDays:    90,
			wantRangeWindows: 2,
		},
		{
			name: "window Rollup",
			request: IngestionRequest{
				DataType: "steps",
				From:     "2026-01-01T00:00:00Z",
				To:       "2026-01-02T00:00:00Z",
				Rollup:   "hourly",
			},
			wantFamily:       "rollUp",
			wantMethod:       "POST",
			wantPath:         "/v4/users/me/dataTypes/steps/dataPoints:rollUp",
			wantRangeWindows: 1,
			wantWindowSize:   "3600s",
		},
		{
			name: "exercise TCX conditional",
			request: IngestionRequest{
				DataType: "exercise",
				From:     "2026-01-01T00:00:00",
				To:       "2026-01-02T00:00:00",
			},
			wantFamily:         "list",
			wantMethod:         "GET",
			wantPath:           "/v4/users/me/dataTypes/exercise/dataPoints",
			wantPageSize:       googleHealthSessionDataPointPageSize,
			wantRangeWindows:   1,
			wantConditionalTCX: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan, err := ingestion.DescribePlan(test.request)
			if err != nil {
				t.Fatalf("DescribePlan: %v", err)
			}
			if plan.EndpointFamily != test.wantFamily {
				t.Errorf("EndpointFamily = %q, want %q", plan.EndpointFamily, test.wantFamily)
			}
			if plan.Request.Method != test.wantMethod {
				t.Errorf("request method = %q, want %q", plan.Request.Method, test.wantMethod)
			}
			parsedURL, err := url.Parse(plan.Request.URL)
			if err != nil {
				t.Fatal(err)
			}
			if parsedURL.Path != test.wantPath {
				t.Errorf("request path = %q, want %q", parsedURL.Path, test.wantPath)
			}
			if got := parsedURL.Query().Get("pageToken"); got != "" {
				t.Errorf("first request pageToken = %q, want omitted", got)
			}
			if test.wantPageSize > 0 && parsedURL.Query().Get("pageSize") == "" {
				t.Errorf("request URL = %q, want pageSize", plan.Request.URL)
			}
			if plan.PagePolicy.PageSize != test.wantPageSize || plan.PagePolicy.Pagination != "nextPageToken" {
				t.Errorf("page policy = %+v, want page size %d + nextPageToken", plan.PagePolicy, test.wantPageSize)
			}
			wantPageSizePolicy := "provider_default"
			if test.wantPageSize > 0 {
				wantPageSizePolicy = "explicit"
			}
			if plan.PagePolicy.PageSizePolicy != wantPageSizePolicy {
				t.Errorf("page size policy = %q, want %q", plan.PagePolicy.PageSizePolicy, wantPageSizePolicy)
			}
			if plan.PagePolicy.RangeWindowMaxDays != test.wantRangeDays || plan.PagePolicy.RangeWindowCount != test.wantRangeWindows {
				t.Errorf("range window policy = %+v, want max days %d, count %d", plan.PagePolicy, test.wantRangeDays, test.wantRangeWindows)
			}
			if !reflect.DeepEqual(plan.Request.RequiredScopes, ScopesForDataType(test.request.DataType)) {
				t.Errorf("required scopes = %v, want %v", plan.Request.RequiredScopes, ScopesForDataType(test.request.DataType))
			}
			if plan.ConditionalExerciseTcx != test.wantConditionalTCX {
				t.Errorf("ConditionalExerciseTcx = %v, want %v", plan.ConditionalExerciseTcx, test.wantConditionalTCX)
			}
			if test.wantSourceFamily != "" && parsedURL.Query().Get("dataSourceFamily") != test.wantSourceFamily {
				t.Errorf("dataSourceFamily = %q, want %q", parsedURL.Query().Get("dataSourceFamily"), test.wantSourceFamily)
			}
			if test.wantWindowSize != "" {
				var body struct {
					WindowSize string `json:"windowSize"`
				}
				if err := json.Unmarshal(plan.Request.Body, &body); err != nil {
					t.Fatal(err)
				}
				if body.WindowSize != test.wantWindowSize {
					t.Errorf("windowSize = %q, want %q", body.WindowSize, test.wantWindowSize)
				}
			}
		})
	}
}

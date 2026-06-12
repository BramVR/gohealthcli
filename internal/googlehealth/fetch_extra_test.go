package googlehealth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/archived"
)

// These tests moved from cmd/gohealthcli/main_test.go with the
// Provider client extraction (#287): they exercise package internals
// (request builders, list filters, the identity endpoint catalog, the
// limited body reader, and the rollup parser's civil-shape guards).

func TestParseStepsDailyRollupRequiresCivilEndTime(t *testing.T) {
	t.Parallel()
	_, err := parseGoogleHealthRollup(archived.Connection{
		ProviderName: "googlehealth",
		ID:           "googlehealth:111111256096816351",
	}, "steps", "dailyRollUp", json.RawMessage(`{
		"steps": {"countSum": "1234"},
		"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}}
	}`))
	if err == nil || !strings.Contains(err.Error(), "missing civilEndTime") {
		t.Fatalf("parse error = %v, want missing civilEndTime", err)
	}
}

func TestDailyNamedDataTypeListRequestIsNotRollup(t *testing.T) {
	t.Parallel()
	request, err := buildGoogleHealthDataTypeListRawRequest("daily-resting-heart-rate", "2026-01-01", "2026-01-02", 0, "")
	if err != nil {
		t.Fatalf("build daily-named list request: %v", err)
	}
	if request.EndpointName != "dataTypes.daily-resting-heart-rate.list" {
		t.Fatalf("endpointName = %q, want daily Data Type list", request.EndpointName)
	}
	if request.Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", request.Method)
	}
	if len(request.Body) != 0 {
		t.Fatalf("request body = %s, want empty list request body", string(request.Body))
	}
	parsedURL, err := url.Parse(request.URL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/daily-resting-heart-rate/dataPoints" {
		t.Fatalf("path = %q, want Data Points list path", parsedURL.Path)
	}
	if strings.Contains(request.EndpointName+parsedURL.Path, "RollUp") || strings.Contains(parsedURL.Path, "rollUp") {
		t.Fatalf("daily Data Type request used Rollup endpoint: %s %s", request.EndpointName, parsedURL.Path)
	}
	wantFilter := `daily_resting_heart_rate.date >= "2026-01-01" AND daily_resting_heart_rate.date < "2026-01-02"`
	if got := parsedURL.Query().Get("filter"); got != wantFilter {
		t.Fatalf("filter = %q, want %q", got, wantFilter)
	}
}

// TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog pins PRD #142
// slice 7 AC: no `[]string{ScopeProfileReadonly}` inline
// literal remains in BuildRawRequest. The only source of
// truth for endpoint scopes is the catalog. We verify behaviourally:
// a catalog mutation for the duration of the test flows through to the
// request's requiredScopes — proving the branch did a catalog lookup,
// not a hard-coded literal.
func TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"getIdentity", "getProfile", "getSettings", "pairedDevices", "getIrnProfile"} {
		t.Run(endpoint, func(t *testing.T) {
			original, ok := identityEndpointScopes[endpoint]
			if !ok {
				t.Fatalf("catalog missing %q — slice 1 contract violated", endpoint)
			}
			sentinel := "https://example.invalid/scope/sentinel-" + endpoint
			identityEndpointScopes[endpoint] = []string{sentinel}
			t.Cleanup(func() { identityEndpointScopes[endpoint] = original })

			request, err := BuildRawRequest([]string{"endpoint", endpoint}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", endpoint, err)
			}
			if len(request.RequiredScopes) != 1 || request.RequiredScopes[0] != sentinel {
				t.Fatalf("requiredScopes = %v, want catalog-driven %q — branch is using an inline scope literal", request.RequiredScopes, sentinel)
			}
		})
	}
}

func TestGoogleHealthRawFilterFieldsCoverFirstReleaseDataTypes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		dataType string
		from     string
		want     string
	}{
		{
			dataType: "steps",
			from:     "2026-01-01",
			want:     `steps.interval.start_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "oxygen-saturation",
			from:     "2026-01-01",
			want:     `oxygen_saturation.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "heart-rate-variability",
			from:     "2026-01-01",
			want:     `heart_rate_variability.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "daily-resting-heart-rate",
			from:     "2026-01-01",
			want:     `daily_resting_heart_rate.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-heart-rate-variability",
			from:     "2026-01-01",
			want:     `daily_heart_rate_variability.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-oxygen-saturation",
			from:     "2026-01-01",
			want:     `daily_oxygen_saturation.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-respiratory-rate",
			from:     "2026-01-01",
			want:     `daily_respiratory_rate.date >= "2026-01-01"`,
		},
		{
			dataType: "exercise",
			from:     "2026-01-01",
			want:     `exercise.interval.civil_start_time >= "2026-01-01"`,
		},
		{
			dataType: "sleep",
			from:     "2026-01-01",
			want:     `sleep.interval.civil_end_time >= "2026-01-01"`,
		},
		{
			dataType: "distance",
			from:     "2026-01-01",
			want:     `distance.interval.start_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "weight",
			from:     "2026-01-01",
			want:     `weight.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
	} {
		t.Run(test.dataType, func(t *testing.T) {
			filter, err := googleHealthDataTypeListFilter(test.dataType, test.from, "")
			if err != nil {
				t.Fatalf("filter: %v", err)
			}
			if filter != test.want {
				t.Fatalf("filter = %q, want %q", filter, test.want)
			}
		})
	}
}

func TestGoogleHealthRawFilterPreservesFractionalRFC3339Bounds(t *testing.T) {
	t.Parallel()
	filter, err := googleHealthDataTypeListFilter("heart-rate", "2026-01-01T00:00:00.500Z", "2026-01-01T01:02:03.123456789+02:00")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	want := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00.5Z" AND heart_rate.sample_time.physical_time < "2025-12-31T23:02:03.123456789Z"`
	if filter != want {
		t.Fatalf("filter = %q, want %q", filter, want)
	}
}

func TestReadLimitedBodyReportsOversize(t *testing.T) {
	t.Parallel()
	body, tooLarge, err := readLimitedBody(strings.NewReader("abcdef"), 5)
	if err != nil {
		t.Fatalf("read limited body: %v", err)
	}
	if !tooLarge {
		t.Fatal("tooLarge = false, want true")
	}
	if body != nil {
		t.Fatalf("body = %q, want nil when oversized", string(body))
	}
}

package googlehealth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

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

func TestBuildElectrocardiogramListRequestUsesCurrentProviderContract(t *testing.T) {
	t.Parallel()
	request, err := buildGoogleHealthDataTypeListRawRequest(
		"electrocardiogram",
		"2026-01-01T00:00:00Z",
		"2026-01-02T00:00:00Z",
		0,
		"",
	)
	if err != nil {
		t.Fatalf("build electrocardiogram list request: %v", err)
	}
	if !slices.Equal(request.RequiredScopes, []string{"https://www.googleapis.com/auth/googlehealth.ecg.readonly"}) {
		t.Fatalf("required scopes = %v, want current ECG read scope", request.RequiredScopes)
	}
	parsedURL, err := url.Parse(request.URL)
	if err != nil {
		t.Fatalf("parse electrocardiogram URL: %v", err)
	}
	want := `electrocardiogram.interval.start_time >= "2026-01-01T00:00:00Z"`
	if got := parsedURL.Query().Get("filter"); got != want {
		t.Fatalf("filter = %q, want Provider-supported lower-bound filter %q", got, want)
	}
}

func TestBuildBasalEnergyBurnedListRequestUsesPhysicalIntervalFilter(t *testing.T) {
	t.Parallel()
	request, err := buildGoogleHealthDataTypeListRawRequest(
		"basal-energy-burned",
		"2026-07-01T00:00:00Z",
		"2026-07-02T00:00:00Z",
		0,
		"",
	)
	if err != nil {
		t.Fatalf("build basal-energy-burned list request: %v", err)
	}
	if request.Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", request.Method)
	}
	if !slices.Equal(request.RequiredScopes, []string{ScopeActivityReadonly}) {
		t.Fatalf("required scopes = %v, want activity read scope", request.RequiredScopes)
	}
	parsedURL, err := url.Parse(request.URL)
	if err != nil {
		t.Fatalf("parse basal-energy-burned URL: %v", err)
	}
	wantPath := "/v4/users/me/dataTypes/basal-energy-burned/dataPoints"
	if parsedURL.Path != wantPath {
		t.Fatalf("path = %q, want %q", parsedURL.Path, wantPath)
	}
	wantFilter := `basal_energy_burned.interval.start_time >= "2026-07-01T00:00:00Z" AND basal_energy_burned.interval.start_time < "2026-07-02T00:00:00Z"`
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
//
// Deliberately serial (no t.Parallel): the sentinel swap mutates the
// package-level identityEndpointScopes catalog while parallel
// siblings read that same map (identity scope prechecks, raw
// endpoint requests). Running it serially keeps the mutation invisible
// to every other test and race-free (flagged by review on #310).
func TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog(t *testing.T) {
	for _, endpoint := range []string{"getIdentity", "getProfile", "getSettings", "pairedDevices", "getIrnProfile"} {
		t.Run(endpoint, func(t *testing.T) {
			original, ok := identityEndpointScopes[endpoint]
			if !ok {
				t.Fatalf("catalog missing %q — slice 1 contract violated", endpoint)
			}
			sentinel := "https://example.invalid/scope/sentinel-" + endpoint
			identityEndpointScopes[endpoint] = []string{sentinel}
			t.Cleanup(func() { identityEndpointScopes[endpoint] = original })

			request, err := BuildRawRequest(RawRequestOptions{Target: []string{"endpoint", endpoint}})
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

func TestRawEndpointNamesProjectCanonicalCatalogs(t *testing.T) {
	t.Parallel()

	want := make([]string, 0, len(identityEndpointURLs)+len(ListableDataTypes()))
	for endpoint := range identityEndpointURLs {
		want = append(want, endpoint)
	}
	for _, dataType := range ListableDataTypes() {
		want = append(want, "dataTypes."+dataType+".list")
	}
	sort.Strings(want)

	if got := RawEndpointNames(); !slices.Equal(got, want) {
		t.Fatalf("RawEndpointNames = %v, want canonical projection %v", got, want)
	}
}

func TestRawTargetNamesProjectAcceptedKinds(t *testing.T) {
	t.Parallel()

	got := RawTargetNames()
	if !slices.Equal(got, []string{"data-type", "endpoint"}) {
		t.Fatalf("RawTargetNames = %v, want [data-type endpoint]", got)
	}
	got[0] = "mutated"
	if fresh := RawTargetNames(); !slices.Equal(fresh, []string{"data-type", "endpoint"}) {
		t.Fatalf("RawTargetNames returned shared state: %v", fresh)
	}
}

func TestDescribeRawRequestMatchesProductionBuilder(t *testing.T) {
	t.Parallel()
	options := RawRequestOptions{
		Target:            []string{"data-type", "steps"},
		From:              "yesterday",
		To:                "today",
		Timezone:          "Europe/Brussels",
		ResolvedAt:        time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC),
		PageSize:          12,
		PageToken:         "synthetic-page-secret",
		PageTokenProvided: true,
	}
	description, err := DescribeRawRequest(options)
	if err != nil {
		t.Fatalf("describe raw request: %v", err)
	}
	request, err := BuildRawRequest(options)
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	if description.Request.Method != request.Method || description.Request.URL != request.URL || description.Request.EndpointName != request.EndpointName {
		t.Fatalf("description request = %+v, builder request = %+v", description.Request, request)
	}
	if description.Headers["Accept"] != "application/json" || len(description.Headers) != 1 {
		t.Fatalf("headers = %#v, want only non-secret Accept", description.Headers)
	}
	if strings.Contains(description.SanitizedURL, "synthetic-page-secret") || !strings.Contains(description.SanitizedURL, "REDACTED") {
		t.Fatalf("sanitized URL = %q", description.SanitizedURL)
	}
	if description.Range == nil || description.Range.Timezone != "Europe/Brussels" {
		t.Fatalf("range = %+v", description.Range)
	}
}

func TestDescribeRawIdentityDoesNotResolveTimezone(t *testing.T) {
	t.Parallel()
	description, err := DescribeRawRequest(RawRequestOptions{
		Target: []string{"endpoint", "getIdentity"},
		TimezoneFallback: func() (string, error) {
			t.Fatal("identity description resolved a timezone")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("describe identity: %v", err)
	}
	if description.Request.Method != http.MethodGet || description.Range != nil {
		t.Fatalf("description = %+v", description)
	}
}

func TestDescribeRawECGRangeMatchesAppliedLowerBoundOnlyFilter(t *testing.T) {
	t.Parallel()
	description, err := DescribeRawRequest(RawRequestOptions{
		Target:     []string{"data-type", "electrocardiogram"},
		From:       "2026-01-01",
		ResolvedAt: time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("describe ECG request: %v", err)
	}
	if description.Range == nil || description.Range.From != "2026-01-01" || description.Range.To != "" {
		t.Fatalf("range = %+v, want open-ended from 2026-01-01", description.Range)
	}
	if strings.Contains(description.Request.URL, "+AND+") {
		t.Fatalf("ECG request unexpectedly contains an upper-bound clause: %s", description.Request.URL)
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

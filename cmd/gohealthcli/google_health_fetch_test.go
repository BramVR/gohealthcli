package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDailyNamedDataTypeListRequestIsNotRollup(t *testing.T) {
	t.Parallel()
	request, err := buildGoogleHealthDataTypeListRawRequest("daily-resting-heart-rate", "2026-01-01", "2026-01-02", 0, "")
	if err != nil {
		t.Fatalf("build daily-named list request: %v", err)
	}
	if request.endpointName != "dataTypes.daily-resting-heart-rate.list" {
		t.Fatalf("endpointName = %q, want daily Data Type list", request.endpointName)
	}
	if request.method != http.MethodGet {
		t.Fatalf("method = %q, want GET", request.method)
	}
	if len(request.body) != 0 {
		t.Fatalf("request body = %s, want empty list request body", string(request.body))
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/daily-resting-heart-rate/dataPoints" {
		t.Fatalf("path = %q, want Data Points list path", parsedURL.Path)
	}
	if strings.Contains(request.endpointName+parsedURL.Path, "RollUp") || strings.Contains(parsedURL.Path, "rollUp") {
		t.Fatalf("daily Data Type request used Rollup endpoint: %s %s", request.endpointName, parsedURL.Path)
	}
	wantFilter := `daily_resting_heart_rate.date >= "2026-01-01" AND daily_resting_heart_rate.date < "2026-01-02"`
	if got := parsedURL.Query().Get("filter"); got != wantFilter {
		t.Fatalf("filter = %q, want %q", got, wantFilter)
	}
}

func TestBuildGoogleHealthRawRequestUsesProviderNamingConventions(t *testing.T) {
	t.Parallel()
	request, err := buildGoogleHealthRawRequest([]string{"endpoint", "dataTypes.heart-rate.list"}, "2026-01-01", "", 0, "")
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse raw URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/heart-rate/dataPoints" {
		t.Fatalf("path = %q, want kebab-case Data Type path", parsedURL.Path)
	}
	wantFilter := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00Z"`
	if parsedURL.Query().Get("filter") != wantFilter {
		t.Fatalf("filter = %q, want snake-case filter", parsedURL.Query().Get("filter"))
	}
}

func TestBuildGoogleHealthRawRequestRejectsNonListableDataTypes(t *testing.T) {
	t.Parallel()
	_, err := buildGoogleHealthRawRequest([]string{"data-type", "total-calories"}, "2026-01-01", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported Data Type")
	}
	if !strings.Contains(err.Error(), "not supported by dataPoints.list") {
		t.Fatalf("error = %v, want unsupported dataPoints.list", err)
	}
}

// TestBuildGoogleHealthRawRequestEndpointCatalog pins PRD #142 slice 7:
// every identity-style endpoint exposed by `raw endpoint <name>` must
// source its requiredScopes from googleHealthIdentityEndpointScopes so
// the scope contract for `raw` and the introspection commands (devices,
// settings, profile, irn-profile) can never drift apart. When slice 2
// of the PRD revises pairedDevices/getSettings scopes empirically, the
// catalog entry changes and this test follows automatically — no inline
// scope literals to update in main.go.
func TestBuildGoogleHealthRawRequestEndpointCatalog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		wantURL string
	}{
		{name: "getIdentity", wantURL: googleHealthIdentityURL},
		{name: "getProfile", wantURL: googleHealthProfileURL},
		{name: "getSettings", wantURL: googleHealthSettingsURL},
		{name: "pairedDevices", wantURL: googleHealthPairedDevicesURL},
		{name: "getIrnProfile", wantURL: googleHealthIRNProfileURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := buildGoogleHealthRawRequest([]string{"endpoint", tt.name}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", tt.name, err)
			}
			if request.endpointName != tt.name {
				t.Fatalf("endpointName = %q, want %q", request.endpointName, tt.name)
			}
			if request.url != tt.wantURL {
				t.Fatalf("url = %q, want %q", request.url, tt.wantURL)
			}
			wantScopes := googleHealthIdentityEndpointScopes[tt.name]
			if len(wantScopes) == 0 {
				t.Fatalf("catalog missing entry for %q — slice 1 contract violated", tt.name)
			}
			if len(request.requiredScopes) != len(wantScopes) {
				t.Fatalf("requiredScopes = %v, want %v (catalog entry)", request.requiredScopes, wantScopes)
			}
			for i, want := range wantScopes {
				if request.requiredScopes[i] != want {
					t.Fatalf("requiredScopes[%d] = %q, want %q (catalog entry)", i, request.requiredScopes[i], want)
				}
			}
		})
	}
}

// TestBuildGoogleHealthRawRequestUnknownEndpoint guards the
// not-found fall-through so a typo (or a renamed endpoint that
// outpaces a catalog update) still surfaces as a clear error rather
// than a nil request.
func TestBuildGoogleHealthRawRequestUnknownEndpoint(t *testing.T) {
	t.Parallel()
	_, err := buildGoogleHealthRawRequest([]string{"endpoint", "nonexistent"}, "", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported raw endpoint")
	}
	if !strings.Contains(err.Error(), `unsupported raw endpoint "nonexistent"`) {
		t.Fatalf("error = %v, want unsupported raw endpoint %q", err, "nonexistent")
	}
}

// TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog pins PRD #142
// slice 7 AC: no `[]string{googleHealthProfileReadonlyScope}` inline
// literal remains in buildGoogleHealthRawRequest. The only source of
// truth for endpoint scopes is the catalog. We verify behaviourally:
// a catalog mutation for the duration of the test flows through to the
// request's requiredScopes — proving the branch did a catalog lookup,
// not a hard-coded literal.
//
// Deliberately serial (no t.Parallel): the sentinel swap mutates the
// package-level googleHealthIdentityEndpointScopes catalog while
// parallel siblings read that same map (identity scope prechecks, raw
// endpoint requests). Running it serially keeps the mutation invisible
// to every other test and race-free (flagged by review on #310).
func TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog(t *testing.T) {
	for _, endpoint := range []string{"getIdentity", "getProfile", "getSettings", "pairedDevices", "getIrnProfile"} {
		t.Run(endpoint, func(t *testing.T) {
			original, ok := googleHealthIdentityEndpointScopes[endpoint]
			if !ok {
				t.Fatalf("catalog missing %q — slice 1 contract violated", endpoint)
			}
			sentinel := "https://example.invalid/scope/sentinel-" + endpoint
			googleHealthIdentityEndpointScopes[endpoint] = []string{sentinel}
			t.Cleanup(func() { googleHealthIdentityEndpointScopes[endpoint] = original })

			request, err := buildGoogleHealthRawRequest([]string{"endpoint", endpoint}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", endpoint, err)
			}
			if len(request.requiredScopes) != 1 || request.requiredScopes[0] != sentinel {
				t.Fatalf("requiredScopes = %v, want catalog-driven %q — branch is using an inline scope literal", request.requiredScopes, sentinel)
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

func TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody(t *testing.T) {
	t.Parallel()
	doer := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != googleHealthIdentityURL {
			t.Fatalf("raw URL = %q, want identity URL", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"access-secret-value rejected"}`)),
		}, nil
	})}

	_, err := fetchGoogleHealthRaw(context.Background(), doer, rawProviderRequest{endpointName: "getIdentity", url: googleHealthIdentityURL}, "access-secret-value")
	if err == nil {
		t.Fatal("fetch raw error = nil, want HTTP failure")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("fetch raw error = %v, want status", err)
	}
	if strings.Contains(err.Error(), "access-secret-value") {
		t.Fatalf("fetch raw error leaked token/body: %v", err)
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

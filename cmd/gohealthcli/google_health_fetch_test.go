package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestBuildGoogleHealthRawRequestUsesProviderNamingConventions(t *testing.T) {
	t.Parallel()
	request, err := googlehealth.BuildRawRequest([]string{"endpoint", "dataTypes.heart-rate.list"}, "2026-01-01", "", 0, "")
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	parsedURL, err := url.Parse(request.URL)
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
	for _, dataType := range []string{"total-calories", "floors", "calories-in-heart-rate-zone"} {
		t.Run(dataType, func(t *testing.T) {
			_, err := googlehealth.BuildRawRequest([]string{"data-type", dataType}, "2026-01-01", "", 0, "")
			if err == nil {
				t.Fatal("build raw request error = nil, want unsupported Data Type")
			}
			if !strings.Contains(err.Error(), "not supported by dataPoints.list") {
				t.Fatalf("error = %v, want unsupported dataPoints.list", err)
			}
		})
	}
}

// TestBuildGoogleHealthRawRequestEndpointCatalog pins PRD #142 slice 7:
// every identity-style endpoint exposed by `raw endpoint <name>` must
// source its requiredScopes from googlehealth.IdentityEndpointScopes so
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
		{name: "getIdentity", wantURL: googlehealth.IdentityURL},
		{name: "getProfile", wantURL: googlehealth.ProfileURL},
		{name: "getSettings", wantURL: googlehealth.SettingsURL},
		{name: "pairedDevices", wantURL: googlehealth.PairedDevicesURL},
		{name: "getIrnProfile", wantURL: googlehealth.IRNProfileURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := googlehealth.BuildRawRequest([]string{"endpoint", tt.name}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", tt.name, err)
			}
			if request.EndpointName != tt.name {
				t.Fatalf("endpointName = %q, want %q", request.EndpointName, tt.name)
			}
			if request.URL != tt.wantURL {
				t.Fatalf("url = %q, want %q", request.URL, tt.wantURL)
			}
			wantScopes := googlehealth.IdentityEndpointScopes(tt.name)
			if len(wantScopes) == 0 {
				t.Fatalf("catalog missing entry for %q — slice 1 contract violated", tt.name)
			}
			if len(request.RequiredScopes) != len(wantScopes) {
				t.Fatalf("requiredScopes = %v, want %v (catalog entry)", request.RequiredScopes, wantScopes)
			}
			for i, want := range wantScopes {
				if request.RequiredScopes[i] != want {
					t.Fatalf("requiredScopes[%d] = %q, want %q (catalog entry)", i, request.RequiredScopes[i], want)
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
	_, err := googlehealth.BuildRawRequest([]string{"endpoint", "nonexistent"}, "", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported raw endpoint")
	}
	if !strings.Contains(err.Error(), `unsupported raw endpoint "nonexistent"`) {
		t.Fatalf("error = %v, want unsupported raw endpoint %q", err, "nonexistent")
	}
}

func TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody(t *testing.T) {
	t.Parallel()
	doer := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != googlehealth.IdentityURL {
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

	_, err := googlehealth.FetchRaw(context.Background(), doer, googlehealth.RawRequest{EndpointName: "getIdentity", URL: googlehealth.IdentityURL}, "access-secret-value")
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

package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestBuildGoogleHealthExportExerciseTcxRawRequest pins the URL/method
// shape for the `users.dataTypes.dataPoints.exportExerciseTcx` REST
// method (ADR-0009, #107 slice D). The endpoint is GET against
// `<base>/<dataPointName>:exportExerciseTcx`. The data point's
// `upstream_resource_name` from the list page (e.g.
// `users/me/dataTypes/exercise/dataPoints/<id>`) is the `name` segment.
func TestBuildGoogleHealthExportExerciseTcxRawRequest(t *testing.T) {
	dataPointName := "users/me/dataTypes/exercise/dataPoints/12345"
	request, err := buildGoogleHealthExportExerciseTcxRawRequest(dataPointName)
	if err != nil {
		t.Fatalf("buildGoogleHealthExportExerciseTcxRawRequest: %v", err)
	}
	wantURL := googleHealthBaseURL + "/" + dataPointName + ":exportExerciseTcx"
	if request.url != wantURL {
		t.Fatalf("url = %q, want %q", request.url, wantURL)
	}
	if request.method != http.MethodGet {
		t.Fatalf("method = %q, want GET", request.method)
	}
	if request.endpointName != "dataTypes.exercise.exportExerciseTcx" {
		t.Fatalf("endpointName = %q, want dataTypes.exercise.exportExerciseTcx", request.endpointName)
	}
	if request.dataType != "exercise" {
		t.Fatalf("dataType = %q, want exercise", request.dataType)
	}
	if len(request.requiredScopes) == 0 {
		t.Fatalf("requiredScopes empty; want activity readonly scope")
	}
}

// TestBuildGoogleHealthExportExerciseTcxRawRequestRejectsBadName guards
// the URL builder against a missing or malformed resource name — an
// empty `name` would otherwise produce a URL ending in `:exportExerciseTcx`
// at the wrong path.
func TestBuildGoogleHealthExportExerciseTcxRawRequestRejectsBadName(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"missing dataPoints prefix", "users/me/dataTypes/exercise"},
		{"wrong data type", "users/me/dataTypes/steps/dataPoints/abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildGoogleHealthExportExerciseTcxRawRequest(tc.in); err == nil {
				t.Fatalf("expected error for %q", tc.in)
			} else if !strings.Contains(err.Error(), "exportExerciseTcx") && !strings.Contains(err.Error(), "exercise") {
				t.Fatalf("error %q does not mention the endpoint or exercise Data Type", err)
			}
		})
	}
}

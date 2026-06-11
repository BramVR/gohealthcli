package main

import (
	"net/http"
	"net/url"
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
	t.Parallel()
	dataPointName := "users/me/dataTypes/exercise/dataPoints/12345"
	request, err := buildGoogleHealthExportExerciseTcxRawRequest(dataPointName)
	if err != nil {
		t.Fatalf("buildGoogleHealthExportExerciseTcxRawRequest: %v", err)
	}
	// The path is rebuilt from its validated segments with the two
	// provider-controlled free segments (`<user>`, `<id>`) percent-encoded
	// via url.PathEscape — matching the rollUp/dailyRollUp/reconcile
	// builders. For a clean name url.PathEscape is a no-op, so the URL is
	// byte-for-byte the segment-escaped concatenation.
	wantURL := googleHealthBaseURL + "/users/" + url.PathEscape("me") +
		"/dataTypes/exercise/dataPoints/" + url.PathEscape("12345") + ":exportExerciseTcx"
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

// TestBuildGoogleHealthExportExerciseTcxRawRequestEscapesFreeSegments
// pins that special characters in the provider-controlled free segments
// (the `<user>` and data point `<id>` segments) are percent-encoded in
// the request path rather than passed through raw. A raw `?`/`#` would
// otherwise become a real query string / fragment and shift the
// `:exportExerciseTcx` custom-method suffix off the path. The escaped URL
// must still target `:exportExerciseTcx` on the fixed health.googleapis.com
// host with no query string or fragment introduced.
func TestBuildGoogleHealthExportExerciseTcxRawRequestEscapesFreeSegments(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"question mark in id", "users/me/dataTypes/exercise/dataPoints/a?b"},
		{"hash in id", "users/me/dataTypes/exercise/dataPoints/a#b"},
		{"percent in id", "users/me/dataTypes/exercise/dataPoints/a%2Fb"},
		{"question mark in user", "users/m?e/dataTypes/exercise/dataPoints/abc"},
		{"hash in user", "users/m#e/dataTypes/exercise/dataPoints/abc"},
		{"percent in user", "users/m%2Fe/dataTypes/exercise/dataPoints/abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := buildGoogleHealthExportExerciseTcxRawRequest(tc.in)
			if err != nil {
				// Rejecting the name outright is an acceptable hardening.
				return
			}
			parsed, parseErr := url.Parse(request.url)
			if parseErr != nil {
				t.Fatalf("url.Parse(%q): %v", request.url, parseErr)
			}
			if parsed.Host != "health.googleapis.com" {
				t.Fatalf("host = %q, want health.googleapis.com (URL %q)", parsed.Host, request.url)
			}
			if parsed.RawQuery != "" {
				t.Fatalf("RawQuery = %q, want empty (URL %q)", parsed.RawQuery, request.url)
			}
			if parsed.Fragment != "" {
				t.Fatalf("Fragment = %q, want empty (URL %q)", parsed.Fragment, request.url)
			}
			if !strings.HasSuffix(parsed.EscapedPath(), ":exportExerciseTcx") {
				t.Fatalf("escaped path %q does not end with :exportExerciseTcx (URL %q)", parsed.EscapedPath(), request.url)
			}
			// The decoded path round-trips back to the exact resource name,
			// proving the free segments were escaped (not dropped/mangled).
			wantPath := "/v4/" + tc.in + ":exportExerciseTcx"
			if parsed.Path != wantPath {
				t.Fatalf("decoded path = %q, want %q", parsed.Path, wantPath)
			}
		})
	}
}

// TestBuildGoogleHealthExportExerciseTcxRawRequestRejectsBadName guards
// the URL builder against a missing or malformed resource name — an
// empty `name` would otherwise produce a URL ending in `:exportExerciseTcx`
// at the wrong path.
func TestBuildGoogleHealthExportExerciseTcxRawRequestRejectsBadName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"missing dataPoints prefix", "users/me/dataTypes/exercise"},
		{"wrong data type", "users/me/dataTypes/steps/dataPoints/abc"},
		{"missing users prefix", "me/dataTypes/exercise/dataPoints/abc/x/y"},
		{"empty user segment", "users//dataTypes/exercise/dataPoints/abc"},
		{"extra trailing segment", "users/me/dataTypes/exercise/dataPoints/abc/extra"},
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

package main

import (
	"errors"
	"strings"
	"testing"
)

// These tests exercise the one shared Provider GET module (issue #280)
// at its public interface — URL + per-fetch label in, validated JSON
// out — through the shared Provider HTTP client's stub transport, the
// same seam the rest of the suite uses until the HTTP doer joins the
// runtime adapters (#281).

func TestProviderGETReturnsValidatedJSONWithBearerAuth(t *testing.T) {
	transport := &stubProviderTransport{status: 200, body: `{"devices":[]}`}
	swapSharedProviderHTTPClient(t, transport)

	body, err := fetchProviderJSON(googleHealthPairedDevicesURL, "pairedDevices", "test-access-token")
	if err != nil {
		t.Fatalf("fetchProviderJSON: %v", err)
	}
	if string(body) != `{"devices":[]}` {
		t.Fatalf("body = %q, want the Provider payload verbatim", body)
	}
	if transport.request == nil {
		t.Fatal("Provider GET bypassed the shared Provider HTTP client")
	}
	if got := transport.request.URL.String(); got != googleHealthPairedDevicesURL {
		t.Fatalf("request URL = %q, want %q", got, googleHealthPairedDevicesURL)
	}
	if got := transport.request.Header.Get("Authorization"); got != "Bearer test-access-token" {
		t.Fatalf("Authorization header = %q, want bearer token", got)
	}
	if got := transport.request.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept header = %q, want application/json", got)
	}
}

func TestProviderGETReturnsTypedStatusErrorCarryingLabel(t *testing.T) {
	swapSharedProviderHTTPClient(t, &stubProviderTransport{status: 404, body: `{"error":"not found"}`})

	_, err := fetchProviderJSON(googleHealthSettingsURL, "settings", "test-access-token")
	if err == nil {
		t.Fatal("fetchProviderJSON returned nil error, want typed status error")
	}
	var httpErr *googleHealthHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 404 {
		t.Fatalf("err = %v, want typed googleHealthHTTPError with status 404", err)
	}
	// The per-fetch label keeps each fetcher's historical message verbatim.
	if got, want := err.Error(), "Google Health settings request failed with HTTP 404"; got != want {
		t.Fatalf("err.Error() = %q, want %q", got, want)
	}
}

func TestProviderGETRejectsInvalidJSONCarryingLabel(t *testing.T) {
	swapSharedProviderHTTPClient(t, &stubProviderTransport{status: 200, body: `{"truncated":`})

	_, err := fetchProviderJSON(googleHealthIRNProfileURL, "irnProfile", "test-access-token")
	if err == nil {
		t.Fatal("fetchProviderJSON returned nil error, want invalid-JSON rejection")
	}
	if got, want := err.Error(), "Google Health irnProfile response is not valid JSON"; got != want {
		t.Fatalf("err.Error() = %q, want %q", got, want)
	}
}

// TestProviderGETBoundsResponseBodySize pins the Identity Snapshot
// fetchers' historical 1 MiB body cap: a larger body is read only up
// to the limit, so the truncated payload fails the JSON validity check
// instead of buffering an unbounded Provider response in memory.
func TestProviderGETBoundsResponseBodySize(t *testing.T) {
	if providerGETResponseLimit != 1<<20 {
		t.Fatalf("providerGETResponseLimit = %d, want the historical 1 MiB cap", providerGETResponseLimit)
	}
	oversized := `{"padding":"` + strings.Repeat("a", providerGETResponseLimit) + `"}`
	swapSharedProviderHTTPClient(t, &stubProviderTransport{status: 200, body: oversized})

	_, err := fetchProviderJSON(googleHealthSettingsURL, "settings", "test-access-token")
	if err == nil {
		t.Fatal("fetchProviderJSON returned nil error, want truncated oversized body rejected")
	}
	if got, want := err.Error(), "Google Health settings response is not valid JSON"; got != want {
		t.Fatalf("err.Error() = %q, want %q", got, want)
	}
}

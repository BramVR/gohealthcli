package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These tests exercise the one shared Provider GET module (issue #280)
// at its public interface — URL + per-fetch label in, validated JSON
// out — through the shared Provider HTTP client's stub transport, the
// same seam the rest of the suite uses until the HTTP doer joins the
// runtime adapters (#281).

// stubProviderResponse is one canned Provider answer for the
// sequenced transport below.
type stubProviderResponse struct {
	status     int
	body       string
	retryAfter string
}

// sequencedProviderTransport replays canned Provider responses in
// order (repeating the last one when the sequence runs out) and counts
// the requests it served, so a test can fake a Provider that fails N
// times before recovering and assert how many attempts the module made.
type sequencedProviderTransport struct {
	responses []stubProviderResponse
	served    int
}

func (transport *sequencedProviderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	index := transport.served
	if index >= len(transport.responses) {
		index = len(transport.responses) - 1
	}
	transport.served++
	canned := transport.responses[index]
	header := make(http.Header)
	if canned.retryAfter != "" {
		header.Set("Retry-After", canned.retryAfter)
	}
	return &http.Response{
		StatusCode: canned.status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(canned.body)),
		Request:    request,
	}, nil
}

// swapSharedProviderGETRetrySeams installs a recording sleeper and
// deterministic jitter into the shared Provider GET module for the
// duration of the test, so retry timing is observable without real
// backoff sleeps.
func swapSharedProviderGETRetrySeams(t *testing.T, record *[]time.Duration) {
	t.Helper()
	original := sharedProviderGET
	sharedProviderGET = providerGET{sleeper: recordingSleeper(record), jitter: noopRetryJitter}
	t.Cleanup(func() { sharedProviderGET = original })
}

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

// TestProviderGETRetriesTransient429HonoringRetryAfter pins the retry
// parity the Identity Snapshot fetchers gain (issue #280): the module
// rides the same bounded-backoff middleware the Sync Run ingestion
// path uses, with the Provider's Retry-After hint as the sleep floor.
func TestProviderGETRetriesTransient429HonoringRetryAfter(t *testing.T) {
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 429, body: `{"error":"rate limited"}`, retryAfter: "7"},
		{status: 429, body: `{"error":"rate limited"}`, retryAfter: "7"},
		{status: 200, body: `{"ok":true}`},
	}}
	swapSharedProviderHTTPClient(t, transport)
	var sleeps []time.Duration
	swapSharedProviderGETRetrySeams(t, &sleeps)

	body, err := fetchProviderJSON(googleHealthProfileURL, "profile", "test-access-token")
	if err != nil {
		t.Fatalf("fetchProviderJSON = %v, want success after two 429 retries", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q, want the recovered payload", body)
	}
	if transport.served != 3 {
		t.Fatalf("Provider served %d requests, want 3 (two 429s then success)", transport.served)
	}
	// Retry-After (7s) outweighs the exponential schedule (250ms, 500ms)
	// and becomes the floor for both sleeps.
	if len(sleeps) != 2 || sleeps[0] != 7*time.Second || sleeps[1] != 7*time.Second {
		t.Fatalf("sleeps = %v, want [7s 7s] honoring Retry-After", sleeps)
	}
}

func TestProviderGETRetriesTransient5xxWithBoundedBackoff(t *testing.T) {
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 503, body: `{"error":"unavailable"}`},
		{status: 502, body: `{"error":"bad gateway"}`},
		{status: 200, body: `{"recovered":true}`},
	}}
	swapSharedProviderHTTPClient(t, transport)
	var sleeps []time.Duration
	swapSharedProviderGETRetrySeams(t, &sleeps)

	body, err := fetchProviderJSON(googleHealthIdentityURL, "identity", "test-access-token")
	if err != nil {
		t.Fatalf("fetchProviderJSON = %v, want success after transient 5xx retries", err)
	}
	if string(body) != `{"recovered":true}` {
		t.Fatalf("body = %q, want the recovered payload", body)
	}
	if transport.served != 3 {
		t.Fatalf("Provider served %d requests, want 3", transport.served)
	}
	// Without a Retry-After hint the schedule is the bounded exponential
	// the raw Provider fetch already uses: 250ms then 500ms.
	if len(sleeps) != 2 || sleeps[0] != 250*time.Millisecond || sleeps[1] != 500*time.Millisecond {
		t.Fatalf("sleeps = %v, want [250ms 500ms] bounded exponential backoff", sleeps)
	}
}

// TestProviderGETExhaustsRetryBudgetKeepingLabelAndTypedChain pins the
// failure shape after the bounded budget runs out: the attempt count
// is reported, the per-fetch label survives inside the message, the
// typed googleHealthHTTPError stays reachable via errors.As, and the
// translation layer still classifies the failure as
// provider_unreachable — so failure statuses are unchanged by #280.
func TestProviderGETExhaustsRetryBudgetKeepingLabelAndTypedChain(t *testing.T) {
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 503, body: `{"error":"unavailable"}`},
	}}
	swapSharedProviderHTTPClient(t, transport)
	var sleeps []time.Duration
	swapSharedProviderGETRetrySeams(t, &sleeps)

	_, err := fetchProviderJSON(googleHealthIRNProfileURL, "irnProfile", "test-access-token")
	if err == nil {
		t.Fatal("fetchProviderJSON returned nil error, want exhausted-retries failure")
	}
	if transport.served != googleHealthRetryMaxAttempts {
		t.Fatalf("Provider served %d requests, want the bounded budget %d", transport.served, googleHealthRetryMaxAttempts)
	}
	if len(sleeps) != googleHealthRetryMaxAttempts-1 {
		t.Fatalf("sleeps = %d, want %d (one between each pair of attempts, none after the last)", len(sleeps), googleHealthRetryMaxAttempts-1)
	}
	wantMessage := "Google Health request failed after 5 attempts: Google Health irnProfile request failed with HTTP 503"
	if err.Error() != wantMessage {
		t.Fatalf("err.Error() = %q, want %q", err.Error(), wantMessage)
	}
	var httpErr *googleHealthHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 503 {
		t.Fatalf("err = %v, want wrapped googleHealthHTTPError{503}", err)
	}
	if !isProviderUnreachableError(err) {
		t.Fatalf("err = %v, want provider_unreachable classification", err)
	}
}

// TestProviderGETDoesNotRetryUnauthorized pins the non-transient
// branch: a Provider 401 is a Connection problem `gohealthcli connect`
// fixes, so it surfaces after exactly one attempt with the historical
// per-fetch message verbatim — no attempt-count wrapping — and still
// matches the unauthorized translation layer.
func TestProviderGETDoesNotRetryUnauthorized(t *testing.T) {
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 401, body: `{"error":"unauthorized"}`},
	}}
	swapSharedProviderHTTPClient(t, transport)
	var sleeps []time.Duration
	swapSharedProviderGETRetrySeams(t, &sleeps)

	_, err := fetchProviderJSON(googleHealthIdentityURL, "identity", "expired-access-token")
	if err == nil {
		t.Fatal("fetchProviderJSON returned nil error, want unauthorized failure")
	}
	if transport.served != 1 {
		t.Fatalf("Provider served %d requests, want exactly 1 (401 is not retryable)", transport.served)
	}
	if len(sleeps) != 0 {
		t.Fatalf("sleeps = %v, want none", sleeps)
	}
	if got, want := err.Error(), "Google Health identity request failed with HTTP 401"; got != want {
		t.Fatalf("err.Error() = %q, want the historical message %q verbatim", got, want)
	}
	if !isUnauthorizedHTTPError(err) {
		t.Fatalf("err = %v, want unauthorized classification for the translation layer", err)
	}
}

// TestProviderGETDoesNotRetryNetworkFailure pins the other
// non-transient branch: a transport-level failure (dial refused, DNS,
// deadline) is not a typed HTTP error, so it surfaces after one
// attempt as the *url.Error the provider_unreachable classification
// expects.
func TestProviderGETDoesNotRetryNetworkFailure(t *testing.T) {
	attempts := 0
	swapSharedProviderHTTPClient(t, countingFailingTransport{attempts: &attempts, err: errors.New("connect: connection refused")})
	var sleeps []time.Duration
	swapSharedProviderGETRetrySeams(t, &sleeps)

	_, err := fetchProviderJSON(googleHealthSettingsURL, "settings", "test-access-token")
	if err == nil {
		t.Fatal("fetchProviderJSON returned nil error, want network failure")
	}
	if attempts != 1 {
		t.Fatalf("transport saw %d attempts, want exactly 1 (network failures are not retried)", attempts)
	}
	if len(sleeps) != 0 {
		t.Fatalf("sleeps = %v, want none", sleeps)
	}
	if !isProviderUnreachableError(err) {
		t.Fatalf("err = %v, want provider_unreachable classification", err)
	}
}

// countingFailingTransport is failingProviderTransport with an attempt
// counter, so a test can prove the module did not loop on a
// non-retryable transport failure.
type countingFailingTransport struct {
	attempts *int
	err      error
}

func (transport countingFailingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*transport.attempts++
	return nil, transport.err
}

// assertFetcherKeepsLabeledErrorMessages pins one Identity Snapshot
// fetcher's historical error strings — the per-fetch label on a
// non-2xx status and on an invalid-JSON body — so converting the
// fetcher into a thin call site over the Provider GET module cannot
// drift its user-facing messages (issue #280 AC).
func assertFetcherKeepsLabeledErrorMessages(t *testing.T, label string, fetch func(accessToken string) error) {
	t.Helper()
	swapSharedProviderHTTPClient(t, &stubProviderTransport{status: 404, body: `{"error":"not found"}`})
	err := fetch("test-access-token")
	if want := "Google Health " + label + " request failed with HTTP 404"; err == nil || err.Error() != want {
		t.Fatalf("status error = %v, want %q verbatim", err, want)
	}

	swapSharedProviderHTTPClient(t, &stubProviderTransport{status: 200, body: `{"truncated":`})
	err = fetch("test-access-token")
	if want := "Google Health " + label + " response is not valid JSON"; err == nil || err.Error() != want {
		t.Fatalf("invalid-JSON error = %v, want %q verbatim", err, want)
	}
}

// assertFetcherRetriesTransient503 drives one Identity Snapshot
// fetcher against a Provider that fails once with a 503 and then
// recovers, proving the fetcher rides the shared Provider GET module's
// retry instead of carrying its own single-shot transport (issue #280).
func assertFetcherRetriesTransient503(t *testing.T, happyBody string, fetch func(accessToken string) (string, error)) {
	t.Helper()
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 503, body: `{"error":"unavailable"}`},
		{status: 200, body: happyBody},
	}}
	swapSharedProviderHTTPClient(t, transport)
	var sleeps []time.Duration
	swapSharedProviderGETRetrySeams(t, &sleeps)

	rawJSON, err := fetch("test-access-token")
	if err != nil {
		t.Fatalf("fetcher = %v, want success after one transient 503 retry", err)
	}
	if rawJSON != happyBody {
		t.Fatalf("rawJSON = %q, want the recovered payload %q", rawJSON, happyBody)
	}
	if transport.served != 2 {
		t.Fatalf("Provider served %d requests, want 2 (one 503 then recovery)", transport.served)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleeps = %v, want exactly one backoff between the attempts", sleeps)
	}
}

func TestPairedDevicesFetcherKeepsLabeledErrorMessages(t *testing.T) {
	assertFetcherKeepsLabeledErrorMessages(t, "pairedDevices", func(accessToken string) error {
		_, err := fetchGooglePairedDevices(accessToken)
		return err
	})
}

func TestPairedDevicesFetcherRetriesTransientFailures(t *testing.T) {
	assertFetcherRetriesTransient503(t, `{"devices":[]}`, func(accessToken string) (string, error) {
		devices, err := fetchGooglePairedDevices(accessToken)
		return devices.rawJSON, err
	})
}

func TestSettingsFetcherKeepsLabeledErrorMessages(t *testing.T) {
	assertFetcherKeepsLabeledErrorMessages(t, "settings", func(accessToken string) error {
		_, err := fetchGoogleSettings(accessToken)
		return err
	})
}

func TestSettingsFetcherRetriesTransientFailures(t *testing.T) {
	assertFetcherRetriesTransient503(t, `{"distanceUnit":"METRIC"}`, func(accessToken string) (string, error) {
		settings, err := fetchGoogleSettings(accessToken)
		return settings.rawJSON, err
	})
}

func TestIRNProfileFetcherKeepsLabeledErrorMessages(t *testing.T) {
	assertFetcherKeepsLabeledErrorMessages(t, "irnProfile", func(accessToken string) error {
		_, err := fetchGoogleIRNProfile(accessToken)
		return err
	})
}

func TestIRNProfileFetcherRetriesTransientFailures(t *testing.T) {
	assertFetcherRetriesTransient503(t, `{"irns":[]}`, func(accessToken string) (string, error) {
		profile, err := fetchGoogleIRNProfile(accessToken)
		return profile.rawJSON, err
	})
}

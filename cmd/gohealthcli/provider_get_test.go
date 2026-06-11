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
// at its public interface — module + URL + per-fetch label in,
// validated JSON out — with a fake HTTP doer injected as the module's
// transport (#281). No global is reassigned: each test constructs the
// providerGET value it drives.

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

// providerGETWithDoer is the Provider GET module under test with the
// given fake transport injected as its HTTP doer.
func providerGETWithDoer(transport http.RoundTripper) providerGET {
	return providerGET{doer: providerDoer(transport)}
}

// providerGETWithRetrySeams is providerGETWithDoer plus a recording
// sleeper and deterministic jitter, so retry timing is observable
// without real backoff sleeps.
func providerGETWithRetrySeams(transport http.RoundTripper, record *[]time.Duration) providerGET {
	get := providerGETWithDoer(transport)
	get.sleeper = recordingSleeper(record)
	get.jitter = noopRetryJitter
	return get
}

func TestProviderGETReturnsValidatedJSONWithBearerAuth(t *testing.T) {
	t.Parallel()
	transport := &stubProviderTransport{status: 200, body: `{"devices":[]}`}

	body, err := fetchProviderJSON(providerGETWithDoer(transport), googleHealthPairedDevicesURL, "pairedDevices", "test-access-token")
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
	t.Parallel()
	get := providerGETWithDoer(&stubProviderTransport{status: 404, body: `{"error":"not found"}`})

	_, err := fetchProviderJSON(get, googleHealthSettingsURL, "settings", "test-access-token")
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
	t.Parallel()
	get := providerGETWithDoer(&stubProviderTransport{status: 200, body: `{"truncated":`})

	_, err := fetchProviderJSON(get, googleHealthIRNProfileURL, "irnProfile", "test-access-token")
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
	t.Parallel()
	if providerGETResponseLimit != 1<<20 {
		t.Fatalf("providerGETResponseLimit = %d, want the historical 1 MiB cap", providerGETResponseLimit)
	}
	oversized := `{"padding":"` + strings.Repeat("a", providerGETResponseLimit) + `"}`
	get := providerGETWithDoer(&stubProviderTransport{status: 200, body: oversized})

	_, err := fetchProviderJSON(get, googleHealthSettingsURL, "settings", "test-access-token")
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
	t.Parallel()
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 429, body: `{"error":"rate limited"}`, retryAfter: "7"},
		{status: 429, body: `{"error":"rate limited"}`, retryAfter: "7"},
		{status: 200, body: `{"ok":true}`},
	}}
	var sleeps []time.Duration
	get := providerGETWithRetrySeams(transport, &sleeps)

	body, err := fetchProviderJSON(get, googleHealthProfileURL, "profile", "test-access-token")
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
	t.Parallel()
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 503, body: `{"error":"unavailable"}`},
		{status: 502, body: `{"error":"bad gateway"}`},
		{status: 200, body: `{"recovered":true}`},
	}}
	var sleeps []time.Duration
	get := providerGETWithRetrySeams(transport, &sleeps)

	body, err := fetchProviderJSON(get, googleHealthIdentityURL, "identity", "test-access-token")
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
	t.Parallel()
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 503, body: `{"error":"unavailable"}`},
	}}
	var sleeps []time.Duration
	get := providerGETWithRetrySeams(transport, &sleeps)

	_, err := fetchProviderJSON(get, googleHealthIRNProfileURL, "irnProfile", "test-access-token")
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
	t.Parallel()
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 401, body: `{"error":"unauthorized"}`},
	}}
	var sleeps []time.Duration
	get := providerGETWithRetrySeams(transport, &sleeps)

	_, err := fetchProviderJSON(get, googleHealthIdentityURL, "identity", "expired-access-token")
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
	t.Parallel()
	attempts := 0
	var sleeps []time.Duration
	get := providerGETWithRetrySeams(countingFailingTransport{attempts: &attempts, err: errors.New("connect: connection refused")}, &sleeps)

	_, err := fetchProviderJSON(get, googleHealthSettingsURL, "settings", "test-access-token")
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
// drift its user-facing messages (issue #280 AC). The fetcher receives
// the module value with the fake doer injected (#281).
func assertFetcherKeepsLabeledErrorMessages(t *testing.T, label string, fetch func(get providerGET, accessToken string) error) {
	t.Helper()
	err := fetch(providerGETWithDoer(&stubProviderTransport{status: 404, body: `{"error":"not found"}`}), "test-access-token")
	if want := "Google Health " + label + " request failed with HTTP 404"; err == nil || err.Error() != want {
		t.Fatalf("status error = %v, want %q verbatim", err, want)
	}

	err = fetch(providerGETWithDoer(&stubProviderTransport{status: 200, body: `{"truncated":`}), "test-access-token")
	if want := "Google Health " + label + " response is not valid JSON"; err == nil || err.Error() != want {
		t.Fatalf("invalid-JSON error = %v, want %q verbatim", err, want)
	}
}

// assertFetcherRetriesTransient503 drives one Identity Snapshot
// fetcher against a Provider that fails once with a 503 and then
// recovers, proving the fetcher rides the shared Provider GET module's
// retry instead of carrying its own single-shot transport (issue #280).
func assertFetcherRetriesTransient503(t *testing.T, happyBody string, fetch func(get providerGET, accessToken string) (string, error)) {
	t.Helper()
	transport := &sequencedProviderTransport{responses: []stubProviderResponse{
		{status: 503, body: `{"error":"unavailable"}`},
		{status: 200, body: happyBody},
	}}
	var sleeps []time.Duration

	rawJSON, err := fetch(providerGETWithRetrySeams(transport, &sleeps), "test-access-token")
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
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "pairedDevices", func(get providerGET, accessToken string) error {
		_, err := fetchGooglePairedDevices(get, accessToken)
		return err
	})
}

func TestPairedDevicesFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"devices":[]}`, func(get providerGET, accessToken string) (string, error) {
		devices, err := fetchGooglePairedDevices(get, accessToken)
		return devices.rawJSON, err
	})
}

func TestSettingsFetcherKeepsLabeledErrorMessages(t *testing.T) {
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "settings", func(get providerGET, accessToken string) error {
		_, err := fetchGoogleSettings(get, accessToken)
		return err
	})
}

func TestSettingsFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"distanceUnit":"METRIC"}`, func(get providerGET, accessToken string) (string, error) {
		settings, err := fetchGoogleSettings(get, accessToken)
		return settings.rawJSON, err
	})
}

func TestIRNProfileFetcherKeepsLabeledErrorMessages(t *testing.T) {
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "irnProfile", func(get providerGET, accessToken string) error {
		_, err := fetchGoogleIRNProfile(get, accessToken)
		return err
	})
}

func TestIRNProfileFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"irns":[]}`, func(get providerGET, accessToken string) (string, error) {
		profile, err := fetchGoogleIRNProfile(get, accessToken)
		return profile.rawJSON, err
	})
}

func TestIdentityFetcherKeepsLabeledErrorMessages(t *testing.T) {
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "identity", func(get providerGET, accessToken string) error {
		_, err := fetchGoogleIdentity(get, accessToken)
		return err
	})
}

func TestIdentityFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"healthUserId":"111111256096816351"}`, func(get providerGET, accessToken string) (string, error) {
		identity, err := fetchGoogleIdentity(get, accessToken)
		return identity.rawJSON, err
	})
}

func TestProfileFetcherKeepsLabeledErrorMessages(t *testing.T) {
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "profile", func(get providerGET, accessToken string) error {
		_, err := fetchGoogleProfile(get, accessToken)
		return err
	})
}

func TestProfileFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"name":"profiles/111111256096816351"}`, func(get providerGET, accessToken string) (string, error) {
		profile, err := fetchGoogleProfile(get, accessToken)
		return profile.rawJSON, err
	})
}

// TestProductionProviderGETBindsSharedTimeoutClient pins the wiring
// the doer conversion (#281) must not break: productionProviderGET —
// the module value every production fetcher binds — carries the
// shared timeout client as its doer, so production Identity Snapshot
// fetches keep the #271 deadline.
func TestProductionProviderGETBindsSharedTimeoutClient(t *testing.T) {
	t.Parallel()
	get := productionProviderGET()
	client, ok := get.doer.(*http.Client)
	if !ok || client != providerHTTPClient {
		t.Fatalf("productionProviderGET doer = %#v, want the shared Provider HTTP client", get.doer)
	}
	if get.sleeper != nil || get.jitter != nil {
		t.Fatal("productionProviderGET must leave retry seams nil (real backoff in production)")
	}
}

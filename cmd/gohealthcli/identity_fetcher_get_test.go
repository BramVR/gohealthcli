package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

// These tests pin each Identity Snapshot fetcher's parity with the
// shared Provider GET module (issue #280): historical error strings
// stay verbatim and transient failures retry. They moved out of the
// Provider package's test file with the #287 extraction because the
// fetchers themselves live in main. The retry timing internals
// (virtual sleeps, Retry-After floors, attempt budgets) are pinned in
// internal/googlehealth's own GET tests; here the single transient-503
// retry rides one real backoff sleep (~250ms) and every test runs
// parallel so the suite's wall clock is unaffected.

// fetcherStubResponse is one canned Provider answer for the sequenced
// transport below.
type fetcherStubResponse struct {
	status int
	body   string
}

// sequencedFetcherTransport replays canned Provider responses in order
// (repeating the last one when the sequence runs out) and counts the
// requests it served.
type sequencedFetcherTransport struct {
	responses []fetcherStubResponse
	served    int
}

func (transport *sequencedFetcherTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	index := transport.served
	if index >= len(transport.responses) {
		index = len(transport.responses) - 1
	}
	transport.served++
	canned := transport.responses[index]
	return &http.Response{
		StatusCode: canned.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(canned.body)),
		Request:    request,
	}, nil
}

// assertFetcherKeepsLabeledErrorMessages pins one Identity Snapshot
// fetcher's historical error strings — the per-fetch label on a
// non-2xx status and on an invalid-JSON body — so the fetcher's thin
// call site over the Provider GET module cannot drift its user-facing
// messages (issue #280 AC). The fetcher receives the module value with
// the fake doer injected (#281). Both probes are single-attempt (404
// and invalid JSON are not retried), so no backoff sleeps occur.
func assertFetcherKeepsLabeledErrorMessages(t *testing.T, label string, fetch func(get googlehealth.GET, accessToken string) error) {
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
// The single retry sleeps one real backoff (~250ms + jitter); the test
// runs parallel so this does not extend the suite's wall clock.
func assertFetcherRetriesTransient503(t *testing.T, happyBody string, fetch func(get googlehealth.GET, accessToken string) (string, error)) {
	t.Helper()
	transport := &sequencedFetcherTransport{responses: []fetcherStubResponse{
		{status: 503, body: `{"error":"unavailable"}`},
		{status: 200, body: happyBody},
	}}

	rawJSON, err := fetch(providerGETWithDoer(transport), "test-access-token")
	if err != nil {
		t.Fatalf("fetcher = %v, want success after one transient 503 retry", err)
	}
	if rawJSON != happyBody {
		t.Fatalf("rawJSON = %q, want the recovered payload %q", rawJSON, happyBody)
	}
	if transport.served != 2 {
		t.Fatalf("Provider served %d requests, want 2 (one 503 then recovery)", transport.served)
	}
}

func TestPairedDevicesFetcherKeepsLabeledErrorMessages(t *testing.T) {
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "pairedDevices", func(get googlehealth.GET, accessToken string) error {
		_, err := fetchGooglePairedDevices(get, accessToken)
		return err
	})
}

func TestPairedDevicesFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"devices":[]}`, func(get googlehealth.GET, accessToken string) (string, error) {
		devices, err := fetchGooglePairedDevices(get, accessToken)
		return devices.rawJSON, err
	})
}

func TestSettingsFetcherKeepsLabeledErrorMessages(t *testing.T) {
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "settings", func(get googlehealth.GET, accessToken string) error {
		_, err := fetchGoogleSettings(get, accessToken)
		return err
	})
}

func TestSettingsFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"distanceUnit":"METRIC"}`, func(get googlehealth.GET, accessToken string) (string, error) {
		settings, err := fetchGoogleSettings(get, accessToken)
		return settings.rawJSON, err
	})
}

func TestIRNProfileFetcherKeepsLabeledErrorMessages(t *testing.T) {
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "irnProfile", func(get googlehealth.GET, accessToken string) error {
		_, err := fetchGoogleIRNProfile(get, accessToken)
		return err
	})
}

func TestIRNProfileFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"irns":[]}`, func(get googlehealth.GET, accessToken string) (string, error) {
		profile, err := fetchGoogleIRNProfile(get, accessToken)
		return profile.rawJSON, err
	})
}

func TestIdentityFetcherKeepsLabeledErrorMessages(t *testing.T) {
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "identity", func(get googlehealth.GET, accessToken string) error {
		_, err := fetchGoogleIdentity(get, accessToken)
		return err
	})
}

func TestIdentityFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"healthUserId":"111111256096816351"}`, func(get googlehealth.GET, accessToken string) (string, error) {
		identity, err := fetchGoogleIdentity(get, accessToken)
		return identity.rawJSON, err
	})
}

func TestProfileFetcherKeepsLabeledErrorMessages(t *testing.T) {
	t.Parallel()
	assertFetcherKeepsLabeledErrorMessages(t, "profile", func(get googlehealth.GET, accessToken string) error {
		_, err := fetchGoogleProfile(get, accessToken)
		return err
	})
}

func TestProfileFetcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	assertFetcherRetriesTransient503(t, `{"name":"profiles/111111256096816351"}`, func(get googlehealth.GET, accessToken string) (string, error) {
		profile, err := fetchGoogleProfile(get, accessToken)
		return profile.rawJSON, err
	})
}

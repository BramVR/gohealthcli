package googlehealth

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// stubProviderTransport answers every request with a canned Provider
// response without touching the network, and records the last request
// so tests can prove a call routed through the injected doer.
type stubProviderTransport struct {
	request *http.Request
	status  int
	body    string
}

func (transport *stubProviderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request
	return &http.Response{
		StatusCode: transport.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(transport.body)),
		Request:    request,
	}, nil
}

// providerDoer wraps a stub transport in an http.Client carrying the
// production timeout — a fake HTTP doer tests inject as the module's
// transport instead of reassigning any package-level client (#281).
func providerDoer(transport http.RoundTripper) Doer {
	return &http.Client{Timeout: HTTPTimeout, Transport: transport}
}

// mustURLQuery parses rawURL and returns its query values, failing the
// test on a malformed URL. (A copy of the identical helper in the main
// package's test harness — the two test binaries cannot share it.)
func mustURLQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL %q: %v", rawURL, err)
	}
	return parsed.Query()
}

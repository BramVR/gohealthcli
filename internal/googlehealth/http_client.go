package googlehealth

import (
	"net/http"
	"time"
)

// HTTPTimeout bounds every Provider HTTP request end to end:
// dial, TLS handshake, response headers, and body read. Without a
// deadline a stalled connection hangs a Sync Run forever — its
// heartbeat goes quiet and the abandoned-run fence
// (syncRunFenceStaleAfter, 5 minutes) can fence a run whose process is
// still alive. Sixty seconds covers the largest Provider page
// (googleHealthRawResponseLimit, 10 MiB) on a slow link while staying
// well inside the fence window, so a stall surfaces as a request error
// the run can report instead of a fenced-while-alive run.
const HTTPTimeout = 60 * time.Second

// Doer is the HTTP transport seam on the runtime adapters (#281):
// exactly (*http.Client).Do, so the production adapter binds the shared
// timeout client below directly and tests inject a fake doer (an
// http.Client over a stub RoundTripper) without touching any global.
type Doer interface {
	Do(request *http.Request) (*http.Response, error)
}

// HTTPClient is the one shared HTTP client for every Provider
// request: Identity Snapshot fetchers, Google identity and profile
// fetchers, OAuth token exchange and refresh, and raw Provider fetch.
// Production code must not use http.DefaultClient — it carries no
// timeout. This value is wiring only: it is bound as the production
// HTTP doer (runtime adapters, ProductionGET) and is never
// reassigned; request paths receive a doer instead of reading it.
var HTTPClient = newHTTPClient(HTTPTimeout)

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

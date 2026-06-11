package main

import (
	"net/http"
	"time"
)

// providerHTTPTimeout bounds every Provider HTTP request end to end:
// dial, TLS handshake, response headers, and body read. Without a
// deadline a stalled connection hangs a Sync Run forever — its
// heartbeat goes quiet and the abandoned-run fence
// (syncRunFenceStaleAfter, 5 minutes) can fence a run whose process is
// still alive. Sixty seconds covers the largest Provider page
// (googleHealthRawResponseLimit, 10 MiB) on a slow link while staying
// well inside the fence window, so a stall surfaces as a request error
// the run can report instead of a fenced-while-alive run.
const providerHTTPTimeout = 60 * time.Second

// providerHTTPClient is the one shared HTTP client for every Provider
// request: Identity Snapshot fetchers, Google identity and profile
// fetchers, OAuth token exchange and refresh, and raw Provider fetch.
// Production code must not use http.DefaultClient — it carries no
// timeout. Tests may swap this variable to inject a stub transport
// until the HTTP doer joins the runtime adapters seam (#281).
var providerHTTPClient = newProviderHTTPClient(providerHTTPTimeout)

func newProviderHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

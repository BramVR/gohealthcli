package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// This file is the one shared Provider GET module (issue #280,
// ADR-0007 "deepen the Provider HTTP path"). Every Identity Snapshot
// fetcher — paired devices, settings, IRN profile, identity, profile —
// is a thin call site over fetchProviderJSON: URL + per-fetch label in,
// validated JSON out. The module owns the bearer auth header, the
// response size limit, the shared timeout client (#271), and typed
// status errors carrying the per-fetch label (#272), so transport
// behavior cannot drift between Identity Snapshot fetches.

// providerGETResponseLimit bounds how much of an Identity Snapshot
// response body the module buffers — the historical 1 MiB cap every
// fetcher carried. Identity-level payloads are small; a body this
// large means a misbehaving Provider, and the truncated read fails the
// JSON validity check below instead of exhausting memory. (The raw
// Provider fetch keeps its own larger googleHealthRawResponseLimit —
// raw pages legitimately reach megabytes.)
const providerGETResponseLimit = 1 << 20

// providerGET is the shared Provider GET module. The zero value is the
// production configuration; sleeper and jitter are test seams that
// mirror runtimeGoogleHealthIngestionProvider's — production leaves
// them nil and fetchWithRetry falls back to sleepWithCancel +
// defaultRetryJitter.
type providerGET struct {
	sleeper googleHealthRetrySleeper
	jitter  func(time.Duration) time.Duration
}

// sharedProviderGET is the production module value every Identity
// Snapshot fetcher calls through. Tests may swap it (alongside
// providerHTTPClient) to observe retry timing without real backoff
// sleeps, until the HTTP doer joins the runtime adapters seam (#281).
var sharedProviderGET = providerGET{}

// fetchProviderJSON is the module's production entry point: one
// Provider GET against url, labeled per fetch for error messages.
func fetchProviderJSON(url, label, accessToken string) ([]byte, error) {
	return sharedProviderGET.fetchJSON(url, label, accessToken)
}

// fetchJSON wraps the single-attempt GET in the same bounded
// retry/Retry-After middleware the Sync Run ingestion path uses
// (google_health_retry.go): up to googleHealthRetryMaxAttempts
// attempts for 429/5xx with exponential backoff capped at
// googleHealthRetryMaxDelay, Retry-After as the sleep floor, and
// immediate surfacing of non-transient failures. Identity Snapshot
// commands carry no cancel channel today, so the sleep is plain
// (context-based cancellation is #284's slice).
func (get providerGET) fetchJSON(url, label, accessToken string) ([]byte, error) {
	fetcher := func(_ rawProviderRequest, token string) ([]byte, error) {
		return providerGETOnce(url, label, token)
	}
	return fetchWithRetry(fetcher, get.sleeper, get.jitter, rawProviderRequest{url: url}, accessToken, nil)
}

// providerGETOnce is the single GET attempt the retry middleware wraps.
func providerGETOnce(url, label, accessToken string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := providerHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, providerGETResponseLimit))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Typed so the translation layer can branch on the status code
		// via errors.As instead of message text (issue #272). The
		// per-fetch label keeps the historical message verbatim, and
		// Retry-After rides along so the retry middleware can honor
		// the Provider's hint (issue #280).
		return nil, &googleHealthHTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
			endpoint:   label,
		}
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("Google Health %s response is not valid JSON", label)
	}
	return body, nil
}

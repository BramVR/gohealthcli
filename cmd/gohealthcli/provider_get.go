package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// This file is the one shared Provider GET module (issue #280,
// ADR-0007 "deepen the Provider HTTP path"). Every Identity Snapshot
// fetcher — paired devices, settings, IRN profile, identity, profile —
// is a thin call site over fetchProviderJSON: module + URL + per-fetch
// label in, validated JSON out. The module owns the bearer auth header,
// the response size limit, and typed status errors carrying the
// per-fetch label (#272); its HTTP transport is the injected doer
// (#281), which production binds to the shared timeout client (#271),
// so transport behavior cannot drift between Identity Snapshot fetches.

// providerGETResponseLimit bounds how much of an Identity Snapshot
// response body the module buffers — the historical 1 MiB cap every
// fetcher carried. Identity-level payloads are small; a body this
// large means a misbehaving Provider. The bound is a memory guard:
// a read truncated mid-document then typically fails the JSON
// validity check below, though a truncation that happens to land on
// a valid JSON boundary would pass it. (The raw Provider fetch keeps
// its own larger googleHealthRawResponseLimit — raw pages
// legitimately reach megabytes.)
const providerGETResponseLimit = 1 << 20

// providerGET is the shared Provider GET module. doer is the HTTP
// transport seam (#281) — required; production constructs the module
// via productionProviderGET or runtimeAdapters.providerGET, tests bind
// a fake. sleeper and jitter are retry test seams that mirror
// runtimeGoogleHealthIngestionProvider's — production leaves them nil
// and fetchWithRetry falls back to sleepWithCancel + defaultRetryJitter.
type providerGET struct {
	doer    httpDoer
	sleeper googleHealthRetrySleeper
	jitter  func(time.Duration) time.Duration
}

// productionProviderGET is the module configuration every production
// call site outside the runtime adapters uses: the shared timeout
// client as the doer, real backoff sleeps.
func productionProviderGET() providerGET {
	return providerGET{doer: providerHTTPClient}
}

// fetchProviderJSON is the module's entry point: one Provider GET
// against url through the given module value, labeled per fetch for
// error messages. ctx scopes the HTTP request and the retry backoff
// sleeps (#284); callers without cancellation instrumentation pass
// context.Background().
func fetchProviderJSON(ctx context.Context, get providerGET, url, label, accessToken string) ([]byte, error) {
	return get.fetchJSON(ctx, url, label, accessToken)
}

// fetchJSON wraps the single-attempt GET in the same bounded
// retry/Retry-After middleware the Sync Run ingestion path uses
// (google_health_retry.go): up to googleHealthRetryMaxAttempts
// attempts for 429/5xx with exponential backoff capped at
// googleHealthRetryMaxDelay, Retry-After as the sleep floor, and
// immediate surfacing of non-transient failures. A canceled ctx aborts
// the in-flight request and short-circuits backoff sleeps (#284).
func (get providerGET) fetchJSON(ctx context.Context, url, label, accessToken string) ([]byte, error) {
	fetcher := func(ctx context.Context, _ rawProviderRequest, token string) ([]byte, error) {
		return get.fetchOnce(ctx, url, label, token)
	}
	return fetchWithRetry(ctx, fetcher, get.sleeper, get.jitter, rawProviderRequest{url: url}, accessToken)
}

// fetchOnce is the single GET attempt the retry middleware wraps.
func (get providerGET) fetchOnce(ctx context.Context, url, label, accessToken string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := get.doer.Do(request)
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

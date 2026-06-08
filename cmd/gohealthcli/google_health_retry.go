package main

import (
	"errors"
	"math/rand"
	"time"
)

// Bounded exponential backoff parameters for the Google Health
// Ingestion retry middleware. These are package constants rather than
// CLI flags (per #96): the goal is to absorb transient blips without
// asking users to tune behavior. A multi-year backfill that takes 30s
// to retry past a 429 is acceptable; raising these any higher would
// risk masking a real provider outage as success.
const (
	googleHealthRetryMaxAttempts = 5
	googleHealthRetryBaseDelay   = 250 * time.Millisecond
	googleHealthRetryMaxDelay    = 30 * time.Second
)

// googleHealthRetryFetcher is the inner Fetch the middleware wraps.
// Matches runtimeGoogleHealthIngestionProvider.Fetch's signature so the
// middleware can sit in front of it without a new interface.
type googleHealthRetryFetcher func(request rawProviderRequest, accessToken string) ([]byte, error)

// googleHealthRetrySleeper is the time-source seam the tests inject so
// they do not actually wait between attempts.
type googleHealthRetrySleeper func(time.Duration)

// fetchWithRetry retries 429 and 5xx responses with bounded exponential
// backoff plus jitter. Non-transient failures (401, 403, 404, network
// errors that are not a googleHealthHTTPError) surface immediately so
// callers see real errors without a multi-second delay. The Retry-After
// header on 429 is honored as the minimum next-attempt delay.
func fetchWithRetry(fetcher googleHealthRetryFetcher, sleeper googleHealthRetrySleeper, jitter func(time.Duration) time.Duration, request rawProviderRequest, accessToken string) ([]byte, error) {
	if sleeper == nil {
		sleeper = time.Sleep
	}
	if jitter == nil {
		jitter = defaultRetryJitter
	}
	var lastErr error
	for attempt := 0; attempt < googleHealthRetryMaxAttempts; attempt++ {
		body, err := fetcher(request, accessToken)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !isRetryableHTTPError(err) {
			return nil, err
		}
		if attempt == googleHealthRetryMaxAttempts-1 {
			break
		}
		delay := backoffDelay(attempt, retryAfterFromError(err))
		sleeper(jitter(delay))
	}
	return nil, lastErr
}

// isRetryableHTTPError returns true for 429 and any 5xx response. Other
// failure modes (network errors that are not a googleHealthHTTPError,
// 4xx other than 429) are treated as terminal — the right action is to
// surface them, not loop.
func isRetryableHTTPError(err error) bool {
	var httpErr *googleHealthHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.StatusCode == 429 {
		return true
	}
	if httpErr.StatusCode >= 500 && httpErr.StatusCode < 600 {
		return true
	}
	return false
}

// retryAfterFromError extracts a Retry-After hint from an HTTP error.
// Returns 0 when the error is not an HTTP error or the header is absent.
func retryAfterFromError(err error) time.Duration {
	var httpErr *googleHealthHTTPError
	if !errors.As(err, &httpErr) {
		return 0
	}
	return httpErr.RetryAfter
}

// backoffDelay computes the next sleep duration. Retry-After (when
// supplied) is the floor; exponential delay (base * 2^attempt, capped
// at the package max) is the alternative when the server gave no hint.
// The actual sleep length is wrapped by the jitter helper before use.
func backoffDelay(attempt int, retryAfter time.Duration) time.Duration {
	delay := googleHealthRetryBaseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay >= googleHealthRetryMaxDelay {
			delay = googleHealthRetryMaxDelay
			break
		}
	}
	if retryAfter > delay {
		return retryAfter
	}
	return delay
}

// defaultRetryJitter spreads a sleep across [delay, delay+25%] so a
// fleet of clients doesn't stampede after a shared rate-limit window.
// gohealthcli is a single-user CLI, but the jitter is cheap and keeps
// the behavior right if the binary is ever scripted to run in parallel.
func defaultRetryJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	jitter := time.Duration(rand.Int63n(int64(delay) / 4))
	return delay + jitter
}

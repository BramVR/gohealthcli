package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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
// middleware can sit in front of it without a new interface. ctx scopes
// the underlying HTTP request, so a SIGINT-canceled context aborts the
// in-flight call instead of waiting for it to return (#284).
type googleHealthRetryFetcher func(ctx context.Context, request rawProviderRequest, accessToken string) ([]byte, error)

// googleHealthRetrySleeper is the time-source seam tests inject. It
// receives the run's context so the production implementation can
// short-circuit a backoff when SIGINT cancels it; tests typically
// inject a no-op sleeper that records the requested duration.
type googleHealthRetrySleeper func(ctx context.Context, d time.Duration) (canceled bool)

// fetchWithRetry retries 429 and 5xx responses with bounded exponential
// backoff plus jitter. Non-transient failures (401, 403, 404, network
// errors that are not a googleHealthHTTPError) surface immediately so
// callers see real errors without a multi-second delay. The Retry-After
// header on 429 is honored as the minimum next-attempt delay. A
// canceled ctx aborts the in-flight HTTP request and short-circuits an
// in-flight backoff sleep, surfacing errSyncCanceled either way, so
// SIGINT during a stalled fetch or a 30s backoff does not leave the
// user waiting (#284).
func fetchWithRetry(ctx context.Context, fetcher googleHealthRetryFetcher, sleeper googleHealthRetrySleeper, jitter func(time.Duration) time.Duration, request rawProviderRequest, accessToken string) ([]byte, error) {
	if sleeper == nil {
		sleeper = sleepWithCancel
	}
	if jitter == nil {
		jitter = defaultRetryJitter
	}
	var lastErr error
	attempts := 0
	for attempt := 0; attempt < googleHealthRetryMaxAttempts; attempt++ {
		attempts++
		body, err := fetcher(ctx, request, accessToken)
		if err == nil {
			return body, nil
		}
		if ctx.Err() != nil {
			// The fetch failed while the run's context was canceled —
			// SIGINT aborted the in-flight request. Surface the
			// cancellation sentinel rather than the transport's wrapping
			// of context.Canceled, so the Sync Run finalizes as
			// sync_canceled, not sync_failed. (A client-side timeout uses
			// the doer's own deadline, leaves ctx.Err() nil, and still
			// takes the failure path below.)
			return nil, errSyncCanceled
		}
		lastErr = err
		if !isRetryableHTTPError(err) {
			return nil, err
		}
		if attempt == googleHealthRetryMaxAttempts-1 {
			break
		}
		delay := backoffDelay(attempt, retryAfterFromError(err))
		if canceled := sleeper(ctx, jitter(delay)); canceled {
			return nil, errSyncCanceled
		}
	}
	return nil, fmt.Errorf("Google Health request failed after %d attempts: %w", attempts, lastErr)
}

// sleepWithCancel sleeps for d, but returns early (canceled=true) if
// ctx is canceled first. Production wires this in so SIGINT is honored
// during a multi-second backoff. context.Background() degrades to a
// plain sleep (its Done channel is nil, so only the timer arm fires).
func sleepWithCancel(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return false
	case <-ctx.Done():
		return true
	}
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
// math/rand/v2 is auto-seeded, so the sequence varies across runs.
func defaultRetryJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	// Sub-4ns delays would make the window collapse to zero and panic
	// rand.IntN; the constants today never produce such small values but
	// guard so a future tweak to googleHealthRetryBaseDelay can't bite.
	window := int64(delay) / 4
	if window <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int64N(window))
}

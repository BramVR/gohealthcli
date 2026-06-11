package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// noopRetryJitter is a deterministic jitter so per-attempt sleep durations
// are easy to assert. The real jitter is exercised in defaultRetryJitter's
// own test below.
var noopRetryJitter = func(d time.Duration) time.Duration { return d }

// recordingSleeper captures the sleep durations the middleware requested
// without actually sleeping. The context is observed but never cancels —
// the cancel-during-sleep path has its own test below.
func recordingSleeper(record *[]time.Duration) googleHealthRetrySleeper {
	return func(_ context.Context, d time.Duration) bool {
		*record = append(*record, d)
		return false
	}
}

func TestFetchWithRetryRetriesTransient429ThenSucceeds(t *testing.T) {
	t.Parallel()
	attempts := 0
	fetcher := func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		attempts++
		if attempts < 3 {
			return nil, &googleHealthHTTPError{StatusCode: 429}
		}
		return []byte(`{"ok":true}`), nil
	}
	var sleepCalls []time.Duration
	sleeper := recordingSleeper(&sleepCalls)

	body, err := fetchWithRetry(context.Background(), fetcher, sleeper, noopRetryJitter, rawProviderRequest{}, "tok")
	if err != nil {
		t.Fatalf("fetchWithRetry returned err = %v, want success after 3 attempts", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q, want successful payload", body)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(sleepCalls) != 2 {
		t.Fatalf("sleep calls = %d, want 2 (one between each retry)", len(sleepCalls))
	}
	// Bounded exponential: first 250ms, second 500ms.
	if sleepCalls[0] != 250*time.Millisecond {
		t.Fatalf("sleep[0] = %v, want 250ms", sleepCalls[0])
	}
	if sleepCalls[1] != 500*time.Millisecond {
		t.Fatalf("sleep[1] = %v, want 500ms", sleepCalls[1])
	}
}

func TestFetchWithRetryRetries5xxThenSucceeds(t *testing.T) {
	t.Parallel()
	attempts := 0
	fetcher := func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		attempts++
		if attempts == 1 {
			return nil, &googleHealthHTTPError{StatusCode: 503}
		}
		return []byte(`{}`), nil
	}
	var sleepCalls []time.Duration
	_, err := fetchWithRetry(context.Background(), fetcher, recordingSleeper(&sleepCalls), noopRetryJitter, rawProviderRequest{}, "tok")
	if err != nil {
		t.Fatalf("err = %v, want success after one 503 retry", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestFetchWithRetryExhaustsBudgetAndReturnsAttemptedCount(t *testing.T) {
	t.Parallel()
	attempts := 0
	fetcher := func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		attempts++
		return nil, &googleHealthHTTPError{StatusCode: 502}
	}
	var sleepCalls []time.Duration
	_, err := fetchWithRetry(context.Background(), fetcher, recordingSleeper(&sleepCalls), noopRetryJitter, rawProviderRequest{}, "tok")
	if err == nil {
		t.Fatal("err = nil, want exhausted-retries error")
	}
	if attempts != googleHealthRetryMaxAttempts {
		t.Fatalf("attempts = %d, want %d (the bounded retry budget)", attempts, googleHealthRetryMaxAttempts)
	}
	if len(sleepCalls) != googleHealthRetryMaxAttempts-1 {
		t.Fatalf("sleep calls = %d, want %d (one between each pair of attempts, none after the last)", len(sleepCalls), googleHealthRetryMaxAttempts-1)
	}
	// The error message must call out how many attempts ran so an
	// operator reading "HTTP 502 after 5 attempts" can tell this from a
	// single-shot 502.
	wantSnippet := fmt.Sprintf("after %d attempts", googleHealthRetryMaxAttempts)
	if !errorContains(err, wantSnippet) {
		t.Fatalf("err = %v, want substring %q", err, wantSnippet)
	}
	// The original typed error must still be reachable via errors.As so
	// callers can pivot on StatusCode.
	var httpErr *googleHealthHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 502 {
		t.Fatalf("err = %v, want wrapped googleHealthHTTPError{502}", err)
	}
}

func TestFetchWithRetryDoesNotRetry401(t *testing.T) {
	t.Parallel()
	attempts := 0
	fetcher := func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		attempts++
		return nil, &googleHealthHTTPError{StatusCode: 401}
	}
	var sleepCalls []time.Duration
	_, err := fetchWithRetry(context.Background(), fetcher, recordingSleeper(&sleepCalls), noopRetryJitter, rawProviderRequest{}, "tok")
	if err == nil {
		t.Fatal("err = nil, want 401 surface")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (401 must not retry)", attempts)
	}
	if len(sleepCalls) != 0 {
		t.Fatalf("sleep calls = %d, want 0 (401 must not delay)", len(sleepCalls))
	}
}

func TestFetchWithRetryDoesNotRetryOther4xx(t *testing.T) {
	t.Parallel()
	for _, statusCode := range []int{400, 403, 404, 422} {
		statusCode := statusCode
		t.Run(fmt.Sprintf("status_%d", statusCode), func(t *testing.T) {
			attempts := 0
			fetcher := func(context.Context, rawProviderRequest, string) ([]byte, error) {
				attempts++
				return nil, &googleHealthHTTPError{StatusCode: statusCode}
			}
			var sleepCalls []time.Duration
			_, err := fetchWithRetry(context.Background(), fetcher, recordingSleeper(&sleepCalls), noopRetryJitter, rawProviderRequest{}, "tok")
			if err == nil {
				t.Fatalf("status %d: err = nil, want surface", statusCode)
			}
			if attempts != 1 {
				t.Fatalf("status %d: attempts = %d, want 1 (non-429 4xx must not retry)", statusCode, attempts)
			}
		})
	}
}

func TestFetchWithRetryDoesNotRetryNonHTTPError(t *testing.T) {
	t.Parallel()
	attempts := 0
	fetcher := func(context.Context, rawProviderRequest, string) ([]byte, error) {
		attempts++
		return nil, errors.New("connection refused")
	}
	var sleepCalls []time.Duration
	_, err := fetchWithRetry(context.Background(), fetcher, recordingSleeper(&sleepCalls), noopRetryJitter, rawProviderRequest{}, "tok")
	if err == nil {
		t.Fatal("err = nil, want surface")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (non-HTTP errors must not retry)", attempts)
	}
}

func TestFetchWithRetryHonorsRetryAfterAsMinimum(t *testing.T) {
	t.Parallel()
	attempts := 0
	fetcher := func(context.Context, rawProviderRequest, string) ([]byte, error) {
		attempts++
		if attempts == 1 {
			// Server says wait 3 seconds. Exponential would have suggested 250ms.
			return nil, &googleHealthHTTPError{StatusCode: 429, RetryAfter: 3 * time.Second}
		}
		return []byte(`{}`), nil
	}
	var sleepCalls []time.Duration
	_, err := fetchWithRetry(context.Background(), fetcher, recordingSleeper(&sleepCalls), noopRetryJitter, rawProviderRequest{}, "tok")
	if err != nil {
		t.Fatalf("err = %v, want success", err)
	}
	if len(sleepCalls) != 1 {
		t.Fatalf("sleep calls = %d, want 1", len(sleepCalls))
	}
	if sleepCalls[0] != 3*time.Second {
		t.Fatalf("sleep[0] = %v, want 3s (Retry-After is the floor)", sleepCalls[0])
	}
}

func TestFetchWithRetryRetryAfterIgnoredIfSmallerThanExponential(t *testing.T) {
	t.Parallel()
	// Force the second sleep to use exponential (500ms) rather than the smaller Retry-After (100ms).
	attempts := 0
	fetcher := func(context.Context, rawProviderRequest, string) ([]byte, error) {
		attempts++
		if attempts <= 2 {
			return nil, &googleHealthHTTPError{StatusCode: 429, RetryAfter: 100 * time.Millisecond}
		}
		return []byte(`{}`), nil
	}
	var sleepCalls []time.Duration
	_, err := fetchWithRetry(context.Background(), fetcher, recordingSleeper(&sleepCalls), noopRetryJitter, rawProviderRequest{}, "tok")
	if err != nil {
		t.Fatalf("err = %v, want success", err)
	}
	// First retry: exponential=250ms, Retry-After=100ms → use 250ms (the floor never beats it).
	// Second retry: exponential=500ms, Retry-After=100ms → use 500ms.
	if len(sleepCalls) != 2 {
		t.Fatalf("sleep calls = %d, want 2", len(sleepCalls))
	}
	if sleepCalls[0] != 250*time.Millisecond {
		t.Fatalf("sleep[0] = %v, want 250ms (exponential > Retry-After)", sleepCalls[0])
	}
	if sleepCalls[1] != 500*time.Millisecond {
		t.Fatalf("sleep[1] = %v, want 500ms", sleepCalls[1])
	}
}

func TestFetchWithRetryShortCircuitsBackoffOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	fetcher := func(context.Context, rawProviderRequest, string) ([]byte, error) {
		attempts++
		return nil, &googleHealthHTTPError{StatusCode: 429, RetryAfter: 10 * time.Second}
	}
	// Use the real sleeper so we exercise the select against ctx.Done().
	_, err := fetchWithRetry(ctx, fetcher, sleepWithCancel, noopRetryJitter, rawProviderRequest{}, "tok")
	if !errors.Is(err, errSyncCanceled) {
		t.Fatalf("err = %v, want errSyncCanceled (canceled context must short-circuit the backoff sleep)", err)
	}
	// The fetcher returned 429 once; cancel arrived before any 10s
	// backoff could run; the second attempt must NOT have run.
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (cancel short-circuits before the next attempt)", attempts)
	}
}

// TestFetchWithRetryCancelDuringBackoffSleepReturnsPromptly pins the
// #284 contract on the Retry-After path with a live cancellation: the
// cancel fires DURING the 10s backoff sleep (not before the loop), and
// the middleware must wake immediately with errSyncCanceled instead of
// serving out the sleep.
func TestFetchWithRetryCancelDuringBackoffSleepReturnsPromptly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	fetcher := func(context.Context, rawProviderRequest, string) ([]byte, error) {
		attempts++
		// Cancel only once the middleware is committed to sleeping 10s.
		time.AfterFunc(50*time.Millisecond, cancel)
		return nil, &googleHealthHTTPError{StatusCode: 429, RetryAfter: 10 * time.Second}
	}
	started := time.Now()
	_, err := fetchWithRetry(ctx, fetcher, sleepWithCancel, noopRetryJitter, rawProviderRequest{}, "tok")
	if !errors.Is(err, errSyncCanceled) {
		t.Fatalf("err = %v, want errSyncCanceled", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("fetchWithRetry took %v, want prompt return (cancel must cut the Retry-After sleep short)", elapsed)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func errorContains(err error, want string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), want)
}

func TestParseRetryAfterAcceptsDeltaSecondsAndIgnoresHTTPDate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"5", 5 * time.Second},
		{"  30  ", 30 * time.Second},
		{"Sat, 06 Jun 2026 00:00:00 GMT", 0}, // HTTP-date — intentionally ignored
		{"-1", 0},
		{"not a number", 0},
	}
	for _, tc := range cases {
		got := parseRetryAfter(tc.header)
		if got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestDefaultRetryJitterStaysWithinExpectedWindow(t *testing.T) {
	t.Parallel()
	base := 1 * time.Second
	for i := 0; i < 200; i++ {
		got := defaultRetryJitter(base)
		if got < base || got >= base+base/4 {
			t.Fatalf("defaultRetryJitter(%v) = %v, want in [%v, %v)", base, got, base, base+base/4)
		}
	}
	if got := defaultRetryJitter(0); got != 0 {
		t.Fatalf("defaultRetryJitter(0) = %v, want 0", got)
	}
}

func TestDefaultRetryJitterDoesNotPanicOnTinyDelays(t *testing.T) {
	t.Parallel()
	// delay/4 < 1 would feed 0 to rand.Int64N and panic; guard returns
	// the delay unchanged for these sub-window cases.
	for _, delay := range []time.Duration{1, 2, 3} {
		got := defaultRetryJitter(delay)
		if got != delay {
			t.Errorf("defaultRetryJitter(%v) = %v, want delay unchanged when window collapses", delay, got)
		}
	}
}

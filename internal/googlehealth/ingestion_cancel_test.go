package googlehealth

import (
	"context"
	"errors"
	"github.com/BramVR/gohealthcli/internal/archived"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

// blockingDoer is an Doer whose Do blocks until the request's
// context is canceled, then returns the transport-shaped error
// net/http produces when an in-flight request is aborted. It simulates
// a stalled Provider fetch so cancellation tests can prove SIGINT
// aborts mid-HTTP rather than waiting for the page boundary (#284).
type blockingDoer struct {
	enterOnce sync.Once
	entered   chan struct{}
}

func newBlockingDoer() *blockingDoer {
	return &blockingDoer{entered: make(chan struct{})}
}

func (doer *blockingDoer) Do(request *http.Request) (*http.Response, error) {
	doer.enterOnce.Do(func() { close(doer.entered) })
	<-request.Context().Done()
	return nil, &url.Error{Op: "Get", URL: request.URL.String(), Err: request.Context().Err()}
}

// TestIngestionCancelAbortsInFlightProviderFetch is the #284 headline
// pin at the ingestion interface: with the production fetch path wired
// over a doer that never returns, canceling the context must abort the
// in-flight Provider request promptly and surface ErrSyncCanceled —
// not hang until a page boundary that will never come, and not surface
// the transport's wrapping of context.Canceled as a sync failure.
func TestIngestionCancelAbortsInFlightProviderFetch(t *testing.T) {
	t.Parallel()
	doer := newBlockingDoer()
	// Production wiring shape: the single-attempt FetchRaw over the
	// (blocking) doer, wrapped by NewIngestion's retry middleware —
	// the same composition main's runtime adapters seam binds.
	fetch := func(ctx context.Context, request RawRequest, accessToken string) ([]byte, error) {
		return FetchRaw(ctx, doer, request, accessToken)
	}
	ingestion := NewIngestion(fetch, func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) })
	archive := &fakeGoogleHealthIngestionArchive{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type executeOutcome struct {
		result IngestionResult
		err    error
	}
	done := make(chan executeOutcome, 1)
	go func() {
		result, err := ingestion.Execute(ctx, archive, IngestionRequest{
			Connection:  archived.Connection{ID: "googlehealth:111111256096816351", ProviderName: "googlehealth"},
			DataType:    "steps",
			From:        "2026-01-01",
			To:          "2026-01-02T00:00:00Z",
			AccessToken: "access-token",
		})
		done <- executeOutcome{result: result, err: err}
	}()

	select {
	case <-doer.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Provider fetch never started")
	}
	cancel()

	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, ErrSyncCanceled) {
			t.Fatalf("Execute err = %v, want ErrSyncCanceled (in-flight abort must surface as cancellation)", outcome.err)
		}
		if outcome.result.DataPointsSeen != 0 || outcome.result.RollupsSeen != 0 {
			t.Fatalf("Execute counted %d Data Points / %d Rollups, want 0/0 (the fetch never returned a page)", outcome.result.DataPointsSeen, outcome.result.RollupsSeen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return within 2s of cancel; the in-flight request was not aborted")
	}
	if len(archive.dataPoints) != 0 || len(archive.rollups) != 0 {
		t.Fatalf("archive received %d points / %d rollups, want none", len(archive.dataPoints), len(archive.rollups))
	}
}

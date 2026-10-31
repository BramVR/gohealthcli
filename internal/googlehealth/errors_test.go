package googlehealth

import (
	"context"
	"errors"
	"testing"

	"github.com/BramVR/gohealthcli/internal/archived"
)

// TestSyncIngestionNormalizesTypedUnauthorizedPreservingChain pins the
// issue #272 chain AC on the Sync Run ingestion path: an upstream
// typed HTTP 401 during pagination surfaces as the shared "run
// `gohealthcli connect` again" category with the typed
// HTTPError still reachable via errors.As — instead of the
// historical text-matched errors.New that discarded the cause chain.
func TestSyncIngestionNormalizesTypedUnauthorizedPreservingChain(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{}
	provider := funcIngestionProvider(func(request RawRequest, accessToken string) ([]byte, error) {
		return nil, &HTTPError{StatusCode: 401}
	})
	ingestion := midRunRefreshTestIngestion(t, provider)

	_, err := ingestion.Execute(context.Background(), archive, IngestionRequest{
		Connection:  archived.Connection{ID: "googlehealth:111"},
		DataType:    "steps",
		From:        "2026-01-01T00:00:00Z",
		To:          "2026-01-02T00:00:00Z",
		AccessToken: "revoked-access",
	})
	if err == nil {
		t.Fatal("Execute returned nil, want normalized unauthorized error")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized category", err)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 401 {
		t.Fatalf("err = %v, want typed HTTPError with status 401 preserved in the chain", err)
	}
	if err.Error() != ErrUnauthorized.Error() {
		t.Fatalf("err.Error() = %q, want the historical message %q verbatim", err.Error(), ErrUnauthorized.Error())
	}
}

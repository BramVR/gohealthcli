package main

import (
	"errors"
	"strings"
	"testing"
)

// TestGoogleHealthIngestionStoresTcxAttachmentForExercise pins #107
// slice D: when exercise sync archives an exercise Data Point and
// Google's `exportExerciseTcx` returns bytes, the bytes are stored as a
// `tcx`-kind Attachment linked to the upserted Data Point. The Attachment
// Store call happens after the upsert so the data_point row exists when
// the FK is enforced.
func TestGoogleHealthIngestionStoresTcxAttachmentForExercise(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/exercise/dataPoints/run-1",
				"dataSource": {"platform": "FITBIT"},
				"exercise": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:30:00Z"
					},
					"exerciseType": "RUNNING"
				}
			}]
		}`,
	})
	tcxBody := `<?xml version="1.0"?><TrainingCenterDatabase>fixture</TrainingCenterDatabase>`
	provider.pages["users/me/dataTypes/exercise/dataPoints/run-1:exportExerciseTcx"] = tcxBody
	ingestion := fakeGoogleHealthIngestion(provider)

	_, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "exercise",
		from:     "2026-01-01",
		to:       "2026-01-02",
	}))
	if err != nil {
		t.Fatalf("ingest exercise: %v", err)
	}

	if len(archive.attachments) != 1 {
		t.Fatalf("attachment count = %d, want 1; archive = %#v", len(archive.attachments), archive.attachments)
	}
	got := archive.attachments[0]
	if got.kind != "tcx" {
		t.Fatalf("attachment kind = %q, want tcx", got.kind)
	}
	if got.point.upstreamResourceName != "users/me/dataTypes/exercise/dataPoints/run-1" {
		t.Fatalf("attachment linked to %q, want run-1", got.point.upstreamResourceName)
	}
	if string(got.payload) != tcxBody {
		t.Fatalf("attachment payload = %q, want fixture TCX body", string(got.payload))
	}
	// The TCX export endpoint must be hit exactly once (one exercise DP).
	tcxRequests := 0
	for _, r := range provider.requests {
		if r.endpointName == "dataTypes.exercise.exportExerciseTcx" {
			tcxRequests++
		}
	}
	if tcxRequests != 1 {
		t.Fatalf("exportExerciseTcx request count = %d, want 1; requests = %#v", tcxRequests, provider.requests)
	}
}

// TestGoogleHealthIngestionSkipsTcxWhenUpstream404 pins the graceful
// degradation case: when `exportExerciseTcx` returns HTTP 404 (no TCX
// available for that exercise — e.g., manually-entered, no GPS), sync
// must remain successful and no Attachment row is inserted.
func TestGoogleHealthIngestionSkipsTcxWhenUpstream404(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/exercise/dataPoints/no-gps",
				"dataSource": {"platform": "FITBIT"},
				"exercise": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:30:00Z"
					},
					"exerciseType": "YOGA"
				}
			}]
		}`,
	})
	provider.errorByPageKey = map[string]error{
		"users/me/dataTypes/exercise/dataPoints/no-gps:exportExerciseTcx": &googleHealthHTTPError{StatusCode: 404},
	}
	ingestion := fakeGoogleHealthIngestion(provider)

	result, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "exercise",
		from:     "2026-01-01",
		to:       "2026-01-02",
	}))
	if err != nil {
		t.Fatalf("ingest must remain green on 404 TCX export, got %v", err)
	}
	if result.dataPointsSeen != 1 || result.dataPointsNew != 1 {
		t.Fatalf("data point counts = (seen=%d, new=%d), want (1, 1)", result.dataPointsSeen, result.dataPointsNew)
	}
	if len(archive.attachments) != 0 {
		t.Fatalf("attachment count = %d, want 0 on 404; archive = %#v", len(archive.attachments), archive.attachments)
	}
}

// TestGoogleHealthIngestionSkipsTcxWhenUpstream403 pins the
// scope-mismatch case observed live: with only
// `activity_and_fitness.readonly` granted, Google returns HTTP 403 on
// `exportExerciseTcx`. The exercise Data Point itself is already
// archived; tanking the whole sync because the optional sidecar is
// forbidden is wrong. Skip the sidecar, keep sync green.
func TestGoogleHealthIngestionSkipsTcxWhenUpstream403(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/exercise/dataPoints/forbidden",
				"dataSource": {"platform": "FITBIT"},
				"exercise": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:30:00Z"
					},
					"exerciseType": "RUNNING"
				}
			}]
		}`,
	})
	provider.errorByPageKey = map[string]error{
		"users/me/dataTypes/exercise/dataPoints/forbidden:exportExerciseTcx": &googleHealthHTTPError{StatusCode: 403},
	}
	ingestion := fakeGoogleHealthIngestion(provider)

	result, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "exercise",
		from:     "2026-01-01",
		to:       "2026-01-02",
	}))
	if err != nil {
		t.Fatalf("ingest must remain green on 403 TCX export, got %v", err)
	}
	if result.dataPointsSeen != 1 || result.dataPointsNew != 1 {
		t.Fatalf("data point counts = (seen=%d, new=%d), want (1, 1)", result.dataPointsSeen, result.dataPointsNew)
	}
	if len(archive.attachments) != 0 {
		t.Fatalf("attachment count = %d, want 0 on 403; archive = %#v", len(archive.attachments), archive.attachments)
	}
}

// TestGoogleHealthIngestionSkipsTcxWhenUpstreamEmpty pins the
// degenerate case where Google returns HTTP 200 with an empty body —
// nothing meaningful to archive, sync stays green, no row inserted.
func TestGoogleHealthIngestionSkipsTcxWhenUpstreamEmpty(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/exercise/dataPoints/empty",
				"dataSource": {"platform": "FITBIT"},
				"exercise": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:30:00Z"
					},
					"exerciseType": "RUNNING"
				}
			}]
		}`,
		"users/me/dataTypes/exercise/dataPoints/empty:exportExerciseTcx": "",
	})
	ingestion := fakeGoogleHealthIngestion(provider)

	_, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "exercise",
		from:     "2026-01-01",
		to:       "2026-01-02",
	}))
	if err != nil {
		t.Fatalf("ingest must remain green on empty TCX body, got %v", err)
	}
	if len(archive.attachments) != 0 {
		t.Fatalf("attachment count = %d, want 0 on empty body", len(archive.attachments))
	}
}

// TestGoogleHealthIngestionSurfacesTcxNon404Errors pins the inverse: a
// 5xx on the TCX endpoint is NOT silently ignored — sync should fail so
// the cursor stays put and the user knows to retry. (A 401 still has
// to be reported clearly per syncProviderRequestError.)
func TestGoogleHealthIngestionSurfacesTcxNon404Errors(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/exercise/dataPoints/boom",
				"dataSource": {"platform": "FITBIT"},
				"exercise": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:30:00Z"
					},
					"exerciseType": "RUNNING"
				}
			}]
		}`,
	})
	provider.errorByPageKey = map[string]error{
		"users/me/dataTypes/exercise/dataPoints/boom:exportExerciseTcx": errors.New("upstream blew up"),
	}
	ingestion := fakeGoogleHealthIngestion(provider)

	_, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "exercise",
		from:     "2026-01-01",
		to:       "2026-01-02",
	}))
	if err == nil {
		t.Fatalf("ingest must surface non-404 TCX errors")
	}
	if !strings.Contains(err.Error(), "upstream blew up") {
		t.Fatalf("error %q does not surface upstream cause", err)
	}
}

// TestGoogleHealthIngestionDoesNotCallTcxForNonExerciseDataTypes guards
// the routing: TCX export is only attempted for exercise sync, never
// for steps / sleep / heart-rate etc.
func TestGoogleHealthIngestionDoesNotCallTcxForNonExerciseDataTypes(t *testing.T) {
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/one",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:15:00Z"
					}
				}
			}]
		}`,
	})
	ingestion := fakeGoogleHealthIngestion(provider)

	_, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "steps",
		from:     "2026-01-01",
		to:       "2026-01-02T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("ingest steps: %v", err)
	}
	for _, r := range provider.requests {
		if r.endpointName == "dataTypes.exercise.exportExerciseTcx" {
			t.Fatalf("exportExerciseTcx called for non-exercise sync: %#v", r)
		}
	}
	if len(archive.attachments) != 0 {
		t.Fatalf("attachment count = %d, want 0 for steps sync", len(archive.attachments))
	}
}

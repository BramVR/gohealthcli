package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestGoogleHealthIngestionStoresTcxAttachmentForExercise pins #107
// slice D + #187: when exercise sync archives an exercise Data Point
// and Google's `exportExerciseTcx` returns the JSON envelope
// `{"tcxData":"<xml>"}`, the hook unwraps the envelope and stores the
// raw TCX XML — not the envelope bytes — as the `tcx`-kind Attachment.
// The Attachment Store call happens after the upsert so the data_point
// row exists when the FK is enforced.
func TestGoogleHealthIngestionStoresTcxAttachmentForExercise(t *testing.T) {
	t.Parallel()
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
	// Google's exportExerciseTcx wraps the XML in a JSON envelope with
	// a single `tcxData` string field. The hook MUST unwrap before
	// archiving — otherwise the sidecar bytes do not parse as TCX.
	tcxXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<TrainingCenterDatabase xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2">` +
		`<Activities><Activity Sport="Running"><Id>2026-01-01T08:00:00Z</Id></Activity></Activities>` +
		`</TrainingCenterDatabase>`
	envelope, err := json.Marshal(struct {
		TcxData string `json:"tcxData"`
	}{TcxData: tcxXML})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	provider.pages["users/me/dataTypes/exercise/dataPoints/run-1:exportExerciseTcx"] = string(envelope)
	ingestion := fakeGoogleHealthIngestion(provider)

	_, err = ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType:      "exercise",
		from:          "2026-01-01",
		to:            "2026-01-02",
		grantedScopes: []string{googleHealthActivityReadonlyScope, googleHealthLocationReadonlyScope},
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
	if string(got.payload) != tcxXML {
		t.Fatalf("attachment payload = %q, want unwrapped TCX XML %q", string(got.payload), tcxXML)
	}
	if strings.Contains(string(got.payload), `"tcxData"`) {
		t.Fatalf("attachment payload still contains JSON envelope; got %q", string(got.payload))
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

// TestGoogleHealthIngestionStoresRawTcxWhenResponseIsNotJsonEnvelope
// pins the #187 defensive fallback: if `exportExerciseTcx` returns raw
// XML bytes (a future shape Google might emit, or a transitional
// response), the hook stores the bytes verbatim instead of dropping
// the sidecar. The principle: the archive stays useful even when the
// upstream shape changes.
func TestGoogleHealthIngestionStoresRawTcxWhenResponseIsNotJsonEnvelope(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/exercise/dataPoints/raw-xml",
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
	rawXML := `<?xml version="1.0"?><TrainingCenterDatabase>raw</TrainingCenterDatabase>`
	provider.pages["users/me/dataTypes/exercise/dataPoints/raw-xml:exportExerciseTcx"] = rawXML
	ingestion := fakeGoogleHealthIngestion(provider)

	_, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType:      "exercise",
		from:          "2026-01-01",
		to:            "2026-01-02",
		grantedScopes: []string{googleHealthActivityReadonlyScope, googleHealthLocationReadonlyScope},
	}))
	if err != nil {
		t.Fatalf("ingest must succeed when response is not a JSON envelope, got %v", err)
	}
	if len(archive.attachments) != 1 {
		t.Fatalf("attachment count = %d, want 1; archive = %#v", len(archive.attachments), archive.attachments)
	}
	if string(archive.attachments[0].payload) != rawXML {
		t.Fatalf("attachment payload = %q, want raw XML stored verbatim %q",
			string(archive.attachments[0].payload), rawXML)
	}
}

// TestGoogleHealthIngestionStoresVerbatimWhenJsonShapeUnexpected pins
// the #187 defensive fallback for a JSON response that is valid JSON
// but missing the `tcxData` field — e.g. Google introducing a wrapper
// field rename or returning an error envelope shape. The bytes are
// archived verbatim so a re-sync after a shape fix produces a new
// SHA and the operator can detect the regression via `doctor`.
func TestGoogleHealthIngestionStoresVerbatimWhenJsonShapeUnexpected(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/exercise/dataPoints/unexpected",
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
	unexpectedJSON := `{"unexpectedField": "future-shape"}`
	provider.pages["users/me/dataTypes/exercise/dataPoints/unexpected:exportExerciseTcx"] = unexpectedJSON
	ingestion := fakeGoogleHealthIngestion(provider)

	_, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType:      "exercise",
		from:          "2026-01-01",
		to:            "2026-01-02",
		grantedScopes: []string{googleHealthActivityReadonlyScope, googleHealthLocationReadonlyScope},
	}))
	if err != nil {
		t.Fatalf("ingest must succeed on unexpected JSON shape, got %v", err)
	}
	if len(archive.attachments) != 1 {
		t.Fatalf("attachment count = %d, want 1; archive = %#v", len(archive.attachments), archive.attachments)
	}
	if string(archive.attachments[0].payload) != unexpectedJSON {
		t.Fatalf("attachment payload = %q, want verbatim JSON %q",
			string(archive.attachments[0].payload), unexpectedJSON)
	}
}

// TestGoogleHealthIngestionSkipsTcxWhenEnvelopeTcxDataEmpty pins the
// #187 edge case: a JSON envelope with an empty `tcxData` string is
// semantically equivalent to "no TCX content" — re-fire the empty-body
// guard against the unwrapped bytes so we don't archive a zero-byte
// sidecar that exists only because the envelope itself was non-empty.
func TestGoogleHealthIngestionSkipsTcxWhenEnvelopeTcxDataEmpty(t *testing.T) {
	t.Parallel()
	archive := &fakeGoogleHealthIngestionArchive{dataPointStatuses: []string{"new"}}
	provider := newFakeGoogleHealthIngestionProvider(t, "access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/exercise/dataPoints/empty-envelope",
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
		"users/me/dataTypes/exercise/dataPoints/empty-envelope:exportExerciseTcx": `{"tcxData":""}`,
	})
	ingestion := fakeGoogleHealthIngestion(provider)

	_, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType:      "exercise",
		from:          "2026-01-01",
		to:            "2026-01-02",
		grantedScopes: []string{googleHealthActivityReadonlyScope, googleHealthLocationReadonlyScope},
	}))
	if err != nil {
		t.Fatalf("ingest must remain green on empty tcxData, got %v", err)
	}
	if len(archive.attachments) != 0 {
		t.Fatalf("attachment count = %d, want 0 on empty tcxData", len(archive.attachments))
	}
}

// TestGoogleHealthIngestionSkipsTcxWhenUpstream404 pins the graceful
// degradation case: when `exportExerciseTcx` returns HTTP 404 (no TCX
// available for that exercise — e.g., manually-entered, no GPS), sync
// must remain successful and no Attachment row is inserted.
func TestGoogleHealthIngestionSkipsTcxWhenUpstream404(t *testing.T) {
	t.Parallel()
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
		dataType:      "exercise",
		from:          "2026-01-01",
		to:            "2026-01-02",
		grantedScopes: []string{googleHealthActivityReadonlyScope, googleHealthLocationReadonlyScope},
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
	t.Parallel()
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
		dataType:      "exercise",
		from:          "2026-01-01",
		to:            "2026-01-02",
		grantedScopes: []string{googleHealthActivityReadonlyScope, googleHealthLocationReadonlyScope},
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
	// Sanity: the test only proves 403-handling if exportExerciseTcx
	// was actually attempted. Without this, an unrelated regression
	// that silently skipped the hook would still pass.
	tcxRequested := false
	for _, req := range provider.requests {
		if strings.Contains(req.url, ":exportExerciseTcx") {
			tcxRequested = true
			break
		}
	}
	if !tcxRequested {
		t.Fatalf("expected the hook to call exportExerciseTcx; requests=%+v", provider.requests)
	}
}

// TestGoogleHealthIngestionSkipsTcxWhenUpstreamEmpty pins the
// degenerate case where Google returns HTTP 200 with an empty body —
// nothing meaningful to archive, sync stays green, no row inserted.
func TestGoogleHealthIngestionSkipsTcxWhenUpstreamEmpty(t *testing.T) {
	t.Parallel()
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
		dataType:      "exercise",
		from:          "2026-01-01",
		to:            "2026-01-02",
		grantedScopes: []string{googleHealthActivityReadonlyScope, googleHealthLocationReadonlyScope},
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
	t.Parallel()
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
		dataType:      "exercise",
		from:          "2026-01-01",
		to:            "2026-01-02",
		grantedScopes: []string{googleHealthActivityReadonlyScope, googleHealthLocationReadonlyScope},
	}))
	if err == nil {
		t.Fatalf("ingest must surface non-404 TCX errors")
	}
	if !strings.Contains(err.Error(), "upstream blew up") {
		t.Fatalf("error %q does not surface upstream cause", err)
	}
}

// TestGoogleHealthIngestionSkipsTcxWhenLocationScopeNotGranted pins
// #140: when the stored Connection's granted-scopes set does NOT
// include `googlehealth.location.readonly`, exercise sync archives the
// Data Point but does NOT round-trip to `exportExerciseTcx` at all.
// Google requires both `activity_and_fitness.readonly` AND
// `location.readonly` for that endpoint; without the second scope the
// call deterministically returns 403, so skipping pre-emptively saves
// one HTTP round-trip per exercise Data Point. Users opt in via
// `gohealthcli connect --add-scopes tcx`.
func TestGoogleHealthIngestionSkipsTcxWhenLocationScopeNotGranted(t *testing.T) {
	t.Parallel()
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
	// Seed a TCX body so the test fails loudly if the hook DID fire.
	provider.pages["users/me/dataTypes/exercise/dataPoints/run-1:exportExerciseTcx"] = `<?xml version="1.0"?>`
	ingestion := fakeGoogleHealthIngestion(provider)

	result, err := ingestion.Execute(archive, fakeGoogleHealthIngestionRequest(googleHealthIngestionRequest{
		dataType: "exercise",
		from:     "2026-01-01",
		to:       "2026-01-02",
		// Only the base activity scope — the location scope that
		// authorises exportExerciseTcx is NOT granted.
		grantedScopes: []string{googleHealthActivityReadonlyScope},
	}))
	if err != nil {
		t.Fatalf("ingest must remain green without location scope, got %v", err)
	}
	if result.dataPointsSeen != 1 || result.dataPointsNew != 1 {
		t.Fatalf("data point counts = (seen=%d, new=%d), want (1, 1)", result.dataPointsSeen, result.dataPointsNew)
	}
	if len(archive.attachments) != 0 {
		t.Fatalf("attachment count = %d, want 0 when location.readonly not granted", len(archive.attachments))
	}
	for _, req := range provider.requests {
		if strings.Contains(req.url, ":exportExerciseTcx") {
			t.Fatalf("exportExerciseTcx must not be called when location.readonly is not granted; got request %+v", req)
		}
	}
}

// TestGoogleHealthIngestionDoesNotCallTcxForNonExerciseDataTypes guards
// the routing: TCX export is only attempted for exercise sync, never
// for steps / sleep / heart-rate etc.
func TestGoogleHealthIngestionDoesNotCallTcxForNonExerciseDataTypes(t *testing.T) {
	t.Parallel()
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

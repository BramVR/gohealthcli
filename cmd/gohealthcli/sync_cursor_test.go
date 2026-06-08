package main

import (
	"strings"
	"testing"
	"time"
)

func TestStatusSurfacesCursorOnlyDataTypeWithZeroCounts(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	connection, err := archive.CurrentConnection()
	if err != nil {
		archive.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	if err := archive.CommitSyncCursor(syncCursorKey{
		connectionID: connection.id,
		dataType:     "blood-glucose",
		rollupKind:   syncCursorRollupKindNone,
	}, syncRunOutcomeCompleted, "2026-01-02T00:00:00Z", "2026-01-02T00:00:01Z"); err != nil {
		archive.Close()
		t.Fatalf("CommitSyncCursor: %v", err)
	}
	archive.Close()

	reader, err := openHealthArchiveReader(archivePath)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()
	summary, err := reader.StatusSummary()
	if err != nil {
		t.Fatalf("StatusSummary: %v", err)
	}
	var found *statusDataType
	for index := range summary.DataTypes {
		if summary.DataTypes[index].DataType == "blood-glucose" {
			found = &summary.DataTypes[index]
			break
		}
	}
	if found == nil {
		t.Fatalf("blood-glucose missing from status: %+v", summary.DataTypes)
	}
	if found.DataPointCount != 0 || found.RollupCount != 0 {
		t.Fatalf("blood-glucose counts = (%d, %d), want (0, 0) for cursor-only Data Type", found.DataPointCount, found.RollupCount)
	}
	if len(found.SyncCursors) != 1 || found.SyncCursors[0].CursorTime != "2026-01-02T00:00:00Z" {
		t.Fatalf("blood-glucose cursors = %+v, want one entry with cursor_time 2026-01-02T00:00:00Z", found.SyncCursors)
	}
}

func TestSyncCursorResolveReturnsZeroWhenNoCursorExists(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	cursorTime, found, err := archive.ResolveSyncCursor(syncCursorKey{
		connectionID: connection.id,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	})
	if err != nil {
		t.Fatalf("ResolveSyncCursor: %v", err)
	}
	if found {
		t.Fatalf("Sync Cursor unexpectedly exists with cursor_time = %q", cursorTime)
	}
}

func TestSyncCursorCommitOnlyAdvancesOnSyncCompleted(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	key := syncCursorKey{
		connectionID: connection.id,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	}

	// Failed commit before any cursor exists must not create one.
	if err := archive.CommitSyncCursor(key, syncRunOutcomeFailed, "2026-01-05T00:00:00Z", "2026-01-05T01:00:00Z"); err != nil {
		t.Fatalf("CommitSyncCursor(failed): %v", err)
	}
	if _, found, err := archive.ResolveSyncCursor(key); err != nil || found {
		t.Fatalf("after failed commit: found=%v err=%v, want absent", found, err)
	}

	// Canceled commit before any cursor exists must also not create one.
	if err := archive.CommitSyncCursor(key, syncRunOutcomeCanceled, "2026-01-05T00:00:00Z", "2026-01-05T01:00:00Z"); err != nil {
		t.Fatalf("CommitSyncCursor(canceled): %v", err)
	}
	if _, found, err := archive.ResolveSyncCursor(key); err != nil || found {
		t.Fatalf("after canceled commit: found=%v err=%v, want absent", found, err)
	}

	// Completed commit advances the cursor.
	if err := archive.CommitSyncCursor(key, syncRunOutcomeCompleted, "2026-01-05T00:00:00Z", "2026-01-05T01:00:00Z"); err != nil {
		t.Fatalf("CommitSyncCursor(completed): %v", err)
	}
	cursorTime, found, err := archive.ResolveSyncCursor(key)
	if err != nil || !found || cursorTime != "2026-01-05T00:00:00Z" {
		t.Fatalf("after completed commit: cursorTime=%q found=%v err=%v, want 2026-01-05T00:00:00Z", cursorTime, found, err)
	}

	// A subsequent failed commit must not move the cursor backwards.
	if err := archive.CommitSyncCursor(key, syncRunOutcomeFailed, "2026-01-10T00:00:00Z", "2026-01-10T01:00:00Z"); err != nil {
		t.Fatalf("CommitSyncCursor(failed after completed): %v", err)
	}
	cursorTime, _, _ = archive.ResolveSyncCursor(key)
	if cursorTime != "2026-01-05T00:00:00Z" {
		t.Fatalf("after failed commit: cursorTime=%q, want unchanged 2026-01-05T00:00:00Z", cursorTime)
	}

	// A subsequent canceled commit must not move the cursor either.
	if err := archive.CommitSyncCursor(key, syncRunOutcomeCanceled, "2026-01-12T00:00:00Z", "2026-01-12T01:00:00Z"); err != nil {
		t.Fatalf("CommitSyncCursor(canceled after completed): %v", err)
	}
	cursorTime, _, _ = archive.ResolveSyncCursor(key)
	if cursorTime != "2026-01-05T00:00:00Z" {
		t.Fatalf("after canceled commit: cursorTime=%q, want unchanged 2026-01-05T00:00:00Z", cursorTime)
	}

	// Another completed commit advances the cursor forward.
	if err := archive.CommitSyncCursor(key, syncRunOutcomeCompleted, "2026-01-20T00:00:00Z", "2026-01-20T01:00:00Z"); err != nil {
		t.Fatalf("CommitSyncCursor(completed advance): %v", err)
	}
	cursorTime, _, _ = archive.ResolveSyncCursor(key)
	if cursorTime != "2026-01-20T00:00:00Z" {
		t.Fatalf("after second completed commit: cursorTime=%q, want 2026-01-20T00:00:00Z", cursorTime)
	}
}

func TestSyncRunExecutorResumesFromSyncCursorWhenFromOmitted(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	// First sync: explicit --from, succeeds, advances cursor.
	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	first, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-03T00:00:00Z",
	})
	if err != nil || first.Status != "sync_completed" {
		t.Fatalf("first sync: status=%q err=%v, want sync_completed", first.Status, err)
	}

	// Second sync: omit --from, expect resume from the cursor.
	testRuntime, secondRequests := withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	second, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		to:          "2026-01-05T00:00:00Z",
	})
	if err != nil || second.Status != "sync_completed" {
		t.Fatalf("resumed sync: status=%q err=%v, want sync_completed", second.Status, err)
	}
	if !second.ResumedFromCursor {
		t.Fatalf("ResumedFromCursor = false, want true")
	}
	if second.From != "2026-01-03T00:00:00Z" {
		t.Fatalf("resumed From = %q, want 2026-01-03T00:00:00Z (the prior cursor_time)", second.From)
	}
	if len(*secondRequests) == 0 {
		t.Fatalf("resumed sync issued no upstream requests")
	}
}

func TestSyncRunExecutorErrorsClearlyWhenCursorMissingAndNoFrom(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
	})
	if err == nil {
		t.Fatal("execute Sync Run error = nil, want missing-cursor failure")
	}
	if result.Status != "sync_failed" || !strings.Contains(err.Error(), "set --from for the initial backfill") {
		t.Fatalf("result = (%q, %v), want sync_failed with initial-backfill hint", result.Status, err)
	}
}

func TestSyncRunExecutorDoesNotCreateCursorWhenFirstRunFails(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{`, // unparseable response forces the first sync to fail
	})
	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err == nil || result.Status != "sync_failed" {
		t.Fatalf("first sync: status=%q err=%v, want sync_failed", result.Status, err)
	}

	// ADR-0008 invariant: failed first sync must NOT create a cursor row.
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("reopen archive: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, found, err := archive.ResolveSyncCursor(syncCursorKey{
		connectionID: connection.id,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	}); err != nil || found {
		t.Fatalf("cursor present after failed first sync: found=%v err=%v, want absent", found, err)
	}
}

// TestSyncRunExecutorRoundTripsCursorThroughExactToString pins ADR-0008's
// contract: whatever string the prior Sync Run resolved as --to is exactly
// what the next sync sees as --from. The cursor does not transform formats
// (date-range vs RFC3339) — it stores the literal --to so callers passing
// inclusive dates and callers passing RFC3339 instants both round-trip.
func TestSyncRunExecutorRoundTripsCursorThroughExactToString(t *testing.T) {
	for _, format := range []struct {
		name   string
		first  string
		second string
	}{
		{"RFC3339 with seconds", "2026-01-02T00:00:00Z", "2026-01-04T00:00:00Z"},
		{"plain calendar date", "2026-01-02", "2026-01-04"},
	} {
		format := format
		t.Run(format.name, func(t *testing.T) {
			tempDir := t.TempDir()
			configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
			testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
				accessToken:        "connect-access-secret",
				refreshToken:       "connect-refresh-secret",
				healthUserID:       "111111256096816351",
				legacyFitbitUserID: "A1B2C3",
			})
			if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
				t.Fatalf("connect setup: %v", err)
			}
			testRuntime.now = func() time.Time { return time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC) }
			testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
				"": `{"dataPoints":[]}`,
			})
			first, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
				configPath:  configPath,
				archivePath: archivePath,
				dataTypes:   []string{"steps"},
				from:        "2026-01-01",
				to:          format.first,
			})
			if err != nil || first.Status != "sync_completed" {
				t.Fatalf("first sync: status=%q err=%v", first.Status, err)
			}

			testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
				"": `{"dataPoints":[]}`,
			})
			second, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
				configPath:  configPath,
				archivePath: archivePath,
				dataTypes:   []string{"steps"},
				to:          format.second,
			})
			if err != nil || second.Status != "sync_completed" {
				t.Fatalf("resumed sync: status=%q err=%v", second.Status, err)
			}
			if !second.ResumedFromCursor {
				t.Fatal("ResumedFromCursor = false on second sync")
			}
			if second.From != format.first {
				t.Fatalf("second.From = %q, want round-trip of first --to %q", second.From, format.first)
			}
		})
	}
}

// brokenCursorCommitWriter wraps an existing healthArchiveWriter and forces
// CommitSyncCursor to fail. It exists to exercise the reconciliation path
// where FinishSyncRun has already persisted sync_completed but the cursor
// commit then errors — the Sync Run row must be re-marked sync_failed so
// the audit trail and the cursor agree.
type brokenCursorCommitWriter struct {
	healthArchiveWriter
}

func (writer brokenCursorCommitWriter) CommitSyncCursor(syncCursorKey, syncRunOutcome, string, string) error {
	return errSimulatedCommitFailure
}

var errSimulatedCommitFailure = errSimulated("simulated cursor commit failure")

type errSimulated string

func (err errSimulated) Error() string { return string(err) }

func TestSyncRunReconcilesSyncRunWhenCursorCommitFailsAfterCompletion(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	t.Cleanup(func() { healthArchiveWriterOpenerForTest = openHealthArchiveWriter })
	healthArchiveWriterOpenerForTest = func(path string) (healthArchiveWriter, error) {
		inner, err := openHealthArchiveWriter(path)
		if err != nil {
			return nil, err
		}
		return brokenCursorCommitWriter{healthArchiveWriter: inner}, nil
	}

	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err == nil {
		t.Fatal("expected commit-cursor failure, got nil error")
	}
	if result.Status != "sync_failed" {
		t.Fatalf("result.Status = %q, want sync_failed", result.Status)
	}
	if !strings.Contains(err.Error(), "simulated cursor commit failure") {
		t.Fatalf("err = %v, want simulated commit failure", err)
	}

	// The Sync Run row should have been reconciled from sync_completed back
	// to sync_failed so the audit trail matches the cursor (still absent).
	assertSyncRunForDataType(t, archivePath, result.SyncRunID, "sync_failed", "steps", "list", 0, 0, 0, "simulated cursor commit failure")
}

func TestSyncRunExecutorPreservesCursorOnFailedRun(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	// First sync succeeds and seeds a cursor.
	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	if _, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}

	// Second sync archives one row then fails on the second page.
	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/partial-before-failure",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-02T08:00:00Z",
						"endTime": "2026-01-02T08:15:00Z"
					}
				}
			}],
			"nextPageToken": "bad-page"
		}`,
		"bad-page": `{`,
	})
	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-02",
		to:          "2026-01-04T00:00:00Z",
	})
	if err == nil {
		t.Fatal("partial sync error = nil, want failure")
	}
	if result.Status != "sync_failed" {
		t.Fatalf("status = %q, want sync_failed", result.Status)
	}

	// ADR-0008 invariant: failed run must NOT have advanced the cursor.
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("reopen archive: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	cursorTime, found, err := archive.ResolveSyncCursor(syncCursorKey{
		connectionID: connection.id,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	})
	if err != nil || !found {
		t.Fatalf("cursor missing after failed run: found=%v err=%v", found, err)
	}
	if cursorTime != "2026-01-02T00:00:00Z" {
		t.Fatalf("cursor_time = %q, want unchanged 2026-01-02T00:00:00Z (ADR-0008 invariant)", cursorTime)
	}
}

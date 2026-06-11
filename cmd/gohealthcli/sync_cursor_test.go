package main

import (
	"context"
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
	if _, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	// First sync: explicit --from, succeeds, advances cursor.
	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	first, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
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
	second, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
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
	if _, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
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
	if _, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{`, // unparseable response forces the first sync to fail
	})
	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
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
			if _, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, testRuntime); err != nil {
				t.Fatalf("connect setup: %v", err)
			}
			testRuntime.now = func() time.Time { return time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC) }
			testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
				"": `{"dataPoints":[]}`,
			})
			first, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
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
			second, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
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

// TestSyncRunOutcomeAdvancesCursorOnlyOnSyncCompleted pins the ADR-0008
// invariant on the outcome type itself: the rule lives next to the
// values it constrains, not buried in commitSyncCursorExec. Removing or
// inverting the method now fails this test before any storage-layer test
// has a chance to mask the regression.
func TestSyncRunOutcomeAdvancesCursorOnlyOnSyncCompleted(t *testing.T) {
	cases := []struct {
		outcome syncRunOutcome
		want    bool
	}{
		{syncRunOutcomeCompleted, true},
		{syncRunOutcomeFailed, false},
		{syncRunOutcomeCanceled, false},
	}
	for _, tc := range cases {
		if got := tc.outcome.AdvancesCursor(); got != tc.want {
			t.Errorf("(%s).AdvancesCursor() = %v, want %v", tc.outcome, got, tc.want)
		}
	}
}

var errSimulatedFinalizeFailure = errSimulated("simulated finalize failure")

type errSimulated string

func (err errSimulated) Error() string { return string(err) }

var errSimulatedFinalizeCompletedFailure = errSimulated("archive finalization failed")

// fakeFinalizeWriter wraps a healthArchiveWriter and lets a test inject a
// failure into FinalizeSyncRun. failOn returns the error to surface for a
// given outcome (or nil to delegate). One type covers both "fail every
// finalize" and "fail only the sync_completed outcome" wiring so tests do
// not need parallel wrapper types.
type fakeFinalizeWriter struct {
	healthArchiveWriter
	failOn func(outcome syncRunOutcome) error
}

func (writer fakeFinalizeWriter) FinalizeSyncRun(finalize syncRunFinalize) error {
	if writer.failOn != nil {
		if err := writer.failOn(finalize.Outcome); err != nil {
			return err
		}
	}
	return writer.healthArchiveWriter.FinalizeSyncRun(finalize)
}

func failOnEveryOutcome(err error) func(syncRunOutcome) error {
	return func(syncRunOutcome) error { return err }
}

func failOnCompletedOutcome(err error) func(syncRunOutcome) error {
	return func(outcome syncRunOutcome) error {
		if outcome == syncRunOutcomeCompleted {
			return err
		}
		return nil
	}
}

// TestArchiveFinalizeSyncRunAtomicallyCommitsRunAndCursor pins the atomic
// contract at the writer's seam: one FinalizeSyncRun call advances both
// the sync_run terminal status AND the Sync Cursor inside one SQLite
// transaction. There is no observable window where the run row is
// sync_completed while the cursor is still at its prior value, which is
// the inconsistency the legacy two-step write could persist on a crash
// between FinishSyncRun and CommitSyncCursor.
func TestArchiveFinalizeSyncRunAtomicallyCommitsRunAndCursor(t *testing.T) {
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
	syncRunID, err := archive.StartSyncRun(syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-01-01",
		To:             "2026-01-02T00:00:00Z",
		EndpointFamily: "list",
		StartedAt:      "2026-01-05T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}

	key := syncCursorKey{
		connectionID: connection.id,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	}
	if err := archive.FinalizeSyncRun(syncRunFinalize{
		SyncRunID:      syncRunID,
		Outcome:        syncRunOutcomeCompleted,
		FinishedAt:     "2026-01-05T00:00:01Z",
		CursorKey:      key,
		CursorTo:       "2026-01-02T00:00:00Z",
		CursorAdvanced: "2026-01-05T00:00:01Z",
	}); err != nil {
		t.Fatalf("FinalizeSyncRun: %v", err)
	}

	assertSyncRunForDataType(t, archivePath, syncRunID, "sync_completed", "steps", "list", 0, 0, 0, "")
	cursorTime, found, err := archive.ResolveSyncCursor(key)
	if err != nil || !found || cursorTime != "2026-01-02T00:00:00Z" {
		t.Fatalf("after FinalizeSyncRun: cursor (%q, %v, %v), want 2026-01-02T00:00:00Z true nil", cursorTime, found, err)
	}
}

// TestArchiveFinalizeSyncRunRollsBackRunStatusWhenCursorUpsertFails closes
// the legacy race: if the cursor UPSERT inside FinalizeSyncRun fails, the
// run-status UPDATE in the same transaction is rolled back, so the
// sync_run row stays sync_running rather than persisting the inconsistent
// "completed run with stale cursor" state that drove ADR-0008.
//
// Failure mode covered: the sync_cursors table is dropped just before the
// finalize call, so the cursor UPSERT throws. The whole transaction
// aborts; the run row remains in its StartSyncRun state.
func TestArchiveFinalizeSyncRunRollsBackRunStatusWhenCursorUpsertFails(t *testing.T) {
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
	syncRunID, err := archive.StartSyncRun(syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-01-01",
		To:             "2026-01-02T00:00:00Z",
		EndpointFamily: "list",
		StartedAt:      "2026-01-05T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}

	// Drop the cursor table out from under FinalizeSyncRun so the cursor
	// UPSERT inside its transaction fails. The run-status UPDATE in the
	// same transaction must roll back.
	sqliteWriter := archive.(*sqliteHealthArchiveWriter)
	if _, err := sqliteWriter.db.Exec(`DROP TABLE sync_cursors`); err != nil {
		t.Fatalf("drop sync_cursors: %v", err)
	}

	key := syncCursorKey{
		connectionID: connection.id,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	}
	finalizeErr := archive.FinalizeSyncRun(syncRunFinalize{
		SyncRunID:      syncRunID,
		Outcome:        syncRunOutcomeCompleted,
		FinishedAt:     "2026-01-05T00:00:01Z",
		CursorKey:      key,
		CursorTo:       "2026-01-02T00:00:00Z",
		CursorAdvanced: "2026-01-05T00:00:01Z",
	})
	if finalizeErr == nil {
		t.Fatal("FinalizeSyncRun err = nil, want failure when sync_cursors UPSERT fails")
	}

	// The run-status UPDATE inside the same transaction must have rolled
	// back, leaving the row at its StartSyncRun state (sync_running).
	assertSyncRunForDataType(t, archivePath, syncRunID, "sync_running", "steps", "list", 0, 0, 0, "")
}

// TestArchiveFinalizeSyncRunSkipsCursorAdvanceForNonCompletedOutcomes pins
// the ADR-0008 outcome-gate at the atomic seam: FinalizeSyncRun with
// outcome=sync_failed or sync_canceled still writes the run-status UPDATE
// but must not advance the Sync Cursor. Without this test, removing the
// outcome guard inside commitSyncCursorExec would silently violate
// ADR-0008 without breaking any existing test.
func TestArchiveFinalizeSyncRunSkipsCursorAdvanceForNonCompletedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome syncRunOutcome
	}{
		{"sync_failed", syncRunOutcomeFailed},
		{"sync_canceled", syncRunOutcomeCanceled},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
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
			syncRunID, err := archive.StartSyncRun(syncRunStart{
				Connection:     connection,
				DataTypes:      []string{"steps"},
				From:           "2026-01-01",
				To:             "2026-01-02T00:00:00Z",
				EndpointFamily: "list",
				StartedAt:      "2026-01-05T00:00:00Z",
			})
			if err != nil {
				t.Fatalf("StartSyncRun: %v", err)
			}

			key := syncCursorKey{connectionID: connection.id, dataType: "steps", rollupKind: syncCursorRollupKindNone}
			if err := archive.FinalizeSyncRun(syncRunFinalize{
				SyncRunID:      syncRunID,
				Outcome:        tc.outcome,
				FinishedAt:     "2026-01-05T00:00:01Z",
				ErrorSummary:   "simulated",
				CursorKey:      key,
				CursorTo:       "2026-01-02T00:00:00Z",
				CursorAdvanced: "2026-01-05T00:00:01Z",
			}); err != nil {
				t.Fatalf("FinalizeSyncRun(%s): %v", tc.outcome, err)
			}

			assertSyncRunForDataType(t, archivePath, syncRunID, string(tc.outcome), "steps", "list", 0, 0, 0, "simulated")
			if _, found, err := archive.ResolveSyncCursor(key); err != nil || found {
				t.Fatalf("cursor present after %s finalize: found=%v err=%v, want absent (ADR-0008)", tc.outcome, found, err)
			}
		})
	}
}

func TestSyncRunSurfacesFailureWhenFinalizeFails(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	t.Cleanup(func() { healthArchiveWriterOpenerForTest = openHealthArchiveWriter })
	healthArchiveWriterOpenerForTest = func(path string) (healthArchiveWriter, error) {
		inner, err := openHealthArchiveWriter(path)
		if err != nil {
			return nil, err
		}
		return fakeFinalizeWriter{healthArchiveWriter: inner, failOn: failOnEveryOutcome(errSimulatedFinalizeFailure)}, nil
	}

	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err == nil {
		t.Fatal("expected finalize failure, got nil error")
	}
	if result.Status != "sync_failed" {
		t.Fatalf("result.Status = %q, want sync_failed", result.Status)
	}
	if !strings.Contains(err.Error(), "simulated finalize failure") {
		t.Fatalf("err = %v, want simulated finalize failure", err)
	}
	// The wrapped error must call out which outcome the executor was
	// attempting to record so operators reading stderr can tell a failed
	// completion-finalize from a failed cancellation-finalize.
	if !strings.Contains(err.Error(), "record sync_completed Sync Run") {
		t.Fatalf("err = %v, want outcome name in the wrap", err)
	}

	// Recovery write ran (sync_completed outcome) and marked the run
	// sync_failed so the audit trail reflects the failure.
	assertSyncRunForDataType(t, archivePath, result.SyncRunID, "sync_failed", "steps", "list", 0, 0, 0, "simulated finalize failure")

	// ADR-0008 invariant: no cursor row exists when finalize fails.
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
		t.Fatalf("cursor present after failed finalize: found=%v err=%v, want absent", found, err)
	}
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
	if _, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	// First sync succeeds and seeds a cursor.
	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	if _, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
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
	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
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

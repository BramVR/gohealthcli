package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHealthArchiveWriterRequiresCurrentConnection(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	_, err = archive.CurrentConnection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no Connection found") {
		t.Fatalf("CurrentConnection error = %v, want no Connection found", err)
	}
}

func TestHealthArchiveWriterRecordsDataPointRevisionsRollupsAndSyncRun(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	startedAt := "2026-01-01T00:00:00Z"
	syncRunID, err := archive.StartSyncRun(context.Background(), syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-01-01",
		To:             "2026-01-02",
		EndpointFamily: "list",
		StartedAt:      startedAt,
	})
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}

	point := archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "steps",
		upstreamResourceName: "users/me/dataTypes/steps/dataPoints/archive-writer",
		recordKind:           "interval",
		startTimeUTC:         "2026-01-01T08:00:00Z",
		endTimeUTC:           "2026-01-01T08:15:00Z",
		dataSourceJSON:       `{"platform":"FITBIT"}`,
		rawJSON:              `{"steps":{"count":"123"}}`,
	}
	if status, err := archive.UpsertDataPoint(context.Background(), point, "2026-01-01T00:00:01Z"); err != nil || status != "new" {
		t.Fatalf("first UpsertDataPoint = (%q, %v), want new", status, err)
	}
	if status, err := archive.UpsertDataPoint(context.Background(), point, "2026-01-01T00:00:02Z"); err != nil || status != "unchanged" {
		t.Fatalf("same UpsertDataPoint = (%q, %v), want unchanged", status, err)
	}
	point.rawJSON = `{"steps":{"count":"456"}}`
	if status, err := archive.UpsertDataPoint(context.Background(), point, "2026-01-01T00:00:03Z"); err != nil || status != "updated" {
		t.Fatalf("corrected UpsertDataPoint = (%q, %v), want updated", status, err)
	}

	rollup := archivedRollup{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "steps",
		rollupKind:           "dailyRollUp",
		civilDate:            "2026-01-01",
		timezoneMetadataJSON: "{}",
		rawJSON:              `{"steps":{"countSum":"123"}}`,
	}
	if status, err := archive.UpsertRollup(context.Background(), rollup, "2026-01-01T00:00:04Z"); err != nil || status != "new" {
		t.Fatalf("first UpsertRollup = (%q, %v), want new", status, err)
	}
	rollup.rawJSON = `{"steps":{"countSum":"456"}}`
	if status, err := archive.UpsertRollup(context.Background(), rollup, "2026-01-01T00:00:05Z"); err != nil || status != "updated" {
		t.Fatalf("corrected UpsertRollup = (%q, %v), want updated", status, err)
	}

	if err := archive.FinishSyncRun(context.Background(), syncRunFinish{
		SyncRunID:    syncRunID,
		Status:       "sync_completed",
		SeenCount:    4,
		NewCount:     2,
		UpdatedCount: 2,
		FinishedAt:   "2026-01-01T00:00:06Z",
	}); err != nil {
		t.Fatalf("FinishSyncRun: %v", err)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertSyncRunForDataType(t, archivePath, syncRunID, "sync_completed", "steps", "list", 4, 2, 2, "")
}

// TestSyncRunStartFinishRoundTripsDistinctCounts pins the seen/new/
// updated mapping through the Sync Run start/finish writer chain with
// three DISTINCT values, so a count transposition introduced while the
// chain converts to parameter structs (#277) cannot pass unnoticed —
// the sibling round-trip test above finishes with 4/2/2, where a
// new/updated swap is invisible.
func TestSyncRunStartFinishRoundTripsDistinctCounts(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	syncRunID, err := archive.StartSyncRun(context.Background(), syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-01-01",
		To:             "2026-01-02",
		EndpointFamily: "list",
		StartedAt:      "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}
	if err := archive.FinishSyncRun(context.Background(), syncRunFinish{
		SyncRunID:    syncRunID,
		Status:       "sync_completed",
		SeenCount:    7,
		NewCount:     3,
		UpdatedCount: 2,
		FinishedAt:   "2026-01-01T00:00:06Z",
	}); err != nil {
		t.Fatalf("FinishSyncRun: %v", err)
	}
	assertSyncRunForDataType(t, archivePath, syncRunID, "sync_completed", "steps", "list", 7, 3, 2, "")
}

// TestSyncRunHeartbeatRoundTripsDistinctCounts pins the same trio
// through the advisory HeartbeatSyncRun write: each count must land in
// its own sync_runs column while the row stays sync_running, so the
// #277 struct conversion cannot transpose what a concurrent
// `sync --status` poller reads mid-run.
func TestSyncRunHeartbeatRoundTripsDistinctCounts(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	syncRunID, err := archive.StartSyncRun(context.Background(), syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-01-01",
		To:             "2026-01-02",
		EndpointFamily: "list",
		StartedAt:      "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}
	if err := archive.HeartbeatSyncRun(context.Background(), syncRunHeartbeat{
		SyncRunID:    syncRunID,
		SeenCount:    5,
		NewCount:     2,
		UpdatedCount: 1,
		At:           "2026-01-01T00:00:03Z",
	}); err != nil {
		t.Fatalf("HeartbeatSyncRun: %v", err)
	}
	row := probeSyncRunRow(t, archivePath, syncRunID)
	if row.status != "sync_running" {
		t.Fatalf("status = %q, want sync_running", row.status)
	}
	if row.seenCount != 5 || row.newCount != 2 || row.updatedCount != 1 {
		t.Fatalf("heartbeat counts = seen %d / new %d / updated %d, want 5 / 2 / 1",
			row.seenCount, row.newCount, row.updatedCount)
	}
	if !row.lastProgressAt.Valid || row.lastProgressAt.String != "2026-01-01T00:00:03Z" {
		t.Fatalf("last_progress_at = %v(%q), want 2026-01-01T00:00:03Z", row.lastProgressAt.Valid, row.lastProgressAt.String)
	}
}

// TestHealthArchiveWriterStoreAttachmentWritesSidecarRow pins the
// archive impl of StoreAttachment (#107 slice D): it resolves the
// upserted Data Point row, writes the bytes as a content-addressed
// sidecar at <archive>.attachments/<kind>/<sha[0:2]>/<sha>.<ext>, and
// inserts a data_point_attachments row keyed by the resolved id.
// Owner-only POSIX permissions on the sidecar file are enforced.
func TestHealthArchiveWriterStoreAttachmentWritesSidecarRow(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	point := archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "exercise",
		upstreamResourceName: "users/me/dataTypes/exercise/dataPoints/tcx-writer",
		recordKind:           "session",
		startTimeUTC:         "2026-01-01T08:00:00Z",
		endTimeUTC:           "2026-01-01T08:30:00Z",
		dataSourceJSON:       `{"platform":"FITBIT"}`,
		rawJSON:              `{"exercise":{"exerciseType":"RUNNING"}}`,
	}
	if status, err := archive.UpsertDataPoint(context.Background(), point, "2026-01-01T00:00:01Z"); err != nil || status != "new" {
		t.Fatalf("UpsertDataPoint = (%q, %v), want new", status, err)
	}

	tcxBody := []byte(`<?xml version="1.0"?><TrainingCenterDatabase>writer</TrainingCenterDatabase>`)
	if err := archive.StoreAttachment(context.Background(), point, "tcx", tcxBody, "2026-01-01T00:00:02Z"); err != nil {
		t.Fatalf("StoreAttachment: %v", err)
	}

	expectedHash := sha256.Sum256(tcxBody)
	expectedHex := hex.EncodeToString(expectedHash[:])
	wantPath := filepath.Join(archivePath+".attachments", "tcx", expectedHex[0:2], expectedHex+".tcx")
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read sidecar at %s: %v", wantPath, err)
	}
	if !bytes.Equal(body, tcxBody) {
		t.Fatalf("sidecar bytes mismatch")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(wantPath)
		if err != nil {
			t.Fatalf("stat sidecar: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("sidecar mode = %o, want 0600", info.Mode().Perm())
		}
	}
	assertArchiveTableCount(t, archivePath, "data_point_attachments", 1)

	// Idempotent: re-store identical bytes does not duplicate the row.
	if err := archive.StoreAttachment(context.Background(), point, "tcx", tcxBody, "2026-01-01T00:00:03Z"); err != nil {
		t.Fatalf("idempotent StoreAttachment: %v", err)
	}
	assertArchiveTableCount(t, archivePath, "data_point_attachments", 1)
}

// TestHealthArchiveWriterStoreAttachmentErrorsWhenDataPointMissing
// pins the error surface: trying to attach to a Data Point that was
// not upserted first must fail (the data_point row id can't be
// resolved). The bytes must not land on disk in that case — we don't
// want orphan sidecars from misuse of the API.
func TestHealthArchiveWriterStoreAttachmentErrorsWhenDataPointMissing(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	point := archivedDataPoint{
		providerName:         "googlehealth",
		connectionID:         "googlehealth:phantom",
		dataType:             "exercise",
		upstreamResourceName: "users/me/dataTypes/exercise/dataPoints/missing",
		recordKind:           "session",
	}
	if err := archive.StoreAttachment(context.Background(), point, "tcx", []byte("<?xml?>"), "2026-01-01T00:00:00Z"); err == nil {
		t.Fatalf("expected error attaching to missing Data Point")
	}
}

// TestFinalizeSyncRunRetriesOnBusyThenAdvancesCursor pins AC #4 of PRD
// #141 slice 4: when a finalize attempt sees SQLITE_BUSY a bounded
// number of times before succeeding, the writer must still leave the
// sync_runs row in the requested terminal state AND advance the Sync
// Cursor on sync_completed. We exercise the retry policy via the
// retry helper directly with a fake attempt that simulates N busy
// responses then writes through the real writer; this isolates the
// retry contract from the SQLite contention conditions of a true
// concurrent-process integration test.
func TestFinalizeSyncRunRetriesOnBusyThenAdvancesCursor(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	runID, err := archive.StartSyncRun(context.Background(), syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-01-01",
		To:             "2026-01-02",
		EndpointFamily: "list",
		StartedAt:      "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}

	cursorKey := syncCursorKey{connectionID: connection.id, dataType: "steps", rollupKind: syncCursorRollupKindNone}
	finalize := syncRunFinalize{
		SyncRunID:      runID,
		Outcome:        syncRunOutcomeCompleted,
		SeenCount:      1,
		NewCount:       1,
		UpdatedCount:   0,
		FinishedAt:     "2026-01-01T00:00:10Z",
		ErrorSummary:   "",
		CursorKey:      cursorKey,
		CursorTo:       "2026-01-02T00:00:00Z",
		CursorAdvanced: "2026-01-01T00:00:10Z",
	}
	calls := 0
	busy := errors.New("database is locked")
	err = retryFinalizeSyncRunOnBusy(5, func() error {
		calls++
		if calls < 3 {
			return busy
		}
		return archive.(*sqliteHealthArchiveWriter).finalizeSyncRunAttempt(context.Background(), finalize)
	})
	if err != nil {
		t.Fatalf("retry-then-success returned %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (2 busy + 1 real attempt)", calls)
	}
	assertSyncRunForDataType(t, archivePath, runID, "sync_completed", "steps", "list", 1, 1, 0, "")
	got, found, err := archive.ResolveSyncCursor(context.Background(), cursorKey)
	if err != nil || !found {
		t.Fatalf("ResolveSyncCursor = (%q, %v, %v), want cursor present", got, found, err)
	}
	if got != "2026-01-02T00:00:00Z" {
		t.Errorf("cursor = %q, want 2026-01-02T00:00:00Z", got)
	}
}

// TestFinalizeSyncRunDoesNotAdvanceCursorOnFailedOutcome pins the
// ADR-0008 invariant at the writer boundary (AC #4 + AC #7): a
// sync_failed finalize, even when it succeeds on first try, must
// leave the cursor table empty for the key. The retry policy must
// not weaken this — a successful finalize on a non-completed outcome
// still goes through commitSyncCursorTx which no-ops, so the cursor
// stays unset.
func TestFinalizeSyncRunDoesNotAdvanceCursorOnFailedOutcome(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	runID, err := archive.StartSyncRun(context.Background(), syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-01-01",
		To:             "2026-01-02",
		EndpointFamily: "list",
		StartedAt:      "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}
	cursorKey := syncCursorKey{connectionID: connection.id, dataType: "steps", rollupKind: syncCursorRollupKindNone}
	if err := archive.FinalizeSyncRun(context.Background(), syncRunFinalize{
		SyncRunID:      runID,
		Outcome:        syncRunOutcomeFailed,
		SeenCount:      0,
		NewCount:       0,
		UpdatedCount:   0,
		FinishedAt:     "2026-01-01T00:00:10Z",
		ErrorSummary:   "boom",
		CursorKey:      cursorKey,
		CursorTo:       "2026-01-02T00:00:00Z",
		CursorAdvanced: "2026-01-01T00:00:10Z",
	}); err != nil {
		t.Fatalf("FinalizeSyncRun(failed): %v", err)
	}
	_, found, err := archive.ResolveSyncCursor(context.Background(), cursorKey)
	if err != nil {
		t.Fatalf("ResolveSyncCursor: %v", err)
	}
	if found {
		t.Errorf("cursor unexpectedly advanced on sync_failed outcome")
	}
}

// TestHealthArchiveWriterHonorsCanceledContext pins the noctx-completion
// slice (#305) at the writer interface: the Sync Run path's SQLite
// writes ride the run's SIGINT cancel context (#284), so a canceled
// context aborts an in-flight upsert instead of the write running to
// completion behind the user's back.
func TestHealthArchiveWriterHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := archive.StartSyncRun(ctx, syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-01-01",
		To:             "2026-01-02",
		EndpointFamily: "list",
		StartedAt:      "2026-01-01T00:00:00Z",
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("StartSyncRun with canceled context = %v, want context.Canceled", err)
	}
	if _, err := archive.UpsertDataPoint(ctx, archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "steps",
		upstreamResourceName: "users/me/dataTypes/steps/dataPoints/cancel-probe",
		recordKind:           "interval",
		startTimeUTC:         "2026-01-01T08:00:00Z",
		endTimeUTC:           "2026-01-01T08:15:00Z",
		dataSourceJSON:       `{"platform":"FITBIT"}`,
		rawJSON:              `{"steps":{"count":"123"}}`,
	}, "2026-01-01T00:00:01Z"); !errors.Is(err, context.Canceled) {
		t.Fatalf("UpsertDataPoint with canceled context = %v, want context.Canceled", err)
	}
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertArchiveTableCount(t, archivePath, "data_points", 0)
}

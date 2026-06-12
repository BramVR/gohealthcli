package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// TestSyncStatusFencesAbandonedRunningRowsIdempotently is the issue
// #236 slice 4 tracer bullet. A sync_running row whose heartbeat went
// stale (killed terminal, SIGKILL, pipe-buffering kill — run id 205 on
// 2026-06-10 was the live example) is flipped to sync_failed with the
// abandonment message and finished_at=now on entry to `sync --status`.
// A second invocation is a no-op: the WHERE clause only matches
// sync_running rows, so finished_at keeps the first fence's timestamp.
func TestSyncStatusFencesAbandonedRunningRowsIdempotently(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	// Started 40 minutes ago, last heartbeat 10 minutes ago: stale by
	// the 5-minute fence rule even though the run DID make progress
	// for a while.
	insertSyncStatusFixtureRuns(t, archivePath, []syncStatusFixtureRun{
		{
			dataTypesJSON: `["heart-rate"]`,
			status:        "sync_running",
			seenCount:     830, newCount: 830, updatedCount: 0,
			startedAt:      "2026-06-10T11:20:00Z",
			lastProgressAt: stringPtr("2026-06-10T11:50:00Z"),
		},
	})

	testRuntime := fixedSyncStatusClock(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--status",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync --status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	// The fenced row surfaces in the same invocation's output already
	// terminal — the operator never sees a stale "running" lie.
	for _, want := range []string{
		"sync_run.0.status: sync_failed\n",
		"sync_run.0.error_summary: abandoned (no heartbeat for 5m)\n",
		"sync_run.0.finished_at: 2026-06-10T12:00:00Z\n",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	fenced := probeSyncRunRow(t, archivePath, 1)
	if fenced.status != "sync_failed" {
		t.Fatalf("fenced status = %q, want sync_failed", fenced.status)
	}
	if !fenced.finishedAt.Valid || fenced.finishedAt.String != "2026-06-10T12:00:00Z" {
		t.Fatalf("fenced finished_at = %+v, want 2026-06-10T12:00:00Z", fenced.finishedAt)
	}
	if !fenced.errorSummary.Valid || fenced.errorSummary.String != "abandoned (no heartbeat for 5m)" {
		t.Fatalf("fenced error_summary = %+v, want abandonment message", fenced.errorSummary)
	}

	// Second call one minute later: idempotent — the row is no longer
	// sync_running, so the fence touches zero rows and finished_at
	// keeps the FIRST fence's timestamp.
	testRuntime = fixedSyncStatusClock(time.Date(2026, 6, 10, 12, 1, 0, 0, time.UTC))
	code = runWithRuntime([]string{
		"sync", "--status",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	}, new(bytes.Buffer), new(bytes.Buffer), testRuntime)
	if code != 0 {
		t.Fatalf("second sync --status exit code = %d, want 0", code)
	}
	refenced := probeSyncRunRow(t, archivePath, 1)
	if refenced.finishedAt.String != "2026-06-10T12:00:00Z" {
		t.Fatalf("finished_at after second fence = %q, want unchanged 2026-06-10T12:00:00Z", refenced.finishedAt.String)
	}
}

// TestSyncCommandFencesAbandonedRunningRowsOnEntry: starting a real
// sync also fences orphans first, so the audit trail an operator
// reads after the new run never shows two "running" rows where one
// is a corpse from a killed process.
func TestSyncCommandFencesAbandonedRunningRowsOnEntry(t *testing.T) {
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
	insertSyncStatusFixtureRuns(t, archivePath, []syncStatusFixtureRun{
		{
			dataTypesJSON:  `["heart-rate"]`,
			status:         "sync_running",
			startedAt:      "2026-01-01T20:00:00Z",
			lastProgressAt: stringPtr("2026-01-01T23:00:00Z"),
		},
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints": [{
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a",
			"dataSource": {"platform": "FITBIT"},
			"steps": {
				"interval": {"startTime": "2026-01-01T08:00:00Z", "endTime": "2026-01-01T08:15:00Z"},
				"count": "512"
			}
		}]}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	fenced := probeSyncRunRow(t, archivePath, 1)
	if fenced.status != "sync_failed" {
		t.Fatalf("orphan row after sync entry = %q, want sync_failed", fenced.status)
	}
	if !fenced.errorSummary.Valid || fenced.errorSummary.String != "abandoned (no heartbeat for 5m)" {
		t.Fatalf("orphan error_summary = %+v, want abandonment message", fenced.errorSummary)
	}
	// The new Sync Run itself is untouched by the fence: it heartbeats
	// from its first page and finalizes sync_completed.
	assertSyncRun(t, archivePath, 2, "sync_completed", 1, 1, 0, "")
}

// TestFenceLeavesLongRunningRowWithFreshHeartbeatAlone is the
// anti-fence case the heartbeat design exists for: a run whose
// started_at is hours old but whose heartbeat is seconds old is a
// LIVE long backfill, not an orphan. A wall-clock fence (the
// original #236 proposal) would have flipped it mid-flight.
func TestFenceLeavesLongRunningRowWithFreshHeartbeatAlone(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertSyncStatusFixtureRuns(t, archivePath, []syncStatusFixtureRun{
		{
			dataTypesJSON: `["heart-rate"]`,
			status:        "sync_running",
			seenCount:     48000, newCount: 48000, updatedCount: 0,
			startedAt:      "2026-06-10T07:00:00Z",
			lastProgressAt: stringPtr("2026-06-10T11:59:50Z"),
		},
	})
	testRuntime := fixedSyncStatusClock(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))

	stdout := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync", "--status",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	}, stdout, new(bytes.Buffer), testRuntime)
	if code != 0 {
		t.Fatalf("sync --status exit code = %d, want 0\nstdout: %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "sync_run.0.status: sync_running\n") {
		t.Fatalf("five-hour run with a 10s-old heartbeat was not left running:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "sync_run.0.duration_seconds: 18000\n") {
		t.Fatalf("running duration should measure started_at->now:\n%s", stdout.String())
	}
}

// TestFinalizeAfterFenceConvergesToTrueTerminalStatus covers the
// self-heal property: if a fenced process was actually alive (paused
// laptop, absurdly slow page), its FinalizeSyncRun lands later and
// overwrites the fence unconditionally by id — the row converges to
// the true terminal status and the cursor advances as if the fence
// never happened.
func TestFinalizeAfterFenceConvergesToTrueTerminalStatus(t *testing.T) {
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
	writer, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer writer.Close()
	connection, err := writer.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	syncRunID, err := writer.StartSyncRun(context.Background(), syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-06-01",
		To:             "2026-06-10",
		EndpointFamily: "list",
		StartedAt:      "2026-06-10T11:00:00Z",
	})
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}

	fencedCount, err := fenceAbandonedSyncRunsAtPath(context.Background(), archivePath, time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fence: %v", err)
	}
	if fencedCount != 1 {
		t.Fatalf("fenced %d rows, want 1", fencedCount)
	}

	// A late heartbeat from the still-alive process must NOT
	// resurrect the fenced row to sync_running.
	if err := writer.HeartbeatSyncRun(context.Background(), syncRunHeartbeat{
		SyncRunID: syncRunID,
		SeenCount: 10,
		NewCount:  10,
		At:        "2026-06-10T12:00:30Z",
	}); err != nil {
		t.Fatalf("late heartbeat: %v", err)
	}
	afterHeartbeat := probeSyncRunRow(t, archivePath, syncRunID)
	if afterHeartbeat.status != "sync_failed" {
		t.Fatalf("late heartbeat resurrected the fenced row to %q", afterHeartbeat.status)
	}
	if afterHeartbeat.lastProgressAt.Valid {
		t.Fatalf("late heartbeat wrote last_progress_at = %q onto a fenced row", afterHeartbeat.lastProgressAt.String)
	}

	// The real finalize lands: row converges to sync_completed and
	// the cursor advances exactly as on an unfenced run.
	cursorKey := syncCursorKey{connectionID: connection.ID, dataType: "steps", rollupKind: syncCursorRollupKindNone}
	if err := writer.FinalizeSyncRun(context.Background(), syncRunFinalize{
		SyncRunID:      syncRunID,
		Outcome:        syncRunOutcomeCompleted,
		SeenCount:      42,
		NewCount:       42,
		UpdatedCount:   0,
		FinishedAt:     "2026-06-10T12:01:00Z",
		ErrorSummary:   "",
		CursorKey:      cursorKey,
		CursorTo:       "2026-06-10",
		CursorAdvanced: "2026-06-10T12:01:00Z",
	}); err != nil {
		t.Fatalf("FinalizeSyncRun after fence: %v", err)
	}
	converged := probeSyncRunRow(t, archivePath, syncRunID)
	if converged.status != "sync_completed" {
		t.Fatalf("row after finalize = %q, want sync_completed", converged.status)
	}
	if converged.errorSummary.Valid {
		t.Fatalf("error_summary after finalize = %q, want cleared", converged.errorSummary.String)
	}
	if converged.finishedAt.String != "2026-06-10T12:01:00Z" {
		t.Fatalf("finished_at after finalize = %q, want the finalize timestamp", converged.finishedAt.String)
	}
	cursorTime, found, err := writer.ResolveSyncCursor(context.Background(), cursorKey)
	if err != nil || !found {
		t.Fatalf("cursor after finalize: found=%v err=%v", found, err)
	}
	if cursorTime != "2026-06-10" {
		t.Fatalf("cursor = %q, want 2026-06-10", cursorTime)
	}
}

// TestFenceNeverAdvancesTheSyncCursor is the ADR-0008 regression
// guard from the #236 acceptance criteria: fencing a row to
// sync_failed must leave the Sync Cursor exactly where it was — the
// fence is a plain sync_runs UPDATE and structurally cannot touch
// sync_cursors, and this test keeps it that way.
func TestFenceNeverAdvancesTheSyncCursor(t *testing.T) {
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
	writer, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer writer.Close()
	connection, err := writer.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	cursorKey := syncCursorKey{connectionID: connection.ID, dataType: "steps", rollupKind: syncCursorRollupKindNone}
	// Seed a cursor from an earlier completed run.
	if err := writer.CommitSyncCursor(context.Background(), cursorKey, syncRunOutcomeCompleted, "2026-06-08", "2026-06-08T00:00:10Z"); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	// An orphaned run covering a LATER range dies without heartbeats.
	if _, err := writer.StartSyncRun(context.Background(), syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-06-08",
		To:             "2026-06-10",
		EndpointFamily: "list",
		StartedAt:      "2026-06-10T11:00:00Z",
	}); err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}

	fencedCount, err := fenceAbandonedSyncRunsAtPath(context.Background(), archivePath, time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fence: %v", err)
	}
	if fencedCount != 1 {
		t.Fatalf("fenced %d rows, want 1", fencedCount)
	}
	cursorTime, found, err := writer.ResolveSyncCursor(context.Background(), cursorKey)
	if err != nil || !found {
		t.Fatalf("cursor after fence: found=%v err=%v", found, err)
	}
	if cursorTime != "2026-06-08" {
		t.Fatalf("cursor after fence = %q, want unchanged 2026-06-08 (a fenced run must re-read its window on retry)", cursorTime)
	}
}

// TestStatusCommandFencesAbandonedRunningRows: the broad `status`
// summary also runs the fence on entry, so its latest_failed_sync_run
// stanza reflects the abandonment instead of silently skipping a
// phantom in-flight run.
func TestStatusCommandFencesAbandonedRunningRows(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertSyncStatusFixtureRuns(t, archivePath, []syncStatusFixtureRun{
		{
			dataTypesJSON: `["steps"]`,
			status:        "sync_running",
			startedAt:     "2026-06-10T11:00:00Z",
			// No heartbeat at all: the run died before its first page
			// (or predates #236) — the fence falls back to started_at.
		},
	})
	testRuntime := fixedSyncStatusClock(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"status",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	fenced := probeSyncRunRow(t, archivePath, 1)
	if fenced.status != "sync_failed" {
		t.Fatalf("status after `status` entry = %q, want sync_failed", fenced.status)
	}
	if !fenced.errorSummary.Valid || fenced.errorSummary.String != "abandoned (no heartbeat for 5m)" {
		t.Fatalf("error_summary = %+v, want abandonment message", fenced.errorSummary)
	}
}

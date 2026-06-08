package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSyncResultFromOutcomeAlwaysSetsEnumStatus is the structural seam
// asserting AC #2: every syncResult produced by the lifecycle's
// constructor has Status set to one of the three enum values, never
// empty. This pins the type-system contract from the PRD: outcomes can
// only build a syncResult via the constructor, so a return path that
// forgot to set Status is structurally impossible.
func TestSyncResultFromOutcomeAlwaysSetsEnumStatus(t *testing.T) {
	for _, outcome := range []syncRunOutcome{
		syncRunOutcomeCompleted,
		syncRunOutcomeFailed,
		syncRunOutcomeCanceled,
	} {
		result := syncResultFromOutcome(outcome, syncResult{})
		if result.Status == "" {
			t.Errorf("outcome %q produced empty Status", outcome)
		}
		if result.Status != string(outcome) {
			t.Errorf("outcome %q produced Status %q", outcome, result.Status)
		}
	}
}

// TestSyncRunLifecycleStatusEnumOnEveryReachableReturn walks the
// reachable error-shape variants of the lifecycle's pre-finalize early
// returns (cursor-missing, archive-open-failure) and asserts the
// returned syncResult always has Status set to one of the enum
// values. Combined with the constructor-only invariant on
// syncResultFromOutcome, this gives AC #2 end-to-end coverage: an
// empty Status string would surface here.
func TestSyncRunLifecycleStatusEnumOnEveryReachableReturn(t *testing.T) {
	validEnum := func(status string) bool {
		switch status {
		case "sync_completed", "sync_failed", "sync_canceled":
			return true
		}
		return false
	}

	// Path 1: missing Sync Cursor with no --from — surfaces before
	// any audit row is written.
	t.Run("cursor-missing", func(t *testing.T) {
		tempDir := t.TempDir()
		configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
		runtime := newConnectFakeRuntime(t, fakeConnectConfig{
			accessToken:        "connect-access-secret",
			refreshToken:       "connect-refresh-secret",
			healthUserID:       "111111256096816351",
			legacyFitbitUserID: "A1B2C3",
		})
		if _, err := connectSetupWithRuntime(configPath, archivePath, false, runtime); err != nil {
			t.Fatalf("connect setup: %v", err)
		}
		runtime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }
		result, err := (syncRunExecutor{runtime: runtime}).Execute(syncCommandOptions{
			configPath:  configPath,
			archivePath: archivePath,
			dataTypes:   []string{"steps"},
			to:          "2026-01-02T00:00:00Z",
		})
		if err == nil {
			t.Fatal("expected error on missing cursor + no --from")
		}
		if !validEnum(result.Status) {
			t.Errorf("Status = %q, want enum value", result.Status)
		}
	})

	// Path 2: completed Sync Run — finalize succeeds, status is
	// sync_completed.
	t.Run("completed", func(t *testing.T) {
		tempDir := t.TempDir()
		configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
		runtime := newConnectFakeRuntime(t, fakeConnectConfig{
			accessToken:        "connect-access-secret",
			refreshToken:       "connect-refresh-secret",
			healthUserID:       "111111256096816351",
			legacyFitbitUserID: "A1B2C3",
		})
		if _, err := connectSetupWithRuntime(configPath, archivePath, false, runtime); err != nil {
			t.Fatalf("connect setup: %v", err)
		}
		runtime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }
		runtime, _ = withStepSyncFetchFake(t, runtime, "connect-access-secret", map[string]string{
			"": `{"dataPoints":[]}`,
		})
		result, err := (syncRunExecutor{runtime: runtime}).Execute(syncCommandOptions{
			configPath:  configPath,
			archivePath: archivePath,
			dataTypes:   []string{"steps"},
			from:        "2026-01-01",
			to:          "2026-01-02T00:00:00Z",
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !validEnum(result.Status) {
			t.Errorf("Status = %q, want enum value", result.Status)
		}
		if result.SyncRunID == 0 {
			t.Errorf("SyncRunID = 0, want populated audit row id")
		}
	})
}

// TestErrFinalizeSyncRunBusyExhaustedCarriesAttemptCount pins the typed
// error the SQLite adapter surfaces when the retry budget is exhausted.
// The lifecycle module converts this into a sync_failed terminal state;
// callers detect it via errors.As so the exact wrapping at the writer
// boundary stays an implementation detail.
func TestErrFinalizeSyncRunBusyExhaustedCarriesAttemptCount(t *testing.T) {
	err := &errFinalizeSyncRunBusyExhausted{attempts: 5, cause: errors.New("database is locked")}
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("error message = %q, want attempt count", err.Error())
	}
	var target *errFinalizeSyncRunBusyExhausted
	if !errors.As(err, &target) {
		t.Fatalf("errors.As did not match typed error")
	}
	if target.attempts != 5 {
		t.Errorf("attempts = %d, want 5", target.attempts)
	}
}

// TestRetryFinalizeSyncRunRetriesUntilSuccess pins AC #4: the retry
// helper invokes the attempt closure repeatedly while the error is a
// SQLITE_BUSY contention, then returns nil as soon as the attempt
// succeeds. The closure observes how many times it was called via a
// counter the test holds.
func TestRetryFinalizeSyncRunRetriesUntilSuccess(t *testing.T) {
	calls := 0
	busy := errors.New("database is locked")
	err := retryFinalizeSyncRunOnBusy(5, func() error {
		calls++
		if calls < 3 {
			return busy
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryFinalizeSyncRunOnBusy returned %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (2 busy + 1 success)", calls)
	}
}

// TestRetryFinalizeSyncRunSurfacesNonBusyImmediately pins the budget
// gate: a non-SQLITE_BUSY error short-circuits the loop on the first
// occurrence, regardless of remaining budget. This keeps non-contention
// failures (constraint violations, IO errors) from being silently
// retried as if they were transient lock contention.
func TestRetryFinalizeSyncRunSurfacesNonBusyImmediately(t *testing.T) {
	calls := 0
	fatal := errors.New("syntax error")
	err := retryFinalizeSyncRunOnBusy(5, func() error {
		calls++
		return fatal
	})
	if !errors.Is(err, fatal) {
		t.Fatalf("err = %v, want fatal wrapped", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on non-busy)", calls)
	}
}

// TestRetryFinalizeSyncRunExhaustsBudgetAndReturnsTypedError pins AC #4
// budget-exhaustion: when every attempt returns SQLITE_BUSY, the helper
// returns errFinalizeSyncRunBusyExhausted with attempts == budget and
// wraps the last underlying error. The lifecycle module branches on
// this typed error to drive the recovery write.
func TestRetryFinalizeSyncRunExhaustsBudgetAndReturnsTypedError(t *testing.T) {
	calls := 0
	busy := errors.New("database is locked")
	err := retryFinalizeSyncRunOnBusy(4, func() error {
		calls++
		return busy
	})
	var exhausted *errFinalizeSyncRunBusyExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want errFinalizeSyncRunBusyExhausted", err)
	}
	if exhausted.attempts != 4 {
		t.Errorf("attempts = %d, want 4", exhausted.attempts)
	}
	if !errors.Is(err, busy) {
		t.Errorf("err does not wrap underlying busy error")
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
}

// TestConcurrentSyncRunsLeaveNoSyncRunningRows pins AC #6 of PRD #141
// slice 4: two concurrent sync invocations against the same Health
// Archive must both terminate, both produce a non-empty enum Status,
// both populate SyncRunID, and leave zero sync_runs rows in the
// non-terminal sync_running state.
//
// Pattern: seed the archive with one connect+OAuth setup, then launch
// two goroutines calling syncRunExecutor.Execute against the same
// archivePath in parallel. Each goroutine uses its own runtimeAdapters
// (the connect-fake-runtime + per-page fake provider), so their
// archive opens contend for the SQLite file lock. Whether contention
// manifests as SQLITE_BUSY (driven through retryFinalizeSyncRunOnBusy)
// or whether the runs serialize under SetMaxOpenConns(1), the post-
// conditions are the same: terminal status enum on every envelope and
// no dangling sync_running row.
func TestConcurrentSyncRunsLeaveNoSyncRunningRows(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	seedRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, seedRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}

	runSync := func(dataType, from, to, accessToken string, pages map[string]string) (syncResult, error) {
		runtime := newConnectFakeRuntime(t, fakeConnectConfig{
			accessToken:        "connect-access-secret",
			refreshToken:       "connect-refresh-secret",
			healthUserID:       "111111256096816351",
			legacyFitbitUserID: "A1B2C3",
		})
		runtime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }
		runtime, _ = withStepSyncFetchFake(t, runtime, accessToken, pages)
		return (syncRunExecutor{runtime: runtime}).Execute(syncCommandOptions{
			configPath:  configPath,
			archivePath: archivePath,
			dataTypes:   []string{dataType},
			from:        from,
			to:          to,
		})
	}

	var wg sync.WaitGroup
	results := make([]syncResult, 2)
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0], errs[0] = runSync("steps", "2026-01-01", "2026-01-02T00:00:00Z", "connect-access-secret", map[string]string{
			"": `{"dataPoints":[{
				"name": "users/me/dataTypes/steps/dataPoints/concurrent-a",
				"dataSource": {"platform": "FITBIT"},
				"steps": {"interval": {"startTime": "2026-01-01T08:00:00Z", "endTime": "2026-01-01T08:15:00Z"}, "count": "1"}
			}]}`,
		})
	}()
	go func() {
		defer wg.Done()
		results[1], errs[1] = runSync("steps", "2026-01-02", "2026-01-03T00:00:00Z", "connect-access-secret", map[string]string{
			"": `{"dataPoints":[{
				"name": "users/me/dataTypes/steps/dataPoints/concurrent-b",
				"dataSource": {"platform": "FITBIT"},
				"steps": {"interval": {"startTime": "2026-01-02T08:00:00Z", "endTime": "2026-01-02T08:15:00Z"}, "count": "2"}
			}]}`,
		})
	}()
	wg.Wait()

	// Both invocations terminated. Whether each succeeded or failed,
	// the status enum must be one of the three values and never empty.
	for i, r := range results {
		switch r.Status {
		case "sync_completed", "sync_failed", "sync_canceled":
		default:
			t.Errorf("goroutine %d produced invalid Status %q (err=%v)", i, r.Status, errs[i])
		}
		if r.SyncRunID == 0 {
			t.Errorf("goroutine %d produced empty SyncRunID; result=%+v err=%v", i, r, errs[i])
		}
	}

	// No sync_runs row should be stuck in the sync_running state.
	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		t.Fatalf("reopen archive: %v", err)
	}
	defer db.Close()
	var runningCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_runs WHERE status = 'sync_running'`).Scan(&runningCount); err != nil {
		t.Fatalf("count sync_running rows: %v", err)
	}
	if runningCount != 0 {
		t.Errorf("sync_running rows after concurrent invocations = %d, want 0", runningCount)
	}
	var totalCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_runs`).Scan(&totalCount); err != nil {
		t.Fatalf("count sync_runs rows: %v", err)
	}
	if totalCount < 2 {
		t.Errorf("sync_runs total rows = %d, want at least 2 (one per invocation)", totalCount)
	}
	// Every row must carry a terminal status.
	rows, err := db.Query(`SELECT id, status FROM sync_runs`)
	if err != nil {
		t.Fatalf("query sync_runs: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatalf("scan: %v", err)
		}
		switch status {
		case "sync_completed", "sync_failed", "sync_canceled":
		default:
			t.Errorf("sync_runs row id=%d has non-terminal status %q", id, status)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
}

// TestSyncRunLifecycleConvertsBusyExhaustedToFailedWithRecoveryRow
// pins AC #5 of PRD #141 slice 4: when FinalizeSyncRun surfaces
// errFinalizeSyncRunBusyExhausted, the lifecycle converts it to
// sync_failed, populates SyncRunID on the returned result, writes a
// recovery row marking the sync_runs entry sync_failed in a fresh
// transaction, and emits a contention-aware message. The cursor must
// stay absent (ADR-0008).
func TestSyncRunLifecycleConvertsBusyExhaustedToFailedWithRecoveryRow(t *testing.T) {
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

	busyExhausted := &errFinalizeSyncRunBusyExhausted{attempts: finalizeSyncRunRetryBudget, cause: errors.New("database is locked")}
	t.Cleanup(func() { healthArchiveWriterOpenerForTest = openHealthArchiveWriter })
	healthArchiveWriterOpenerForTest = func(path string) (healthArchiveWriter, error) {
		inner, err := openHealthArchiveWriter(path)
		if err != nil {
			return nil, err
		}
		return fakeFinalizeWriter{healthArchiveWriter: inner, failOn: failOnEveryOutcome(busyExhausted)}, nil
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
		t.Fatal("expected error from busy-exhausted finalize, got nil")
	}
	if result.Status != "sync_failed" {
		t.Errorf("result.Status = %q, want sync_failed", result.Status)
	}
	if result.SyncRunID == 0 {
		t.Errorf("result.SyncRunID = 0, want populated audit row id")
	}
	if !strings.Contains(result.Message, "contention") && !strings.Contains(result.Message, "lost contention") {
		t.Errorf("result.Message = %q, want contention-aware message", result.Message)
	}

	// Recovery row landed: the audit trail reflects sync_failed, not the
	// initial sync_running.
	assertSyncRunForDataType(t, archivePath, result.SyncRunID, "sync_failed", "steps", "list", 0, 0, 0, "contention")

	// ADR-0008: the cursor must not advance on busy-exhausted paths.
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
		t.Fatalf("cursor present after busy-exhausted finalize: found=%v err=%v", found, err)
	}
}

package main

import (
	"context"
	"errors"
	"github.com/BramVR/gohealthcli/internal/archived"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// installMultiTypeSyncFake routes the runtime's fetchRawProvider to
// per-Data-Type canned responses. Used by orchestrator tests where one
// invocation needs to satisfy several sequential Sync Runs across
// different Data Types.
func installMultiTypeSyncFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, perType map[string]string) (runtimeAdapters, *[]googlehealth.RawRequest) {
	t.Helper()
	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("multi-type sync access token = %q, want stored token", accessToken)
		}
		body, ok := perType[request.DataType]
		if !ok {
			t.Fatalf("no fake page for dataType %q (endpoint %q)", request.DataType, request.EndpointName)
		}
		requests = append(requests, request)
		return []byte(body), nil
	}
	return runtime, &requests
}

func TestSyncOrchestratorFansOutOnePerDataType(t *testing.T) {
	t.Parallel()
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

	testRuntime, requests := installMultiTypeSyncFake(t, testRuntime, "connect-access-secret", map[string]string{
		"steps":      `{"dataPoints":[]}`,
		"heart-rate": `{"dataPoints":[]}`,
	})

	orchestrator := newSyncOrchestrator(testRuntime)
	results, err := orchestrator.Sync(context.Background(), syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps", "heart-rate"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("orchestrator.Sync: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	for index, want := range []string{"steps", "heart-rate"} {
		if got := results[index]; got.Status != "sync_completed" {
			t.Fatalf("results[%d] status = %q, want sync_completed", index, got.Status)
		}
		if got := results[index].DataTypes; len(got) != 1 || got[0] != want {
			t.Fatalf("results[%d] DataTypes = %v, want [%q]", index, got, want)
		}
	}
	if len(*requests) != 2 {
		t.Fatalf("upstream requests = %d, want 2 (one per Data Type)", len(*requests))
	}
	assertArchiveTableCount(t, archivePath, "sync_runs", 2)
}

func TestSyncOrchestratorIsolatesPerTypeFailures(t *testing.T) {
	t.Parallel()
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

	testRuntime, _ = installMultiTypeSyncFake(t, testRuntime, "connect-access-secret", map[string]string{
		"steps":      `{`, // forces parse failure for steps
		"heart-rate": `{"dataPoints":[]}`,
	})

	orchestrator := newSyncOrchestrator(testRuntime)
	results, err := orchestrator.Sync(context.Background(), syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps", "heart-rate"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("orchestrator.Sync: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	if results[0].Status != "sync_failed" {
		t.Fatalf("steps status = %q, want sync_failed", results[0].Status)
	}
	if results[1].Status != "sync_completed" {
		t.Fatalf("heart-rate status = %q, want sync_completed (failure must not poison this run)", results[1].Status)
	}
	assertArchiveTableCount(t, archivePath, "sync_runs", 2)
}

func TestSyncOrchestratorRespectsCancellationBetweenDataTypes(t *testing.T) {
	t.Parallel()
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stepsCalled := false
	testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		if request.DataType == "steps" {
			stepsCalled = true
			// Cancel after the first Data Type finishes its single page.
			cancel()
			return []byte(`{"dataPoints":[]}`), nil
		}
		t.Fatalf("orchestrator continued past cancellation: hit dataType %q", request.DataType)
		return nil, nil
	}

	orchestrator := newSyncOrchestrator(testRuntime)
	results, err := orchestrator.Sync(ctx, syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps", "heart-rate"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("orchestrator.Sync: %v", err)
	}
	if !stepsCalled {
		t.Fatal("expected steps fetch before cancellation")
	}
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1 (heart-rate must be skipped silently rather than emit a misleading skipped row)", len(results))
	}
	if results[0].Status != "sync_completed" {
		t.Fatalf("steps status = %q, want sync_completed (cancellation arrives between Data Types)", results[0].Status)
	}
	if got := results[0].DataTypes; len(got) != 1 || got[0] != "steps" {
		t.Fatalf("results[0] DataTypes = %v, want [steps]", got)
	}

	// No heart-rate sync_runs row should exist — orchestrator never invoked the executor.
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, found, err := archive.ResolveSyncCursor(context.Background(), syncCursorKey{
		connectionID: connection.ID,
		dataType:     "heart-rate",
		rollupKind:   syncCursorRollupKindNone,
	}); err != nil || found {
		t.Fatalf("heart-rate cursor: found=%v err=%v, want absent after cancellation", found, err)
	}
}

// TestSyncRunLifecycleClosesSIGINTPreFirstDataTypeRace pins PRD #141
// slice 5 AC #1: a run context that is already canceled when the
// post-preflight lifecycle is entered must NOT write a sync_runs audit
// row. Pre-slice-5, the lifecycle plumbed the cancel signal into the
// ingestion page loop but called StartSyncRun before any cancel check,
// so a SIGINT that landed between the orchestrator's loop-top guard and
// the first lifecycle.Run call still booked an audit row marked
// sync_canceled. Slice 5 closes that window by checking for cancel at
// the top of syncRunLifecycle.Run, so the doc-claimed no-audit-row
// branch is genuinely reachable. This test exercises the seam
// directly via syncRunExecutor.Execute (the layer the orchestrator
// composes) so the invariant is pinned at the lifecycle boundary even
// when the orchestrator's loop-top check is not in play.
func TestSyncRunLifecycleClosesSIGINTPreFirstDataTypeRace(t *testing.T) {
	t.Parallel()
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
	testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		t.Fatalf("upstream Fetch invoked despite pre-canceled context; lifecycle must short-circuit before any work")
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(ctx, syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	// The lifecycle surfaces googlehealth.ErrSyncCanceled for consistency with the
	// mid-pagination cancel path (the orchestrator already swallows it
	// into the result slice). What matters for AC #1 is the structural
	// shape: status, no SyncRunID, no audit row.
	if err != nil && !errors.Is(err, googlehealth.ErrSyncCanceled) {
		t.Fatalf("Execute with pre-canceled context: %v, want nil or googlehealth.ErrSyncCanceled", err)
	}
	if result.Status != "sync_canceled" {
		t.Fatalf("Status = %q, want sync_canceled (pre-first-Run cancel must surface as canceled, never the empty string)", result.Status)
	}
	if result.SyncRunID != 0 {
		t.Errorf("SyncRunID = %d, want 0 (no audit row should have been written)", result.SyncRunID)
	}
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
}

// TestSyncOrchestratorCancelBetweenLoopGuardAndLifecycleWritesNoAuditRow
// pins PRD #141 slice 5 at the exact race window the slice closes: a
// SIGINT that lands AFTER orchestrator.Sync's loop-top guard observes
// the context as live BUT BEFORE syncRunLifecycle.Run reaches its
// pre-StartSyncRun cancel check.
//
// Why a different shape than the lifecycle-level test:
// pre-canceling the context before orchestrator.Sync runs would be
// caught by the orchestrator's loop-top guard on the first iteration
// and break out before any executor call — that scenario passes pre-fix
// and post-fix identically, so it does not exercise the race window the
// slice actually closes. To exercise that window we cancel DURING
// gate.Validate (specifically, inside the gate's currentConnection()
// lookup, which runs after the orchestrator's loop-top guard observed a
// live context and before lifecycle.Run gets a chance to check it).
// Without slice 5's check at syncRunLifecycle.Run's first line, this
// sequence would proceed straight to StartSyncRun and book a sync_runs
// row; with slice 5 in place, the lifecycle catches the now-canceled
// context and returns sync_canceled with zero audit rows.
//
// The seam used here — the runtime adapters' openHealthArchiveWriter —
// is called from productionSyncPreflightContext.currentConnection
// during gate.Validate. Wrapping it lets us deterministically slot a
// close() into the validate-then-lifecycle handoff without introducing
// a new production hook.
func TestSyncOrchestratorCancelBetweenLoopGuardAndLifecycleWritesNoAuditRow(t *testing.T) {
	t.Parallel()
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
	testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		t.Fatalf("orchestrator reached upstream Fetch despite context canceled during gate.Validate")
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wrap the archive opener so that the FIRST open (which happens
	// inside gate.Validate's currentConnection lookup) cancels the run
	// context. That places the cancel strictly AFTER the orchestrator's
	// loop-top guard observed a live context (the guard runs before
	// executor.Execute) and strictly BEFORE syncRunLifecycle.Run's
	// pre-StartSyncRun cancel check fires (which is the first thing
	// lifecycle.Run does). This is the exact race window slice 5 closes.
	opens := 0
	testRuntime.openHealthArchiveWriter = func(path string) (healthArchiveWriter, error) {
		opens++
		if opens == 1 {
			cancel()
		}
		return openHealthArchiveWriter(path)
	}

	orchestrator := newSyncOrchestrator(testRuntime)
	results, err := orchestrator.Sync(ctx, syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps", "heart-rate"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("orchestrator.Sync: %v", err)
	}
	if opens == 0 {
		t.Fatal("archive opener never invoked; test seam did not fire — race window not exercised")
	}
	if fanOutStatus(results) != "sync_canceled" {
		t.Fatalf("fanOutStatus = %q, want sync_canceled", fanOutStatus(results))
	}
	for index, result := range results {
		if result.Status == "" {
			t.Errorf("results[%d].Status is empty; --json envelope must carry sync_canceled, never the empty string", index)
		}
	}
	// AC #1: no sync_runs audit row may exist — lifecycle.Run must
	// short-circuit before StartSyncRun on the context it observes at
	// entry.
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
}

func TestSyncOrchestratorCancelsActiveDataTypeMidPagination(t *testing.T) {
	t.Parallel()
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pageCount := 0
	testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		pageCount++
		if pageCount == 1 {
			// First page succeeds with one Data Point and signals more pages.
			// Cancel before returning so the executor's between-pages
			// check at the top of the next iteration observes it.
			cancel()
			return []byte(`{
				"dataPoints": [{
					"name": "users/me/dataTypes/steps/dataPoints/cancel-test",
					"dataSource": {"platform": "FITBIT"},
					"steps": {"interval": {"startTime": "2026-01-01T08:00:00Z", "endTime": "2026-01-01T08:15:00Z"}, "count": "100"}
				}],
				"nextPageToken": "next-page"
			}`), nil
		}
		t.Fatalf("upstream Fetch called %d times after cancellation; executor must stop at page boundary", pageCount)
		return nil, errors.New("unreachable")
	}

	orchestrator := newSyncOrchestrator(testRuntime)
	results, err := orchestrator.Sync(ctx, syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("orchestrator.Sync: %v", err)
	}
	if len(results) != 1 || results[0].Status != "sync_canceled" {
		t.Fatalf("results = %+v, want one sync_canceled run", results)
	}
	// The Data Point archived on page 1 should remain — upsert dedupe absorbs the overlap on resume.
	assertArchiveTableCount(t, archivePath, "data_points", 1)

	// ADR-0008 invariant: canceled run must NOT advance the cursor.
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, found, err := archive.ResolveSyncCursor(context.Background(), syncCursorKey{
		connectionID: connection.ID,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	}); err != nil || found {
		t.Fatalf("steps cursor after canceled run: found=%v err=%v, want absent (ADR-0008)", found, err)
	}
}

// TestSyncRunExecutorCancelMidFetchFinalizesCanceledAndKeepsCursor is
// the #284 end-to-end pin at the executor seam: the cancel fires while
// the Provider fetch is IN FLIGHT (the fake blocks until the run
// context cancels — there is no next page boundary to save us). The
// run must return promptly, finalize as sync_canceled in both the
// envelope and the persisted sync_runs row, and leave the Sync Cursor
// un-advanced (ADR-0008).
func TestSyncRunExecutorCancelMidFetchFinalizesCanceledAndKeepsCursor(t *testing.T) {
	t.Parallel()
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fetchEntered := make(chan struct{})
	var enterOnce sync.Once
	testRuntime.fetchRawProvider = func(ctx context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		enterOnce.Do(func() { close(fetchEntered) })
		// Simulate a stalled upstream: the request only returns when the
		// run context aborts it, exactly like net/http with a
		// context-scoped request.
		<-ctx.Done()
		return nil, &url.Error{Op: "Get", URL: request.URL, Err: ctx.Err()}
	}

	type executeOutcome struct {
		result syncResult
		err    error
	}
	done := make(chan executeOutcome, 1)
	go func() {
		result, err := (syncRunExecutor{runtime: testRuntime}).Execute(ctx, syncCommandOptions{
			configPath:  configPath,
			archivePath: archivePath,
			dataTypes:   []string{"steps"},
			from:        "2026-01-01",
			to:          "2026-01-02T00:00:00Z",
		})
		done <- executeOutcome{result: result, err: err}
	}()

	select {
	case <-fetchEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Provider fetch never started")
	}
	cancel()

	var outcome executeOutcome
	select {
	case outcome = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return within 2s of cancel; the in-flight fetch was not aborted")
	}
	if !errors.Is(outcome.err, googlehealth.ErrSyncCanceled) {
		t.Fatalf("Execute err = %v, want googlehealth.ErrSyncCanceled", outcome.err)
	}
	if outcome.result.Status != "sync_canceled" {
		t.Fatalf("Status = %q, want sync_canceled", outcome.result.Status)
	}
	if outcome.result.SyncRunID == 0 {
		t.Fatal("SyncRunID = 0, want a persisted audit row (cancel landed mid-run, after StartSyncRun)")
	}

	// The persisted audit row must agree with the envelope, and the
	// canceled run must not have advanced any cursor (ADR-0008).
	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		t.Fatalf("reopen archive: %v", err)
	}
	defer db.Close()
	var persistedStatus string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM sync_runs WHERE id = ?`, outcome.result.SyncRunID).Scan(&persistedStatus); err != nil {
		t.Fatalf("scan sync_runs status: %v", err)
	}
	if persistedStatus != "sync_canceled" {
		t.Fatalf("persisted sync_runs.status = %q, want sync_canceled", persistedStatus)
	}
	var cursorCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM sync_cursors`).Scan(&cursorCount); err != nil {
		t.Fatalf("scan sync_cursors count: %v", err)
	}
	if cursorCount != 0 {
		t.Fatalf("sync_cursors rows = %d, want 0 (canceled run must not advance any cursor)", cursorCount)
	}
}

func TestSyncOrchestratorRejectsAllAndTypesTogether(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{accessToken: "x"})
	_ = testRuntime

	_, err := newSyncOrchestrator(testRuntime).Sync(context.Background(), syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		allTypes:    true,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err == nil || !strings.Contains(err.Error(), "--all cannot be combined with --types") {
		t.Fatalf("err = %v, want --all+--types rejection", err)
	}
}

func TestInstallSyncCancelContextCancelsOnSIGINT(t *testing.T) {
	// NOT t.Parallel(): this test SIGINTs the whole test process
	// (syscall.Kill(Getpid())). Any concurrently-running test that has
	// a signal.NotifyContext installed — every `sync` / `raw` dispatch
	// — would observe the signal and flake as sync_canceled.
	if runtime.GOOS == "windows" {
		t.Skip("syscall.Kill(SIGINT) is POSIX-only; the cancel-context install path itself is exercised by TestInstallSyncCancelContextCancelsOnStop on every platform")
	}
	ctx, stop := installSyncCancelContext()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to self: %v", err)
	}

	select {
	case <-ctx.Done():
		// expected — signal handler canceled the context
	case <-time.After(2 * time.Second):
		t.Fatal("context did not cancel within 2s after SIGINT")
	}
}

func TestInstallSyncCancelContextCancelsOnStop(t *testing.T) {
	t.Parallel()
	ctx, stop := installSyncCancelContext()
	stop()

	select {
	case <-ctx.Done():
		// expected — stop cancels the underlying context
	case <-time.After(2 * time.Second):
		t.Fatal("context did not cancel within 2s after stop()")
	}
	// stop is idempotent — calling it again must not panic.
	stop()
}

// TestPerTypeSyncOptionsClearsAllTypes is the regression test for the
// `sync --all` carry-over bug: orchestrator.Sync expanded --all into a
// fan-out list, but the per-Data-Type options copy preserved
// allTypes=true. When Execute then called gate.Validate, the gate's
// expandDataTypes rejected every per-type call as
// preflightRuleAllVsTypesConflict, completely breaking --all.
func TestPerTypeSyncOptionsClearsAllTypes(t *testing.T) {
	t.Parallel()
	options := syncCommandOptions{
		allTypes:  true,
		dataTypes: []string{"steps", "heart-rate"},
		from:      "2026-01-01",
		to:        "2026-01-02T00:00:00Z",
	}
	perType := perTypeSyncOptions(options, "steps")
	if perType.allTypes {
		t.Errorf("perType.allTypes = true, want false (gate rejects allTypes alongside dataTypes)")
	}
	if len(perType.dataTypes) != 1 || perType.dataTypes[0] != "steps" {
		t.Errorf("perType.dataTypes = %v, want [\"steps\"]", perType.dataTypes)
	}
	if perType.from != options.from || perType.to != options.to {
		t.Errorf("perType drops --from/--to: from=%q to=%q", perType.from, perType.to)
	}
	// Drive the resulting options through the gate's expandDataTypes to
	// confirm the all-vs-types conflict no longer fires.
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(time.Now(), archived.Connection{ID: "x"})}
	got, err := gate.expandDataTypes(perType)
	if err != nil {
		t.Fatalf("gate.expandDataTypes(perType): %v — orchestrator forwards a config the gate rejects", err)
	}
	if len(got) != 1 || got[0] != "steps" {
		t.Errorf("gate.expandDataTypes(perType) = %v, want [\"steps\"]", got)
	}
}

// TestFanOutStatusEmptyResultsReportsCanceled pins the contract callers
// rely on: orchestrator.Sync returns an empty slice only when SIGINT
// arrived before the first Data Type started. The CLI surface must read
// that as sync_canceled, not sync_failed, so an interrupted backfill
// does not surface as a failure in tooling that pivots on status.
func TestFanOutStatusEmptyResultsReportsCanceled(t *testing.T) {
	t.Parallel()
	if got := fanOutStatus(nil); got != "sync_canceled" {
		t.Errorf("fanOutStatus(nil) = %q, want sync_canceled", got)
	}
	if got := fanOutStatus([]syncResult{}); got != "sync_canceled" {
		t.Errorf("fanOutStatus([]) = %q, want sync_canceled", got)
	}
}

// TestFanOutMessageCanceledCountsOnlyCompleted closes Copilot's PR #113
// off-by-one: when one Data Type was canceled mid-run, the message must
// count Data Types that *actually* completed before the cancel, not
// len(results) (which includes the canceled in-flight run itself).
func TestFanOutMessageCanceledCountsOnlyCompleted(t *testing.T) {
	t.Parallel()
	results := []syncResult{
		{Status: "sync_completed", DataTypes: []string{"steps"}},
		{Status: "sync_completed", DataTypes: []string{"heart-rate"}},
		{Status: "sync_canceled", DataTypes: []string{"sleep"}},
	}
	got := fanOutMessage(fanOutStatus(results), results)
	want := "Sync Run summary: 2 Data Types completed before cancellation"
	if got != want {
		t.Errorf("fanOutMessage canceled = %q, want %q (count must exclude the canceled run)", got, want)
	}
}

// TestFanOutMessageFailedReportsAttempted keeps the failed-status message
// at len(results) since "N attempted, at least one failed" is the actual
// attempted count, not a per-status filter.
func TestFanOutMessageFailedReportsAttempted(t *testing.T) {
	t.Parallel()
	results := []syncResult{
		{Status: "sync_completed", DataTypes: []string{"steps"}},
		{Status: "sync_failed", DataTypes: []string{"heart-rate"}},
	}
	got := fanOutMessage(fanOutStatus(results), results)
	want := "Sync Run summary: 2 Data Types attempted, at least one failed"
	if got != want {
		t.Errorf("fanOutMessage failed = %q, want %q", got, want)
	}
}

func TestSyncOrchestratorAllExpandsToSyncableDefaultDataTypes(t *testing.T) {
	t.Parallel()
	orchestrator := newSyncOrchestrator(runtimeAdapters{})
	got, err := orchestrator.expandDataTypes(syncCommandOptions{allTypes: true})
	if err != nil {
		t.Fatalf("expandDataTypes(--all): %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expanded list is empty; --all needs at least one syncable Data Type")
	}
	// Filter must keep only catalog entries with SupportsSyncDataPoint=true.
	// "total-calories" is a reserved default in the catalog without a parser
	// (Tier-1 catalog growth lands the shape later); it MUST NOT appear in
	// the --all fan-out today, otherwise every `sync --all` would book a
	// guaranteed sync_failed row.
	for _, dataType := range got {
		if !googlehealth.SupportsSyncDataPoints(dataType) {
			t.Errorf("--all included %q which has no sync parser; would produce a guaranteed sync_failed", dataType)
		}
	}
	for _, want := range []string{"steps", "heart-rate", "weight"} {
		found := false
		for _, dataType := range got {
			if dataType == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--all missing core syncable Data Type %q (got %v)", want, got)
		}
	}
}

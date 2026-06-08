package main

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

// installMultiTypeSyncFake routes the runtime's fetchRawProvider to
// per-Data-Type canned responses. Used by orchestrator tests where one
// invocation needs to satisfy several sequential Sync Runs across
// different Data Types.
func installMultiTypeSyncFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, perType map[string]string) (runtimeAdapters, *[]rawProviderRequest) {
	t.Helper()
	var requests []rawProviderRequest
	runtime.fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("multi-type sync access token = %q, want stored token", accessToken)
		}
		body, ok := perType[request.dataType]
		if !ok {
			t.Fatalf("no fake page for dataType %q (endpoint %q)", request.dataType, request.endpointName)
		}
		requests = append(requests, request)
		return []byte(body), nil
	}
	return runtime, &requests
}

func TestSyncOrchestratorFansOutOnePerDataType(t *testing.T) {
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

	testRuntime, requests := installMultiTypeSyncFake(t, testRuntime, "connect-access-secret", map[string]string{
		"steps":      `{"dataPoints":[]}`,
		"heart-rate": `{"dataPoints":[]}`,
	})

	orchestrator := newSyncOrchestrator(testRuntime, nil)
	results, err := orchestrator.Sync(syncCommandOptions{
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

	testRuntime, _ = installMultiTypeSyncFake(t, testRuntime, "connect-access-secret", map[string]string{
		"steps":      `{`, // forces parse failure for steps
		"heart-rate": `{"dataPoints":[]}`,
	})

	orchestrator := newSyncOrchestrator(testRuntime, nil)
	results, err := orchestrator.Sync(syncCommandOptions{
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

	cancelCh := make(chan struct{})
	stepsCalled := false
	testRuntime.fetchRawProvider = func(request rawProviderRequest, _ string) ([]byte, error) {
		if request.dataType == "steps" {
			stepsCalled = true
			// Close the channel after the first Data Type finishes its single page.
			close(cancelCh)
			return []byte(`{"dataPoints":[]}`), nil
		}
		t.Fatalf("orchestrator continued past cancellation: hit dataType %q", request.dataType)
		return nil, nil
	}

	orchestrator := newSyncOrchestrator(testRuntime, cancelCh)
	results, err := orchestrator.Sync(syncCommandOptions{
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
	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, found, err := archive.ResolveSyncCursor(syncCursorKey{
		connectionID: connection.id,
		dataType:     "heart-rate",
		rollupKind:   syncCursorRollupKindNone,
	}); err != nil || found {
		t.Fatalf("heart-rate cursor: found=%v err=%v, want absent after cancellation", found, err)
	}
}

func TestSyncOrchestratorCancelsActiveDataTypeMidPagination(t *testing.T) {
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

	cancelCh := make(chan struct{})
	pageCount := 0
	testRuntime.fetchRawProvider = func(request rawProviderRequest, _ string) ([]byte, error) {
		pageCount++
		if pageCount == 1 {
			// First page succeeds with one Data Point and signals more pages.
			// Close cancel before returning so the executor's between-pages
			// check at the top of the next iteration observes it.
			close(cancelCh)
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

	orchestrator := newSyncOrchestrator(testRuntime, cancelCh)
	results, err := orchestrator.Sync(syncCommandOptions{
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
	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, found, err := archive.ResolveSyncCursor(syncCursorKey{
		connectionID: connection.id,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	}); err != nil || found {
		t.Fatalf("steps cursor after canceled run: found=%v err=%v, want absent (ADR-0008)", found, err)
	}
}

func TestSyncOrchestratorRejectsAllAndTypesTogether(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{accessToken: "x"})
	_ = testRuntime

	_, err := newSyncOrchestrator(testRuntime, nil).Sync(syncCommandOptions{
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

func TestInstallSyncCancelChannelClosesOnSIGINT(t *testing.T) {
	cancelCh, stop := installSyncCancelChannel()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to self: %v", err)
	}

	select {
	case <-cancelCh:
		// expected — signal handler closed the channel
	case <-time.After(2 * time.Second):
		t.Fatal("cancelCh did not close within 2s after SIGINT")
	}
}

func TestInstallSyncCancelChannelClosesOnStop(t *testing.T) {
	cancelCh, stop := installSyncCancelChannel()
	stop()

	select {
	case <-cancelCh:
		// expected — stop cancels the underlying context
	case <-time.After(2 * time.Second):
		t.Fatal("cancelCh did not close within 2s after stop()")
	}
	// stop is idempotent — calling it again must not panic.
	stop()
}

func TestSyncOrchestratorAllExpandsToSyncableDefaultDataTypes(t *testing.T) {
	orchestrator := newSyncOrchestrator(runtimeAdapters{}, nil)
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
		if !syncDataPointDataTypeSupported(dataType) {
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

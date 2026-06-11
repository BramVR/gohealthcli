package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// probedSyncRunRow captures the sync_runs columns the #236 tests
// assert on, read back directly from the archive. One shared probe for
// the heartbeat and fence suites so the column list cannot drift.
type probedSyncRunRow struct {
	status         string
	seenCount      int
	newCount       int
	updatedCount   int
	finishedAt     sql.NullString
	lastProgressAt sql.NullString
	errorSummary   sql.NullString
}

// probeSyncRunRow opens a second, read-only archive handle — the same
// way a `sync --status` poller in another terminal would — and returns
// the sync_runs row with the given id.
func probeSyncRunRow(t *testing.T, archivePath string, id int64) probedSyncRunRow {
	t.Helper()
	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		t.Fatalf("open archive read-only: %v", err)
	}
	defer db.Close()
	var row probedSyncRunRow
	if err := db.QueryRow(`SELECT status, seen_count, new_count, updated_count, finished_at, last_progress_at, error_summary
		FROM sync_runs WHERE id = ?`, id).Scan(
		&row.status, &row.seenCount, &row.newCount, &row.updatedCount, &row.finishedAt, &row.lastProgressAt, &row.errorSummary,
	); err != nil {
		t.Fatalf("read sync_runs row %d: %v", id, err)
	}
	return row
}

// syncStatusFixtureRun seeds one sync_runs row for `sync --status`
// tests. Pointers model the NULLable columns (finished_at,
// last_progress_at, error_summary).
type syncStatusFixtureRun struct {
	dataTypesJSON  string
	status         string
	seenCount      int
	newCount       int
	updatedCount   int
	startedAt      string
	finishedAt     *string
	lastProgressAt *string
	errorSummary   *string
}

func insertSyncStatusFixtureRuns(t *testing.T, archivePath string, runs []syncStatusFixtureRun) {
	t.Helper()
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	for _, run := range runs {
		if _, err := db.Exec(`INSERT INTO sync_runs (
			provider_name,
			connection_id,
			data_types_requested,
			range_requested_json,
			endpoint_family,
			status,
			seen_count,
			new_count,
			updated_count,
			started_at,
			finished_at,
			last_progress_at,
			error_summary
		) VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"googlehealth",
			run.dataTypesJSON,
			`{"from":"2026-06-01","to":"2026-06-10"}`,
			"list",
			run.status,
			run.seenCount,
			run.newCount,
			run.updatedCount,
			run.startedAt,
			run.finishedAt,
			run.lastProgressAt,
			run.errorSummary,
		); err != nil {
			t.Fatalf("insert sync_runs fixture: %v", err)
		}
	}
}

func stringPtr(value string) *string { return &value }

// fixedSyncStatusClock returns runtime adapters whose clock is pinned
// so window filtering, durations, and heartbeat ages are deterministic.
// Tests dispatch through runWithRuntime with the returned adapters
// instead of mutating package state (#283).
func fixedSyncStatusClock(now time.Time) runtimeAdapters {
	return runtimeAdapters{now: func() time.Time { return now }}
}

// seedSyncStatusFixture inserts the canonical four-row fixture the
// `sync --status` tests share:
//
//	id 1: completed 2 minutes ago (inside the window)
//	id 2: failed 10 minutes ago, multi-line error (inside the window)
//	id 3: running, started 20 minutes ago — OUTSIDE the 15m window but
//	      window-exempt because it is still sync_running — with a
//	      heartbeat 5 seconds ago
//	id 4: completed 2 hours ago (outside the window, must not appear)
func seedSyncStatusFixture(t *testing.T, archivePath string) {
	t.Helper()
	insertSyncStatusFixtureRuns(t, archivePath, []syncStatusFixtureRun{
		{
			dataTypesJSON: `["steps"]`,
			status:        "sync_completed",
			seenCount:     120, newCount: 5, updatedCount: 0,
			startedAt:      "2026-06-10T11:58:00Z",
			finishedAt:     stringPtr("2026-06-10T11:58:08Z"),
			lastProgressAt: stringPtr("2026-06-10T11:58:07Z"),
		},
		{
			dataTypesJSON: `["heart-rate"]`,
			status:        "sync_failed",
			startedAt:     "2026-06-10T11:50:00Z",
			finishedAt:    stringPtr("2026-06-10T11:50:05Z"),
			errorSummary:  stringPtr("Provider timeout after 30s\nretry later"),
		},
		{
			dataTypesJSON: `["heart-rate"]`,
			status:        "sync_running",
			seenCount:     50, newCount: 50, updatedCount: 0,
			startedAt:      "2026-06-10T11:40:00Z",
			lastProgressAt: stringPtr("2026-06-10T11:59:55Z"),
		},
		{
			dataTypesJSON: `["sleep"]`,
			status:        "sync_completed",
			seenCount:     7, newCount: 7, updatedCount: 0,
			startedAt:  "2026-06-10T09:58:00Z",
			finishedAt: stringPtr("2026-06-10T09:59:00Z"),
		},
	})
}

// TestSyncStatusListsRecentRunsWithWindowExemptRunningRows is the
// issue #236 slice 2 tracer bullet: `sync --status` renders one table
// row per recent Sync Run, sorted by id. Terminal rows outside the
// 15-minute window are dropped, but a sync_running row is
// window-exempt — the long in-flight run is exactly the row the
// operator is polling for, so it must never age out of the default
// view while still running.
func TestSyncStatusListsRecentRunsWithWindowExemptRunningRows(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := fixedSyncStatusClock(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	seedSyncStatusFixture(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--status",
		"--config", configPath,
		"--db", archivePath,
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync --status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := "Sync Run status\n" +
		"Health Archive: " + archivePath + "\n" +
		"ID  DATA_TYPES  STATUS          SEEN  NEW  UPDATED  DURATION  LAST_PROGRESS  ERROR\n" +
		"1   steps       sync_completed  120   5    0        8s        1m53s          -\n" +
		"2   heart-rate  sync_failed     0     0    0        5s        -              Provider timeout after 30s\n" +
		"3   heart-rate  sync_running    50    50   0        20m0s     5s             -\n" +
		"Message: 3 Sync Runs in the last 15m0s\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

// TestSyncStatusJSONEmitsSharedEnvelope pins the --json wire shape:
// the same status/archive_path/message envelope keys the other read
// commands carry, a window echo, and one runs[] entry per listed Sync
// Run with NULLable fields omitted. Golden-string equality keeps key
// order and indentation pinned the way `status --json` consumers
// already rely on.
func TestSyncStatusJSONEmitsSharedEnvelope(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := fixedSyncStatusClock(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	seedSyncStatusFixture(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--status",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync --status --json exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := `{
  "status": "ok",
  "archive_path": ` + jsonString(t, archivePath) + `,
  "window": "15m0s",
  "runs": [
    {
      "id": 1,
      "data_types": [
        "steps"
      ],
      "status": "sync_completed",
      "seen_count": 120,
      "new_count": 5,
      "updated_count": 0,
      "duration_seconds": 8,
      "started_at": "2026-06-10T11:58:00Z",
      "finished_at": "2026-06-10T11:58:08Z",
      "last_progress_at": "2026-06-10T11:58:07Z"
    },
    {
      "id": 2,
      "data_types": [
        "heart-rate"
      ],
      "status": "sync_failed",
      "seen_count": 0,
      "new_count": 0,
      "updated_count": 0,
      "duration_seconds": 5,
      "started_at": "2026-06-10T11:50:00Z",
      "finished_at": "2026-06-10T11:50:05Z",
      "error_summary": "Provider timeout after 30s"
    },
    {
      "id": 3,
      "data_types": [
        "heart-rate"
      ],
      "status": "sync_running",
      "seen_count": 50,
      "new_count": 50,
      "updated_count": 0,
      "duration_seconds": 1200,
      "started_at": "2026-06-10T11:40:00Z",
      "last_progress_at": "2026-06-10T11:59:55Z"
    }
  ],
  "message": "3 Sync Runs in the last 15m0s"
}
`
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

// jsonString renders value exactly the way encoding/json will inside
// the golden envelope (the archive path contains no specials on most
// systems, but Windows backslashes must round-trip).
func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %q: %v", value, err)
	}
	return string(encoded)
}

// TestSyncStatusWindowFlagWidensTheTerminalRowCutoff covers --window:
// a 4h window pulls the 2-hour-old completed run (fixture id 4) back
// into view; the window echo and message follow the parsed duration.
func TestSyncStatusWindowFlagWidensTheTerminalRowCutoff(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := fixedSyncStatusClock(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	seedSyncStatusFixture(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--status",
		"--window", "4h",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync --status --window 4h exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	for _, want := range []string{
		"window: 4h0m0s\n",
		"sync_run.3.id: 4\n",
		"sync_run.3.status: sync_completed\n",
		"message: 4 Sync Runs in the last 4h0m0s\n",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

// TestSyncStatusRejectsInvalidWindow pins the flag-validation
// failures: a non-duration, a non-positive duration, and a window
// past the 24h cap all exit 1 with a targeted message and never open
// the archive.
func TestSyncStatusRejectsInvalidWindow(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	for _, testCase := range []struct {
		window      string
		wantMessage string
	}{
		{"soon", "--window soon is not a valid Go duration"},
		{"-5m", "--window -5m must be positive"},
		{"0s", "--window 0s must be positive"},
		{"25h", "--window 25h exceeds the 24h maximum"},
	} {
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		code := run([]string{
			"sync",
			"--status",
			"--window", testCase.window,
			"--config", configPath,
			"--db", archivePath,
		}, stdout, stderr)
		if code != 1 {
			t.Fatalf("--window %s exit code = %d, want 1\nstdout: %s\nstderr: %s", testCase.window, code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), testCase.wantMessage) {
			t.Fatalf("--window %s stderr = %q, want it to contain %q", testCase.window, stderr.String(), testCase.wantMessage)
		}
	}
}

// TestSyncStatusRejectsSyncExecutionFlags: --status flips sync into a
// read-only view, so combining it with any flag that shapes an actual
// sync is a usage error, not a silent ignore. --window inverts the
// rule: it only means something WITH --status.
func TestSyncStatusRejectsSyncExecutionFlags(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	for _, testCase := range []struct {
		name        string
		args        []string
		wantMessage string
	}{
		{"types", []string{"--status", "--types", "steps"}, "--types cannot be combined with --status"},
		{"all", []string{"--status", "--all"}, "--all cannot be combined with --status"},
		{"from", []string{"--status", "--from", "2026-01-01"}, "--from cannot be combined with --status"},
		{"to", []string{"--status", "--to", "2026-01-02"}, "--to cannot be combined with --status"},
		{"rollup", []string{"--status", "--rollup", "daily"}, "--rollup cannot be combined with --status"},
		{"source-family", []string{"--status", "--source-family", "wearable"}, "--source-family cannot be combined with --status"},
		{"window without status", []string{"--window", "15m"}, "--window requires --status"},
	} {
		args := append([]string{"sync"}, testCase.args...)
		args = append(args, "--config", configPath, "--db", archivePath)
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		code := run(args, stdout, stderr)
		if code != 1 {
			t.Fatalf("%s: exit code = %d, want 1\nstdout: %s\nstderr: %s", testCase.name, code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), testCase.wantMessage) {
			t.Fatalf("%s: stderr = %q, want it to contain %q", testCase.name, stderr.String(), testCase.wantMessage)
		}
	}
}

// TestSyncStatusPlainEmitsKeyValueLinesPerRun pins the --plain shape:
// indexed sync_run.<n>.<key> lines in the same style query and status
// use, with NULLable fields omitted rather than emitted empty, and the
// multi-line upstream error truncated to its first line.
func TestSyncStatusPlainEmitsKeyValueLinesPerRun(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := fixedSyncStatusClock(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	seedSyncStatusFixture(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--status",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync --status --plain exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := "status: ok\n" +
		"archive_path: " + archivePath + "\n" +
		"window: 15m0s\n" +
		"sync_run.0.id: 1\n" +
		"sync_run.0.data_types: steps\n" +
		"sync_run.0.status: sync_completed\n" +
		"sync_run.0.seen_count: 120\n" +
		"sync_run.0.new_count: 5\n" +
		"sync_run.0.updated_count: 0\n" +
		"sync_run.0.duration_seconds: 8\n" +
		"sync_run.0.started_at: 2026-06-10T11:58:00Z\n" +
		"sync_run.0.finished_at: 2026-06-10T11:58:08Z\n" +
		"sync_run.0.last_progress_at: 2026-06-10T11:58:07Z\n" +
		"sync_run.1.id: 2\n" +
		"sync_run.1.data_types: heart-rate\n" +
		"sync_run.1.status: sync_failed\n" +
		"sync_run.1.seen_count: 0\n" +
		"sync_run.1.new_count: 0\n" +
		"sync_run.1.updated_count: 0\n" +
		"sync_run.1.duration_seconds: 5\n" +
		"sync_run.1.started_at: 2026-06-10T11:50:00Z\n" +
		"sync_run.1.finished_at: 2026-06-10T11:50:05Z\n" +
		"sync_run.1.error_summary: Provider timeout after 30s\n" +
		"sync_run.2.id: 3\n" +
		"sync_run.2.data_types: heart-rate\n" +
		"sync_run.2.status: sync_running\n" +
		"sync_run.2.seen_count: 50\n" +
		"sync_run.2.new_count: 50\n" +
		"sync_run.2.updated_count: 0\n" +
		"sync_run.2.duration_seconds: 1200\n" +
		"sync_run.2.started_at: 2026-06-10T11:40:00Z\n" +
		"sync_run.2.last_progress_at: 2026-06-10T11:59:55Z\n" +
		"message: 3 Sync Runs in the last 15m0s\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// `sync --status` (#236) is the polling surface for in-flight and
// recent Sync Runs: it reads the local sync_runs audit table — the
// same rows the per-page heartbeat keeps fresh — and renders them
// without any provider I/O. It is deliberately narrower than
// `gohealthcli status` (whole-archive summary): this view answers
// "is the long sync still alive and moving?" for a human in a second
// terminal or an agent polling between its own tool calls.

const (
	// syncStatusDefaultWindow bounds how far back terminal Sync Runs
	// are listed when --window is not passed. sync_running rows are
	// window-EXEMPT: a 24-minute heart-rate run started before the
	// cutoff is exactly the row the operator is polling for, so it
	// must never age out of the default view while still running.
	syncStatusDefaultWindow = 15 * time.Minute
	// syncStatusMaxWindow caps --window so a typo (`--window 15000m`)
	// cannot turn the live-oriented view into a full-table dump —
	// `query` is the right tool for archaeology.
	syncStatusMaxWindow = 24 * time.Hour
	// syncRunFenceStaleMinutes is the single source of truth for how
	// long a sync_running row may go without a heartbeat before the
	// fence declares it abandoned — syncRunFenceStaleAfter and the
	// audit-trail marker both derive from it. The fence keys on
	// heartbeat staleness, NOT wall-clock age: a legitimate
	// single-type run has been observed at 1422s (~24 min), so any
	// started_at-based threshold either mis-flags live runs or reacts
	// uselessly slowly. With pre-fetch heartbeats, five silent minutes
	// means the process is gone (or wedged inside a single page beyond
	// any retry budget) — and rows that predate heartbeats fall back
	// to started_at via the COALESCE in fenceAbandonedSyncRuns.
	syncRunFenceStaleMinutes = 5
	syncRunFenceStaleAfter   = syncRunFenceStaleMinutes * time.Minute
)

// syncRunFenceErrorSummary is the audit-trail marker for fenced rows;
// operators and tests grep for it verbatim. Derived from
// syncRunFenceStaleMinutes so retuning the threshold cannot leave the
// marker lying about the rule that fired.
var syncRunFenceErrorSummary = fmt.Sprintf("abandoned (no heartbeat for %dm)", syncRunFenceStaleMinutes)

// fenceAbandonedSyncRuns drives orphaned sync_running rows (killed
// terminal, SIGKILL — anything that skipped the finalize path) to
// sync_failed so they stop reading as alive. Idempotent: the WHERE
// clause only matches sync_running rows, so a second pass touches
// zero rows. The fence never touches sync_cursors — only
// FinalizeSyncRun advances the cursor (ADR-0008), so a fenced row
// behaves exactly like any other failed run for resume purposes. If
// the fenced process is somehow still alive, its eventual
// FinalizeSyncRun UPDATE is unconditional on id and simply overwrites
// the fence — the row converges to its true terminal status.
func fenceAbandonedSyncRuns(ctx context.Context, db *sql.DB, now time.Time) (int64, error) {
	cutoff := now.Add(-syncRunFenceStaleAfter).UTC().Format(time.RFC3339)
	var fenced int64
	// The UPDATE runs under the same SQLITE_BUSY retry budget the
	// terminal writes use: the fence fires exactly when another
	// process may be mid-sync, so brief lock contention is the
	// expected case, not the exception.
	err := retryFinalizeSyncRunOnBusy(finalizeSyncRunRetryBudget, func() error {
		result, err := db.ExecContext(ctx, `UPDATE sync_runs SET
			status = 'sync_failed',
			error_summary = ?,
			finished_at = ?
		WHERE status = 'sync_running'
		  AND COALESCE(last_progress_at, started_at) < ?`,
			syncRunFenceErrorSummary,
			now.UTC().Format(time.RFC3339),
			cutoff,
		)
		if err != nil {
			return err
		}
		fenced, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return 0, err
	}
	return fenced, nil
}

// fenceAbandonedSyncRunsAtPath is the entry-point flavor for callers
// that do not already hold an open archive handle (`status`, the
// `sync` write path). Opening through the lifecycle runs pending
// migrations first, so the fence can rely on last_progress_at
// existing.
func fenceAbandonedSyncRunsAtPath(ctx context.Context, archivePath string, now time.Time) (int64, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(ctx, writeArchive)
	if err != nil {
		return 0, err
	}
	defer handle.Close()
	return fenceAbandonedSyncRuns(ctx, handle.db, now)
}

type syncStatusRun struct {
	ID           int64    `json:"id"`
	DataTypes    []string `json:"data_types,omitempty"`
	Status       string   `json:"status"`
	SeenCount    int      `json:"seen_count"`
	NewCount     int      `json:"new_count"`
	UpdatedCount int      `json:"updated_count"`
	// DurationSeconds is now-relative for sync_running rows and
	// finished_at-relative for terminal rows, so a poller can read
	// "how long has this been going" without re-deriving it.
	DurationSeconds int64  `json:"duration_seconds"`
	StartedAt       string `json:"started_at,omitempty"`
	FinishedAt      string `json:"finished_at,omitempty"`
	LastProgressAt  string `json:"last_progress_at,omitempty"`
	ErrorSummary    string `json:"error_summary,omitempty"`
}

type syncStatusResult struct {
	Status      string          `json:"status"`
	ArchivePath string          `json:"archive_path"`
	Window      string          `json:"window"`
	Runs        []syncStatusRun `json:"runs,omitempty"`
	Message     string          `json:"message"`
}

// syncExecutionFlagNames are the sync flags that configure an actual
// sync and therefore conflict with --status. Order matters: the error
// message names the FIRST conflicting flag in this order, so the
// message is deterministic when several are passed at once.
var syncExecutionFlagNames = []string{"types", "all", "from", "to", "rollup", "source-family"}

// validateSyncStatusFlagSet enforces the --status flag-surface rules
// against the flags the user ACTUALLY passed (flagWasProvided walks
// the set flags, so defaults never trip it).
func validateSyncStatusFlagSet(flags *flag.FlagSet, statusRequested bool) error {
	if statusRequested {
		for _, name := range syncExecutionFlagNames {
			if flagWasProvided(flags, name) {
				return fmt.Errorf("--%s cannot be combined with --status", name)
			}
		}
		return nil
	}
	if flagWasProvided(flags, "window") {
		return fmt.Errorf("--window requires --status")
	}
	return nil
}

func runSyncStatusWithRuntime(common CommonFlagValues, windowValue string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	runtime = runtime.withDefaults()
	window, err := parseSyncStatusWindow(windowValue)
	if err != nil {
		return ReportFailure(FailureReport{
			Command: "sync",
			Status:  StatusFlagInvalid,
			Message: err.Error(),
			Mode:    mode,
		}, stdout, stderr)
	}
	// `sync --status` is a read command in sync's clothing: archive
	// path resolution follows the status/query/export resolver rules,
	// not the write-side config requirement.
	now := runtime.now().UTC()
	resolvedArchivePath, err := resolveReadArchivePath(common)
	if err != nil {
		return writeSyncStatusResultWithExit(syncStatusResult{
			Status:      "sync_status_failed",
			ArchivePath: common.ArchivePath,
			Window:      window.String(),
			Message:     err.Error(),
		}, 1, mode, now, stdout, stderr)
	}
	// context.Background(): `sync --status` is a synchronous read view
	// with no cancellation path today; the context keeps the SQLite
	// calls on the Context API (#305) without changing behavior.
	result, err := syncStatusSetup(context.Background(), resolvedArchivePath, now, window)
	if err != nil {
		result.Message = err.Error()
		return writeSyncStatusResultWithExit(result, 1, mode, now, stdout, stderr)
	}
	return writeSyncStatusResultWithExit(result, 0, mode, now, stdout, stderr)
}

// writeSyncStatusResultWithExit renders the envelope and returns
// exitCode, except when the rendering itself fails — then the
// write-output failure contract takes over. One helper for the
// success and failure paths so that contract cannot fork.
func writeSyncStatusResultWithExit(result syncStatusResult, exitCode int, mode outputMode, now time.Time, stdout, stderr io.Writer) int {
	if err := writeSyncStatusResult(result, mode, now, stdout); err != nil {
		return reportWriteFailure("sync", err, mode, stdout, stderr)
	}
	return exitCode
}

func parseSyncStatusWindow(windowValue string) (time.Duration, error) {
	if windowValue == "" {
		return syncStatusDefaultWindow, nil
	}
	window, err := time.ParseDuration(windowValue)
	if err != nil {
		return 0, fmt.Errorf("--window %s is not a valid Go duration (try 15m or 2h)", windowValue)
	}
	if window <= 0 {
		return 0, fmt.Errorf("--window %s must be positive", windowValue)
	}
	if window > syncStatusMaxWindow {
		return 0, fmt.Errorf("--window %s exceeds the 24h maximum", windowValue)
	}
	return window, nil
}

func syncStatusSetup(ctx context.Context, archivePath string, now time.Time, window time.Duration) (syncStatusResult, error) {
	result := syncStatusResult{
		Status:      "sync_status_failed",
		ArchivePath: archivePath,
		Window:      window.String(),
	}
	// Fence abandoned sync_running rows first (#236), so the rows this
	// very invocation renders are already truthful. Best-effort, on a
	// separate short-lived write handle: the view itself reads through
	// a read-only open below, so `sync --status` keeps working on
	// read-only media and never fails because a fence write lost its
	// SQLITE_BUSY retry budget — the worst case is one stale corpse
	// row that the next entry point fences.
	_, _ = fenceAbandonedSyncRunsAtPath(ctx, archivePath, now)
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(ctx, readOnlyArchive)
	if err != nil {
		return result, err
	}
	defer handle.Close()
	runs, err := readSyncStatusRuns(ctx, handle.db, now, window)
	if err != nil {
		return result, err
	}
	result.Runs = runs
	result.Status = "ok"
	result.Message = syncStatusMessage(len(runs), window)
	return result, nil
}

func syncStatusMessage(runCount int, window time.Duration) string {
	if runCount == 0 {
		return fmt.Sprintf("no Sync Runs in the last %s", window)
	}
	if runCount == 1 {
		return fmt.Sprintf("1 Sync Run in the last %s", window)
	}
	return fmt.Sprintf("%d Sync Runs in the last %s", runCount, window)
}

// readSyncStatusRuns returns the recent Sync Runs, oldest first.
// Terminal rows are windowed on COALESCE(finished_at, started_at) —
// recency of ACTIVITY, not of launch — so a 2-hour run that finished
// (or was fenced) a minute ago still shows, while sync_running rows
// bypass the window entirely (see syncStatusDefaultWindow).
// RFC3339-UTC timestamps compare correctly as strings, so the cutoff
// is computed in Go — keeping `now` injectable for tests — rather
// than via SQLite datetime().
func readSyncStatusRuns(ctx context.Context, db *sql.DB, now time.Time, window time.Duration) ([]syncStatusRun, error) {
	cutoff := now.Add(-window).Format(time.RFC3339)
	rows, err := db.QueryContext(ctx, `SELECT
		id,
		data_types_requested,
		status,
		seen_count,
		new_count,
		updated_count,
		started_at,
		finished_at,
		last_progress_at,
		error_summary
	FROM sync_runs
	WHERE status = 'sync_running' OR COALESCE(finished_at, started_at) > ?
	ORDER BY id ASC`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []syncStatusRun
	for rows.Next() {
		var run syncStatusRun
		var dataTypesJSON string
		var finishedAt, lastProgressAt, errorSummary sql.NullString
		if err := rows.Scan(
			&run.ID,
			&dataTypesJSON,
			&run.Status,
			&run.SeenCount,
			&run.NewCount,
			&run.UpdatedCount,
			&run.StartedAt,
			&finishedAt,
			&lastProgressAt,
			&errorSummary,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dataTypesJSON), &run.DataTypes); err != nil {
			return nil, fmt.Errorf("Sync Run %d data types are not valid JSON: %w", run.ID, err)
		}
		if finishedAt.Valid {
			run.FinishedAt = finishedAt.String
		}
		if lastProgressAt.Valid {
			run.LastProgressAt = lastProgressAt.String
		}
		if errorSummary.Valid {
			run.ErrorSummary = shortErrorSummary(errorSummary.String)
		}
		run.DurationSeconds = syncStatusRunDurationSeconds(run, now)
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// syncStatusRunDurationSeconds derives the duration column: terminal
// rows measure started_at→finished_at; in-flight rows measure
// started_at→now so the poller watches the number grow. Unparseable
// timestamps degrade to 0 rather than failing the whole view — this
// is a status surface, not an integrity check.
func syncStatusRunDurationSeconds(run syncStatusRun, now time.Time) int64 {
	started, err := time.Parse(time.RFC3339, run.StartedAt)
	if err != nil {
		return 0
	}
	end := now
	if run.FinishedAt != "" {
		finished, err := time.Parse(time.RFC3339, run.FinishedAt)
		if err != nil {
			return 0
		}
		end = finished
	}
	seconds := int64(end.Sub(started) / time.Second)
	if seconds < 0 {
		return 0
	}
	return seconds
}

func writeSyncStatusResult(result syncStatusResult, mode outputMode, now time.Time, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeSyncStatusPlain(writer, result)
	} else {
		writeSyncStatusHuman(writer, result, now)
	}
	return writer.Err()
}

func writeSyncStatusHuman(writer *stickyWriter, result syncStatusResult, now time.Time) {
	writer.Println("Sync Run status")
	if result.ArchivePath != "" {
		writer.Printf("Health Archive: %s\n", result.ArchivePath)
	}
	if len(result.Runs) != 0 {
		// The tabwriter buffers rows and only touches the sticky writer
		// on Flush, where any write error latches via the io.Writer
		// face — Flush's return is the same error, already captured.
		table := tabwriter.NewWriter(writer, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "ID\tDATA_TYPES\tSTATUS\tSEEN\tNEW\tUPDATED\tDURATION\tLAST_PROGRESS\tERROR")
		for _, run := range result.Runs {
			fmt.Fprintf(table, "%d\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\n",
				run.ID,
				strings.Join(run.DataTypes, ","),
				run.Status,
				run.SeenCount,
				run.NewCount,
				run.UpdatedCount,
				(time.Duration(run.DurationSeconds) * time.Second).String(),
				syncStatusTableCell(syncStatusLastProgressAge(run, now)),
				syncStatusTableCell(run.ErrorSummary),
			)
		}
		_ = table.Flush()
	}
	writer.Printf("Message: %s\n", result.Message)
}

// syncStatusLastProgressAge renders the heartbeat recency ("5s",
// "1m53s") a poller cares about more than the raw timestamp. Empty
// when the row has no heartbeat (pre-#236 rows, or a run that died
// before its first page).
func syncStatusLastProgressAge(run syncStatusRun, now time.Time) string {
	if run.LastProgressAt == "" {
		return ""
	}
	lastProgress, err := time.Parse(time.RFC3339, run.LastProgressAt)
	if err != nil {
		return ""
	}
	age := now.Sub(lastProgress)
	if age < 0 {
		age = 0
	}
	return age.Truncate(time.Second).String()
}

// syncStatusTableCell keeps empty cells visibly aligned: tabwriter
// pads every non-terminal cell, so an empty string would render as a
// blank gap that reads like a parsing bug.
func syncStatusTableCell(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func writeSyncStatusPlain(writer *stickyWriter, result syncStatusResult) {
	writer.Printf("status: %s\n", result.Status)
	if result.ArchivePath != "" {
		writer.Printf("archive_path: %s\n", result.ArchivePath)
	}
	writer.Printf("window: %s\n", result.Window)
	for index, run := range result.Runs {
		writeSyncStatusRunPlain(writer, fmt.Sprintf("sync_run.%d.", index), run)
	}
	writer.Printf("message: %s\n", result.Message)
}

func writeSyncStatusRunPlain(writer *stickyWriter, prefix string, run syncStatusRun) {
	writer.Printf("%sid: %d\n", prefix, run.ID)
	if len(run.DataTypes) != 0 {
		writer.Printf("%sdata_types: %s\n", prefix, strings.Join(run.DataTypes, ","))
	}
	writer.Printf("%sstatus: %s\n", prefix, run.Status)
	writer.Printf("%sseen_count: %d\n", prefix, run.SeenCount)
	writer.Printf("%snew_count: %d\n", prefix, run.NewCount)
	writer.Printf("%supdated_count: %d\n", prefix, run.UpdatedCount)
	writer.Printf("%sduration_seconds: %d\n", prefix, run.DurationSeconds)
	writer.Printf("%sstarted_at: %s\n", prefix, run.StartedAt)
	if run.FinishedAt != "" {
		writer.Printf("%sfinished_at: %s\n", prefix, run.FinishedAt)
	}
	if run.LastProgressAt != "" {
		writer.Printf("%slast_progress_at: %s\n", prefix, run.LastProgressAt)
	}
	if run.ErrorSummary != "" {
		writer.Printf("%serror_summary: %s\n", prefix, run.ErrorSummary)
	}
}

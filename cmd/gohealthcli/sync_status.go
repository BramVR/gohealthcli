package main

import (
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
	// syncRunFenceStaleAfter is how long a sync_running row may go
	// without a heartbeat before the fence declares it abandoned. The
	// fence keys on heartbeat staleness, NOT wall-clock age: a
	// legitimate single-type run has been observed at 1422s (~24 min),
	// so any started_at-based threshold either mis-flags live runs or
	// reacts uselessly slowly. With per-page heartbeats, five silent
	// minutes means the process is gone (or wedged mid-page beyond any
	// retry budget) — and rows that predate heartbeats fall back to
	// started_at via the COALESCE in fenceAbandonedSyncRuns.
	syncRunFenceStaleAfter = 5 * time.Minute
	// syncRunFenceErrorSummary is the audit-trail marker for fenced
	// rows. Tests and operators grep for it verbatim; keep in sync
	// with syncRunFenceStaleAfter.
	syncRunFenceErrorSummary = "abandoned (no heartbeat for 5m)"
)

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
func fenceAbandonedSyncRuns(db *sql.DB, now time.Time) (int64, error) {
	cutoff := now.Add(-syncRunFenceStaleAfter).UTC().Format(time.RFC3339)
	result, err := db.Exec(`UPDATE sync_runs SET
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
		return 0, err
	}
	return result.RowsAffected()
}

// fenceAbandonedSyncRunsAtPath is the entry-point flavor for callers
// that do not already hold an open archive handle (`status`, the
// `sync` write path). Opening through the lifecycle runs pending
// migrations first, so the fence can rely on last_progress_at
// existing.
func fenceAbandonedSyncRunsAtPath(archivePath string, now time.Time) (int64, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(writeArchive)
	if err != nil {
		return 0, err
	}
	defer handle.Close()
	return fenceAbandonedSyncRuns(handle.db, now)
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
// against the flags the user ACTUALLY passed (flag.Visit only walks
// set flags, so defaults never trip it).
func validateSyncStatusFlagSet(flags *flag.FlagSet, statusRequested bool) error {
	passed := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { passed[item.Name] = true })
	if statusRequested {
		for _, name := range syncExecutionFlagNames {
			if passed[name] {
				return fmt.Errorf("--%s cannot be combined with --status", name)
			}
		}
		return nil
	}
	if passed["window"] {
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
	resolvedArchivePath, err := resolveReadArchivePath(common)
	if err != nil {
		return writeSyncStatusFailure(syncStatusResult{
			Status:      "sync_status_failed",
			ArchivePath: common.ArchivePath,
			Window:      window.String(),
			Message:     err.Error(),
		}, mode, runtime.now().UTC(), stdout, stderr)
	}
	now := runtime.now().UTC()
	result, err := syncStatusSetup(resolvedArchivePath, now, window)
	if err != nil {
		result.Message = err.Error()
		return writeSyncStatusFailure(result, mode, now, stdout, stderr)
	}
	if err := writeSyncStatusResult(result, mode, now, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "sync",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func writeSyncStatusFailure(result syncStatusResult, mode outputMode, now time.Time, stdout, stderr io.Writer) int {
	if err := writeSyncStatusResult(result, mode, now, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "sync",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 1
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

func syncStatusSetup(archivePath string, now time.Time, window time.Duration) (syncStatusResult, error) {
	result := syncStatusResult{
		Status:      "sync_status_failed",
		ArchivePath: archivePath,
		Window:      window.String(),
	}
	// Write mode, not read-only: entering `sync --status` fences
	// abandoned sync_running rows first (#236), so the rows this very
	// invocation renders are already truthful. The Health Archive is
	// owner-only by construction (0600, enforced on every open), so
	// requiring writability here costs nothing real.
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(writeArchive)
	if err != nil {
		return result, err
	}
	defer handle.Close()
	if _, err := fenceAbandonedSyncRuns(handle.db, now); err != nil {
		return result, err
	}
	runs, err := readSyncStatusRuns(handle.db, now, window)
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
func readSyncStatusRuns(db *sql.DB, now time.Time, window time.Duration) ([]syncStatusRun, error) {
	cutoff := now.Add(-window).Format(time.RFC3339)
	rows, err := db.Query(`SELECT
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
	if mode.plain {
		return writeSyncStatusPlain(result, stdout)
	}
	if _, err := fmt.Fprintln(stdout, "Sync Run status"); err != nil {
		return err
	}
	if result.ArchivePath != "" {
		if _, err := fmt.Fprintf(stdout, "Health Archive: %s\n", result.ArchivePath); err != nil {
			return err
		}
	}
	if len(result.Runs) != 0 {
		table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(table, "ID\tDATA_TYPES\tSTATUS\tSEEN\tNEW\tUPDATED\tDURATION\tLAST_PROGRESS\tERROR"); err != nil {
			return err
		}
		for _, run := range result.Runs {
			if _, err := fmt.Fprintf(table, "%d\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\n",
				run.ID,
				strings.Join(run.DataTypes, ","),
				run.Status,
				run.SeenCount,
				run.NewCount,
				run.UpdatedCount,
				(time.Duration(run.DurationSeconds) * time.Second).String(),
				syncStatusTableCell(syncStatusLastProgressAge(run, now)),
				syncStatusTableCell(run.ErrorSummary),
			); err != nil {
				return err
			}
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
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

func writeSyncStatusPlain(result syncStatusResult, stdout io.Writer) error {
	if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
		return err
	}
	if result.ArchivePath != "" {
		if _, err := fmt.Fprintf(stdout, "archive_path: %s\n", result.ArchivePath); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "window: %s\n", result.Window); err != nil {
		return err
	}
	for index, run := range result.Runs {
		prefix := fmt.Sprintf("sync_run.%d.", index)
		if _, err := fmt.Fprintf(stdout, "%sid: %d\n", prefix, run.ID); err != nil {
			return err
		}
		if len(run.DataTypes) != 0 {
			if _, err := fmt.Fprintf(stdout, "%sdata_types: %s\n", prefix, strings.Join(run.DataTypes, ",")); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "%sstatus: %s\n", prefix, run.Status); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "%sseen_count: %d\n", prefix, run.SeenCount); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "%snew_count: %d\n", prefix, run.NewCount); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "%supdated_count: %d\n", prefix, run.UpdatedCount); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "%sduration_seconds: %d\n", prefix, run.DurationSeconds); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "%sstarted_at: %s\n", prefix, run.StartedAt); err != nil {
			return err
		}
		if run.FinishedAt != "" {
			if _, err := fmt.Fprintf(stdout, "%sfinished_at: %s\n", prefix, run.FinishedAt); err != nil {
				return err
			}
		}
		if run.LastProgressAt != "" {
			if _, err := fmt.Fprintf(stdout, "%slast_progress_at: %s\n", prefix, run.LastProgressAt); err != nil {
				return err
			}
		}
		if run.ErrorSummary != "" {
			if _, err := fmt.Fprintf(stdout, "%serror_summary: %s\n", prefix, run.ErrorSummary); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
	return err
}

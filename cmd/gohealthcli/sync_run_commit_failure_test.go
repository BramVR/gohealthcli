package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/archived"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestSyncRunCommitFailureReleasesTransaction(t *testing.T) {
	t.Parallel()
	checkCommitFailureCases(t, requireHolds)
}

func checkCommitFailureCases(t *testing.T, consume func(*testing.T, syncVerdict)) {
	t.Helper()
	for _, seeded := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent_cursor", true: "existing_cursor"}[seeded], func(t *testing.T) {
			consume(t, observeCommitFailure(t, seeded))
		})
	}
}

func observeCommitFailure(t *testing.T, seeded bool) syncVerdict {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.sqlite")
	db, err := openArchive(path)
	if err != nil {
		t.Fatalf("open writer: %q", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close writer: %q", err)
		}
	})
	if err := applyMigrations(ctx, db, func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }); err != nil {
		t.Fatalf("migrate: %q", err)
	}
	// An acquired reader lock permits writes but rejects rollback-journal COMMIT.
	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&journal); err != nil || journal != "delete" {
		t.Fatalf("journal mode: %q, error: %q", journal, err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		t.Fatalf("set busy timeout: %q", err)
	}
	var timeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 0 {
		t.Fatalf("busy timeout: %d, error: %q", timeout, err)
	}
	connection := archived.Connection{ID: "google_health:commit-test", ProviderName: "google_health"}
	if _, err := db.ExecContext(ctx, `INSERT INTO connections
		(id, provider_name, google_health_user_id, created_at, updated_at)
		VALUES (?, ?, 'commit-test', '2026-01-01', '2026-01-01')`, connection.ID, connection.ProviderName); err != nil {
		t.Fatalf("seed connection: %q", err)
	}
	archive := &sqliteHealthArchiveWriter{db: db, archivePath: path}
	key := syncCursorKey{connectionID: connection.ID, dataType: "steps", rollupKind: syncCursorRollupKindNone}
	if seeded {
		if err := archive.CommitSyncCursor(ctx, key, syncRunOutcomeCompleted, "2026-01-02T00:00:00Z", "2026-01-02T01:00:00Z"); err != nil {
			t.Fatalf("seed cursor: %q", err)
		}
	}
	id, err := archive.StartSyncRun(ctx, syncRunStart{Connection: connection, DataTypes: []string{"steps"},
		From: "2026-01-02", To: "2026-01-03", EndpointFamily: "list", StartedAt: "2026-01-03T00:00:00Z"})
	if err != nil {
		t.Fatalf("start run: %q", err)
	}
	beforeRuns := commitTableSnapshot(t, db, "SELECT * FROM sync_runs ORDER BY id")
	beforeCursors := commitTableSnapshot(t, db, "SELECT * FROM sync_cursors ORDER BY connection_id, data_type, source_family_filter, rollup_kind")
	reader, err := openArchiveReadOnly(path)
	if err != nil {
		t.Fatalf("open reader: %q", err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close reader: %q", err)
		}
	})
	readTx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("reader Begin: %q", err)
	}
	readerReleased := false
	t.Cleanup(func() {
		if !readerReleased {
			if err := readTx.Rollback(); err != nil {
				t.Errorf("reader cleanup Rollback: %q", err)
			}
		}
	})
	var count int
	if err := readTx.QueryRowContext(ctx, "SELECT count(*) FROM sync_runs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("acquire reader lock: count=%d, error: %q", count, err)
	}
	finalize := syncRunFinalize{SyncRunID: id, Outcome: syncRunOutcomeCompleted, SeenCount: 7, NewCount: 3, UpdatedCount: 2,
		FinishedAt: "2026-01-03T01:00:00Z", CursorKey: key, CursorTo: "2026-01-03T00:00:00Z", CursorAdvanced: "2026-01-03T01:00:00Z"}
	requireCommitBusy(t, archive.finalizeSyncRunAttempt(ctx, finalize))
	if verdict := probeTransactionRelease(t, db); verdict.violated != "" {
		return verdict
	}
	finalizeErr := archive.FinalizeSyncRun(ctx, finalize)
	var exhausted *errFinalizeSyncRunBusyExhausted
	if !errors.As(finalizeErr, &exhausted) || exhausted.attempts != finalizeSyncRunRetryBudget {
		t.Fatalf("expected bounded busy exhaustion: %q", finalizeErr)
	}
	requireCommitBusy(t, exhausted.cause)
	if err := readTx.Rollback(); err != nil {
		t.Fatalf("release reader: %q", err)
	}
	readerReleased = true
	if verdict := probeTransactionRelease(t, db); verdict.violated != "" {
		return verdict
	}
	if got := commitTableSnapshot(t, reader, "SELECT * FROM sync_runs ORDER BY id"); !reflect.DeepEqual(got, beforeRuns) {
		return syncVerdict{syncAtomicRun, fmt.Sprintf("rejected COMMIT changed durable runs: got %+v, want %+v", got, beforeRuns)}
	}
	if got := commitTableSnapshot(t, reader, "SELECT * FROM sync_cursors ORDER BY connection_id, data_type, source_family_filter, rollup_kind"); !reflect.DeepEqual(got, beforeCursors) {
		return syncVerdict{syncCompletedOnly, fmt.Sprintf("rejected COMMIT changed durable cursors: got %+v, want %+v", got, beforeCursors)}
	}
	if err := archive.FinalizeSyncRun(ctx, finalize); err != nil {
		t.Fatalf("later finalization: %q", err)
	}
	completed := probeSyncRunRow(t, path, id)
	if completed.status != "sync_completed" || completed.seenCount != 7 || completed.newCount != 3 || completed.updatedCount != 2 ||
		completed.finishedAt != (sql.NullString{String: finalize.FinishedAt, Valid: true}) || completed.errorSummary.Valid {
		return syncVerdict{syncAtomicRun, fmt.Sprintf("later finalization run: %+v", completed)}
	}
	wantCursors := [][]any{{key.connectionID, key.dataType, key.sourceFamilyFilter, string(key.rollupKind), finalize.CursorTo, finalize.CursorAdvanced}}
	if got := commitTableSnapshot(t, reader, "SELECT * FROM sync_cursors ORDER BY connection_id, data_type, source_family_filter, rollup_kind"); !reflect.DeepEqual(got, wantCursors) {
		return syncVerdict{syncCompletedOnly, fmt.Sprintf("later cursors: got %+v, want %+v", got, wantCursors)}
	}
	t.Log("direct rejected COMMIT released; bounded retries exhausted; next Begin succeeded; durable tables preserved; later finalization published run and cursor")
	return syncVerdict{}
}

func requireCommitBusy(t *testing.T, err error) {
	t.Helper()
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
		t.Fatalf("expected typed SQLite BUSY from rejected COMMIT: %q", err)
	}
}

func probeTransactionRelease(t *testing.T, db *sql.DB) syncVerdict {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err == nil {
		if err := tx.Rollback(); err != nil {
			t.Fatalf("probe Rollback: %q", err)
		}
		return syncVerdict{}
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("unexpected probe Begin error: %q", err)
	}
	// Successful raw rollback confirms an active transaction, unlike an error code alone.
	if _, rollbackErr := db.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
		t.Fatalf("probe Begin: %q; active transaction not confirmed: %q", err, rollbackErr)
	}
	fresh, freshErr := db.BeginTx(ctx, nil)
	if freshErr != nil {
		t.Fatalf("Begin after confirmed rollback: %q", freshErr)
	}
	if rollbackErr := fresh.Rollback(); rollbackErr != nil {
		t.Fatalf("fresh probe Rollback: %q", rollbackErr)
	}
	return syncVerdict{syncTransactionReleased, fmt.Sprintf("typed SQLite Begin error %d: %q; raw rollback confirmed active transaction; fresh Begin/Rollback succeeded", sqliteErr.Code(), err)}
}

func commitTableSnapshot(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("snapshot query: %q", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close snapshot rows: %q", err)
		}
	}()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("snapshot columns: %q", err)
	}
	values := [][]any{}
	for rows.Next() {
		row := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range row {
			dest[i] = &row[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("snapshot scan: %q", err)
		}
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot rows: %q", err)
	}
	return values
}

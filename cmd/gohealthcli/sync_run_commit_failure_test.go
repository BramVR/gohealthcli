package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/archived"
)

func TestSyncRunCommitFailureReleasesTransaction(t *testing.T) {
	t.Parallel()
	for _, seeded := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent_cursor", true: "existing_cursor"}[seeded], func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "archive.sqlite")
			db, err := openArchive(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := applyMigrations(ctx, db, func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }); err != nil {
				t.Fatal(err)
			}
			// A rollback journal lets a reader permit both writes but reject COMMIT.
			for _, statement := range []string{"PRAGMA journal_mode = DELETE", "PRAGMA busy_timeout = 0"} {
				if _, err := db.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			connection := archived.Connection{ID: "google_health:commit-test", ProviderName: "google_health"}
			if _, err := db.ExecContext(ctx, `INSERT INTO connections
				(id, provider_name, google_health_user_id, created_at, updated_at)
				VALUES (?, ?, 'commit-test', '2026-01-01', '2026-01-01')`, connection.ID, connection.ProviderName); err != nil {
				t.Fatal(err)
			}
			archive := &sqliteHealthArchiveWriter{db: db, archivePath: path}
			key := syncCursorKey{connectionID: connection.ID, dataType: "steps", rollupKind: syncCursorRollupKindNone}
			if seeded {
				if err := archive.CommitSyncCursor(ctx, key, syncRunOutcomeCompleted, "2026-01-02T00:00:00Z", "2026-01-02T01:00:00Z"); err != nil {
					t.Fatal(err)
				}
			}
			id, err := archive.StartSyncRun(ctx, syncRunStart{Connection: connection, DataTypes: []string{"steps"},
				From: "2026-01-02", To: "2026-01-03", EndpointFamily: "list", StartedAt: "2026-01-03T00:00:00Z"})
			if err != nil {
				t.Fatal(err)
			}
			before := probeSyncRunRow(t, path, id)
			reader, err := openArchiveReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reader.Close() })
			readTx, err := reader.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = readTx.Rollback() }()
			var count int
			if err := readTx.QueryRowContext(ctx, "SELECT count(*) FROM sync_runs").Scan(&count); err != nil {
				t.Fatal(err)
			}
			finalize := syncRunFinalize{SyncRunID: id, Outcome: syncRunOutcomeCompleted, SeenCount: 7, NewCount: 3, UpdatedCount: 2,
				FinishedAt: "2026-01-03T01:00:00Z", CursorKey: key, CursorTo: "2026-01-03T00:00:00Z", CursorAdvanced: "2026-01-03T01:00:00Z"}
			finalizeErr := archive.FinalizeSyncRun(ctx, finalize)
			var exhausted *errFinalizeSyncRunBusyExhausted
			if !errors.As(finalizeErr, &exhausted) || exhausted.attempts != finalizeSyncRunRetryBudget || !isSQLiteBusy(exhausted.cause) {
				t.Fatalf("TransactionReleased: reader must reject COMMIT through the bounded SQLITE_BUSY retries: %v", finalizeErr)
			}
			if err := readTx.Rollback(); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("TransactionReleased: next Begin after rejected COMMIT: %v; finalization: %v", err, finalizeErr)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if got := probeSyncRunRow(t, path, id); !reflect.DeepEqual(got, before) {
				t.Fatalf("rejected COMMIT changed durable Sync Run: got %+v, want %+v", got, before)
			}
			var cursorTime, advancedAt string
			var cursorCount int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sync_cursors").Scan(&cursorCount); err != nil {
				t.Fatal(err)
			}
			if seeded {
				if err := db.QueryRowContext(ctx, "SELECT cursor_time, advanced_at FROM sync_cursors").Scan(&cursorTime, &advancedAt); err != nil {
					t.Fatal(err)
				}
				if cursorCount != 1 || cursorTime != "2026-01-02T00:00:00Z" || advancedAt != "2026-01-02T01:00:00Z" {
					t.Fatalf("rejected COMMIT changed existing cursor: count=%d time=%s advanced_at=%s", cursorCount, cursorTime, advancedAt)
				}
			} else if cursorCount != 0 {
				t.Fatalf("rejected COMMIT created %d cursors", cursorCount)
			}
			if err := archive.FinalizeSyncRun(ctx, finalize); err != nil {
				t.Fatalf("later finalization: %v", err)
			}
			completed := probeSyncRunRow(t, path, id)
			if completed.status != "sync_completed" || completed.seenCount != 7 || completed.newCount != 3 || completed.updatedCount != 2 {
				t.Fatalf("later finalization Sync Run = %+v", completed)
			}
			if err := reader.QueryRowContext(ctx, "SELECT cursor_time, advanced_at FROM sync_cursors").Scan(&cursorTime, &advancedAt); err != nil {
				t.Fatal(err)
			}
			if cursorTime != finalize.CursorTo || advancedAt != finalize.CursorAdvanced {
				t.Fatalf("later finalization cursor = %s, %s; want %s, %s", cursorTime, advancedAt, finalize.CursorTo, finalize.CursorAdvanced)
			}
			t.Log("rejected COMMIT exhausted bounded retries; next Begin succeeded; durable run and cursor preserved; later finalization committed both")
		})
	}
}

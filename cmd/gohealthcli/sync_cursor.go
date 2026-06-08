package main

import (
	"database/sql"
	"errors"
	"fmt"
)

// syncCursorRollupKind is the rollup-kind discriminator used as part of the
// Sync Cursor key. The first release only emits "none" (raw Data Points) and
// "daily" (daily Rollups); follow-up slices widen this to "hourly", "weekly",
// and "window:<duration>" per the PRD §Implementation Decisions.
type syncCursorRollupKind string

const (
	syncCursorRollupKindNone  syncCursorRollupKind = "none"
	syncCursorRollupKindDaily syncCursorRollupKind = "daily"
)

// syncRunOutcome is the terminal Sync Run status passed to
// commitSyncCursor. Only sync_completed advances the cursor (ADR-0008);
// sync_failed and sync_canceled flow through Commit symmetrically but
// leave the cursor at its prior value.
type syncRunOutcome string

const (
	syncRunOutcomeCompleted syncRunOutcome = "sync_completed"
	syncRunOutcomeFailed    syncRunOutcome = "sync_failed"
	syncRunOutcomeCanceled  syncRunOutcome = "sync_canceled"
)

type syncCursorKey struct {
	connectionID       string
	dataType           string
	sourceFamilyFilter string
	rollupKind         syncCursorRollupKind
}

func rollupKindForSync(rollup string) syncCursorRollupKind {
	if rollup == "" {
		return syncCursorRollupKindNone
	}
	return syncCursorRollupKind(rollup)
}

// resolveSyncCursor returns the current cursor_time for a key, plus a
// boolean indicating whether a row exists. cursor_time itself is stored
// as TEXT NOT NULL and commitSyncCursor rejects empty values, so the
// returned string is non-empty whenever the boolean is true; the boolean
// is the canonical "no prior successful Sync Run" signal.
func resolveSyncCursor(db *sql.DB, key syncCursorKey) (string, bool, error) {
	var cursorTime string
	err := db.QueryRow(`SELECT cursor_time FROM sync_cursors
		WHERE connection_id = ?
			AND data_type = ?
			AND source_family_filter = ?
			AND rollup_kind = ?`,
		key.connectionID,
		key.dataType,
		key.sourceFamilyFilter,
		string(key.rollupKind),
	).Scan(&cursorTime)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return cursorTime, true, nil
}

// commitSyncCursor enforces the ADR-0008 invariant at the seam where
// the cursor data lives: only sync_completed advances cursor_time.
// sync_failed and sync_canceled are accepted so callers can route every
// terminal outcome through one path, but they no-op on storage.
func commitSyncCursor(db *sql.DB, key syncCursorKey, outcome syncRunOutcome, to, advancedAt string) error {
	return commitSyncCursorExec(db, key, outcome, to, advancedAt)
}

// commitSyncCursorTx is the same write as commitSyncCursor but bound to
// an open transaction so it can compose with finishSyncRunTx inside the
// writer's FinalizeSyncRun atomic-commit path.
func commitSyncCursorTx(tx *sql.Tx, key syncCursorKey, outcome syncRunOutcome, to, advancedAt string) error {
	return commitSyncCursorExec(tx, key, outcome, to, advancedAt)
}

func commitSyncCursorExec(executor sqlExecutor, key syncCursorKey, outcome syncRunOutcome, to, advancedAt string) error {
	if outcome != syncRunOutcomeCompleted {
		return nil
	}
	if to == "" {
		return errors.New("sync cursor commit requires a non-empty cursor time")
	}
	_, err := executor.Exec(`INSERT INTO sync_cursors (
		connection_id,
		data_type,
		source_family_filter,
		rollup_kind,
		cursor_time,
		advanced_at
	) VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(connection_id, data_type, source_family_filter, rollup_kind)
	DO UPDATE SET cursor_time = excluded.cursor_time, advanced_at = excluded.advanced_at`,
		key.connectionID,
		key.dataType,
		key.sourceFamilyFilter,
		string(key.rollupKind),
		to,
		advancedAt,
	)
	if err != nil {
		return fmt.Errorf("commit Sync Cursor: %w", err)
	}
	return nil
}


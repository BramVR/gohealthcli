//go:build formal

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/archived"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type formalCursorValue struct {
	time       string
	advancedAt string
}

type formalRunValue struct {
	status     string
	seen       int
	newCount   int
	updated    int
	finishedAt sql.NullString
	summary    sql.NullString
}

func formalCursorArchive(t *testing.T) *sqliteHealthArchiveWriter {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("formal SQL: %q", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close formal archive: %q", err)
		}
	})
	db.SetMaxOpenConns(1)
	formalExec(t, db, "PRAGMA foreign_keys = ON")
	return formalPrepareArchive(t, db)
}

func formalPrepareArchive(t *testing.T, db *sql.DB) *sqliteHealthArchiveWriter {
	t.Helper()
	if err := applyMigrations(context.Background(), db, func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }); err != nil {
		t.Fatalf("formal SQL: %q", err)
	}
	for _, id := range []string{"google_health:formal-a", "google_health:formal-b"} {
		formalExec(t, db, `INSERT INTO connections
			(id, provider_name, google_health_user_id, created_at, updated_at)
			VALUES (?, 'google_health', ?, '2026-01-01', '2026-01-01')`, id, id)
	}
	return &sqliteHealthArchiveWriter{db: db}
}

func formalExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("fixture SQL: %q", err)
	}
}

func formalCursorSnapshot(t *testing.T, db *sql.DB) map[syncCursorKey]formalCursorValue {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT connection_id, data_type, source_family_filter,
		rollup_kind, cursor_time, advanced_at FROM sync_cursors`)
	if err != nil {
		t.Fatalf("formal SQL: %q", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close formal rows: %q", err)
		}
	}()
	values := map[syncCursorKey]formalCursorValue{}
	for rows.Next() {
		var key syncCursorKey
		var value formalCursorValue
		if err := rows.Scan(&key.connectionID, &key.dataType, &key.sourceFamilyFilter,
			&key.rollupKind, &value.time, &value.advancedAt); err != nil {
			t.Fatalf("formal SQL: %q", err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("formal SQL: %q", err)
	}
	return values
}

func formalRunSnapshot(t *testing.T, db *sql.DB) map[int64]formalRunValue {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT id, status, seen_count, new_count, updated_count,
		finished_at, error_summary FROM sync_runs`)
	if err != nil {
		t.Fatalf("formal SQL: %q", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close formal rows: %q", err)
		}
	}()
	values := map[int64]formalRunValue{}
	for rows.Next() {
		var id int64
		var value formalRunValue
		if err := rows.Scan(&id, &value.status, &value.seen, &value.newCount,
			&value.updated, &value.finishedAt, &value.summary); err != nil {
			t.Fatalf("formal SQL: %q", err)
		}
		values[id] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("formal SQL: %q", err)
	}
	return values
}

func formalCursorKeys() []syncCursorKey {
	base := syncCursorKey{connectionID: "google_health:formal-a", dataType: "steps", rollupKind: "none"}
	keys := []syncCursorKey{base, base, base, base, base}
	keys[1].connectionID = "google_health:formal-b"
	keys[2].dataType = "heart-rate"
	keys[3].sourceFamilyFilter = "fitbit"
	keys[4].rollupKind = "daily"
	return keys
}

func formalReset(t *testing.T, archive *sqliteHealthArchiveWriter, seeded bool) {
	t.Helper()
	formalExec(t, archive.db, "DELETE FROM sync_runs")
	formalExec(t, archive.db, "DELETE FROM sync_cursors")
	if seeded {
		for _, key := range formalCursorKeys() {
			formalExec(t, archive.db, `INSERT INTO sync_cursors
				(connection_id, data_type, source_family_filter, rollup_kind, cursor_time, advanced_at)
				VALUES (?, ?, ?, ?, '2026-01-02T00:00:00Z', '2026-01-01T00:00:00Z')`,
				key.connectionID, key.dataType, key.sourceFamilyFilter, key.rollupKind)
		}
	}
}

func formalStartRuns(t *testing.T, archive *sqliteHealthArchiveWriter, keys [2]syncCursorKey) [2]int64 {
	t.Helper()
	var ids [2]int64
	for i, key := range keys {
		id, err := archive.StartSyncRun(context.Background(), syncRunStart{
			Connection: archived.Connection{ID: key.connectionID, ProviderName: "google_health"},
			DataTypes:  []string{key.dataType}, From: "2026-01-01T00:00:00Z", To: formalHorizon(i),
			EndpointFamily: "list", SourceFamilyFilter: key.sourceFamilyFilter, StartedAt: "2026-01-04T00:00:00Z"})
		if err != nil {
			t.Fatalf("formal SQL: %q", err)
		}
		ids[i] = id
	}
	return ids
}

func formalHorizon(run int) string {
	return []string{"2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z"}[run]
}

func formalSetFault(t *testing.T, db *sql.DB, fault string) {
	t.Helper()
	switch fault {
	case "none":
	case "run":
		formalExec(t, db, `CREATE TEMP TRIGGER formal_run_fault BEFORE UPDATE ON sync_runs
			BEGIN SELECT RAISE(ABORT, 'formal run fault'); END`)
	case "cursor":
		for _, op := range []string{"INSERT", "UPDATE"} {
			formalExec(t, db, fmt.Sprintf(`CREATE TEMP TRIGGER formal_cursor_%s BEFORE %s ON sync_cursors
				BEGIN SELECT RAISE(ABORT, 'formal cursor fault'); END`, op, op))
		}
	default:
		t.Fatalf("unknown fault %q", fault)
	}
}

func formalClearFault(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range []string{"formal_run_fault", "formal_cursor_INSERT", "formal_cursor_UPDATE"} {
		formalExec(t, db, "DROP TRIGGER IF EXISTS "+name)
	}
}

func TestFormalSyncCursorSafety(t *testing.T) {
	recordVerdict(t, observeCursorSafety(t))
}

func observeCursorSafety(t *testing.T) syncVerdict {
	archive := formalCursorArchive(t)
	outcomes := []syncRunOutcome{syncRunOutcomeCompleted, syncRunOutcomeFailed, syncRunOutcomeCanceled}
	faults := []string{"none", "run", "cursor"}
	checked := 0
	for variant, other := range formalCursorKeys() {
		keys := [2]syncCursorKey{formalCursorKeys()[0], other}
		for _, seeded := range []bool{false, true} {
			for _, first := range outcomes {
				for _, second := range outcomes {
					for _, firstFault := range faults {
						for _, secondFault := range faults {
							for _, order := range [][2]int{{0, 1}, {1, 0}} {
								label := fmt.Sprintf("key=%d seeded=%v outcomes=%s,%s faults=%s,%s order=%v",
									variant, seeded, first, second, firstFault, secondFault, order)
								formalReset(t, archive, seeded)
								ids := formalStartRuns(t, archive, keys)
								for _, r := range order {
									outcome := [2]syncRunOutcome{first, second}[r]
									fault := [2]string{firstFault, secondFault}[r]
									beforeCursors := formalCursorSnapshot(t, archive.db)
									beforeRuns := formalRunSnapshot(t, archive.db)
									formalSetFault(t, archive.db, fault)
									finalize := syncRunFinalize{SyncRunID: ids[r], Outcome: outcome,
										SeenCount: 3, NewCount: 2, UpdatedCount: 1, FinishedAt: "2026-01-04T00:00:01Z",
										CursorKey: keys[r], CursorTo: formalHorizon(r), CursorAdvanced: "2026-01-04T00:00:01Z"}
									err := archive.FinalizeSyncRun(context.Background(), finalize)
									wantError := fault == "run" || (fault == "cursor" && outcome == syncRunOutcomeCompleted)
									if err != nil {
										var sqliteErr *sqlite.Error
										if !wantError || !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_TRIGGER {
											t.Fatalf("%s run=%d: unexpected finalization error: %q", label, r, err)
										}
									} else if wantError {
										return syncVerdict{syncFinalizeOutcome, fmt.Sprintf("%s run=%d: expected selected trigger fault, got success", label, r)}
									}
									formalClearFault(t, archive.db)
									if !wantError {
										beforeRuns[ids[r]] = formalRunValue{status: string(outcome), seen: 3, newCount: 2, updated: 1,
											finishedAt: sql.NullString{String: finalize.FinishedAt, Valid: true}}
										if outcome == syncRunOutcomeCompleted {
											beforeCursors[keys[r]] = formalCursorValue{time: finalize.CursorTo, advancedAt: finalize.CursorAdvanced}
										}
									}
									if got := formalRunSnapshot(t, archive.db); !reflect.DeepEqual(got, beforeRuns) {
										return syncVerdict{syncAtomicRun, fmt.Sprintf("%s run=%d: got %+v, want %+v", label, r, got, beforeRuns)}
									}
									if got := formalCursorSnapshot(t, archive.db); !reflect.DeepEqual(got, beforeCursors) {
										return syncVerdict{syncCompletedOnly, fmt.Sprintf("%s run=%d: got %+v, want %+v", label, r, got, beforeCursors)}
									}
								}
								checked++
							}
						}
					}
				}
			}
		}
	}
	if checked != 1620 {
		t.Fatalf("incomplete schedule enumeration: %d, want 1620", checked)
	}
	t.Logf("checked %d two-run SQLite schedules, both commit orders and every key dimension", checked)
	return syncVerdict{}
}

func TestFormalSyncCursorCommitFailure(t *testing.T) {
	t.Parallel()
	checkCommitFailureCases(t, recordVerdict)
}

func TestFormalSyncCursorHighwater(t *testing.T) {
	recordVerdict(t, observeCursorHighwater(t))
}

func observeCursorHighwater(t *testing.T) syncVerdict {
	archive := formalCursorArchive(t)
	key := formalCursorKeys()[0]
	formalReset(t, archive, false)
	ids := formalStartRuns(t, archive, [2]syncCursorKey{key, key})
	for _, r := range []int{1, 0} {
		if err := archive.FinalizeSyncRun(context.Background(), syncRunFinalize{SyncRunID: ids[r], Outcome: syncRunOutcomeCompleted,
			FinishedAt: "2026-01-04T00:00:01Z", CursorKey: key, CursorTo: formalHorizon(r), CursorAdvanced: "2026-01-04T00:00:01Z"}); err != nil {
			t.Fatalf("formal SQL: %q", err)
		}
		got, found, err := archive.ResolveSyncCursor(context.Background(), key)
		if err != nil || !found {
			t.Fatalf("resolve cursor: %q found=%v error=%q", got, found, err)
		}
		cursorTime, err := time.Parse(time.RFC3339, got)
		if err != nil {
			t.Fatalf("parse resolved cursor: %q", err)
		}
		highwater, err := time.Parse(time.RFC3339, formalHorizon(1))
		if err != nil {
			t.Fatalf("parse highwater: %q", err)
		}
		if cursorTime.Before(highwater) {
			return syncVerdict{syncCursorHighwater, fmt.Sprintf("run through Jan 3 completed, then run through Jan 2 completed; cursor=%q, want %q", got, formalHorizon(1))}
		}
	}
	return syncVerdict{}
}

const syncProofAttr = "sync-cursor.case.v1"
const syncProofNonceEnv = "GOHEALTHCLI_SYNC_PROOF_NONCE"

func recordVerdict(t *testing.T, verdict syncVerdict) {
	t.Helper()
	nonce := os.Getenv(syncProofNonceEnv)
	if nonce == "" {
		requireHolds(t, verdict)
		return
	}
	wire := struct {
		Version   int           `json:"v"`
		Nonce     string        `json:"nonce"`
		Verdict   string        `json:"verdict"`
		Invariant syncInvariant `json:"invariant,omitempty"`
		Detail    *string       `json:"detail,omitempty"`
	}{Version: 1, Nonce: nonce, Verdict: "holds"}
	if verdict.violated != "" {
		wire.Verdict, wire.Invariant, wire.Detail = "violated", verdict.violated, &verdict.detail
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("encode proof record: %q", err)
	}
	t.Attr(syncProofAttr, string(encoded))
}

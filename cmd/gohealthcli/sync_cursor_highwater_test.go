package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/archived"
)

func TestSyncCursorHighwaterOutOfOrder(t *testing.T) {
	_, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		accessToken: "synthetic-access", refreshToken: "synthetic-refresh",
		healthUserID: "111111256096816351", legacyFitbitUserID: "A1B2C3",
	})
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	ctx := context.Background()
	connection, err := archive.CurrentConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := syncCursorKey{connectionID: connection.ID, dataType: "steps", rollupKind: syncCursorRollupKindNone}
	bounds := []string{"2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z"}
	ids := make([]int64, len(bounds))
	for i, bound := range bounds {
		ids[i], err = archive.StartSyncRun(ctx, syncRunStart{
			Connection: connection, DataTypes: []string{"steps"}, From: "2026-01-01",
			To: bound, EndpointFamily: "list", StartedAt: "2026-01-04T00:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range []int{1, 0} {
		if err := archive.FinalizeSyncRun(ctx, syncRunFinalize{
			SyncRunID: ids[i], Outcome: syncRunOutcomeCompleted, FinishedAt: "2026-01-04T00:00:01Z",
			CursorKey: key, CursorTo: bounds[i], CursorAdvanced: "2026-01-04T00:00:01Z",
		}); err != nil {
			t.Fatal(err)
		}
		assertSyncRunForDataType(t, archivePath, ids[i], "sync_completed", "steps", "list", 0, 0, 0, "")
	}
	got, found, err := archive.ResolveSyncCursor(ctx, key)
	if err != nil || !found || got != bounds[1] {
		t.Fatalf("cursor after Jan 3 then Jan 2 completion = %q, found=%v, err=%v; want %q", got, found, err, bounds[1])
	}
}

func TestSyncCursorHighwaterCompletionOrders(t *testing.T) {
	for _, initial := range []string{"absent", "present"} {
		for _, order := range []struct {
			name    string
			indices []int
		}{
			{"older-first", []int{0, 1}},
			{"newer-first", []int{1, 0}},
		} {
			t.Run(initial+"/"+order.name, func(t *testing.T) {
				archive, key := newSyncCursorHighwaterArchive(t)
				if initial == "present" {
					completeSyncCursorHighwaterRun(t, archive, key, "2026-01-01", "2026-01-04T00:00:00Z")
				}
				bounds := []string{"2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z"}
				advanced := []string{"2026-01-04T00:00:01Z", "2026-01-04T00:00:02Z"}
				ids := []int64{
					startSyncCursorHighwaterRun(t, archive, key, bounds[0]),
					startSyncCursorHighwaterRun(t, archive, key, bounds[1]),
				}
				for _, i := range order.indices {
					if err := archive.FinalizeSyncRun(context.Background(), syncRunFinalize{
						SyncRunID: ids[i], Outcome: syncRunOutcomeCompleted, FinishedAt: advanced[i],
						CursorKey: key, CursorTo: bounds[i], CursorAdvanced: advanced[i],
					}); err != nil {
						t.Fatal(err)
					}
					assertSyncCursorHighwaterStatus(t, archive, ids[i], "sync_completed")
				}
				assertSyncCursorHighwater(t, archive, key, bounds[1], advanced[1])
			})
		}
	}
}

func TestSyncCursorHighwaterHistoricalResync(t *testing.T) {
	archive, key := newSyncCursorHighwaterArchive(t)
	completeSyncCursorHighwaterRun(t, archive, key, "2026-01-10", "2026-01-11T00:00:00Z")
	completeSyncCursorHighwaterRun(t, archive, key, "2026-01-02", "2026-01-12T00:00:00Z")
	assertSyncCursorHighwater(t, archive, key, "2026-01-10", "2026-01-11T00:00:00Z")
}

func TestSyncCursorHighwaterBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, current, incoming string
		advances                bool
	}{
		{"equal", "2026-01-02T00:00:00Z", "2026-01-02T00:00:00Z", false},
		{"later", "2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z", true},
		{"date-to-midnight", "2026-01-02", "2026-01-02T00:00:00Z", false},
		{"midnight-to-date", "2026-01-02T00:00:00Z", "2026-01-02", false},
		{"later-date", "2026-01-02T00:00:00Z", "2026-01-03", true},
		{"offset-earlier", "2026-01-02T00:00:00Z", "2026-01-02T01:00:00+02:00", false},
		{"offset-later", "2026-01-02T00:00:00Z", "2026-01-01T23:30:00-01:00", true},
		{"offset-equal", "2026-01-02T01:00:00+01:00", "2026-01-02T00:00:00Z", false},
		{"whole-to-fraction", "2026-01-02T00:00:00Z", "2026-01-02T00:00:00.5Z", true},
		{"fraction-to-whole", "2026-01-02T00:00:00.5Z", "2026-01-02T00:00:00Z", false},
		{"nanosecond-later", "2026-01-02T00:00:00.123456788Z", "2026-01-02T00:00:00.123456789Z", true},
		{"nanosecond-earlier", "2026-01-02T00:00:00.123456789Z", "2026-01-02T00:00:00.123456788Z", false},
		{"civil-equal", "2026-01-02T12:00:00", "2026-01-02T12:00:00Z", false},
		{"civil-later", "2026-01-02T12:00:00Z", "2026-01-02T12:00:01", true},
		{"civil-earlier", "2026-01-02T12:00:00Z", "2026-01-02T11:59:59", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive, key := newSyncCursorHighwaterArchive(t)
			completeSyncCursorHighwaterRun(t, archive, key, tc.current, "2026-01-04T00:00:00Z")
			completeSyncCursorHighwaterRun(t, archive, key, tc.incoming, "2026-01-04T00:00:01Z")
			want, advanced := tc.current, "2026-01-04T00:00:00Z"
			if tc.advances {
				want, advanced = tc.incoming, "2026-01-04T00:00:01Z"
			}
			assertSyncCursorHighwater(t, archive, key, want, advanced)
		})
	}
}

func TestSyncCursorHighwaterNonCompleted(t *testing.T) {
	for _, outcome := range []syncRunOutcome{syncRunOutcomeFailed, syncRunOutcomeCanceled} {
		for _, initial := range []struct{ name, to string }{
			{"absent", ""}, {"present", "2026-01-03"}, {"malformed", "not-a-time"},
		} {
			t.Run(string(outcome)+"/"+initial.name, func(t *testing.T) {
				archive, key := newSyncCursorHighwaterArchive(t)
				advanced := ""
				if initial.to != "" {
					advanced = "2026-01-04T00:00:00Z"
					seedSyncCursorHighwater(t, archive, key, initial.to, advanced)
				}
				id := startSyncCursorHighwaterRun(t, archive, key, "not-a-time")
				if err := archive.FinalizeSyncRun(context.Background(), syncRunFinalize{
					SyncRunID: id, Outcome: outcome, FinishedAt: "2026-01-04T00:00:01Z",
					CursorKey: key, CursorTo: "not-a-time", CursorAdvanced: "not-a-time",
				}); err != nil {
					t.Fatal(err)
				}
				assertSyncCursorHighwaterStatus(t, archive, id, string(outcome))
				assertSyncCursorHighwater(t, archive, key, initial.to, advanced)
				if err := archive.CommitSyncCursor(context.Background(), key, outcome, "not-a-time", "not-a-time"); err != nil {
					t.Fatal(err)
				}
				assertSyncCursorHighwater(t, archive, key, initial.to, advanced)
			})
		}
	}
}

func TestSyncCursorHighwaterKeyIsolation(t *testing.T) {
	archive, key := newSyncCursorHighwaterArchive(t)
	otherConnection := key.connectionID + "-other"
	if _, err := archive.db.ExecContext(context.Background(), `INSERT INTO connections
		(id, provider_name, google_health_user_id, created_at, updated_at)
		VALUES (?, 'google_health', '222222256096816351', '2026-01-01', '2026-01-01')`, otherConnection); err != nil {
		t.Fatal(err)
	}
	keys := []syncCursorKey{
		key,
		{connectionID: otherConnection, dataType: key.dataType, rollupKind: key.rollupKind},
		{connectionID: key.connectionID, dataType: "heart-rate", rollupKind: key.rollupKind},
		{connectionID: key.connectionID, dataType: key.dataType, sourceFamilyFilter: "wearable", rollupKind: key.rollupKind},
	}
	for _, kind := range []syncCursorRollupKind{syncCursorRollupKindDaily, "hourly", "weekly", "window=3600s", "window=7200s"} {
		variant := key
		variant.rollupKind = kind
		keys = append(keys, variant)
	}
	for _, variant := range keys {
		completeSyncCursorHighwaterRun(t, archive, variant, "2026-01-03", "2026-01-04T00:00:00Z")
	}
	completeSyncCursorHighwaterRun(t, archive, key, "2026-01-10", "2026-01-11T00:00:00Z")
	for _, variant := range keys[1:] {
		completeSyncCursorHighwaterRun(t, archive, variant, "2026-01-02", "2026-01-12T00:00:00Z")
		assertSyncCursorHighwater(t, archive, variant, "2026-01-03", "2026-01-04T00:00:00Z")
	}
	assertSyncCursorHighwater(t, archive, key, "2026-01-10", "2026-01-11T00:00:00Z")
}

func TestSyncCursorHighwaterMalformedBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, current, incoming string }{
		{"empty-absent", "", ""},
		{"invalid-absent", "", "not-a-time"},
		{"empty-present", "2026-01-02", ""},
		{"invalid-present", "2026-01-02", "2026-02-30"},
		{"invalid-existing", "not-a-time", "2026-01-03"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive, key := newSyncCursorHighwaterArchive(t)
			advanced := ""
			if tc.current != "" {
				advanced = "2026-01-04T00:00:00Z"
				seedSyncCursorHighwater(t, archive, key, tc.current, advanced)
			}
			id := startSyncCursorHighwaterRun(t, archive, key, tc.incoming)
			if err := archive.FinalizeSyncRun(context.Background(), syncRunFinalize{
				SyncRunID: id, Outcome: syncRunOutcomeCompleted, FinishedAt: "2026-01-04T00:00:01Z",
				CursorKey: key, CursorTo: tc.incoming, CursorAdvanced: "2026-01-04T00:00:01Z",
			}); err == nil {
				t.Fatal("expected invalid boundary to reject finalization")
			}
			assertSyncCursorHighwaterStatus(t, archive, id, "sync_running")
			assertSyncCursorHighwater(t, archive, key, tc.current, advanced)
			if err := archive.CommitSyncCursor(context.Background(), key, syncRunOutcomeCompleted, tc.incoming, "2026-01-04T00:00:01Z"); err == nil {
				t.Fatal("expected invalid boundary to reject direct commit")
			}
			assertSyncCursorHighwater(t, archive, key, tc.current, advanced)
		})
	}
}

func TestSyncCursorHighwaterDirectCommit(t *testing.T) {
	archive, key := newSyncCursorHighwaterArchive(t)
	for _, tc := range []struct{ to, advanced, want, wantAdvanced string }{
		{"2026-01-03T00:00:00Z", "2026-01-04T00:00:00Z", "2026-01-03T00:00:00Z", "2026-01-04T00:00:00Z"},
		{"2026-01-02", "2026-01-04T00:00:01Z", "2026-01-03T00:00:00Z", "2026-01-04T00:00:00Z"},
		{"2026-01-03", "2026-01-04T00:00:02Z", "2026-01-03T00:00:00Z", "2026-01-04T00:00:00Z"},
		{"2026-01-03T00:00:00.000000001Z", "2026-01-04T00:00:03Z", "2026-01-03T00:00:00.000000001Z", "2026-01-04T00:00:03Z"},
	} {
		if err := archive.CommitSyncCursor(context.Background(), key, syncRunOutcomeCompleted, tc.to, tc.advanced); err != nil {
			t.Fatal(err)
		}
		assertSyncCursorHighwater(t, archive, key, tc.want, tc.wantAdvanced)
	}
}

func TestSyncCursorHighwaterWriteFailure(t *testing.T) {
	for _, path := range []string{"finalize", "direct"} {
		t.Run(path, func(t *testing.T) {
			archive, key := newSyncCursorHighwaterArchive(t)
			completeSyncCursorHighwaterRun(t, archive, key, "2026-01-02", "2026-01-04T00:00:00Z")
			id := startSyncCursorHighwaterRun(t, archive, key, "2026-01-03")
			// FAIL preserves the statement's partial write until its transaction rolls back.
			if _, err := archive.db.ExecContext(context.Background(), `CREATE TRIGGER reject_cursor_advance AFTER UPDATE ON sync_cursors
				BEGIN SELECT RAISE(FAIL, 'synthetic cursor write failure'); END`); err != nil {
				t.Fatal(err)
			}
			write := func() error {
				if path == "direct" {
					return archive.CommitSyncCursor(context.Background(), key, syncRunOutcomeCompleted, "2026-01-03", "2026-01-04T00:00:01Z")
				}
				return archive.FinalizeSyncRun(context.Background(), syncRunFinalize{
					SyncRunID: id, Outcome: syncRunOutcomeCompleted, FinishedAt: "2026-01-04T00:00:01Z",
					SeenCount: 3, NewCount: 2, UpdatedCount: 1,
					CursorKey: key, CursorTo: "2026-01-03", CursorAdvanced: "2026-01-04T00:00:01Z",
				})
			}
			if err := write(); err == nil || !strings.Contains(err.Error(), "synthetic cursor write failure") {
				t.Fatalf("write error = %v, want injected SQLite failure", err)
			}
			assertSyncCursorHighwater(t, archive, key, "2026-01-02", "2026-01-04T00:00:00Z")
			assertSyncCursorHighwaterStatus(t, archive, id, "sync_running")
			var finished sql.NullString
			var seen, added, updated int
			if err := archive.db.QueryRowContext(context.Background(), `SELECT finished_at, seen_count, new_count, updated_count
				FROM sync_runs WHERE id = ?`, id).Scan(&finished, &seen, &added, &updated); err != nil {
				t.Fatal(err)
			}
			if finished.Valid || seen != 0 || added != 0 || updated != 0 {
				t.Fatalf("run changed after failed write: finished=%v counts=%d/%d/%d", finished, seen, added, updated)
			}
			if _, err := archive.db.ExecContext(context.Background(), `DROP TRIGGER reject_cursor_advance`); err != nil {
				t.Fatal(err)
			}
			if err := write(); err != nil {
				t.Fatalf("retry after rollback: %v", err)
			}
			assertSyncCursorHighwater(t, archive, key, "2026-01-03", "2026-01-04T00:00:01Z")
			if path == "finalize" {
				assertSyncCursorHighwaterStatus(t, archive, id, "sync_completed")
			}
		})
	}
}

func newSyncCursorHighwaterArchive(t *testing.T) (*sqliteHealthArchiveWriter, syncCursorKey) {
	t.Helper()
	_, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		accessToken: "synthetic-access", refreshToken: "synthetic-refresh",
		healthUserID: "111111256096816351", legacyFitbitUserID: "A1B2C3",
	})
	writer, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	connection, err := writer.CurrentConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return writer.(*sqliteHealthArchiveWriter), syncCursorKey{connectionID: connection.ID, dataType: "steps", rollupKind: syncCursorRollupKindNone}
}

func startSyncCursorHighwaterRun(t *testing.T, archive *sqliteHealthArchiveWriter, key syncCursorKey, to string) int64 {
	t.Helper()
	id, err := archive.StartSyncRun(context.Background(), syncRunStart{
		Connection: archived.Connection{ID: key.connectionID, ProviderName: "google_health"},
		DataTypes:  []string{key.dataType}, From: "2025-01-01", To: to,
		EndpointFamily: "list", SourceFamilyFilter: key.sourceFamilyFilter, StartedAt: "2026-01-04T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func completeSyncCursorHighwaterRun(t *testing.T, archive *sqliteHealthArchiveWriter, key syncCursorKey, to, advanced string) {
	t.Helper()
	id := startSyncCursorHighwaterRun(t, archive, key, to)
	if err := archive.FinalizeSyncRun(context.Background(), syncRunFinalize{
		SyncRunID: id, Outcome: syncRunOutcomeCompleted, FinishedAt: advanced,
		CursorKey: key, CursorTo: to, CursorAdvanced: advanced,
	}); err != nil {
		t.Fatal(err)
	}
	assertSyncCursorHighwaterStatus(t, archive, id, "sync_completed")
}

func seedSyncCursorHighwater(t *testing.T, archive *sqliteHealthArchiveWriter, key syncCursorKey, to, advanced string) {
	t.Helper()
	if _, err := archive.db.ExecContext(context.Background(), `INSERT INTO sync_cursors
		(connection_id, data_type, source_family_filter, rollup_kind, cursor_time, advanced_at)
		VALUES (?, ?, ?, ?, ?, ?)`, key.connectionID, key.dataType, key.sourceFamilyFilter, string(key.rollupKind), to, advanced); err != nil {
		t.Fatal(err)
	}
}

func assertSyncCursorHighwater(t *testing.T, archive *sqliteHealthArchiveWriter, key syncCursorKey, want, advanced string) {
	t.Helper()
	var got, gotAdvanced string
	err := archive.db.QueryRowContext(context.Background(), `SELECT cursor_time, advanced_at FROM sync_cursors
		WHERE connection_id = ? AND data_type = ? AND source_family_filter = ? AND rollup_kind = ?`,
		key.connectionID, key.dataType, key.sourceFamilyFilter, string(key.rollupKind)).Scan(&got, &gotAdvanced)
	if want == "" && errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil || got != want || gotAdvanced != advanced {
		t.Fatalf("cursor for %+v = (%q, %q), err=%v; want (%q, %q)", key, got, gotAdvanced, err, want, advanced)
	}
}

func assertSyncCursorHighwaterStatus(t *testing.T, archive *sqliteHealthArchiveWriter, id int64, want string) {
	t.Helper()
	var got string
	if err := archive.db.QueryRowContext(context.Background(), `SELECT status FROM sync_runs WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Sync Run %d status = %q, want %q", id, got, want)
	}
}

package main

import (
	"context"
	"testing"
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

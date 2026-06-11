package main

import "testing"

// TestSyncCursorRollupKindIndependence pins the #106 AC: the cursor
// for (connection, dataType, sourceFamily, rollup_kind) resumes
// independently from cursors for other rollup kinds of the same Data
// Type. Writes one cursor per rollup kind for the same
// (connection, dataType) pair and confirms reads return the matching
// row, not the wrong one.
func TestSyncCursorRollupKindIndependence(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}

	kinds := []syncCursorRollupKind{"none", "daily", "hourly", "weekly", "window=6h"}
	wantTo := map[syncCursorRollupKind]string{
		"none":      "2026-01-01T00:00:00Z",
		"daily":     "2026-02-01",
		"hourly":    "2026-03-01T00:00:00Z",
		"weekly":    "2026-04-01T00:00:00Z",
		"window=6h": "2026-05-01T00:00:00Z",
	}
	for _, kind := range kinds {
		key := syncCursorKey{connectionID: connection.id, dataType: "steps", rollupKind: kind}
		if err := archive.CommitSyncCursor(key, syncRunOutcomeCompleted, wantTo[kind], "2026-06-01T00:00:00Z"); err != nil {
			t.Fatalf("CommitSyncCursor %s: %v", kind, err)
		}
	}
	for _, kind := range kinds {
		key := syncCursorKey{connectionID: connection.id, dataType: "steps", rollupKind: kind}
		got, found, err := archive.ResolveSyncCursor(key)
		if err != nil {
			t.Fatalf("ResolveSyncCursor %s: %v", kind, err)
		}
		if !found {
			t.Errorf("ResolveSyncCursor %s: not found", kind)
			continue
		}
		if got != wantTo[kind] {
			t.Errorf("ResolveSyncCursor %s = %q, want %q", kind, got, wantTo[kind])
		}
	}

	// Advancing one cursor must not affect the others.
	advanced := syncCursorKey{connectionID: connection.id, dataType: "steps", rollupKind: "hourly"}
	if err := archive.CommitSyncCursor(advanced, syncRunOutcomeCompleted, "2026-07-01T00:00:00Z", "2026-07-02T00:00:00Z"); err != nil {
		t.Fatalf("re-advance hourly: %v", err)
	}
	for _, kind := range kinds {
		key := syncCursorKey{connectionID: connection.id, dataType: "steps", rollupKind: kind}
		got, _, err := archive.ResolveSyncCursor(key)
		if err != nil {
			t.Fatalf("post-advance ResolveSyncCursor %s: %v", kind, err)
		}
		want := wantTo[kind]
		if kind == "hourly" {
			want = "2026-07-01T00:00:00Z"
		}
		if got != want {
			t.Errorf("post-advance ResolveSyncCursor %s = %q, want %q (other kinds must stay put)", kind, got, want)
		}
	}
}

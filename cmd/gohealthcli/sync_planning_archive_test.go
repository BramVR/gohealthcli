package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/archived"
)

func TestSyncPlanningReportsReadAndCloseErrors(t *testing.T) {
	t.Parallel()

	readErr := errors.New("planning read failed")
	closeErr := errors.New("planning close failed")
	fakeArchive := &failingSyncPlanningArchive{
		connectionErr: readErr,
		cursorErr:     readErr,
		closeErr:      closeErr,
	}

	t.Run("preflight Connection", func(t *testing.T) {
		configPath, archivePath, testRuntime := connectedArchiveViaSetup(t, fakeConnectConfig{
			accessToken:        "connect-access-secret",
			refreshToken:       "connect-refresh-secret",
			healthUserID:       "111111256096816351",
			legacyFitbitUserID: "A1B2C3",
		})
		testRuntime.openSyncPlanningArchive = func(context.Context, string) (syncPlanningArchive, error) {
			return fakeArchive, nil
		}

		_, err := productionSyncPreflightContext(context.Background(), syncCommandOptions{
			configPath:  configPath,
			archivePath: archivePath,
		}, testRuntime).currentConnection()
		if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
			t.Fatalf("currentConnection error = %v, want read and close errors", err)
		}
	})

	t.Run("Sync Cursor", func(t *testing.T) {
		testRuntime := (runtimeAdapters{
			openSyncPlanningArchive: func(context.Context, string) (syncPlanningArchive, error) {
				return fakeArchive, nil
			},
			openHealthArchiveWriter: func(string) (healthArchiveWriter, error) {
				t.Fatal("writer opened after failed planning")
				return nil, nil
			},
		}).withDefaults()
		_, err := (syncRunLifecycle{
			options: syncCommandOptions{
				archivePath: "unused.sqlite",
				dataTypes:   []string{"steps"},
			},
			plan: preflightPlan{
				dataTypes:  []string{"steps"},
				cursorKeys: []syncCursorKey{{dataType: "steps"}},
			},
			runtime: testRuntime,
		}).Run(context.Background())
		if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
			t.Fatalf("Run error = %v, want read and close errors", err)
		}
	})
}

func TestInspectionOnlyOpenErrorPreservesInspectionAndCloseFailures(t *testing.T) {
	t.Parallel()

	inspectionErr := errors.New("inspection failed")
	closeErr := errors.New("inspection handle close failed")
	err := inspectionOnlyOpenError(archiveCheck{schemaVersion: 4}, inspectionErr, closeErr)
	if !errors.Is(err, inspectionErr) || !errors.Is(err, closeErr) {
		t.Fatalf("inspection-only open error = %v, want inspection and close errors", err)
	}
	var openErr healthArchiveOpenError
	if !errors.As(err, &openErr) || openErr.schemaVersion != 4 {
		t.Fatalf("inspection-only open error = %#v, want schema version 4", err)
	}
}

func TestSyncPlanningArchiveReadsConnectionAndCursorWithoutMutation(t *testing.T) {
	t.Parallel()
	_, archivePath, _ := connectedArchiveViaSetup(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	writer, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	connection, err := writer.CurrentConnection(context.Background())
	if err != nil {
		writer.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	key := syncCursorKey{
		connectionID: connection.ID,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	}
	if err := writer.CommitSyncCursor(
		context.Background(),
		key,
		syncRunOutcomeCompleted,
		"2026-07-29T00:00:00Z",
		"2026-07-29T00:00:01Z",
	); err != nil {
		writer.Close()
		t.Fatalf("seed Sync Cursor: %v", err)
	}
	if _, err := writer.StartSyncRun(context.Background(), syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-07-29T00:00:00Z",
		To:             "2026-07-30T00:00:00Z",
		EndpointFamily: "list",
		StartedAt:      "2026-07-29T00:00:00Z",
	}); err != nil {
		writer.Close()
		t.Fatalf("seed abandoned Sync Run: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	attachmentRoot := attachmentRootDirForArchive(archivePath)
	if err := os.Rename(attachmentRoot, attachmentRoot+".moved"); err != nil {
		t.Fatalf("move Attachment root: %v", err)
	}
	before := capturePlanningArchiveState(t, archivePath)

	archive, err := openSyncPlanningArchive(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("open planning archive: %v", err)
	}
	gotConnection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		archive.Close()
		t.Fatalf("planning CurrentConnection: %v", err)
	}
	if gotConnection != connection {
		archive.Close()
		t.Fatalf("planning Connection = %+v, want %+v", gotConnection, connection)
	}
	cursorTime, found, err := archive.ResolveSyncCursor(context.Background(), key)
	if err != nil {
		archive.Close()
		t.Fatalf("planning ResolveSyncCursor: %v", err)
	}
	if !found || cursorTime != "2026-07-29T00:00:00Z" {
		archive.Close()
		t.Fatalf("planning Sync Cursor = (%q, %v), want seeded cursor", cursorTime, found)
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close planning archive: %v", err)
	}

	after := capturePlanningArchiveState(t, archivePath)
	if after != before {
		t.Fatalf("planning archive mutated filesystem state:\nbefore: %+v\nafter:  %+v", before, after)
	}
	if _, err := os.Stat(attachmentRoot); !os.IsNotExist(err) {
		t.Fatalf("Attachment root stat error = %v, want missing root", err)
	}

	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		t.Fatalf("open archive proof handle: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM sync_runs ORDER BY id DESC LIMIT 1`).Scan(&status); err != nil {
		t.Fatalf("read seeded Sync Run: %v", err)
	}
	if status != "sync_running" {
		t.Fatalf("planning archive fenced Sync Run to %q, want sync_running unchanged", status)
	}

	interfaceType := reflect.TypeOf((*syncPlanningArchive)(nil)).Elem()
	gotMethods := make([]string, 0, interfaceType.NumMethod())
	for index := 0; index < interfaceType.NumMethod(); index++ {
		gotMethods = append(gotMethods, interfaceType.Method(index).Name)
	}
	wantMethods := []string{"Close", "CurrentConnection", "ResolveSyncCursor"}
	if !slices.Equal(gotMethods, wantMethods) {
		t.Fatalf("syncPlanningArchive methods = %v, want minimum planning reads %v", gotMethods, wantMethods)
	}
}

func TestSyncPlanningFailureDoesNotOpenWriterOrFenceRuns(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchiveViaSetup(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time {
		return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	}

	writer, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	connection, err := writer.CurrentConnection(context.Background())
	if err != nil {
		writer.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, err := writer.StartSyncRun(context.Background(), syncRunStart{
		Connection:     connection,
		DataTypes:      []string{"steps"},
		From:           "2026-07-28T00:00:00Z",
		To:             "2026-07-29T00:00:00Z",
		EndpointFamily: "list",
		StartedAt:      "2026-07-29T00:00:00Z",
	}); err != nil {
		writer.Close()
		t.Fatalf("seed abandoned Sync Run: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	attachmentRoot := attachmentRootDirForArchive(archivePath)
	if err := os.Rename(attachmentRoot, attachmentRoot+".moved"); err != nil {
		t.Fatalf("move Attachment root: %v", err)
	}
	before := capturePlanningArchiveState(t, archivePath)

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(context.Background(), syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		to:          "2026-07-30T00:00:00Z",
	})
	if err == nil {
		t.Fatal("Execute error = nil, want missing Sync Cursor failure")
	}
	if result.Status != "sync_failed" {
		t.Fatalf("result status = %q, want sync_failed", result.Status)
	}
	if want := "set --from for the initial backfill"; !strings.Contains(err.Error(), want) {
		t.Fatalf("Execute error = %q, want %q", err, want)
	}

	after := capturePlanningArchiveState(t, archivePath)
	if after != before {
		t.Fatalf("failed sync planning mutated filesystem state:\nbefore: %+v\nafter:  %+v", before, after)
	}
	if _, err := os.Stat(attachmentRoot); !os.IsNotExist(err) {
		t.Fatalf("Attachment root stat error = %v, want missing root", err)
	}

	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		t.Fatalf("open archive proof handle: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM sync_runs ORDER BY id DESC LIMIT 1`).Scan(&status); err != nil {
		t.Fatalf("read seeded Sync Run: %v", err)
	}
	if status != "sync_running" {
		t.Fatalf("failed sync planning fenced Sync Run to %q, want sync_running unchanged", status)
	}
}

func TestSyncPlanningArchiveRejectsMissingAndStaleWithoutMutation(t *testing.T) {
	t.Parallel()

	t.Run("missing", func(t *testing.T) {
		parent := t.TempDir()
		makeOwnerOnlyTestDir(t, parent)
		archivePath := filepath.Join(parent, "missing.sqlite")
		beforeEntries := readDirectoryEntryNames(t, parent)

		archive, err := openSyncPlanningArchive(context.Background(), archivePath)
		if archive != nil {
			archive.Close()
			t.Fatal("missing planning archive returned a handle")
		}
		if !os.IsNotExist(err) {
			t.Fatalf("missing planning archive error = %v, want os.ErrNotExist", err)
		}
		if _, statErr := os.Stat(archivePath); !os.IsNotExist(statErr) {
			t.Fatalf("missing archive stat error = %v, want os.ErrNotExist", statErr)
		}
		afterEntries := readDirectoryEntryNames(t, parent)
		if afterEntries != beforeEntries {
			t.Fatalf("missing archive open changed directory entries: before=%s after=%s", beforeEntries, afterEntries)
		}
	})

	t.Run("stale", func(t *testing.T) {
		parent := t.TempDir()
		makeOwnerOnlyTestDir(t, parent)
		archivePath := filepath.Join(parent, "stale.sqlite")
		createLegacyV4Archive(t, archivePath)
		beforeState := capturePlanningArchiveState(t, archivePath)
		beforeSchema := readArchiveSchemaFingerprint(t, archivePath)

		archive, err := openSyncPlanningArchive(context.Background(), archivePath)
		if archive != nil {
			archive.Close()
			t.Fatal("stale planning archive returned a handle")
		}
		if err == nil || !strings.Contains(err.Error(), "schema version 4") {
			t.Fatalf("stale planning archive error = %v, want schema version 4 failure", err)
		}
		afterState := capturePlanningArchiveState(t, archivePath)
		if afterState != beforeState {
			t.Fatalf("stale archive open mutated filesystem state:\nbefore: %+v\nafter:  %+v", beforeState, afterState)
		}
		afterSchema := readArchiveSchemaFingerprint(t, archivePath)
		if afterSchema != beforeSchema {
			t.Fatalf("stale archive schema/tables changed:\nbefore:\n%s\nafter:\n%s", beforeSchema, afterSchema)
		}
		if _, statErr := os.Stat(attachmentRootDirForArchive(archivePath)); !os.IsNotExist(statErr) {
			t.Fatalf("stale archive Attachment root stat error = %v, want missing", statErr)
		}
	})
}

type planningArchiveState struct {
	bytesSHA256 string
	size        int64
	mode        os.FileMode
	modTime     time.Time
	dirEntries  string
}

type failingSyncPlanningArchive struct {
	connectionErr error
	cursorErr     error
	closeErr      error
}

func (archive *failingSyncPlanningArchive) Close() error {
	return archive.closeErr
}

func (archive *failingSyncPlanningArchive) CurrentConnection(context.Context) (archived.Connection, error) {
	return archived.Connection{}, archive.connectionErr
}

func (archive *failingSyncPlanningArchive) ResolveSyncCursor(context.Context, syncCursorKey) (string, bool, error) {
	return "", false, archive.cursorErr
}

func capturePlanningArchiveState(t *testing.T, archivePath string) planningArchiveState {
	t.Helper()
	content, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read Health Archive: %v", err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("stat Health Archive: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(archivePath))
	if err != nil {
		t.Fatalf("read Health Archive directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return planningArchiveState{
		bytesSHA256: fmt.Sprintf("%x", sha256.Sum256(content)),
		size:        info.Size(),
		mode:        info.Mode(),
		modTime:     info.ModTime(),
		dirEntries:  fmt.Sprint(names),
	}
}

func readDirectoryEntryNames(t *testing.T, path string) string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read directory %s: %v", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return fmt.Sprint(names)
}

func makeOwnerOnlyTestDir(t *testing.T, path string) {
	t.Helper()
	if usesPOSIXPermissions() {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatalf("chmod test directory: %v", err)
		}
	}
}

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/BramVR/gohealthcli/internal/archived"
)

// syncPlanningArchive exposes only the local reads needed to build a
// Sync Run plan. Its production implementation opens current-schema,
// rollback-journal SQLite in mode=ro and cannot migrate, repair
// sidecars, fence runs, or create an archive. WAL is rejected because
// its reader coordination writes shared-memory state.
type syncPlanningArchive interface {
	Close() error
	CurrentConnection(ctx context.Context) (archived.Connection, error)
	ResolveSyncCursor(ctx context.Context, key syncCursorKey) (string, bool, error)
}

type sqliteSyncPlanningArchive struct {
	db *sql.DB
}

func openSyncPlanningArchive(ctx context.Context, archivePath string) (syncPlanningArchive, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).openInspectionOnly(ctx)
	if err != nil {
		return nil, err
	}
	return &sqliteSyncPlanningArchive{db: handle.db}, nil
}

func (archive *sqliteSyncPlanningArchive) Close() error {
	return archive.db.Close()
}

func (archive *sqliteSyncPlanningArchive) CurrentConnection(ctx context.Context) (archived.Connection, error) {
	connection, err := readCurrentConnection(ctx, archive.db)
	if errors.Is(err, sql.ErrNoRows) {
		return archived.Connection{}, errors.New("no Connection found; run `gohealthcli connect` first")
	}
	return connection, err
}

func (archive *sqliteSyncPlanningArchive) ResolveSyncCursor(ctx context.Context, key syncCursorKey) (string, bool, error) {
	return resolveSyncCursor(ctx, archive.db, key)
}

func syncPlanningResultError(readErr, closeErr error) error {
	if closeErr == nil {
		return readErr
	}
	closeErr = fmt.Errorf("close planning Health Archive: %w", closeErr)
	if readErr == nil {
		return closeErr
	}
	return errors.Join(readErr, closeErr)
}

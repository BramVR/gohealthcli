package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type healthArchiveOpenMode int

const (
	readOnlyArchive healthArchiveOpenMode = iota
	writeArchive
)

type healthArchiveLifecycle struct {
	path string
	// now stamps schema_migrations.applied_at. Zero value means the
	// real clock: callers that do not care about migration timestamps
	// construct the lifecycle with the path only, tests inject a fixed
	// clock through this field (#283).
	now func() time.Time
}

// clock returns the lifecycle's stamp clock, defaulting to the real
// clock when no fixed clock was injected.
func (lifecycle healthArchiveLifecycle) clock() func() time.Time {
	if lifecycle.now != nil {
		return lifecycle.now
	}
	return productionNow
}

type healthArchiveHandle struct {
	path          string
	db            *sql.DB
	schemaVersion int
}

type healthArchiveOpenError struct {
	schemaVersion int
	err           error
}

func (err healthArchiveOpenError) Error() string {
	return err.err.Error()
}

func (err healthArchiveOpenError) Unwrap() error {
	return err.err
}

func (lifecycle healthArchiveLifecycle) Create(ctx context.Context) (err error) {
	if err := ensureOwnerOnlyDir(filepath.Dir(lifecycle.path)); err != nil {
		return err
	}
	file, err := os.OpenFile(lifecycle.path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(lifecycle.path)
		}
	}()
	if err := file.Close(); err != nil {
		return err
	}

	db, err := openArchive(lifecycle.path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := applyMigrations(ctx, db, lifecycle.clock()); err != nil {
		return err
	}
	if !usesPOSIXPermissions() {
		return nil
	}
	return os.Chmod(lifecycle.path, 0o600)
}

func (lifecycle healthArchiveLifecycle) Open(ctx context.Context, mode healthArchiveOpenMode) (healthArchiveHandle, error) {
	archive, err := lifecycle.MigrateAndInspect(ctx, false)
	if err != nil {
		return healthArchiveHandle{}, err
	}
	db, err := lifecycle.openDB(mode)
	if err != nil {
		return healthArchiveHandle{}, err
	}
	return healthArchiveHandle{
		path:          lifecycle.path,
		db:            db,
		schemaVersion: archive.schemaVersion,
	}, nil
}

func (lifecycle healthArchiveLifecycle) Migrate(ctx context.Context) error {
	if err := lifecycle.validateExistingFile(); err != nil {
		return err
	}
	db, err := openArchive(lifecycle.path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := applyPendingMigrations(ctx, db, lifecycle.clock()); err != nil {
		return err
	}
	// Backfill the attachment root for archives that predate #107 /
	// migration 15. Idempotent — no-op when the dir already exists.
	return ensureOwnerOnlyDir(attachmentRootDirForArchive(lifecycle.path))
}

func (lifecycle healthArchiveLifecycle) MigrateAndInspect(ctx context.Context, validateTokens bool) (archiveCheck, error) {
	if err := lifecycle.Migrate(ctx); err != nil {
		return archiveCheck{}, fmt.Errorf("Health Archive migration failed: %w", err)
	}
	archive, err := lifecycle.Inspect(ctx, validateTokens)
	if err != nil {
		return archive, healthArchiveOpenError{
			schemaVersion: archive.schemaVersion,
			err:           fmt.Errorf("Health Archive check failed: %w", err),
		}
	}
	return archive, nil
}

func (lifecycle healthArchiveLifecycle) Inspect(ctx context.Context, validateTokens bool) (archiveCheck, error) {
	if err := lifecycle.validateExistingFile(); err != nil {
		return archiveCheck{}, err
	}
	db, err := openArchiveReadOnly(lifecycle.path)
	if err != nil {
		return archiveCheck{}, err
	}
	defer db.Close()

	var userVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return archiveCheck{}, err
	}
	if userVersion != currentSchemaVersion {
		return archiveCheck{schemaVersion: userVersion}, fmt.Errorf("schema version %d, want %d", userVersion, currentSchemaVersion)
	}

	for version, name := range expectedSchemaMigrations() {
		var migrationCount int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ? AND name = ?`, version, name).Scan(&migrationCount); err != nil {
			return archiveCheck{schemaVersion: userVersion}, err
		}
		if migrationCount != 1 {
			return archiveCheck{schemaVersion: userVersion}, fmt.Errorf("missing schema migration %d", version)
		}
	}
	if !validateTokens {
		return archiveCheck{schemaVersion: userVersion}, nil
	}
	count, tokenStatus, err := inspectConnectionTokenMetadata(ctx, db)
	if err != nil {
		return archiveCheck{}, err
	}
	return archiveCheck{
		schemaVersion:   userVersion,
		connectionCount: count,
		tokenStatus:     tokenStatus,
	}, nil
}

func (handle healthArchiveHandle) Close() error {
	return handle.db.Close()
}

func (lifecycle healthArchiveLifecycle) openDB(mode healthArchiveOpenMode) (*sql.DB, error) {
	switch mode {
	case readOnlyArchive:
		return openArchiveReadOnly(lifecycle.path)
	case writeArchive:
		return openArchive(lifecycle.path)
	default:
		return nil, fmt.Errorf("unsupported Health Archive open mode: %d", mode)
	}
}

func (lifecycle healthArchiveLifecycle) validateExistingFile() error {
	if err := validateOwnerOnlyDir(filepath.Dir(lifecycle.path)); err != nil {
		return err
	}
	info, err := os.Stat(lifecycle.path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", lifecycle.path)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%s is not owner-only: mode %04o, want 0600", lifecycle.path, info.Mode().Perm())
	}
	return nil
}

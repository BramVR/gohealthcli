package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

type healthArchiveOpenMode int

const (
	readOnlyArchive healthArchiveOpenMode = iota
	writeArchive
)

type healthArchiveLifecycle struct {
	path string
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

func (lifecycle healthArchiveLifecycle) Create() (err error) {
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
	if err := applyMigrations(db); err != nil {
		return err
	}
	if !usesPOSIXPermissions() {
		return nil
	}
	return os.Chmod(lifecycle.path, 0o600)
}

func (lifecycle healthArchiveLifecycle) Open(mode healthArchiveOpenMode) (healthArchiveHandle, error) {
	if err := lifecycle.Migrate(); err != nil {
		return healthArchiveHandle{}, fmt.Errorf("Health Archive migration failed: %w", err)
	}
	archive, err := lifecycle.Inspect(false)
	if err != nil {
		return healthArchiveHandle{}, healthArchiveOpenError{
			schemaVersion: archive.schemaVersion,
			err:           fmt.Errorf("Health Archive check failed: %w", err),
		}
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

func (lifecycle healthArchiveLifecycle) Migrate() error {
	if err := lifecycle.validateExistingFile(); err != nil {
		return err
	}
	db, err := openArchive(lifecycle.path)
	if err != nil {
		return err
	}
	defer db.Close()
	return applyPendingMigrations(db)
}

func (lifecycle healthArchiveLifecycle) Inspect(validateTokens bool) (archiveCheck, error) {
	if err := lifecycle.validateExistingFile(); err != nil {
		return archiveCheck{}, err
	}
	db, err := openArchiveReadOnly(lifecycle.path)
	if err != nil {
		return archiveCheck{}, err
	}
	defer db.Close()

	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		return archiveCheck{}, err
	}
	if userVersion != currentSchemaVersion {
		return archiveCheck{schemaVersion: userVersion}, fmt.Errorf("schema version %d, want %d", userVersion, currentSchemaVersion)
	}

	for version, name := range expectedSchemaMigrations() {
		var migrationCount int
		if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = ? AND name = ?`, version, name).Scan(&migrationCount); err != nil {
			return archiveCheck{schemaVersion: userVersion}, err
		}
		if migrationCount != 1 {
			return archiveCheck{schemaVersion: userVersion}, fmt.Errorf("missing schema migration %d", version)
		}
	}
	if !validateTokens {
		return archiveCheck{schemaVersion: userVersion}, nil
	}
	count, tokenStatus, err := inspectConnectionTokenMetadata(db)
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

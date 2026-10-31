package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthArchiveLifecycleOpensReadOnlyAndWriteHandlesThroughOnePath(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	createLegacyV4Archive(t, archivePath)

	lifecycle := healthArchiveLifecycle{path: archivePath}
	readOnly, err := lifecycle.Open(context.Background(), readOnlyArchive)
	if err != nil {
		t.Fatalf("open read-only Health Archive: %v", err)
	}
	defer readOnly.Close()
	if readOnly.schemaVersion != currentSchemaVersion {
		t.Fatalf("read-only schema version = %d, want %d", readOnly.schemaVersion, currentSchemaVersion)
	}
	if _, err := readOnly.db.ExecContext(context.Background(), `INSERT INTO schema_migrations (version, name, applied_at) VALUES (99, 'readonly_probe', '2026-06-07T00:00:00Z')`); err == nil {
		t.Fatal("read-only write error = nil, want SQLite read-only failure")
	}

	write, err := lifecycle.Open(context.Background(), writeArchive)
	if err != nil {
		t.Fatalf("open writable Health Archive: %v", err)
	}
	defer write.Close()
	if write.schemaVersion != currentSchemaVersion {
		t.Fatalf("write schema version = %d, want %d", write.schemaVersion, currentSchemaVersion)
	}
	if _, err := write.db.ExecContext(context.Background(), `INSERT INTO schema_migrations (version, name, applied_at) VALUES (99, 'write_probe', '2026-06-07T00:00:00Z')`); err != nil {
		t.Fatalf("writable insert: %v", err)
	}
}

// TestFreshHealthArchiveSchemaMatchesFullyMigratedLegacyArchive pins the
// invariant the migration runner must preserve: creating a fresh Health
// Archive and migrating a legacy v1 archive through every upgrade step
// must land on byte-identical schemas — same sqlite_master entries, same
// user_version, same schema_migrations history.
func TestFreshHealthArchiveSchemaMatchesFullyMigratedLegacyArchive(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	freshPath := filepath.Join(tempDir, "fresh", "gohealthcli.sqlite")
	if err := (healthArchiveLifecycle{path: freshPath}).Create(context.Background()); err != nil {
		t.Fatalf("create fresh Health Archive: %v", err)
	}

	migratedPath := filepath.Join(tempDir, "legacy", "gohealthcli.sqlite")
	createLegacyV1Archive(t, migratedPath)
	if err := (healthArchiveLifecycle{path: migratedPath}).Migrate(context.Background()); err != nil {
		t.Fatalf("migrate legacy v1 Health Archive: %v", err)
	}

	freshSchema := readArchiveSchemaFingerprint(t, freshPath)
	migratedSchema := readArchiveSchemaFingerprint(t, migratedPath)
	if freshSchema != migratedSchema {
		t.Fatalf("fresh archive schema differs from fully migrated legacy archive schema:\nfresh:\n%s\nmigrated:\n%s", freshSchema, migratedSchema)
	}
}

// TestCreateStampsSchemaMigrationsWithInjectedClock drives the injected
// clock into fresh Health Archive creation: every schema_migrations row
// must carry the runtime clock's time, not a stray time.Now() reading.
func TestCreateStampsSchemaMigrationsWithInjectedClock(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	clock := func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) }
	if err := (healthArchiveLifecycle{path: archivePath, now: clock}).Create(context.Background()); err != nil {
		t.Fatalf("create fresh Health Archive: %v", err)
	}

	assertSchemaMigrationStamps(t, archivePath, 1, "2026-06-11T12:00:00Z")
}

// TestMigrateStampsPendingSchemaMigrationsWithInjectedClock drives the
// injected clock into the upgrade path: migrating a legacy v1 Health
// Archive must stamp every newly applied schema_migrations row (2..N)
// with the runtime clock's time.
func TestMigrateStampsPendingSchemaMigrationsWithInjectedClock(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	createLegacyV1Archive(t, archivePath)

	clock := func() time.Time { return time.Date(2026, 6, 11, 13, 30, 0, 0, time.UTC) }
	if err := (healthArchiveLifecycle{path: archivePath, now: clock}).Migrate(context.Background()); err != nil {
		t.Fatalf("migrate legacy v1 Health Archive: %v", err)
	}

	assertSchemaMigrationStamps(t, archivePath, 2, "2026-06-11T13:30:00Z")
}

// TestMigrationStampsNormalizeInjectedClockToUTC pins the historical
// stamp contract: applied_at is always stored in UTC, even when the
// injected clock reports a zoned local time.
func TestMigrationStampsNormalizeInjectedClockToUTC(t *testing.T) {
	t.Parallel()
	zoned := time.FixedZone("UTC+2", 2*60*60)
	clock := func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, zoned) }

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	if err := (healthArchiveLifecycle{path: archivePath, now: clock}).Create(context.Background()); err != nil {
		t.Fatalf("create fresh Health Archive: %v", err)
	}

	assertSchemaMigrationStamps(t, archivePath, 1, "2026-06-11T10:00:00Z")
}

// assertSchemaMigrationStamps verifies every schema_migrations row from
// fromVersion onward carries exactly the expected applied_at stamp.
func assertSchemaMigrationStamps(t *testing.T, archivePath string, fromVersion int, wantAppliedAt string) {
	t.Helper()
	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		t.Fatalf("open archive read-only: %v", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(context.Background(), `SELECT version, applied_at FROM schema_migrations WHERE version >= ? ORDER BY version`, fromVersion)
	if err != nil {
		t.Fatalf("query schema migration stamps: %v", err)
	}
	defer rows.Close()
	stamped := 0
	for rows.Next() {
		var version int
		var appliedAt string
		if err := rows.Scan(&version, &appliedAt); err != nil {
			t.Fatalf("scan schema migration stamp: %v", err)
		}
		if appliedAt != wantAppliedAt {
			t.Fatalf("migration %d applied_at = %q, want injected clock stamp %q", version, appliedAt, wantAppliedAt)
		}
		stamped++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("schema migration stamp rows: %v", err)
	}
	if want := currentSchemaVersion - fromVersion + 1; stamped != want {
		t.Fatalf("stamped migrations = %d, want %d (versions %d..%d)", stamped, want, fromVersion, currentSchemaVersion)
	}
}

// readArchiveSchemaFingerprint renders the archive's full schema surface
// as one comparable string: every sqlite_master row (type, name, SQL),
// the user_version pragma, and the schema_migrations version/name rows.
func readArchiveSchemaFingerprint(t *testing.T, archivePath string) string {
	t.Helper()
	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		t.Fatalf("open archive read-only: %v", err)
	}
	defer db.Close()

	var fingerprint strings.Builder
	var userVersion int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	fmt.Fprintf(&fingerprint, "user_version=%d\n", userVersion)

	appendRows := func(query string, scanLine func(rows *sql.Rows) (string, error)) {
		rows, err := db.QueryContext(context.Background(), query)
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		defer rows.Close()
		for rows.Next() {
			line, err := scanLine(rows)
			if err != nil {
				t.Fatalf("scan row of %q: %v", query, err)
			}
			fingerprint.WriteString(line + "\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows of %q: %v", query, err)
		}
	}
	appendRows(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`, func(rows *sql.Rows) (string, error) {
		var objectType, name, objectSQL string
		err := rows.Scan(&objectType, &name, &objectSQL)
		return fmt.Sprintf("%s %s: %s", objectType, name, objectSQL), err
	})
	appendRows(`SELECT version, name FROM schema_migrations ORDER BY version`, func(rows *sql.Rows) (string, error) {
		var version int
		var name string
		err := rows.Scan(&version, &name)
		return fmt.Sprintf("migration %d: %s", version, name), err
	})
	return fingerprint.String()
}

func TestHealthArchiveLifecycleReportsInspectedSchemaVersion(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	createLegacyV4Archive(t, archivePath)
	setArchiveUserVersion(t, archivePath, currentSchemaVersion+1)

	archive, err := (healthArchiveLifecycle{path: archivePath}).Inspect(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("inspect error = %v, want schema version failure", err)
	}
	if archive.schemaVersion != currentSchemaVersion+1 {
		t.Fatalf("inspected schema version = %d, want %d", archive.schemaVersion, currentSchemaVersion+1)
	}
}

// TestHealthArchiveLifecycleCreateHonorsCanceledContext pins the
// noctx-completion slice (#305): the lifecycle's SQLite work rides the
// caller's context, so a canceled context aborts Create before any
// migration lands — and Create's cleanup contract still holds (no
// half-created archive file left behind).
func TestHealthArchiveLifecycleCreateHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := (healthArchiveLifecycle{path: archivePath}).Create(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create with canceled context = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(archivePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Create left %s behind after cancellation (stat err = %v)", archivePath, statErr)
	}
}

// TestHealthArchiveLifecycleOpenHonorsCanceledContext: the Open path
// (migrate + inspect) aborts under a canceled context instead of
// silently running PRAGMA and history checks to completion.
func TestHealthArchiveLifecycleOpenHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	createLegacyV4Archive(t, archivePath)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (healthArchiveLifecycle{path: archivePath}).Open(ctx, readOnlyArchive); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open with canceled context = %v, want context.Canceled", err)
	}
}

func TestArchiveDSNUsesAbsoluteFileURI(t *testing.T) {
	t.Parallel()
	dsn, err := archiveDSN("relative.sqlite", false)
	if err != nil {
		t.Fatalf("archiveDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:///") {
		t.Fatalf("dsn = %q, want absolute file URI", dsn)
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys%3Don") {
		t.Fatalf("dsn = %q, want foreign key pragma", dsn)
	}
	readOnlyDSN, err := archiveDSN("relative.sqlite", true)
	if err != nil {
		t.Fatalf("archiveDSN readonly: %v", err)
	}
	if !strings.Contains(readOnlyDSN, "mode=ro") {
		t.Fatalf("dsn = %q, want readonly mode", readOnlyDSN)
	}
}

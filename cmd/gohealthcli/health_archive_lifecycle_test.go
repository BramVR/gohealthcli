package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthArchiveLifecycleOpensReadOnlyAndWriteHandlesThroughOnePath(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	createLegacyV4Archive(t, archivePath)

	lifecycle := healthArchiveLifecycle{path: archivePath}
	readOnly, err := lifecycle.Open(readOnlyArchive)
	if err != nil {
		t.Fatalf("open read-only Health Archive: %v", err)
	}
	defer readOnly.Close()
	if readOnly.schemaVersion != currentSchemaVersion {
		t.Fatalf("read-only schema version = %d, want %d", readOnly.schemaVersion, currentSchemaVersion)
	}
	if _, err := readOnly.db.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (99, 'readonly_probe', '2026-06-07T00:00:00Z')`); err == nil {
		t.Fatal("read-only write error = nil, want SQLite read-only failure")
	}

	write, err := lifecycle.Open(writeArchive)
	if err != nil {
		t.Fatalf("open writable Health Archive: %v", err)
	}
	defer write.Close()
	if write.schemaVersion != currentSchemaVersion {
		t.Fatalf("write schema version = %d, want %d", write.schemaVersion, currentSchemaVersion)
	}
	if _, err := write.db.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (99, 'write_probe', '2026-06-07T00:00:00Z')`); err != nil {
		t.Fatalf("writable insert: %v", err)
	}
}

// TestFreshHealthArchiveSchemaMatchesFullyMigratedLegacyArchive pins the
// invariant the migration runner must preserve: creating a fresh Health
// Archive and migrating a legacy v1 archive through every upgrade step
// must land on byte-identical schemas — same sqlite_master entries, same
// user_version, same schema_migrations history.
func TestFreshHealthArchiveSchemaMatchesFullyMigratedLegacyArchive(t *testing.T) {
	tempDir := t.TempDir()

	freshPath := filepath.Join(tempDir, "fresh", "gohealthcli.sqlite")
	if err := (healthArchiveLifecycle{path: freshPath}).Create(); err != nil {
		t.Fatalf("create fresh Health Archive: %v", err)
	}

	migratedPath := filepath.Join(tempDir, "legacy", "gohealthcli.sqlite")
	createLegacyV1Archive(t, migratedPath)
	if err := (healthArchiveLifecycle{path: migratedPath}).Migrate(); err != nil {
		t.Fatalf("migrate legacy v1 Health Archive: %v", err)
	}

	freshSchema := readArchiveSchemaFingerprint(t, freshPath)
	migratedSchema := readArchiveSchemaFingerprint(t, migratedPath)
	if freshSchema != migratedSchema {
		t.Fatalf("fresh archive schema differs from fully migrated legacy archive schema:\nfresh:\n%s\nmigrated:\n%s", freshSchema, migratedSchema)
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
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	fmt.Fprintf(&fingerprint, "user_version=%d\n", userVersion)

	appendRows := func(query string, scanLine func(rows *sql.Rows) (string, error)) {
		rows, err := db.Query(query)
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
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	createLegacyV4Archive(t, archivePath)
	setArchiveUserVersion(t, archivePath, currentSchemaVersion+1)

	archive, err := (healthArchiveLifecycle{path: archivePath}).Inspect(false)
	if err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("inspect error = %v, want schema version failure", err)
	}
	if archive.schemaVersion != currentSchemaVersion+1 {
		t.Fatalf("inspected schema version = %d, want %d", archive.schemaVersion, currentSchemaVersion+1)
	}
}

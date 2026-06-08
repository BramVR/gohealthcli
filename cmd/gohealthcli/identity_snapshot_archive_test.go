package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFreshArchiveHasIdentitySnapshotsTable is the tracer bullet for
// slice A of #97: an archive created from scratch must carry the renamed
// table identity_snapshots (with the snapshot_kind discriminator) and
// must NOT carry the legacy profile_snapshots table. This is a behavior
// test through the public archive surface: `gohealthcli init` succeeds
// and the resulting SQLite file has the expected schema.
func TestFreshArchiveHasIdentitySnapshotsTable(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	if !archiveTableExists(t, db, "identity_snapshots") {
		t.Fatal("identity_snapshots table missing from fresh archive")
	}
	if archiveTableExists(t, db, "profile_snapshots") {
		t.Fatal("legacy profile_snapshots table still present after rename")
	}

	// snapshot_kind column carries the kind discriminator and defaults to 'profile'
	// so rows migrated from a v6 archive keep their identity.
	if !archiveColumnExists(t, db, "identity_snapshots", "snapshot_kind") {
		t.Fatal("identity_snapshots.snapshot_kind column missing")
	}
}

// TestV6ArchiveMigratesProfileSnapshotsWithKindDefault drives behavior 2
// of slice A: a v6 archive that contains profile_snapshots rows must
// migrate forward so those rows surface as identity_snapshots rows with
// snapshot_kind='profile'. The migration is the single ALTER RENAME +
// ALTER ADD COLUMN; existing data must round-trip without manual repair.
func TestV6ArchiveMigratesProfileSnapshotsWithKindDefault(t *testing.T) {
	tempDir := t.TempDir()
	if usesPOSIXPermissions() {
		if err := os.Chmod(tempDir, 0o700); err != nil {
			t.Fatalf("tighten tempDir perms: %v", err)
		}
	}
	archivePath := filepath.Join(tempDir, "legacy.sqlite")
	createLegacyV6ArchiveWithProfileSnapshot(t, archivePath, "conn_v6", `{"profile":"snapshot"}`, "2026-06-01T00:00:00Z")

	if err := migrateArchiveIfNeeded(archivePath); err != nil {
		t.Fatalf("migrate v6 → v7: %v", err)
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d post-migration", version, currentSchemaVersion)
	}
	if archiveTableExists(t, db, "profile_snapshots") {
		t.Fatal("profile_snapshots must be renamed to identity_snapshots")
	}

	var kind, rawJSON, fetchedAt string
	err = db.QueryRow(`SELECT snapshot_kind, raw_json, fetched_at FROM identity_snapshots WHERE id = 1`).Scan(&kind, &rawJSON, &fetchedAt)
	if err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if kind != "profile" {
		t.Fatalf("snapshot_kind = %q, want profile (migration default for pre-existing rows)", kind)
	}
	if rawJSON != `{"profile":"snapshot"}` {
		t.Fatalf("raw_json = %q, want round-tripped payload", rawJSON)
	}
	if fetchedAt != "2026-06-01T00:00:00Z" {
		t.Fatalf("fetched_at = %q, want round-tripped timestamp", fetchedAt)
	}
}

func archiveTableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite_master for %s: %v", name, err)
	}
	return got == name
}

// createLegacyV6ArchiveWithProfileSnapshot builds an archive at schema
// version 6 (the last version with profile_snapshots as the canonical
// table name) and seeds one profile_snapshots row. The migration under
// test must carry that row forward.
func createLegacyV6ArchiveWithProfileSnapshot(t *testing.T, archivePath, connectionID, rawJSON, fetchedAt string) {
	t.Helper()

	if err := ensureOwnerOnlyDir(filepath.Dir(archivePath)); err != nil {
		t.Fatalf("create legacy archive parent: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open legacy archive: %v", err)
	}
	defer db.Close()
	if err := applyV6SchemaForLegacyTest(db); err != nil {
		t.Fatalf("apply legacy v6 schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO connections (id, provider_name, google_health_user_id, token_metadata_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		connectionID, "googlehealth", "user-123", "{}", fetchedAt, fetchedAt); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO profile_snapshots (provider_name, connection_id, raw_json, fetched_at) VALUES (?, ?, ?, ?)`,
		"googlehealth", connectionID, rawJSON, fetchedAt); err != nil {
		t.Fatalf("seed profile_snapshot: %v", err)
	}
	if usesPOSIXPermissions() {
		if err := os.Chmod(archivePath, 0o600); err != nil {
			t.Fatalf("chmod 0600: %v", err)
		}
	}
}

func applyV6SchemaForLegacyTest(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range initialMigrationStatements() {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	applied := time.Date(2026, 5, 31, 21, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, 'initial_archive_schema', ?)`, applied); err != nil {
		return err
	}
	for _, apply := range []func(*sql.Tx, string) error{
		applyGoogleIdentityArchiveMigration,
		applySourceFamilyArchiveMigration,
		applyDailyStepsViewMigration,
		applyFirstReleaseNormalizedViewsMigration,
		applySyncCursorsMigration,
	} {
		if err := apply(tx, applied); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 6`); err != nil {
		return err
	}
	return tx.Commit()
}

func archiveColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return false
}

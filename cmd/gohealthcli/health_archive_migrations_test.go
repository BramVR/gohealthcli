package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestSchemaMigrationTableIsOrderedAndComplete pins the migration
// table's contract: contiguous versions 1..currentSchemaVersion in
// ascending order, each with a unique non-empty history name and a
// step. Adding schema vN+1 must stay one table row plus the
// currentSchemaVersion bump.
func TestSchemaMigrationTableIsOrderedAndComplete(t *testing.T) {
	t.Parallel()
	table := schemaMigrationTable()
	if len(table) != currentSchemaVersion {
		t.Fatalf("migration table rows = %d, want %d (one row per schema version)", len(table), currentSchemaVersion)
	}
	seenNames := make(map[string]int, len(table))
	for index, migration := range table {
		if migration.version != index+1 {
			t.Fatalf("migration table row %d has version %d, want contiguous ascending version %d", index, migration.version, index+1)
		}
		if migration.name == "" {
			t.Fatalf("migration %d has empty history name", migration.version)
		}
		if previous, duplicated := seenNames[migration.name]; duplicated {
			t.Fatalf("migration %d reuses history name %q of migration %d", migration.version, migration.name, previous)
		}
		seenNames[migration.name] = migration.version
		if migration.apply == nil {
			t.Fatalf("migration %d (%s) has no apply step", migration.version, migration.name)
		}
	}
}

func TestHealthArchiveUpsertLookupIndexesExistInLiveSchema(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	freshPath := filepath.Join(tempDir, "fresh", "gohealthcli.sqlite")
	if err := (healthArchiveLifecycle{path: freshPath}).Create(context.Background()); err != nil {
		t.Fatalf("create fresh Health Archive: %v", err)
	}
	assertUpsertLookupIndexes(t, freshPath)

	migratedPath := filepath.Join(tempDir, "legacy", "gohealthcli.sqlite")
	createLegacyV1Archive(t, migratedPath)
	if err := (healthArchiveLifecycle{path: migratedPath}).Migrate(context.Background()); err != nil {
		t.Fatalf("migrate legacy Health Archive: %v", err)
	}
	assertUpsertLookupIndexes(t, migratedPath)
}

func assertUpsertLookupIndexes(t *testing.T, archivePath string) {
	t.Helper()
	db := openArchiveForTest(t, archivePath)
	defer db.Close()
	want := map[string]string{
		"data_points_upstream_resource_lookup": `CREATE INDEX data_points_upstream_resource_lookup ON data_points (
			provider_name,
			connection_id,
			data_type,
			upstream_resource_name,
			IFNULL(source_family_filter, '')
		)`,
		"data_points_time_metadata_lookup": `CREATE INDEX data_points_time_metadata_lookup ON data_points (
			provider_name,
			connection_id,
			data_type,
			IFNULL(upstream_resource_name, ''),
			record_kind,
			IFNULL(start_time_utc, ''),
			IFNULL(end_time_utc, ''),
			IFNULL(start_civil_time, ''),
			IFNULL(end_civil_time, ''),
			IFNULL(provider_civil_date, ''),
			IFNULL(timezone_metadata, ''),
			data_source_json,
			IFNULL(source_family_filter, '')
		)`,
		"rollups_identity_lookup": `CREATE INDEX rollups_identity_lookup ON rollups (
			provider_name,
			connection_id,
			data_type,
			rollup_kind,
			IFNULL(window_start_utc, ''),
			IFNULL(window_end_utc, ''),
			IFNULL(civil_date, '')
		)`,
	}
	for name, expectedSQL := range want {
		var gotSQL string
		if err := db.QueryRowContext(context.Background(), `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&gotSQL); err != nil {
			t.Fatalf("missing index %s: %v", name, err)
		}
		if normalizeSQL(gotSQL) != normalizeSQL(expectedSQL) {
			t.Fatalf("%s SQL = %q, want %q", name, gotSQL, expectedSQL)
		}
	}
}

func normalizeSQL(sql string) string {
	return strings.Join(strings.Fields(sql), " ")
}

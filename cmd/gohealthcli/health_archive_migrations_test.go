package main

import "testing"

// TestSchemaMigrationTableIsOrderedAndComplete pins the migration
// table's contract: contiguous versions 1..currentSchemaVersion in
// ascending order, each with a unique non-empty history name and a
// step. Adding schema vN+1 must stay one table row plus the
// currentSchemaVersion bump.
func TestSchemaMigrationTableIsOrderedAndComplete(t *testing.T) {
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

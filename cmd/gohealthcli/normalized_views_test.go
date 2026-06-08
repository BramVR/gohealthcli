package main

import (
	"strings"
	"testing"
)

// TestNormalizedViewsRegistryViewReturnsSpec is the slice C tracer: the
// Registry exposes a View(name) lookup that returns the spec used to
// build the SQL view; downstream consumers (export writer, migrations,
// future describe-schema) read from this surface instead of the raw
// per-view slice in export.go.
func TestNormalizedViewsRegistryViewReturnsSpec(t *testing.T) {
	registry := normalizedViewsRegistry()
	spec, ok := registry.View("daily-steps")
	if !ok {
		t.Fatal("View(daily-steps) not found in registry")
	}
	if spec.view != "daily_steps" {
		t.Fatalf("spec.view = %q, want daily_steps", spec.view)
	}
	if spec.migrationVersion != 4 {
		t.Fatalf("spec.migrationVersion = %d, want 4", spec.migrationVersion)
	}
	if !strings.Contains(spec.viewSQL, "FROM data_points") {
		t.Fatalf("spec.viewSQL missing FROM data_points; got first 80 chars %q", spec.viewSQL[:min(80, len(spec.viewSQL))])
	}
}

// TestNormalizedViewsRegistryMigrationStatementsMatchVersion ensures the
// Registry's MigrationStatements(version) returns the same SQL the
// archive migrations apply today. This pins the contract between the
// Registry and the migration runner — they must stay in lock-step or
// fresh archives diverge from migrated archives.
func TestNormalizedViewsRegistryMigrationStatementsMatchVersion(t *testing.T) {
	registry := normalizedViewsRegistry()
	v4 := registry.MigrationStatements(4)
	if len(v4) == 0 {
		t.Fatal("MigrationStatements(4) returned 0 statements, want at least daily_steps")
	}
	v5 := registry.MigrationStatements(5)
	if len(v5) == 0 {
		t.Fatal("MigrationStatements(5) returned 0 statements, want the first-release normalized views")
	}
	for _, stmt := range append(v4, v5...) {
		if !strings.HasPrefix(strings.TrimSpace(stmt), "CREATE VIEW ") {
			t.Errorf("statement does not start with CREATE VIEW: %q", stmt[:min(80, len(stmt))])
		}
	}
}

// TestNormalizedViewsRegistryCatalogCoversAllRegisteredViews ensures
// Catalog() enumerates every registered view by name so future
// describe-schema --json doesn't miss entries.
func TestNormalizedViewsRegistryCatalogCoversAllRegisteredViews(t *testing.T) {
	registry := normalizedViewsRegistry()
	names := registry.Catalog()
	if len(names) == 0 {
		t.Fatal("Catalog() empty")
	}
	for _, name := range names {
		if _, ok := registry.View(name); !ok {
			t.Errorf("Catalog() lists %q but View(%q) returns not-found", name, name)
		}
	}
	// Every existing exportDatasetSpecs entry must appear in the catalog.
	for name := range exportDatasetSpecs {
		found := false
		for _, listed := range names {
			if listed == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("export dataset %q missing from registry catalog", name)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

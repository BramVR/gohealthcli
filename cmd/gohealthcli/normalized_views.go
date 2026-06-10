package main

// normalizedViewsRegistryHandle is the entry point through which every
// consumer reads Normalized View specs:
//   - The export writer (export.go) looks up a spec by name to render CSV/JSONL.
//   - Archive migrations apply MigrationStatements(version) so a fresh
//     archive's CREATE VIEWs match what an upgraded archive sees.
//   - A future describe-schema --json (#109) emits the catalog so an LLM
//     reading the archive knows what views are available.
//
// The spec storage currently lives in export.go (as the historical
// exportDatasetDefinitions slice) for transitional reasons; #109 splits
// it into category-named files (views_steps.go, views_sleep.go,
// views_identity.go, …) and re-anchors the Registry as the only place
// the slice is defined. The Registry's surface (View / MigrationStatements
// / Catalog) stays stable across that move — downstream consumers
// reading through the Registry today don't need to change.
type normalizedViewsRegistryHandle struct {
	definitions []exportDatasetSpec
}

func normalizedViewsRegistry() normalizedViewsRegistryHandle {
	return normalizedViewsRegistryHandle{definitions: exportDatasetDefinitions}
}

// View looks up a registered view by its public name (the CLI-facing
// dataset identifier, e.g. "daily-steps").
func (registry normalizedViewsRegistryHandle) View(name string) (exportDatasetSpec, bool) {
	for _, definition := range registry.definitions {
		if definition.name == name {
			return definition, true
		}
	}
	return exportDatasetSpec{}, false
}

// MigrationStatements returns the CREATE VIEW SQL the migration runner
// should apply at the given schema version. A registered view's
// migrationVersion is the version that introduces it; subsequent
// versions re-applying the same view would be a bug.
func (registry normalizedViewsRegistryHandle) MigrationStatements(version int) []string {
	var statements []string
	for _, definition := range registry.definitions {
		if definition.migrationVersion != version {
			continue
		}
		statements = append(statements, exportDatasetViewMigrationStatement(definition))
	}
	return statements
}

// Catalog enumerates every registered view by its public name in
// definition order. Downstream consumers iterate Catalog() then call
// View(name) to read the full spec.
func (registry normalizedViewsRegistryHandle) Catalog() []string {
	names := make([]string, 0, len(registry.definitions))
	for _, definition := range registry.definitions {
		names = append(names, definition.name)
	}
	return names
}

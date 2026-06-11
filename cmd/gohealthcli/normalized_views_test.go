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

// normalizedViewCatalogOrder is the pinned Catalog() contract: every
// registered Normalized View by public dataset name, in definition
// order. Definition order is observable behavior — describe-schema
// --json emits views in this order — so a spec-storage move (issue
// #276) must keep it byte-stable. Append new views; never reorder.
var normalizedViewCatalogOrder = []string{
	"daily-steps",
	"heart-rate-samples",
	"resting-heart-rate-by-day",
	"sleep-sessions",
	"exercise-sessions",
	"weight-samples",
	"active-minutes-intervals",
	"active-zone-minutes-intervals",
	"altitude-intervals",
	"activity-level-intervals",
	"sedentary-period-intervals",
	"time-in-heart-rate-zone-intervals",
	"swim-lengths-data-intervals",
	"vo2-max-samples",
	"run-vo2-max-samples",
	"daily-vo2-max",
	"daily-heart-rate-zones",
	"daily-sleep-temperature-derivations",
	"respiratory-rate-sleep-summary",
	"floors-intervals",
	"electrocardiogram-sessions",
	"irregular-rhythm-notifications",
	"hydration-log-sessions",
	"searchable-text",
	"sleep-stages",
	"exercise-splits",
	"current-irn-profile",
	"paired-devices",
	"current-settings",
	"body-fat-samples",
	"blood-glucose-samples",
	"core-body-temperature-samples",
	"height-samples",
	"current-height",
}

// TestNormalizedViewsRegistryCatalogOrderIsStable pins the full
// Catalog() listing — names AND order. The catalog feeds
// describe-schema --json directly, so any accidental reorder during
// the #276 spec-storage move would change user-visible output.
func TestNormalizedViewsRegistryCatalogOrderIsStable(t *testing.T) {
	names := normalizedViewsRegistry().Catalog()
	if len(names) != len(normalizedViewCatalogOrder) {
		t.Fatalf("Catalog() lists %d Normalized Views, want %d", len(names), len(normalizedViewCatalogOrder))
	}
	for index, want := range normalizedViewCatalogOrder {
		if names[index] != want {
			t.Fatalf("Catalog()[%d] = %q, want %q", index, names[index], want)
		}
	}
}

// normalizedViewMigrationOrder pins which SQL view each schema version
// introduces, in registry definition order. MigrationStatements(version)
// is what the Health Archive lifecycle replays on a fresh archive, so
// this order decides sqlite_master order and therefore
// describe-schema --sql output. Pinned before the #276 move so a
// dropped or reordered spec fails loudly.
var normalizedViewMigrationOrder = map[int][]string{
	4:  {"daily_steps"},
	5:  {"heart_rate_samples", "resting_heart_rate_by_day", "sleep_sessions", "exercise_sessions", "weight_samples"},
	8:  {"current_settings"},
	9:  {"paired_devices"},
	10: {"current_irn_profile"},
	11: {"sleep_stages", "exercise_splits"},
	13: {"searchable_text"},
	16: {"floors_intervals"},
	17: {"active_minutes_intervals", "active_zone_minutes_intervals", "altitude_intervals", "activity_level_intervals", "sedentary_period_intervals", "time_in_heart_rate_zone_intervals", "swim_lengths_data_intervals", "vo2_max_samples", "run_vo2_max_samples"},
	18: {"body_fat_samples", "blood_glucose_samples", "core_body_temperature_samples", "height_samples", "current_height"},
	19: {"daily_vo2_max", "daily_heart_rate_zones", "daily_sleep_temperature_derivations", "respiratory_rate_sleep_summary"},
	20: {"electrocardiogram_sessions", "irregular_rhythm_notifications"},
	21: {"hydration_log_sessions"},
}

// TestNormalizedViewsRegistryMigrationStatementsPinViewsPerVersion pins
// the per-version MigrationStatements surface: each schema version
// yields exactly the pinned CREATE VIEW statements, in order, and no
// version outside the pinned set yields any. Together with the catalog
// pin this guarantees the #276 spec move cannot silently drop, duplicate,
// or reshuffle a Normalized View definition.
func TestNormalizedViewsRegistryMigrationStatementsPinViewsPerVersion(t *testing.T) {
	registry := normalizedViewsRegistry()
	pinnedTotal := 0
	for version, wantViews := range normalizedViewMigrationOrder {
		statements := registry.MigrationStatements(version)
		if len(statements) != len(wantViews) {
			t.Fatalf("MigrationStatements(%d) returned %d statements, want %d", version, len(statements), len(wantViews))
		}
		for index, want := range wantViews {
			prefix := "CREATE VIEW " + want + " AS\n"
			if !strings.HasPrefix(statements[index], prefix) {
				t.Errorf("MigrationStatements(%d)[%d] does not begin %q; got %q", version, index, prefix, statements[index][:min(60, len(statements[index]))])
			}
		}
		pinnedTotal += len(wantViews)
	}
	if pinnedTotal != len(normalizedViewCatalogOrder) {
		t.Fatalf("pinned migration views = %d, catalog pins %d — pins drifted apart", pinnedTotal, len(normalizedViewCatalogOrder))
	}
	// No unpinned version may introduce views: walk a generous version
	// range and require emptiness everywhere outside the pinned map.
	for version := 0; version <= 64; version++ {
		if _, pinned := normalizedViewMigrationOrder[version]; pinned {
			continue
		}
		if statements := registry.MigrationStatements(version); len(statements) != 0 {
			t.Errorf("MigrationStatements(%d) = %d statements, want 0 (version not pinned)", version, len(statements))
		}
	}
}

package main

import (
	"fmt"
	"strings"
)

// exportDatasetSpec is one Normalized View dataset definition: the
// CLI-facing dataset name, the SQL view it projects, the view SQL the
// Health Archive lifecycle applies at migrationVersion, the exported
// field order, and the stable sort order `export` reads with. The
// definitions themselves live in category-named view files
// (views_steps.go, views_sleep.go, views_identity.go, …); this file
// owns the assembled registry.
type exportDatasetSpec struct {
	name             string
	view             string
	viewSQL          string
	migrationVersion int
	fields           []exportFieldSpec
	orderBy          string
}

type exportFieldSpec struct {
	name string
	kind string
}

// exportDatasetDefinitions is the canonical Normalized View registry
// slice (issue #276 finished the spec-storage move documented under
// closed #109): each entry is defined in its category-named view file
// and listed here once, in registration order. Order is contract —
// Catalog() and describe-schema --json emit it verbatim, and within a
// schema version the migration runner applies CREATE VIEWs in this
// order — so append new views; never reorder existing entries. Every
// consumer reads through normalizedViewsRegistry(), never this slice
// directly.
var exportDatasetDefinitions = []exportDatasetSpec{
	dailyStepsViewSpec,
	heartRateSamplesViewSpec,
	restingHeartRateByDayViewSpec,
	sleepSessionsViewSpec,
	exerciseSessionsViewSpec,
	weightSamplesViewSpec,
	activeMinutesIntervalsViewSpec,
	activeZoneMinutesIntervalsViewSpec,
	altitudeIntervalsViewSpec,
	activityLevelIntervalsViewSpec,
	sedentaryPeriodIntervalsViewSpec,
	timeInHeartRateZoneIntervalsViewSpec,
	swimLengthsDataIntervalsViewSpec,
	vo2MaxSamplesViewSpec,
	runVo2MaxSamplesViewSpec,
	dailyVo2MaxViewSpec,
	dailyHeartRateZonesViewSpec,
	dailySleepTemperatureDerivationsViewSpec,
	respiratoryRateSleepSummaryViewSpec,
	floorsIntervalsViewSpec,
	electrocardiogramSessionsViewSpec,
	irregularRhythmNotificationsViewSpec,
	hydrationLogSessionsViewSpec,
	searchableTextViewSpec,
	sleepStagesViewSpec,
	exerciseSplitsViewSpec,
	currentIrnProfileViewSpec,
	pairedDevicesViewSpec,
	currentSettingsViewSpec,
	bodyFatSamplesViewSpec,
	bloodGlucoseSamplesViewSpec,
	coreBodyTemperatureSamplesViewSpec,
	heightSamplesViewSpec,
	currentHeightViewSpec,
}

var exportDatasetSpecs = exportDatasetSpecByName(exportDatasetDefinitions)

func exportDatasetSpecByName(definitions []exportDatasetSpec) map[string]exportDatasetSpec {
	specs := make(map[string]exportDatasetSpec, len(definitions))
	for _, definition := range definitions {
		if definition.name == "" {
			panic("export dataset definition missing name")
		}
		if _, exists := specs[definition.name]; exists {
			panic(fmt.Sprintf("duplicate export dataset definition: %s", definition.name))
		}
		specs[definition.name] = definition
	}
	return specs
}

func exportDatasetViewMigrationStatement(spec exportDatasetSpec) string {
	return fmt.Sprintf("CREATE VIEW %s AS\n%s", spec.view, strings.TrimSpace(spec.viewSQL))
}

// normalizedViewsRegistryHandle is the entry point through which every
// consumer reads Normalized View specs:
//   - The export writer (export.go) looks up a spec by name to render CSV/JSONL.
//   - Archive migrations apply MigrationStatements(version) so a fresh
//     archive's CREATE VIEWs match what an upgraded archive sees.
//   - describe-schema --json emits the catalog so an LLM reading the
//     archive knows what views are available.
//
// The spec storage lives in category-named view files (views_steps.go,
// views_sleep.go, views_identity.go, …) assembled into the
// exportDatasetDefinitions slice above. The Registry's surface
// (View / MigrationStatements / Catalog) is the stable contract —
// downstream consumers never read the per-category vars directly.
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

package main

// Health Archive schema migrations, owned by the Health Archive
// lifecycle (ADR-0007: lifecycle owns create/open/migrate). The whole
// schema history is one ordered table of (version, name, step); one
// loop applies whatever an archive is missing. Adding schema vN+1 is
// one new table row plus the currentSchemaVersion bump below.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// currentSchemaVersion is the schema version a fully migrated Health
// Archive reports via PRAGMA user_version. It must equal the version of
// the last row in schemaMigrationTable.
const currentSchemaVersion = 24

// schemaMigration is one Health Archive schema step: the version it
// migrates an archive *to*, the schema_migrations history name recorded
// for it, and the DDL it applies. Steps run inside the caller's
// transaction and carry the caller's context (#305) so a canceled
// migration aborts at the next statement boundary; the apply loop
// records the history row.
type schemaMigration struct {
	version int
	name    string
	apply   func(ctx context.Context, tx *sql.Tx) error
}

// schemaMigrationTable is the ordered schema history of the Health
// Archive. The loop in applySchemaMigrationSteps applies every step
// whose version an archive has not reached yet, in table order.
func schemaMigrationTable() []schemaMigration {
	return []schemaMigration{
		{version: 1, name: "initial_archive_schema", apply: statementsStep(initialMigrationStatements)},
		{version: 2, name: "add_google_identity_json", apply: statementsStep(googleIdentityJSONMigrationStatements)},
		{version: 3, name: "add_source_family_filter", apply: statementsStep(sourceFamilyFilterMigrationStatements)},
		{version: 4, name: "add_daily_steps_view", apply: registryViewsStep(4)},
		{version: 5, name: "add_first_release_normalized_views", apply: registryViewsStep(5)},
		{version: 6, name: "add_sync_cursors", apply: statementsStep(syncCursorsMigrationStatements)},
		{version: 7, name: "rename_profile_snapshots_to_identity_snapshots", apply: statementsStep(identitySnapshotsMigrationStatements)},
		{version: 8, name: "add_current_settings_view", apply: registryViewsStep(8)},
		{version: 9, name: "add_paired_devices_view", apply: registryViewsStep(9)},
		{version: 10, name: "add_current_irn_profile_view", apply: registryViewsStep(10)},
		{version: 11, name: "add_sleep_stages_and_exercise_splits_views", apply: registryViewsStep(11)},
		// Version 12 drops the migration-11 exercise_splits view that
		// extracted distance from $.distanceMeters (a path Google Health
		// API does not emit) and recreates it against the real shape:
		// $.metricsSummary.distanceMillimeters in millimeters. Live
		// testing in the original #105 PR returned all-NULL distances;
		// this is the follow-up that pins the view to what the upstream
		// actually returns.
		{version: 12, name: "fix_exercise_splits_real_shape", apply: recreateRegistryViewStep("exercise-splits", "exercise_splits")},
		{version: 13, name: "add_searchable_text_view", apply: registryViewsStep(13)},
		// Version 14 drops the migration-13 searchable_text view and
		// recreates it from the Registry. The new definition restricts
		// the profile kind to the latest snapshot per Connection and
		// filters empty-string values from data_source and exercise_type
		// rows (Copilot findings on PR #121).
		{version: 14, name: "fix_searchable_text_latest_profile_and_empty_filter", apply: recreateRegistryViewStep("searchable-text", "searchable_text")},
		{version: 15, name: "add_data_point_attachments", apply: statementsStep(dataPointAttachmentsMigrationStatements)},
		{version: 16, name: "add_floors_intervals_view", apply: registryViewsStep(16)},
		{version: 17, name: "add_tier1_activity_views", apply: registryViewsStep(17)},
		{version: 18, name: "add_tier1_health_metrics_views", apply: registryViewsStep(18)},
		// Version 19 installs the four daily/sample Normalized Views for
		// #103: daily_vo2_max, daily_heart_rate_zones,
		// daily_sleep_temperature_derivations,
		// respiratory_rate_sleep_summary. The session-shaped
		// hydration_log_sessions view ships separately at schema version
		// 21 so the migration row history records the two payload shapes
		// independently.
		{version: 19, name: "add_tier1_daily_hydration_views", apply: registryViewsStep(19)},
		// Version 20 registers the Tier 2 ECG and IRN Normalized Views
		// (#104) — electrocardiogram_sessions and
		// irregular_rhythm_notifications. The view SQL itself lives in
		// the shared registry; this step just runs the registered CREATE
		// VIEW statements pinned to schema version 20.
		{version: 20, name: "add_tier2_ecg_irn_views", apply: registryViewsStep(20)},
		// Version 21 installs the session-shaped hydration_log_sessions
		// Normalized View (#103). The view projects
		// $.hydrationLog.volume.liters (TEXT for precision) plus the
		// standard session timing columns. The daily/sample Tier 1
		// daily+hydration views shipped at v19; this seals the slice.
		{version: 21, name: "add_hydration_log_sessions_view", apply: registryViewsStep(21)},
		// Version 22 adds the last_progress_at heartbeat column to
		// sync_runs (#236). The ingestion writes it (together with the
		// running counts) after every archived page, so a concurrent
		// `sync --status` reader can tell an alive long run from an
		// abandoned one. NULL on historical rows: those predate
		// heartbeats, and the stale-run fence falls back to started_at
		// for them.
		{version: 22, name: "add_sync_run_heartbeat", apply: statementsStep(syncRunHeartbeatMigrationStatements)},
		// Version 23 drops the migration-9 paired_devices view and the
		// searchable_text view that selects its columns, recreating both
		// from the registry's current specs. Live verification against a
		// real archive (#298) showed users.pairedDevices.list wraps the
		// device list in a pairedDevices key with name / deviceType /
		// batteryStatus / batteryLevel / deviceVersion per device; the
		// original #98 view read $.devices and model / manufacturer /
		// batteryPercentage paths the upstream never emits, so both views
		// always returned zero device rows. Same follow-up shape as
		// version 12's exercise_splits fix.
		{version: 23, name: "fix_paired_devices_real_shape", apply: func(ctx context.Context, tx *sql.Tx) error {
			if err := recreateRegistryViewStep("paired-devices", "paired_devices")(ctx, tx); err != nil {
				return err
			}
			return recreateRegistryViewStep("searchable-text", "searchable_text")(ctx, tx)
		}},
		// Version 24 drops the migration-19 daily_vo2_max view and
		// recreates it from the registry's current spec. modernc.org/sqlite
		// 1.52 changed implicit CAST-to-TEXT float rendering; the registry
		// now formats VO2 max floats explicitly so existing archives keep the
		// same stable export text as fresh archives.
		{version: 24, name: "fix_daily_vo2_max_stable_float_text", apply: recreateRegistryViewStep("daily-vo2-max", "daily_vo2_max")},
	}
}

// applyMigrations creates the full current schema on a fresh, empty
// Health Archive by applying every table row from version 1. The
// caller's clock stamps schema_migrations.applied_at.
func applyMigrations(ctx context.Context, db *sql.DB, now func() time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback after a successful Commit returns sql.ErrTxDone; the error is
	// deliberately ignored because this defer is only the abort path.
	defer func() { _ = tx.Rollback() }()
	if err := applySchemaMigrationSteps(ctx, tx, 0, currentSchemaVersion, now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, currentSchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// applyPendingMigrations upgrades an existing Health Archive from its
// recorded schema version to the current one, applying only the table
// rows the archive has not reached yet.
func applyPendingMigrations(ctx context.Context, db *sql.DB, now func() time.Time) error {
	var userVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return err
	}
	switch {
	case userVersion == currentSchemaVersion:
		return nil
	case userVersion >= 1 && userVersion < currentSchemaVersion:
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// Rollback after a successful Commit returns sql.ErrTxDone; the error is
		// deliberately ignored because this defer is only the abort path.
		defer func() { _ = tx.Rollback() }()
		if err := applySchemaMigrationSteps(ctx, tx, userVersion, currentSchemaVersion, now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, currentSchemaVersion)); err != nil {
			return err
		}
		return tx.Commit()
	default:
		return fmt.Errorf("schema version %d, want %d", userVersion, currentSchemaVersion)
	}
}

// applySchemaMigrationSteps runs every schema migration step with
// fromVersion < version <= toVersion, in table order, recording one
// schema_migrations history row per applied step. The caller owns the
// transaction and the closing PRAGMA user_version write.
func applySchemaMigrationSteps(ctx context.Context, tx *sql.Tx, fromVersion, toVersion int, appliedAt string) error {
	for _, migration := range schemaMigrationTable() {
		if migration.version <= fromVersion || migration.version > toVersion {
			continue
		}
		if err := migration.apply(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`, migration.version, migration.name, appliedAt); err != nil {
			return err
		}
	}
	return nil
}

func migrateArchiveIfNeeded(ctx context.Context, archivePath string) error {
	// healthArchiveLifecycle.Migrate already backfills the attachment
	// root, so this thin wrapper just forwards.
	return (healthArchiveLifecycle{path: archivePath}).Migrate(ctx)
}

// expectedSchemaMigrations is the version → name history Inspect
// verifies row-by-row, derived straight from the migration table.
func expectedSchemaMigrations() map[int]string {
	table := schemaMigrationTable()
	expected := make(map[int]string, len(table))
	for _, migration := range table {
		expected[migration.version] = migration.name
	}
	return expected
}

// statementsStep wraps a fixed DDL statement list as a migration step.
func statementsStep(statements func() []string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		return execMigrationStatements(ctx, tx, statements())
	}
}

// registryViewsStep applies the Normalized View CREATE statements the
// registry pins to one schema version.
func registryViewsStep(version int) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		return execMigrationStatements(ctx, tx, normalizedViewsRegistry().MigrationStatements(version))
	}
}

// recreateRegistryViewStep drops an existing Normalized View and
// recreates it from the registry's current spec — the step shape for
// fix-style migrations that repair a view shipped by an earlier
// version.
func recreateRegistryViewStep(datasetName, viewName string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DROP VIEW IF EXISTS `+viewName); err != nil {
			return err
		}
		spec, ok := normalizedViewsRegistry().View(datasetName)
		if !ok {
			return fmt.Errorf("%s view missing from registry; cannot recreate", datasetName)
		}
		_, err := tx.ExecContext(ctx, exportDatasetViewMigrationStatement(spec))
		return err
	}
}

func execMigrationStatements(ctx context.Context, tx *sql.Tx, statements []string) error {
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func initialMigrationStatements() []string {
	return []string{
		`CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`,
		`CREATE TABLE connections (
			id TEXT PRIMARY KEY,
			provider_name TEXT NOT NULL,
			google_health_user_id TEXT NOT NULL,
			legacy_fitbit_user_id TEXT,
			token_metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE data_points (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_name TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			data_type TEXT NOT NULL,
			upstream_resource_name TEXT,
			record_kind TEXT NOT NULL,
			start_time_utc TEXT,
			end_time_utc TEXT,
			start_civil_time TEXT,
			end_civil_time TEXT,
			provider_civil_date TEXT,
			timezone_metadata TEXT,
			data_source_json TEXT NOT NULL,
			raw_json TEXT NOT NULL,
			inserted_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
		`CREATE TABLE data_point_revisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			data_point_id INTEGER NOT NULL,
			previous_raw_json TEXT NOT NULL,
			replaced_at TEXT NOT NULL,
			replacement_reason TEXT,
			FOREIGN KEY (data_point_id) REFERENCES data_points(id)
		)`,
		`CREATE TABLE rollups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_name TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			data_type TEXT NOT NULL,
			rollup_kind TEXT NOT NULL,
			window_start_utc TEXT,
			window_end_utc TEXT,
			civil_date TEXT,
			timezone_metadata TEXT,
			raw_json TEXT NOT NULL,
			inserted_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
		`CREATE TABLE profile_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_name TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			raw_json TEXT NOT NULL,
			fetched_at TEXT NOT NULL,
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
		`CREATE TABLE sync_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_name TEXT NOT NULL,
			connection_id TEXT,
			data_types_requested TEXT NOT NULL,
			range_requested_json TEXT NOT NULL,
			endpoint_family TEXT NOT NULL,
			status TEXT NOT NULL,
			seen_count INTEGER NOT NULL DEFAULT 0,
			new_count INTEGER NOT NULL DEFAULT 0,
			updated_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			error_summary TEXT,
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
	}
}

func googleIdentityJSONMigrationStatements() []string {
	return []string{
		`ALTER TABLE connections ADD COLUMN google_identity_json TEXT NOT NULL DEFAULT '{}'`,
	}
}

func sourceFamilyFilterMigrationStatements() []string {
	return []string{
		`ALTER TABLE data_points ADD COLUMN source_family_filter TEXT`,
		`ALTER TABLE sync_runs ADD COLUMN source_family_filter TEXT`,
	}
}

func syncCursorsMigrationStatements() []string {
	return []string{
		`CREATE TABLE sync_cursors (
			connection_id TEXT NOT NULL,
			data_type TEXT NOT NULL,
			source_family_filter TEXT NOT NULL DEFAULT '',
			rollup_kind TEXT NOT NULL DEFAULT 'none',
			cursor_time TEXT NOT NULL,
			advanced_at TEXT NOT NULL,
			PRIMARY KEY (connection_id, data_type, source_family_filter, rollup_kind),
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
	}
}

// identitySnapshotsMigrationStatements renames profile_snapshots to
// identity_snapshots and adds the snapshot_kind discriminator. All
// existing rows keep snapshot_kind = 'profile' via the column default,
// preserving every prior profile snapshot's identity without a
// parallel-table-with-view shim (per the PRD §"identity_snapshots
// migration: explicit strategy").
func identitySnapshotsMigrationStatements() []string {
	return []string{
		`ALTER TABLE profile_snapshots RENAME TO identity_snapshots`,
		`ALTER TABLE identity_snapshots ADD COLUMN snapshot_kind TEXT NOT NULL DEFAULT 'profile'`,
	}
}

func dataPointAttachmentsMigrationStatements() []string {
	// The CREATE TABLE text below keeps the exact whitespace of the
	// original migration: SQLite stores the literal statement text in
	// sqlite_master, so reindenting it would make fresh archives differ
	// textually from every archive migrated before this table existed.
	return []string{
		`CREATE TABLE data_point_attachments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		data_point_id INTEGER NOT NULL,
		kind TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		path_relative TEXT NOT NULL,
		byte_size INTEGER NOT NULL,
		fetched_at TEXT NOT NULL,
		FOREIGN KEY (data_point_id) REFERENCES data_points(id)
	)`,
		`CREATE UNIQUE INDEX data_point_attachments_dp_sha ON data_point_attachments (data_point_id, sha256)`,
	}
}

func syncRunHeartbeatMigrationStatements() []string {
	return []string{
		`ALTER TABLE sync_runs ADD COLUMN last_progress_at TEXT`,
	}
}

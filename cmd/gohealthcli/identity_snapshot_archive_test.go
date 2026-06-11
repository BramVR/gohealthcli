package main

import (
	"bytes"
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
	t.Parallel()
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

// TestIdentitySnapshotArchiveInsertAndLatestRoundTrip is the slice B
// tracer: Insert(kind, raw, fetchedAt) writes a row tagged with the
// supplied kind, and the newest-of-kind read-back returns it. Insert
// is the live write seam every Identity Snapshot command reaches; the
// read-back uses latestIdentitySnapshotRow (the same SQL surface the
// `query` command exposes) because the zombie Latest() reader was
// deleted with the dead command-wrapper layer (#270).
func TestIdentitySnapshotArchiveInsertAndLatestRoundTrip(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open identity snapshot archive: %v", err)
	}
	defer archive.Close()

	connection, err := readCurrentConnection(archive.db)
	if err != nil {
		t.Fatalf("read current Connection: %v", err)
	}
	if _, err := archive.Insert(connection, "settings", `{"unit":"metric"}`, "2026-06-01T00:00:00Z"); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	snapshot, found := latestIdentitySnapshotRow(t, archive.db, connection.id, "settings")
	if !found {
		t.Fatal("latest settings snapshot returned not-found after Insert")
	}
	if snapshot.RawJSON != `{"unit":"metric"}` {
		t.Fatalf("RawJSON = %q, want round-tripped payload", snapshot.RawJSON)
	}
	if snapshot.FetchedAt != "2026-06-01T00:00:00Z" {
		t.Fatalf("FetchedAt = %q, want round-tripped timestamp", snapshot.FetchedAt)
	}
	if snapshot.Kind != "settings" {
		t.Fatalf("Kind = %q, want settings", snapshot.Kind)
	}
}

// TestIdentitySnapshotArchiveLatestFiltersByKind verifies that the
// newest-of-kind read returns the newest row of the requested kind
// even when other kinds and older rows of the same kind are also
// present — i.e. Insert tags every row so kinds never bleed.
func TestIdentitySnapshotArchiveLatestFiltersByKind(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer archive.Close()
	connection, err := readCurrentConnection(archive.db)
	if err != nil {
		t.Fatalf("read current Connection: %v", err)
	}

	// Three settings snapshots (oldest → newest) plus one profile snapshot
	// interleaved. Latest('settings') must return the newest settings row;
	// the profile row must not bleed across kinds.
	for _, row := range []struct {
		kind, raw, at string
	}{
		{"settings", `{"unit":"imperial"}`, "2026-05-01T00:00:00Z"},
		{"profile", `{"name":"Old"}`, "2026-05-15T00:00:00Z"},
		{"settings", `{"unit":"metric"}`, "2026-06-01T00:00:00Z"},
		{"settings", `{"unit":"metric","timezone":"UTC"}`, "2026-06-10T00:00:00Z"},
	} {
		if _, err := archive.Insert(connection, row.kind, row.raw, row.at); err != nil {
			t.Fatalf("Insert(%s): %v", row.kind, err)
		}
	}

	latestSettings, found := latestIdentitySnapshotRow(t, archive.db, connection.id, "settings")
	if !found {
		t.Fatal("latest settings snapshot: not found")
	}
	if latestSettings.RawJSON != `{"unit":"metric","timezone":"UTC"}` {
		t.Fatalf("latest settings RawJSON = %q, want newest settings row", latestSettings.RawJSON)
	}
	latestProfile, found := latestIdentitySnapshotRow(t, archive.db, connection.id, "profile")
	if !found {
		t.Fatal("latest profile snapshot: not found")
	}
	if latestProfile.RawJSON != `{"name":"Old"}` {
		t.Fatalf("latest profile RawJSON = %q, want profile row", latestProfile.RawJSON)
	}
	// A kind never inserted must surface as not-found, not as some
	// accidental cross-kind match.
	if _, found := latestIdentitySnapshotRow(t, archive.db, connection.id, "paired-devices"); found {
		t.Fatal("latest paired-devices snapshot returned a row, want not-found")
	}
}

// TestProfileCommandWritesViaIdentitySnapshotArchive verifies the slice B
// lifting: the `profile` command no longer writes through the Connection
// API. After `gohealthcli profile`, reading the newest profile snapshot
// back from the archive must surface the row the command wrote.
func TestProfileCommandWritesViaIdentitySnapshotArchive(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	// Run the profile command via the existing test surface — same path
	// the real CLI uses end-to-end.
	testRuntime.fetchProfile = func(string) (googleProfile, error) {
		return googleProfile{
			healthUserID: "111111256096816351",
			resourceName: "users/111111256096816351/profile",
			rawJSON:      `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}`,
		}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("profile exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open identity snapshot archive: %v", err)
	}
	defer archive.Close()
	connection, err := readCurrentConnection(archive.db)
	if err != nil {
		t.Fatalf("read current Connection: %v", err)
	}
	latest, found := latestIdentitySnapshotRow(t, archive.db, connection.id, "profile")
	if !found {
		t.Fatal("latest profile snapshot: not found")
	}
	if latest.Kind != "profile" {
		t.Fatalf("Kind = %q, want profile", latest.Kind)
	}
	if latest.RawJSON != `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}` {
		t.Fatalf("RawJSON = %q, want round-tripped profile payload", latest.RawJSON)
	}
}

// TestSettingsCommandArchivesSnapshotWithKindSettings is the slice D
// tracer: `gohealthcli settings` calls users.getSettings, archives the
// payload via the Identity Snapshot Archive with kind='settings', and
// reports success to the user.
func TestSettingsCommandArchivesSnapshotWithKindSettings(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}
	// PRD #142 slice 2 / #176: getSettings now requires
	// settings.readonly, so simulate the user having run
	// `connect --add-scopes settings`.
	addStoredConnectionScope(t, archivePath, googleHealthSettingsReadonlyScope)

	testRuntime.fetchSettings = func(string) (googleSettings, error) {
		return googleSettings{
			rawJSON: `{"name":"users/111111256096816351/settings","measurementSystem":"METRIC","timezone":"Europe/Brussels"}`,
		}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"settings", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("settings exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open identity snapshot archive: %v", err)
	}
	defer archive.Close()
	connection, err := readCurrentConnection(archive.db)
	if err != nil {
		t.Fatalf("read current Connection: %v", err)
	}
	latest, found := latestIdentitySnapshotRow(t, archive.db, connection.id, "settings")
	if !found {
		t.Fatal("latest settings snapshot: not found")
	}
	if latest.Kind != "settings" {
		t.Fatalf("Kind = %q, want settings", latest.Kind)
	}
	if latest.RawJSON != `{"name":"users/111111256096816351/settings","measurementSystem":"METRIC","timezone":"Europe/Brussels"}` {
		t.Fatalf("RawJSON = %q, want round-tripped settings payload", latest.RawJSON)
	}
}

// TestCurrentSettingsViewProjectsLatestSnapshot pins the slice D view:
// once at least one settings snapshot has been archived, current_settings
// returns one row per Connection projecting the latest payload as
// columns (measurement_system, timezone) plus the source identifiers.
func TestCurrentSettingsViewProjectsLatestSnapshot(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	connection, err := readCurrentConnection(archive.db)
	if err != nil {
		archive.Close()
		t.Fatalf("read current Connection: %v", err)
	}
	// Two settings snapshots: only the newest (id=2) should surface.
	if _, err := archive.Insert(connection, "settings", `{"measurementSystem":"IMPERIAL","timezone":"America/New_York"}`, "2026-05-01T00:00:00Z"); err != nil {
		t.Fatalf("insert old settings: %v", err)
	}
	if _, err := archive.Insert(connection, "settings", `{"measurementSystem":"METRIC","timezone":"Europe/Brussels"}`, "2026-06-08T00:00:00Z"); err != nil {
		t.Fatalf("insert new settings: %v", err)
	}
	archive.Close()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var measurementSystem, timezone, fetchedAt string
	err = db.QueryRow(`SELECT measurement_system, timezone, fetched_at FROM current_settings WHERE connection_id = ?`, connection.id).
		Scan(&measurementSystem, &timezone, &fetchedAt)
	if err != nil {
		t.Fatalf("query current_settings: %v", err)
	}
	if measurementSystem != "METRIC" {
		t.Fatalf("measurement_system = %q, want METRIC (newest snapshot's value)", measurementSystem)
	}
	if timezone != "Europe/Brussels" {
		t.Fatalf("timezone = %q, want Europe/Brussels (newest snapshot's value)", timezone)
	}
	if fetchedAt != "2026-06-08T00:00:00Z" {
		t.Fatalf("fetched_at = %q, want newest snapshot's timestamp", fetchedAt)
	}
}

// TestIdentitySnapshotArchiveLatestUsesFetchedAtForRecency guards against
// id-order vs fetched_at-order divergence. Insert order is id=1 (newer
// fetched_at) then id=2 (older fetched_at); a naive ORDER BY id DESC
// would surface id=2 as "latest" — wrong. The live newest-of-kind
// reader is the current_settings Normalized View (the surface `query`
// and `export` expose), so the assertion goes through it: the view
// must project the row that was fetched most recently, not the row
// inserted most recently.
func TestIdentitySnapshotArchiveLatestUsesFetchedAtForRecency(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	connection, err := readCurrentConnection(archive.db)
	if err != nil {
		archive.Close()
		t.Fatalf("read current Connection: %v", err)
	}

	// id=1, fetched_at=2026-06-08 (the actually-newest)
	if _, err := archive.Insert(connection, "settings", `{"measurementSystem":"NEWEST-FETCH"}`, "2026-06-08T00:00:00Z"); err != nil {
		archive.Close()
		t.Fatalf("Insert newer: %v", err)
	}
	// id=2, fetched_at=2026-05-01 (older, even though inserted later)
	if _, err := archive.Insert(connection, "settings", `{"measurementSystem":"OLDER-FETCH"}`, "2026-05-01T00:00:00Z"); err != nil {
		archive.Close()
		t.Fatalf("Insert older: %v", err)
	}
	archive.Close()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var measurementSystem, fetchedAt string
	err = db.QueryRow(`SELECT measurement_system, fetched_at FROM current_settings WHERE connection_id = ?`, connection.id).
		Scan(&measurementSystem, &fetchedAt)
	if err != nil {
		t.Fatalf("query current_settings: %v", err)
	}
	if measurementSystem != "NEWEST-FETCH" || fetchedAt != "2026-06-08T00:00:00Z" {
		t.Fatalf("current_settings = (%q, %q), want the row with the latest fetched_at (id ordering would have given the wrong answer)", measurementSystem, fetchedAt)
	}
}

// TestV6ArchiveMigratesProfileSnapshotsWithKindDefault drives behavior 2
// of slice A: a v6 archive that contains profile_snapshots rows must
// migrate forward so those rows surface as identity_snapshots rows with
// snapshot_kind='profile'. The migration is the single ALTER RENAME +
// ALTER ADD COLUMN; existing data must round-trip without manual repair.
func TestV6ArchiveMigratesProfileSnapshotsWithKindDefault(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	if usesPOSIXPermissions() {
		if err := os.Chmod(tempDir, 0o700); err != nil {
			t.Fatalf("tighten tempDir perms: %v", err)
		}
	}
	archivePath := filepath.Join(tempDir, "legacy.sqlite")
	createLegacyV6ArchiveWithProfileSnapshot(t, archivePath, "conn_v6", `{"profile":"snapshot"}`, "2026-06-01T00:00:00Z")

	if err := migrateArchiveIfNeeded(archivePath); err != nil {
		t.Fatalf("migrate legacy v6 archive to current schema version: %v", err)
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

// identitySnapshotRow is the test-side projection of one
// identity_snapshots row.
type identitySnapshotRow struct {
	Kind      string
	RawJSON   string
	FetchedAt string
}

// latestIdentitySnapshotRow reads the newest snapshot of one kind back
// through plain SQL on identity_snapshots — the same surface the
// `query` command exposes to users — using the (fetched_at DESC,
// id DESC) recency contract every live reader pins (current_settings,
// paired_devices, readStatusSnapshotFreshness). Test-only: the binary
// reads snapshots through the Normalized Views, never row-by-row.
func latestIdentitySnapshotRow(t *testing.T, db *sql.DB, connectionID, kind string) (identitySnapshotRow, bool) {
	t.Helper()
	var row identitySnapshotRow
	err := db.QueryRow(`SELECT snapshot_kind, raw_json, fetched_at
		FROM identity_snapshots
		WHERE connection_id = ? AND snapshot_kind = ?
		ORDER BY fetched_at DESC, id DESC
		LIMIT 1`, connectionID, kind).Scan(&row.Kind, &row.RawJSON, &row.FetchedAt)
	if err == sql.ErrNoRows {
		return identitySnapshotRow{}, false
	}
	if err != nil {
		t.Fatalf("read latest %s snapshot: %v", kind, err)
	}
	return row, true
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
	defer func() { _ = tx.Rollback() }()
	applied := time.Date(2026, 5, 31, 21, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if err := applySchemaMigrationSteps(tx, 0, 6, applied); err != nil {
		return err
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

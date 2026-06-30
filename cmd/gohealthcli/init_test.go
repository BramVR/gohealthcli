package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesConfigAndEmptyHealthArchive(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	oauthClientPath := filepath.Join(tempDir, "client_secret.json")

	code, stdout, stderr := runCommand(t,
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"init",
		"--oauth-client-file", oauthClientPath,
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "initialized" {
		t.Fatalf("status = %v, want initialized", got["status"])
	}
	assertJSONString(t, got, "config_path", configPath)
	assertJSONString(t, got, "archive_path", archivePath)
	assertJSONString(t, got, "oauth_client_source", "file")
	if got["schema_version"] != float64(currentSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", got["schema_version"], currentSchemaVersion)
	}
	dataTypes, ok := got["default_data_types"].([]any)
	if !ok {
		t.Fatalf("default_data_types = %T(%v), want array", got["default_data_types"], got["default_data_types"])
	}
	if len(dataTypes) == 0 || dataTypes[0] != "steps" {
		t.Fatalf("default_data_types = %v, want steps first", dataTypes)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	assertMode(t, filepath.Dir(configPath), 0o700)
	assertMode(t, filepath.Dir(archivePath), 0o700)
	assertMode(t, configPath, 0o600)
	assertMode(t, archivePath, 0o600)

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := string(configBytes)
	for _, want := range []string{
		`archive_path = "` + archivePath + `"`,
		`source = "file"`,
		`path = "` + oauthClientPath + `"`,
		`[credential_store]`,
		`"steps"`,
		`"weight"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
	if expectedDefaultCredentialStoreKind() == "os_native" {
		for _, want := range []string{`type = "os_native"`, `service = "gohealthcli"`} {
			if !strings.Contains(config, want) {
				t.Fatalf("config missing %q:\n%s", want, config)
			}
		}
	} else {
		for _, want := range []string{`type = "file"`, `path = "` + filepath.Join(filepath.Dir(configPath), "tokens.json") + `"`} {
			if !strings.Contains(config, want) {
				t.Fatalf("config missing %q:\n%s", want, config)
			}
		}
	}

	db := openArchiveForTest(t, archivePath)

	var foreignKeys int
	if err := db.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign key pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	rows, err := db.QueryContext(context.Background(), `SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query schema migrations: %v", err)
	}
	defer rows.Close()
	var migrations []string
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			t.Fatalf("scan schema migration: %v", err)
		}
		migrations = append(migrations, fmt.Sprintf("%d:%s", version, name))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("schema migration rows: %v", err)
	}
	if strings.Join(migrations, ",") != "1:initial_archive_schema,2:add_google_identity_json,3:add_source_family_filter,4:add_daily_steps_view,5:add_first_release_normalized_views,6:add_sync_cursors,7:rename_profile_snapshots_to_identity_snapshots,8:add_current_settings_view,9:add_paired_devices_view,10:add_current_irn_profile_view,11:add_sleep_stages_and_exercise_splits_views,12:fix_exercise_splits_real_shape,13:add_searchable_text_view,14:fix_searchable_text_latest_profile_and_empty_filter,15:add_data_point_attachments,16:add_floors_intervals_view,17:add_tier1_activity_views,18:add_tier1_health_metrics_views,19:add_tier1_daily_hydration_views,20:add_tier2_ecg_irn_views,21:add_hydration_log_sessions_view,22:add_sync_run_heartbeat,23:fix_paired_devices_real_shape,24:fix_numeric_text_view_formatting,25:add_sync_upsert_lookup_indexes" {
		t.Fatalf("migrations = %v, want all migrations 1..25", migrations)
	}

	for _, table := range []string{
		"connections",
		"data_points",
		"data_point_revisions",
		"rollups",
		"identity_snapshots",
		"sync_runs",
		"sync_cursors",
	} {
		var tableName string
		if err := db.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&tableName); err != nil {
			t.Fatalf("missing table %s: %v", table, err)
		}
	}

	db.SetMaxOpenConns(2)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var txForeignKeys int
	if err := tx.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&txForeignKeys); err != nil {
		t.Fatalf("query transaction foreign key pragma: %v", err)
	}
	if txForeignKeys != 1 {
		t.Fatalf("transaction foreign_keys = %d, want 1", txForeignKeys)
	}

	_, err = db.ExecContext(context.Background(), `INSERT INTO data_points (
		provider_name,
		connection_id,
		data_type,
		record_kind,
		data_source_json,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"missing-connection",
		"steps",
		"sample",
		"{}",
		"{}",
		"2026-05-31T00:00:00Z",
		"2026-05-31T00:00:00Z",
	)
	if err == nil {
		t.Fatal("insert orphan Data Point succeeded, want foreign key failure")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("insert orphan Data Point error = %v, want foreign key failure", err)
	}
}

func TestInitMigratesLegacyV1ArchiveBeforeValidation(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV1Archive(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["schema_version"] != float64(currentSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", got["schema_version"], currentSchemaVersion)
	}
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
}

func TestInitStoresSecretProviderReference(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
		"--secret-provider", "1password",
		"--oauth-client-item", "Google Health OAuth",
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	outText := stdout.String()
	for _, want := range []string{
		"status: initialized\n",
		"oauth_client_source: secret_provider\n",
		fmt.Sprintf("schema_version: %d\n", currentSchemaVersion),
	} {
		if !strings.Contains(outText, want) {
			t.Fatalf("stdout missing %q:\n%s", want, outText)
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := string(configBytes)
	for _, want := range []string{
		`source = "secret_provider"`,
		`provider = "1password"`,
		`item = "Google Health OAuth"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
}

func TestInitRequiresExactOAuthClientSource(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	archivePath := filepath.Join(tempDir, "gohealthcli.sqlite")

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	// Slice 7 PRD #143 routes --json failures through the Failure
	// Reporter, which emits a single-line `{"status":...,"message":...}`
	// envelope on stdout (Failure Reporter contract). Stderr stays
	// empty in --json mode; pre-slice-7 the message landed on stderr.
	if !strings.Contains(stdout.String(), "requires --oauth-client-file or --secret-provider") {
		t.Fatalf("stdout missing source error: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"status":"flag_invalid"`) {
		t.Fatalf("stdout missing flag_invalid status: %q", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty in --json failure mode", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive stat err = %v, want not exist", err)
	}
}

func TestInitOAuthClientItemWithoutSecretProviderNamesProvidedFlag(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	archivePath := filepath.Join(tempDir, "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-item", "Google Health OAuth",
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	// Issue #150: the message must name the flag the user actually
	// provided (--oauth-client-item) and the flag that is missing
	// (--secret-provider), not the other way around.
	if !strings.Contains(stderr.String(), "init: --oauth-client-item requires --secret-provider") {
		t.Fatalf("stderr missing direction-correct dependency error: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive stat err = %v, want not exist", err)
	}
}

func TestInitSecretProviderWithoutOAuthClientItemNamesProvidedFlag(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	archivePath := filepath.Join(tempDir, "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--secret-provider", "1password",
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	// Regression guard for issue #150: this direction was already
	// correct — --secret-provider was provided, --oauth-client-item
	// is the missing flag.
	if !strings.Contains(stderr.String(), "init: --secret-provider requires --oauth-client-item") {
		t.Fatalf("stderr missing direction-correct dependency error: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive stat err = %v, want not exist", err)
	}
}

func TestInitRejectsInvalidOAuthClientFileBeforeCreatingSetup(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	code := runInit(
		[]string{
			"--config", configPath,
			"--db", archivePath,
			"--oauth-client-file", filepath.Join(tempDir, "missing-client.json"),
		},
		CommonFlagValues{ConfigPath: defaultConfigPath(), ArchivePath: defaultArchivePath()},
		stdout,
		stderr,
		runtimeAdapters{},
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "OAuth client file") {
		t.Fatalf("stderr missing OAuth client file error: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive stat err = %v, want not exist", err)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestInitNamesMissingInstalledObjectForEmptyOAuthClientJSON(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	clientPath := filepath.Join(tempDir, "empty.json")
	if err := os.WriteFile(clientPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write empty OAuth client file: %v", err)
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	code := runInit(
		[]string{
			"--config", configPath,
			"--db", archivePath,
			"--oauth-client-file", clientPath,
		},
		CommonFlagValues{ConfigPath: defaultConfigPath(), ArchivePath: defaultArchivePath()},
		stdout,
		stderr,
		runtimeAdapters{},
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	// Regression guard for issue #149: {} IS a JSON object, so the error
	// must name the missing "installed" structure instead.
	if !strings.Contains(stderr.String(), `missing the "installed" object`) {
		t.Fatalf("stderr missing \"installed\" object error: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "must contain a JSON object") {
		t.Fatalf("stderr still claims {} is not a JSON object: %q", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive stat err = %v, want not exist", err)
	}
}

func TestInitRejectsWebOAuthClientFile(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	clientPath := filepath.Join(tempDir, "web-client.json")
	content := []byte(`{"web":{"client_id":"test-client","client_secret":"test-secret","redirect_uris":["http://127.0.0.1:8080/oauth2callback"]}}`)
	if err := os.WriteFile(clientPath, content, 0o600); err != nil {
		t.Fatalf("write web OAuth client file: %v", err)
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	code := runInit(
		[]string{
			"--config", configPath,
			"--db", archivePath,
			"--oauth-client-file", clientPath,
		},
		CommonFlagValues{ConfigPath: defaultConfigPath(), ArchivePath: defaultArchivePath()},
		stdout,
		stderr,
		runtimeAdapters{},
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "must be an installed desktop client, not a web client") {
		t.Fatalf("stderr missing web client rejection: %q", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive stat err = %v, want not exist", err)
	}
}

func TestInitKeepsNonObjectMessageForNullOAuthClientJSON(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	clientPath := filepath.Join(tempDir, "null.json")
	// JSON "null" unmarshals into a nil map without error, so it must keep
	// the non-object message rather than the missing-"installed" one.
	if err := os.WriteFile(clientPath, []byte(`null`), 0o600); err != nil {
		t.Fatalf("write null OAuth client file: %v", err)
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	code := runInit(
		[]string{
			"--config", configPath,
			"--db", archivePath,
			"--oauth-client-file", clientPath,
		},
		CommonFlagValues{ConfigPath: defaultConfigPath(), ArchivePath: defaultArchivePath()},
		stdout,
		stderr,
		runtimeAdapters{},
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "must contain a JSON object") {
		t.Fatalf("stderr missing non-object error: %q", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestInitIsIdempotentForExistingSetup(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("initial init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	)
	if code != 0 {
		t.Fatalf("second init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status: already_initialized\n") {
		t.Fatalf("stdout missing already initialized status:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestInitIdempotencyDoesNotRequireHealthyTokenMetadata(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("initial init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `INSERT INTO connections (
		id,
		provider_name,
		google_health_user_id,
		token_metadata_json,
		created_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?)`,
		"googlehealth:123",
		"googlehealth",
		"123",
		"{}",
		"2026-05-31T00:00:00Z",
		"2026-05-31T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	)
	if code != 0 {
		t.Fatalf("second init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status: already_initialized\n") {
		t.Fatalf("stdout missing already initialized status:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestInitRejectsExistingInvalidArchive(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("initial init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if err := os.WriteFile(archivePath, []byte{}, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	// Slice 7 PRD #143: --json failure routes through the Failure
	// Reporter envelope on stdout; stderr stays empty in --json mode.
	if !strings.Contains(stdout.String(), "existing Health Archive is not initialized") {
		t.Fatalf("stdout missing archive validation error: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "Health Archive check failed") {
		t.Fatalf("stdout included extra check wrapper: %q", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty in --json failure mode", stderr.String())
	}
}

func TestInitRejectsExistingInvalidConfig(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("initial init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if err := os.WriteFile(configPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	// Slice 7 PRD #143: --plain failure routes the `status:` /
	// `message:` block to stdout AND keeps the `init: <msg>` line on
	// stderr. Both streams now carry the error in --plain mode.
	if !strings.Contains(stdout.String(), "existing config is not initialized") {
		t.Fatalf("stdout missing config validation error block: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "existing config is not initialized") {
		t.Fatalf("stderr missing config validation error line: %q", stderr.String())
	}
}

func TestInitRemovesCreatedConfigWhenArchiveCreationFails(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archiveParentPath := filepath.Join(tempDir, "not-a-directory")
	archivePath := filepath.Join(archiveParentPath, "gohealthcli.sqlite")
	if err := os.WriteFile(archiveParentPath, []byte{}, 0o600); err != nil {
		t.Fatalf("write archive parent file: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "create Health Archive") {
		t.Fatalf("stderr missing archive creation error: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
}

func TestInitRejectsExistingUnsafeDirectory(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, "config")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "not owner-only") {
		t.Fatalf("stderr missing owner-only error: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
}

func TestInitJSONReportsWriteFailure(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	oauthClientPath := filepath.Join(tempDir, "client_secret.json")
	if err := os.WriteFile(oauthClientPath, []byte(`{"installed":{"client_id":"test-client","client_secret":"test-secret"}}`), 0o600); err != nil {
		t.Fatalf("write OAuth client file: %v", err)
	}
	stderr := new(bytes.Buffer)

	code := runInit(
		[]string{
			"--config", filepath.Join(tempDir, "config", "config.toml"),
			"--db", filepath.Join(tempDir, "data", "gohealthcli.sqlite"),
			"--json",
			"--oauth-client-file", oauthClientPath,
		},
		CommonFlagValues{ConfigPath: defaultConfigPath(), ArchivePath: defaultArchivePath()},
		failingWriter{},
		stderr,
		runtimeAdapters{},
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "write output") {
		t.Fatalf("stderr missing write error: %q", stderr.String())
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var testBinaryPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gohealthcli-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	testBinaryPath = filepath.Join(dir, "gohealthcli")
	build := exec.Command("go", "build", "-o", testBinaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build command: %v\n%s", err, string(output))
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestDoctorJSONReportsMissingSetup(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	archivePath := filepath.Join(tempDir, "gohealthcli.sqlite")

	code, stdout, stderr := runCommand(t,
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}

	if got["status"] != "setup_missing" {
		t.Fatalf("status = %v, want setup_missing", got["status"])
	}
	assertJSONString(t, got, "config_path", configPath)
	assertJSONString(t, got, "archive_path", archivePath)

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestDoctorJSONReportsMissingSetupAfterCommand(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--json",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "setup_missing" {
		t.Fatalf("status = %v, want setup_missing", got["status"])
	}
	if got["config_path"] != filepath.Join(tempDir, "config.toml") {
		t.Fatalf("config_path = %v, want command flag value", got["config_path"])
	}
	if got["archive_path"] != filepath.Join(tempDir, "gohealthcli.sqlite") {
		t.Fatalf("archive_path = %v, want command flag value", got["archive_path"])
	}

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestDoctorRejectsUnknownFlagAfterCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "doctor", "--bogus")

	if code == 0 || code == 2 {
		t.Fatalf("exit code = %d, want flag error", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr missing flag error: %q", stderr.String())
	}
}

func TestDoctorAcceptsNoInputBeforeCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "--no-input", "doctor", "--plain")

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "status: setup_missing\n") {
		t.Fatalf("stdout missing setup status: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", stderr.String())
	}
}

func TestDoctorAcceptsNoInputAfterCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "doctor", "--no-input", "--plain")

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "status: setup_missing\n") {
		t.Fatalf("stdout missing setup status: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", stderr.String())
	}
}

func TestDoctorPlainReportsMissingSetup(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--plain",
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	outText := stdout.String()
	for _, want := range []string{
		"status: setup_missing\n",
		"config_path: " + filepath.Join(tempDir, "config.toml") + "\n",
		"archive_path: " + filepath.Join(tempDir, "gohealthcli.sqlite") + "\n",
	} {
		if !strings.Contains(outText, want) {
			t.Fatalf("stdout missing %q:\n%s", want, outText)
		}
	}
	if strings.Contains(outText, "connection_count:") {
		t.Fatalf("stdout reported uninspected connection count:\n%s", outText)
	}

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestDoctorHumanReportsMissingSetup(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	outText := stdout.String()
	if !strings.Contains(outText, "Setup missing") {
		t.Fatalf("stdout missing human setup status: %q", outText)
	}
	if strings.Contains(outText, "run `gohealthcli init`") {
		t.Fatalf("stdout contains human hint that should be stderr: %q", outText)
	}

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestVersionDoesNotCheckSetup(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "missing-config.toml"),
		"--db", filepath.Join(tempDir, "missing.sqlite"),
		"--version",
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "gohealthcli dev" {
		t.Fatalf("version stdout = %q, want %q", got, "gohealthcli dev")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestHelpExitsSuccessfully(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"doctor", "--help"},
	} {
		code, stdout, stderr := runCommand(t, args...)

		if code != 0 {
			t.Fatalf("%v exit code = %d, want 0\nstderr: %s", args, code, stderr.String())
		}
		if stdout.String() != "" {
			t.Fatalf("%v stdout = %q, want empty", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "Usage of") {
			t.Fatalf("%v stderr missing usage: %q", args, stderr.String())
		}
	}
}

func TestDoctorDefaultPathsAreUsable(t *testing.T) {
	home := t.TempDir()
	xdgConfig := filepath.Join(home, "xdg-config")
	xdgData := filepath.Join(home, "xdg-data")

	code, stdout, stderr := runCommandWithEnv(t,
		[]string{
			"HOME=" + home,
			"XDG_CONFIG_HOME=" + xdgConfig,
			"XDG_DATA_HOME=" + xdgData,
		},
		"--json",
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2\nstderr: %s", code, stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["config_path"] != filepath.Join(xdgConfig, "gohealthcli", "config.toml") {
		t.Fatalf("config_path = %v, want XDG config path", got["config_path"])
	}
	if got["archive_path"] != filepath.Join(xdgData, "gohealthcli", "gohealthcli.sqlite") {
		t.Fatalf("archive_path = %v, want XDG data path", got["archive_path"])
	}
	if strings.Contains(stdout.String(), "~") {
		t.Fatalf("stdout contains unexpanded home path: %s", stdout.String())
	}
}

func TestInitCreatesConfigAndEmptyHealthArchive(t *testing.T) {
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
	if got["schema_version"] != float64(1) {
		t.Fatalf("schema_version = %v, want 1", got["schema_version"])
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
		`type = "os_native"`,
		`service = "gohealthcli"`,
		`"steps"`,
		`"weight"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign key pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	var version int
	var name string
	if err := db.QueryRow(`SELECT version, name FROM schema_migrations`).Scan(&version, &name); err != nil {
		t.Fatalf("query schema migration: %v", err)
	}
	if version != 1 || name != "initial_archive_schema" {
		t.Fatalf("migration = (%d, %q), want (1, initial_archive_schema)", version, name)
	}

	for _, table := range []string{
		"connections",
		"data_points",
		"data_point_revisions",
		"rollups",
		"profile_snapshots",
		"sync_runs",
	} {
		var tableName string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&tableName); err != nil {
			t.Fatalf("missing table %s: %v", table, err)
		}
	}

	db.SetMaxOpenConns(2)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer tx.Rollback()

	var txForeignKeys int
	if err := tx.QueryRow(`PRAGMA foreign_keys`).Scan(&txForeignKeys); err != nil {
		t.Fatalf("query transaction foreign key pragma: %v", err)
	}
	if txForeignKeys != 1 {
		t.Fatalf("transaction foreign_keys = %d, want 1", txForeignKeys)
	}

	_, err = db.Exec(`INSERT INTO data_points (
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

func TestDoctorReportsInitializedSetup(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %v, want ok", got["status"])
	}
	assertJSONString(t, got, "config_path", configPath)
	assertJSONString(t, got, "archive_path", archivePath)
	assertJSONString(t, got, "oauth_client_source", "file")
	assertJSONString(t, got, "credential_store", "os_native")
	assertJSONString(t, got, "token_status", "not_connected")
	if got["schema_version"] != float64(1) {
		t.Fatalf("schema_version = %v, want 1", got["schema_version"])
	}
	if got["connection_count"] != float64(0) {
		t.Fatalf("connection_count = %v, want 0", got["connection_count"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorPlainReportsOfflineHealthCheck(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	want := fmt.Sprintf("status: ok\nconfig_path: %s\narchive_path: %s\noauth_client_source: file\ncredential_store: os_native\nschema_version: 1\nconnection_count: 0\ntoken_status: not_connected\nmessage: local gohealthcli setup is initialized\n", configPath, archivePath)
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorJSONReportsInvalidSetup(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if err := os.WriteFile(configPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "setup_invalid" {
		t.Fatalf("status = %v, want setup_invalid", got["status"])
	}
	assertJSONString(t, got, "config_path", configPath)
	assertJSONString(t, got, "archive_path", archivePath)
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "config check failed") {
		t.Fatalf("message = %T(%v), want config check failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsMalformedOAuthClientReference(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := strings.Replace(string(configBytes), `path = "`+filepath.Join(tempDir, "client_secret.json")+`"`+"\n", "", 1)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "OAuth client file path") {
		t.Fatalf("message = %T(%v), want OAuth client file path failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsMissingOAuthClientFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	oauthClientPath := filepath.Join(tempDir, "client_secret.json")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", oauthClientPath,
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if err := os.Remove(oauthClientPath); err != nil {
		t.Fatalf("remove OAuth client file: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "OAuth client file") {
		t.Fatalf("message = %T(%v), want OAuth client file failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorDefaultsLegacyConfigWithoutCredentialStore(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := strings.Replace(string(configBytes), "\n[credential_store]\ntype = \"os_native\"\nservice = \"gohealthcli\"\n", "", 1)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "credential_store", "os_native")
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsInlineConfigComments(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := string(configBytes)
	config = strings.Replace(config, `archive_path = "`+archivePath+`"`, `archive_path = "`+archivePath+`" # local Health Archive`, 1)
	config = strings.Replace(config, `"steps",`, `"steps", # default Data Type`, 1)
	config = strings.Replace(config, `type = "os_native"`, `type = "os_native" # default Credential Store`, 1)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %v, want ok", got["status"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsInlineDefaultDataTypesArray(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	inlineDataTypes := "default_data_types = [\"" + strings.Join(defaultDataTypes, "\", \"") + "\"]"
	config := strings.Replace(string(configBytes), multilineDataTypes, inlineDataTypes, 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %v, want ok", got["status"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsMultivalueDefaultDataTypeRows(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	config := strings.Replace(string(configBytes), multilineDataTypes, "default_data_types = [\n  \"steps\", \"weight\",\n]", 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsOpeningLineMultilineDefaultDataTypesArray(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	config := strings.Replace(string(configBytes), multilineDataTypes, "default_data_types = [\"steps\",\n  \"weight\",\n]", 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsConfiguredDefaultDataTypeSubset(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	config := strings.Replace(string(configBytes), multilineDataTypes, `default_data_types = ["steps"]`, 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsUnsupportedDefaultDataType(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	config := strings.Replace(string(configBytes), multilineDataTypes, `default_data_types = ["bogus"]`, 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "unsupported default Data Type") {
		t.Fatalf("message = %T(%v), want unsupported Data Type failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsMissingDefaultDataTypes(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]\n\n"
	config := strings.Replace(string(configBytes), multilineDataTypes, "", 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "missing default_data_types") {
		t.Fatalf("message = %T(%v), want missing default_data_types failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsMalformedCredentialStoreConfig(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := strings.Replace(string(configBytes), `type = "os_native"`, `type = "1password"`, 1)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "Credential Store") {
		t.Fatalf("message = %T(%v), want Credential Store failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorValidatesConnectionTokenMetadata(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.Exec(`INSERT INTO connections (
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
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "missing token metadata") {
		t.Fatalf("message = %T(%v), want missing token metadata failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db, err = openArchive(archivePath)
	if err != nil {
		t.Fatalf("reopen archive: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`,
		`{"credential_store_key":"googlehealth:123","expires_at":"2026-06-01T00:00:00Z","scopes":[""]}`,
		"googlehealth:123",
	)
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	code, stdout, stderr = runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	got = map[string]any{}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok = got["message"].(string)
	if !ok || !strings.Contains(message, "empty strings") {
		t.Fatalf("message = %T(%v), want empty scope failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db, err = openArchive(archivePath)
	if err != nil {
		t.Fatalf("reopen archive: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`,
		`{"credential_store_key":"googlehealth:123","expires_at":"2026-06-01T00:00:00Z","scopes":["health.activity.read"]}`,
		"googlehealth:123",
	)
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	code, stdout, stderr = runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	got = map[string]any{}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["connection_count"] != float64(1) {
		t.Fatalf("connection_count = %v, want 1", got["connection_count"])
	}
	assertJSONString(t, got, "token_status", "metadata_present")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorDoesNotLeakTokenMetadataSecretMaterial(t *testing.T) {
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.Exec(`INSERT INTO connections (
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
		`{"credential_store_key":"googlehealth:123","expires_at":"2026-06-01T00:00:00Z","scopes":["health.activity.read"],"nested":{"idToken":"very-secret-value"}}`,
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
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "very-secret-value") {
		t.Fatalf("output leaked token value: %s", combined)
	}
	assertNoSecretWords(t, combined)
}

func TestInitStoresSecretProviderReference(t *testing.T) {
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
		"schema_version: 1\n",
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
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "requires --oauth-client-file or --secret-provider") {
		t.Fatalf("stderr missing source error: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive stat err = %v, want not exist", err)
	}
}

func TestInitIsIdempotentForExistingSetup(t *testing.T) {
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
	_, err = db.Exec(`INSERT INTO connections (
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
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "existing Health Archive is not initialized") {
		t.Fatalf("stderr missing archive validation error: %q", stderr.String())
	}
}

func TestInitRejectsExistingInvalidConfig(t *testing.T) {
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
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "existing config is not initialized") {
		t.Fatalf("stderr missing config validation error: %q", stderr.String())
	}
}

func TestInitRemovesCreatedConfigWhenArchiveCreationFails(t *testing.T) {
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
	tempDir := t.TempDir()
	stderr := new(bytes.Buffer)

	code := runInit(
		[]string{
			"--config", filepath.Join(tempDir, "config", "config.toml"),
			"--db", filepath.Join(tempDir, "data", "gohealthcli.sqlite"),
			"--json",
			"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
		},
		defaultConfigPath(),
		defaultArchivePath(),
		outputMode{},
		failingWriter{},
		stderr,
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "write output") {
		t.Fatalf("stderr missing write error: %q", stderr.String())
	}
}

func TestValidateConfigDoesNotCreateMissingParent(t *testing.T) {
	tempDir := t.TempDir()
	parentPath := filepath.Join(tempDir, "missing")

	err := validateConfig(filepath.Join(parentPath, "config.toml"), filepath.Join(tempDir, "archive.sqlite"))
	if err == nil {
		t.Fatal("validateConfig error = nil, want missing parent failure")
	}
	if _, statErr := os.Stat(parentPath); !os.IsNotExist(statErr) {
		t.Fatalf("parent stat err = %v, want not exist", statErr)
	}
}

func TestArchiveDSNUsesAbsoluteFileURI(t *testing.T) {
	dsn, err := archiveDSN("relative.sqlite", false)
	if err != nil {
		t.Fatalf("archiveDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:///") {
		t.Fatalf("dsn = %q, want absolute file URI", dsn)
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys%3Don") {
		t.Fatalf("dsn = %q, want foreign key pragma", dsn)
	}
	readOnlyDSN, err := archiveDSN("relative.sqlite", true)
	if err != nil {
		t.Fatalf("archiveDSN readonly: %v", err)
	}
	if !strings.Contains(readOnlyDSN, "mode=ro") {
		t.Fatalf("dsn = %q, want readonly mode", readOnlyDSN)
	}
}

func runCommand(t *testing.T, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runCommandWithEnv(t, nil, args...)
}

func runCommandWithEnv(t *testing.T, env []string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	ensureTestOAuthClientFiles(t, args)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd := exec.Command(testBinaryPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), env...)

	err := cmd.Run()
	if err == nil {
		return 0, stdout, stderr
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), stdout, stderr
	}
	t.Fatalf("run command: %v\nstderr: %s", err, stderr.String())
	return 1, stdout, stderr
}

func ensureTestOAuthClientFiles(t *testing.T, args []string) {
	t.Helper()

	for index, arg := range args {
		if arg != "--oauth-client-file" || index+1 >= len(args) {
			continue
		}
		path := args[index+1]
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat OAuth client file: %v", err)
		}
		content := []byte(`{"installed":{"client_id":"test-client","client_secret":"test-secret"}}`)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write OAuth client file: %v", err)
		}
	}
}

func assertJSONString(t *testing.T, got map[string]any, key, want string) {
	t.Helper()

	value, ok := got[key].(string)
	if !ok {
		t.Fatalf("%s = %T(%v), want string %q", key, got[key], got[key], want)
	}
	if value != want {
		t.Fatalf("%s = %q, want %q", key, value, want)
	}
}

func assertNoSecretWords(t *testing.T, text string) {
	t.Helper()
	for _, word := range []string{"access_token", "refresh_token", "client_secret", "id_token", "accessToken", "refreshToken", "clientSecret", "idToken"} {
		if strings.Contains(text, word) {
			t.Fatalf("output leaked %s: %s", word, text)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if !usesPOSIXPermissions() {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %v, want %v", path, got, want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

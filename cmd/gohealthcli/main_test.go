package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

var testBinaryPath string

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

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
	if got["schema_version"] != float64(2) {
		t.Fatalf("schema_version = %v, want 2", got["schema_version"])
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

	rows, err := db.Query(`SELECT version, name FROM schema_migrations ORDER BY version`)
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
	if strings.Join(migrations, ",") != "1:initial_archive_schema,2:add_google_identity_json" {
		t.Fatalf("migrations = %v, want initial + identity", migrations)
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
	assertJSONString(t, got, "credential_store", expectedDefaultCredentialStoreKind())
	assertJSONString(t, got, "token_status", "not_connected")
	if got["schema_version"] != float64(2) {
		t.Fatalf("schema_version = %v, want 2", got["schema_version"])
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

	want := fmt.Sprintf("status: ok\nconfig_path: %s\narchive_path: %s\noauth_client_source: file\ncredential_store: %s\nschema_version: 2\nconnection_count: 0\ntoken_status: not_connected\nmessage: local gohealthcli setup is initialized\n", configPath, archivePath, expectedDefaultCredentialStoreKind())
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

func TestDoctorAcceptsRelativeOAuthClientFileAfterInitFromDifferentDirectory(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	initDir := filepath.Join(tempDir, "init-cwd")
	doctorDir := filepath.Join(tempDir, "doctor-cwd")
	if err := os.Mkdir(initDir, 0o700); err != nil {
		t.Fatalf("create init dir: %v", err)
	}
	if err := os.Mkdir(doctorDir, 0o700); err != nil {
		t.Fatalf("create doctor dir: %v", err)
	}

	code, _, stderr := runCommandInDir(t,
		initDir,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", "client_secret.json",
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	oauthClientPath, err := filepath.EvalSymlinks(filepath.Join(initDir, "client_secret.json"))
	if err != nil {
		t.Fatalf("resolve OAuth client path: %v", err)
	}
	if want := `path = "` + oauthClientPath + `"`; !strings.Contains(string(configBytes), want) {
		t.Fatalf("config missing absolute OAuth client path %q:\n%s", want, string(configBytes))
	}

	code, stdout, stderr := runCommandInDir(t,
		doctorDir,
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
	config := removeCredentialStoreSection(t, string(configBytes))
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
	assertJSONString(t, got, "credential_store", expectedDefaultCredentialStoreKind())
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
	storeType := `type = "` + expectedDefaultCredentialStoreKind() + `"`
	config = strings.Replace(config, storeType, storeType+` # default Credential Store`, 1)
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
	config := strings.Replace(string(configBytes), `type = "`+expectedDefaultCredentialStoreKind()+`"`, `type = "1password"`, 1)
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

func TestDoctorOnlineRefreshesExpiredTokenAndChecksProvider(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T21:00:00Z")
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                     time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		refreshedAccessToken:    "refreshed-access-secret",
		wantRefreshToken:        "refresh-secret-value",
		wantProviderAccessToken: "refreshed-access-secret",
		healthUserID:            "111111256096816351",
		legacyFitbitUserID:      "A1B2C3",
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("doctor --online exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONString(t, got, "token_status", "online_ok")
	assertJSONString(t, got, "message", "online Google Health check passed")
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "old-access-secret") || strings.Contains(stdout.String()+stderr.String(), "refreshed-access-secret") || strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") {
		t.Fatalf("doctor --online output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if !strings.Contains(string(tokenStoreBytes), "refreshed-access-secret") || !strings.Contains(string(tokenStoreBytes), "refresh-secret-value") {
		t.Fatal("token store was not refreshed")
	}
	tokenMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if strings.Contains(tokenMetadata, "refreshed-access-secret") || strings.Contains(tokenMetadata, "refresh-secret-value") {
		t.Fatalf("token metadata leaked refreshed token material: %s", tokenMetadata)
	}
	if !strings.Contains(tokenMetadata, `"expires_at":"2026-05-31T23:00:00Z"`) {
		t.Fatalf("token metadata = %s, want refreshed expiry", tokenMetadata)
	}
}

func TestDoctorOnlineReportsRefreshFailureAsConnectionHealth(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T21:00:00Z")
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		wantRefreshToken:     "refresh-secret-value",
		refreshErr:           errors.New("OAuth token refresh failed with HTTP 400"),
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "refresh_failed")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "HTTP 400") {
		t.Fatalf("message = %T(%v), want refresh failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") || strings.Contains(stdout.String()+stderr.String(), "old-access-secret") {
		t.Fatalf("doctor --online output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestDoctorOnlineValidatesRefreshWhenAccessTokenIsCurrent(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "current-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 20, 30, 0, 0, time.UTC),
		wantRefreshToken:     "refresh-secret-value",
		refreshErr:           errors.New("OAuth token refresh failed with HTTP 400"),
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "refresh_failed")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "HTTP 400") {
		t.Fatalf("message = %T(%v), want refresh failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") || strings.Contains(stdout.String()+stderr.String(), "current-access-secret") {
		t.Fatalf("doctor --online output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestDoctorOnlineReportsProviderFailureAsConnectionHealth(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                     time.Date(2026, 5, 31, 20, 30, 0, 0, time.UTC),
		refreshedAccessToken:    "refreshed-access-secret",
		wantRefreshToken:        "connect-refresh-secret",
		wantProviderAccessToken: "refreshed-access-secret",
		providerErr:             errors.New("Google Health identity request failed with HTTP 503"),
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "provider_unreachable")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "HTTP 503") {
		t.Fatalf("message = %T(%v), want provider failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if strings.Contains(string(tokenStoreBytes), "refreshed-access-secret") || !strings.Contains(string(tokenStoreBytes), "connect-access-secret") {
		t.Fatal("token store should keep old token material after provider failure")
	}
}

func TestDoctorOnlineReportsMissingTokenAsConnectionHealth(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	store := fileCredentialStore{path: tokenStorePath}
	if err := store.Store("googlehealth:111111256096816351", map[string]any{"refresh_token": "connect-refresh-secret"}); err != nil {
		t.Fatalf("replace token material: %v", err)
	}
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		failRefreshIfCalled:  true,
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "token_missing")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "missing access token") {
		t.Fatalf("message = %T(%v), want missing access token", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorOnlineReportsMissingRefreshTokenBeforeProvider(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	store := fileCredentialStore{path: tokenStorePath}
	if err := store.Store("googlehealth:111111256096816351", map[string]any{"access_token": "connect-access-secret"}); err != nil {
		t.Fatalf("replace token material: %v", err)
	}
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 20, 30, 0, 0, time.UTC),
		failRefreshIfCalled:  true,
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "token_missing")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "missing refresh token") {
		t.Fatalf("message = %T(%v), want missing refresh token", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorOnlineDoesNotPersistRefreshBeforeIdentityMatch(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T21:00:00Z")
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                     time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		refreshedAccessToken:    "refreshed-access-secret",
		wantRefreshToken:        "refresh-secret-value",
		wantProviderAccessToken: "refreshed-access-secret",
		healthUserID:            "222222256096816351",
		legacyFitbitUserID:      "DIFFERENT",
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "identity_mismatch")
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if strings.Contains(string(tokenStoreBytes), "refreshed-access-secret") || !strings.Contains(string(tokenStoreBytes), "old-access-secret") {
		t.Fatal("token store should keep old token material after identity mismatch")
	}
	tokenMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if strings.Contains(tokenMetadata, `"expires_at":"2026-05-31T23:00:00Z"`) || !strings.Contains(tokenMetadata, `"expires_at":"2026-05-31T21:00:00Z"`) {
		t.Fatalf("token metadata should keep old expiry after identity mismatch: %s", tokenMetadata)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "old-access-secret") || strings.Contains(stdout.String()+stderr.String(), "refreshed-access-secret") || strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") {
		t.Fatalf("doctor --online output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestPersistDoctorOnlineRefreshedTokenRollsBackOnMetadataFailure(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	store := fileCredentialStore{path: tokenStorePath}
	previousTokenMaterial, err := store.Load("googlehealth:111111256096816351")
	if err != nil {
		t.Fatalf("load previous token material: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM connections WHERE id = ?`, "googlehealth:111111256096816351"); err != nil {
		_ = db.Close()
		t.Fatalf("delete connection: %v", err)
	}
	refreshedToken := oauthTokenResponse{
		accessToken:  "refreshed-access-secret",
		refreshToken: "refresh-secret-value",
		tokenType:    "Bearer",
		scopes:       []string{googleHealthActivityReadonlyScope},
		expiresAt:    time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC),
		rawTokenMaterialObject: map[string]any{
			"access_token":  "refreshed-access-secret",
			"refresh_token": "refresh-secret-value",
			"expires_in":    float64(3600),
			"scope":         googleHealthActivityReadonlyScope,
			"token_type":    "Bearer",
		},
	}
	err = persistDoctorOnlineRefreshedToken(db, credentialStoreConfig{kind: "file", path: tokenStorePath}, "googlehealth:111111256096816351", refreshedToken, previousTokenMaterial)
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err == nil {
		t.Fatal("persist refreshed token succeeded after connection was removed")
	}
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if strings.Contains(string(tokenStoreBytes), "refreshed-access-secret") || !strings.Contains(string(tokenStoreBytes), "old-access-secret") {
		t.Fatal("token store should roll back to previous token material")
	}
}

func TestDoctorDefaultDoesNotRefreshOrCallProvider(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T19:00:00Z")
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		failRefreshIfCalled:  true,
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONString(t, got, "token_status", "metadata_present")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestConnectStoresFileFallbackTokenAndAnchorsIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	connectNow := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	refreshExpiresAt := connectNow.Add(24 * time.Hour)
	installConnectFakes(t, fakeConnectConfig{
		now:                connectNow,
		accessToken:        "access-secret-value",
		refreshToken:       "refresh-secret-value",
		refreshExpiresAt:   &refreshExpiresAt,
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("connect exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connected")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "google_health_user_id", "111111256096816351")
	assertJSONString(t, got, "legacy_fitbit_user_id", "A1B2C3")
	assertJSONString(t, got, "credential_store", "file")
	assertJSONString(t, got, "token_status", "metadata_present")
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "access-secret-value") || strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") {
		t.Fatalf("connect output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}

	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	tokenStore := string(tokenStoreBytes)
	if !strings.Contains(tokenStore, "access-secret-value") || !strings.Contains(tokenStore, "refresh-secret-value") {
		t.Fatalf("token store missing token material: %s", tokenStore)
	}
	assertMode(t, tokenStorePath, 0o600)

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var connectionID, providerName, healthUserID, legacyUserID, tokenMetadata, identityJSON string
	if err := db.QueryRow(`SELECT id, provider_name, google_health_user_id, legacy_fitbit_user_id, token_metadata_json, google_identity_json FROM connections`).Scan(&connectionID, &providerName, &healthUserID, &legacyUserID, &tokenMetadata, &identityJSON); err != nil {
		t.Fatalf("query connection: %v", err)
	}
	if connectionID != "googlehealth:111111256096816351" || providerName != "googlehealth" || healthUserID != "111111256096816351" || legacyUserID != "A1B2C3" {
		t.Fatalf("connection = (%q, %q, %q, %q), want anchored identity", connectionID, providerName, healthUserID, legacyUserID)
	}
	if strings.Contains(tokenMetadata, "access-secret-value") || strings.Contains(tokenMetadata, "refresh-secret-value") || strings.Contains(tokenMetadata, "access_token") || strings.Contains(tokenMetadata, "refresh_token") {
		t.Fatalf("token metadata leaked token material: %s", tokenMetadata)
	}
	for _, want := range []string{"credential_store_key", "expires_at", "scopes"} {
		if !strings.Contains(tokenMetadata, want) {
			t.Fatalf("token metadata missing %q: %s", want, tokenMetadata)
		}
	}
	if strings.Contains(tokenMetadata, "refresh_token_expires_at") {
		t.Fatalf("token metadata uses rejected refresh token key: %s", tokenMetadata)
	}
	if !strings.Contains(tokenMetadata, "refresh_expires_at") {
		t.Fatalf("token metadata missing refresh expiry: %s", tokenMetadata)
	}
	if err := validateTokenMetadata(tokenMetadata); err != nil {
		t.Fatalf("token metadata does not validate: %v", err)
	}
	if !strings.Contains(identityJSON, `"healthUserId":"111111256096816351"`) {
		t.Fatalf("identity JSON not archived: %s", identityJSON)
	}
}

func TestConnectReauthorizesSameIdentityWithoutSecondConnection(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		accessToken:        "first-access-secret",
		refreshToken:       "first-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	code := runConnectCommand(t, configPath, archivePath)
	if code != 0 {
		t.Fatalf("first connect exit code = %d, want 0", code)
	}

	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC),
		accessToken:        "second-access-secret",
		refreshToken:       "second-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	code = runConnectCommand(t, configPath, archivePath)
	if code != 0 {
		t.Fatalf("second connect exit code = %d, want 0", code)
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM connections`).Scan(&count); err != nil {
		t.Fatalf("count connections: %v", err)
	}
	if count != 1 {
		t.Fatalf("connection count = %d, want 1", count)
	}
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	tokenStore := string(tokenStoreBytes)
	if !strings.Contains(tokenStore, "second-access-secret") || strings.Contains(tokenStore, "first-access-secret") {
		t.Fatalf("token store was not updated on reauth: %s", tokenStore)
	}
}

func TestConnectRejectsDifferentGoogleIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		accessToken:        "first-access-secret",
		refreshToken:       "first-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("first connect exit code = %d, want 0", code)
	}

	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC),
		accessToken:        "other-access-secret",
		refreshToken:       "other-refresh-secret",
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "D4E5F6",
	})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "different Google Identity") {
		t.Fatalf("message = %T(%v), want different identity failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if strings.Contains(string(tokenStoreBytes), "other-access-secret") {
		t.Fatalf("different identity token was stored: %s", string(tokenStoreBytes))
	}
}

func TestConnectDoesNotResolveSecretProviderAtRuntime(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--secret-provider", "1password",
		"--oauth-client-item", "Google Health OAuth",
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})
	stdout := new(bytes.Buffer)
	connectStderr := new(bytes.Buffer)
	code = run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, connectStderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "Secret Provider") {
		t.Fatalf("message = %T(%v), want Secret Provider runtime refusal", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+connectStderr.String())
}

func TestIdentityRefreshesArchivedGoogleIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installIdentityFetchFake(t, "connect-access-secret", googleIdentity{
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"111111256096816351","legacyUserId":"Z9Y8X7","refreshed":true}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("identity exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_refreshed")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "google_health_user_id", "111111256096816351")
	assertJSONString(t, got, "legacy_fitbit_user_id", "Z9Y8X7")
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") || strings.Contains(stdout.String()+stderr.String(), "connect-refresh-secret") {
		t.Fatalf("identity output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var legacyUserID, identityJSON string
	if err := db.QueryRow(`SELECT legacy_fitbit_user_id, google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&legacyUserID, &identityJSON); err != nil {
		t.Fatalf("query refreshed identity: %v", err)
	}
	if legacyUserID != "Z9Y8X7" {
		t.Fatalf("legacy_fitbit_user_id = %q, want refreshed value", legacyUserID)
	}
	if !strings.Contains(identityJSON, `"refreshed":true`) {
		t.Fatalf("google_identity_json = %s, want refreshed raw identity", identityJSON)
	}
}

func TestIdentityPlainIncludesStableIdentityFields(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installIdentityFetchFake(t, "connect-access-secret", googleIdentity{
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
		rawJSON:            `{"healthUserId":"111111256096816351","legacyUserId":"A1B2C3"}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("identity exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := "status: identity_refreshed\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ngoogle_health_user_id: 111111256096816351\nlegacy_fitbit_user_id: A1B2C3\nmessage: Google Identity refreshed\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestIdentityHumanOutputDistinguishesFailureStatuses(t *testing.T) {
	for _, test := range []struct {
		status string
		want   string
	}{
		{status: "identity_mismatch", want: "Google Identity mismatch\n"},
		{status: "identity_unavailable", want: "Google Identity unavailable\n"},
		{status: "identity_failed", want: "Google Identity failed\n"},
	} {
		t.Run(test.status, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			err := writeIdentityResult(identityResult{Status: test.status, Message: "message"}, outputMode{}, stdout)
			if err != nil {
				t.Fatalf("write identity result: %v", err)
			}
			if !strings.HasPrefix(stdout.String(), test.want) {
				t.Fatalf("stdout = %q, want prefix %q", stdout.String(), test.want)
			}
		})
	}
}

func TestIdentityRequiresArchivedConnection(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("identity exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_unavailable")
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "gohealthcli connect") {
		t.Fatalf("message = %T(%v), want connect guidance", got["message"], got["message"])
	}
	if _, ok := got["connection_id"]; ok {
		t.Fatalf("connection_id = %v, want omitted when no Connection exists", got["connection_id"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestIdentityReportsExpiredConnectionTokenBeforeProviderFetch(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		t.Fatalf("identity fetch should not be called for expired token")
		return googleIdentity{}, nil
	}
	currentTime = func() time.Time {
		return time.Date(2026, 5, 31, 23, 1, 0, 0, time.UTC)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("identity exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "expired") || !strings.Contains(message, "gohealthcli connect") {
		t.Fatalf("message = %T(%v), want expired-token reconnect guidance", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestIdentityRejectsDifferentGoogleIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installIdentityFetchFake(t, "connect-access-secret", googleIdentity{
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"222222222222222222","legacyUserId":"Z9Y8X7"}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("identity exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_mismatch")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "different Google Identity") {
		t.Fatalf("message = %T(%v), want different identity refusal", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var healthUserID, legacyUserID, identityJSON string
	if err := db.QueryRow(`SELECT google_health_user_id, legacy_fitbit_user_id, google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&healthUserID, &legacyUserID, &identityJSON); err != nil {
		t.Fatalf("query identity after mismatch: %v", err)
	}
	if healthUserID != "111111256096816351" || legacyUserID != "A1B2C3" {
		t.Fatalf("archived identity = (%q, %q), want unchanged", healthUserID, legacyUserID)
	}
	if strings.Contains(identityJSON, "222222222222222222") {
		t.Fatalf("different provider identity was archived: %s", identityJSON)
	}
}

func TestProfileArchivesSnapshotAndPrintsSummary(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{
		healthUserID: "111111256096816351",
		rawJSON:      `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}`,
	}, nil)
	currentTime = func() time.Time {
		return time.Date(2026, 6, 1, 10, 30, 0, 0, time.UTC)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("profile exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_archived")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "google_health_user_id", "111111256096816351")
	assertJSONString(t, got, "legacy_fitbit_user_id", "A1B2C3")
	assertJSONString(t, got, "fetched_at", "2026-06-01T10:30:00Z")
	snapshotID, ok := got["snapshot_id"].(float64)
	if !ok || snapshotID != 1 {
		t.Fatalf("snapshot_id = %T(%v), want 1", got["snapshot_id"], got["snapshot_id"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var providerName, connectionID, rawJSON, fetchedAt string
	if err := db.QueryRow(`SELECT provider_name, connection_id, raw_json, fetched_at FROM profile_snapshots WHERE id = ?`, 1).Scan(&providerName, &connectionID, &rawJSON, &fetchedAt); err != nil {
		t.Fatalf("query profile snapshot: %v", err)
	}
	if providerName != "googlehealth" || connectionID != "googlehealth:111111256096816351" {
		t.Fatalf("snapshot owner = (%q, %q), want archived Connection", providerName, connectionID)
	}
	if rawJSON != `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}` {
		t.Fatalf("raw_json = %s, want provider profile JSON", rawJSON)
	}
	if fetchedAt != "2026-06-01T10:30:00Z" {
		t.Fatalf("fetched_at = %q, want fixed timestamp", fetchedAt)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
}

func TestProfilePlainIncludesStableSnapshotFields(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{
		healthUserID: "111111256096816351",
		rawJSON:      `{"name":"users/111111256096816351/profile"}`,
	}, nil)
	currentTime = func() time.Time {
		return time.Date(2026, 6, 1, 10, 30, 0, 0, time.UTC)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("profile exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := "status: profile_archived\nsnapshot_id: 1\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ngoogle_health_user_id: 111111256096816351\nlegacy_fitbit_user_id: A1B2C3\nfetched_at: 2026-06-01T10:30:00Z\nmessage: Profile Snapshot archived\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestProfileProviderFailureDoesNotArchiveSnapshot(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{}, errors.New("Google Health profile request failed with HTTP 503"))

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_failed")
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "HTTP 503") {
		t.Fatalf("message = %T(%v), want provider status", got["message"], got["message"])
	}
	if _, ok := got["snapshot_id"]; ok {
		t.Fatalf("snapshot_id = %v, want omitted on failure", got["snapshot_id"])
	}
	assertArchiveTableCount(t, archivePath, "profile_snapshots", 0)
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") || strings.Contains(stdout.String()+stderr.String(), "connect-refresh-secret") {
		t.Fatalf("profile output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestProfileFailsBeforeProviderWhenProfileScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenScopes(t, archivePath, []string{googleHealthActivityReadonlyScope})
	originalFetchProfile := fetchProfile
	fetchProfile = func(accessToken string) (googleProfile, error) {
		t.Fatalf("profile fetch should not be called when profile scope is missing")
		return googleProfile{}, nil
	}
	t.Cleanup(func() { fetchProfile = originalFetchProfile })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, googleHealthProfileReadonlyScope) || !strings.Contains(message, "connect") {
		t.Fatalf("message = %T(%v), want profile-scope reconnect guidance", got["message"], got["message"])
	}
	assertArchiveTableCount(t, archivePath, "profile_snapshots", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestProfileRejectsAliasProfileWhenIdentityVerificationDiffers(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{
		rawJSON: `{"name":"users/me/profile","profile":{"unit":"metric"}}`,
	}, nil)
	installIdentityFetchFake(t, "connect-access-secret", googleIdentity{
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"222222222222222222","legacyUserId":"Z9Y8X7"}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_mismatch")
	if _, ok := got["snapshot_id"]; ok {
		t.Fatalf("snapshot_id = %v, want omitted on mismatch", got["snapshot_id"])
	}
	assertArchiveTableCount(t, archivePath, "profile_snapshots", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestProfileRejectsDifferentGoogleIdentityWithoutArchiving(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{
		healthUserID: "222222222222222222",
		rawJSON:      `{"name":"users/222222222222222222/profile"}`,
	}, nil)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_mismatch")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	if _, ok := got["snapshot_id"]; ok {
		t.Fatalf("snapshot_id = %v, want omitted on mismatch", got["snapshot_id"])
	}
	assertArchiveTableCount(t, archivePath, "profile_snapshots", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncRequiresFrom(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"sync", "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	if !strings.Contains(got["message"].(string), "--from") {
		t.Fatalf("message = %v, want --from hint", got["message"])
	}
	if _, ok := got["sync_run_id"]; ok {
		t.Fatalf("sync_run_id = %v, want omitted before setup", got["sync_run_id"])
	}
}

func TestSyncArchivesStepsIdempotentlyAndTracksRevisions(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	firstPage := `{
		"dataPoints": [{
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a",
			"dataSource": {"platform": "FITBIT", "device": {"manufacturer": "Google", "model": "Pixel Watch"}},
			"steps": {
				"interval": {
					"startTime": "2026-01-01T08:00:00+01:00",
					"startUtcOffset": "3600s",
					"endTime": "2026-01-01T08:15:00+01:00",
					"endUtcOffset": "3600s",
					"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}},
					"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 15}}
				},
				"count": "512"
			}
		}],
		"nextPageToken": "page-2"
	}`
	secondPage := `{
		"dataPoints": [{
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-b",
			"dataSource": {"platform": "FITBIT"},
			"steps": {
				"interval": {
					"startTime": "2026-01-01T09:00:00Z",
					"endTime": "2026-01-01T09:05:00Z"
				},
				"count": "200"
			}
		}]
	}`
	requests := installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"":       firstPage,
		"page-2": secondPage,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "from", "2026-01-01")
	assertJSONString(t, got, "to", "2026-01-02T00:00:00Z")
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 2)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if len(*requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(*requests))
	}
	if (*requests)[0].endpointName != "dataTypes.steps.list" || (*requests)[0].dataType != "steps" {
		t.Fatalf("request target = (%q, %q), want steps list", (*requests)[0].endpointName, (*requests)[0].dataType)
	}
	if strings.Contains((*requests)[0].url, "source") {
		t.Fatalf("sync URL unexpectedly includes source filtering: %s", (*requests)[0].url)
	}
	if pageToken := mustURLQuery(t, (*requests)[1].url).Get("pageToken"); pageToken != "page-2" {
		t.Fatalf("second pageToken = %q, want page-2", pageToken)
	}
	assertArchivedStepDataPoint(t, archivePath)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 1, "sync_completed", 2, 2, 0, "")

	requests = installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"":       firstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--plain",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("second sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	wantPlain := "status: sync_completed\nsync_run_id: 2\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ndata_types: steps\nfrom: 2026-01-01\nto: 2026-01-02T00:00:00Z\nendpoint_family: list\ndata_points_seen: 2\ndata_points_new: 0\ndata_points_updated: 0\nmessage: Sync Run archived steps Data Points\n"
	if stdout.String() != wantPlain {
		t.Fatalf("stdout = %q, want %q", stdout.String(), wantPlain)
	}
	if len(*requests) != 2 {
		t.Fatalf("second request count = %d, want 2", len(*requests))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 2, "sync_completed", 2, 0, 0, "")

	correctedFirstPage := strings.Replace(firstPage, `"count": "512"`, `"count": "999"`, 1)
	correctedFirstPage = strings.Replace(correctedFirstPage, `"startTime": "2026-01-01T08:00:00+01:00"`, `"startTime": "2026-01-01T08:01:00+01:00"`, 1)
	correctedFirstPage = strings.Replace(correctedFirstPage, `"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}}`, `"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 1}}`, 1)
	installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"":       correctedFirstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("corrected sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertSyncRun(t, archivePath, 3, "sync_completed", 2, 0, 1, "")
	assertCorrectedStepRevision(t, archivePath)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncProviderFailureRecordsFailedRun(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != "connect-access-secret" {
			t.Fatalf("sync access token = %q, want stored token", accessToken)
		}
		return nil, errors.New("Google Health raw request failed with HTTP 503")
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	message := got["message"].(string)
	if !strings.Contains(message, "HTTP 503") {
		t.Fatalf("message = %q, want provider status", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, "HTTP 503")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncFailsBeforeProviderWhenScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenScopes(t, archivePath, []string{googleHealthProfileReadonlyScope})
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run with missing scope")
		return nil, nil
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), googleHealthActivityReadonlyScope) || !strings.Contains(stdout.String(), "connect") {
		t.Fatalf("stdout = %q, want missing scope reconnect hint", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, googleHealthActivityReadonlyScope)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestRawEndpointIdentityPrintsProviderJSONWithoutArchiving(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	beforeIdentityJSON := archivedConnectionIdentityJSON(t, archivePath)
	installRawFetchFake(t, "connect-access-secret", func(request rawProviderRequest) []byte {
		if request.url != googleHealthIdentityURL {
			t.Fatalf("raw URL = %q, want identity URL", request.url)
		}
		return []byte(`{"healthUserId":"999999999999999999","legacyUserId":"RAW"}`)
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, stderr)
	if code != 0 {
		t.Fatalf("raw exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stdout.String() != `{"healthUserId":"999999999999999999","legacyUserId":"RAW"}` {
		t.Fatalf("stdout = %q, want raw provider JSON", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := archivedConnectionIdentityJSON(t, archivePath); got != beforeIdentityJSON {
		t.Fatalf("raw mutated archived identity JSON: %s", got)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") {
		t.Fatalf("raw output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestRawDataTypeStepsPrintsFixtureJSON(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	fixture := readTestFixture(t, "googlehealth_steps_list.json")
	installRawFetchFake(t, "connect-access-secret", func(request rawProviderRequest) []byte {
		if request.endpointName != "dataTypes.steps.list" || request.dataType != "steps" {
			t.Fatalf("raw request = (%q, %q), want steps list", request.endpointName, request.dataType)
		}
		parsedURL, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("parse raw URL: %v", err)
		}
		if parsedURL.Path != "/v4/users/me/dataTypes/steps/dataPoints" {
			t.Fatalf("raw path = %q, want steps dataPoints path", parsedURL.Path)
		}
		query := parsedURL.Query()
		wantFilter := `steps.interval.start_time >= "2026-01-01T00:00:00Z" AND steps.interval.start_time < "2026-01-02T00:00:00Z"`
		if query.Get("filter") != wantFilter {
			t.Fatalf("filter = %q, want %q", query.Get("filter"), wantFilter)
		}
		if query.Get("pageSize") != "12" || query.Get("pageToken") != "abc123" {
			t.Fatalf("pagination query = %v, want pageSize/pageToken", query)
		}
		return fixture
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"raw",
		"data-type", "steps",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--page-size", "12",
		"--page-token", "abc123",
		"--config", configPath,
		"--db", archivePath,
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("raw exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if !bytes.Equal(stdout.Bytes(), fixture) {
		t.Fatalf("stdout = %q, want fixture JSON", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestRawProviderErrorDoesNotLeakToken(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != "connect-access-secret" {
			t.Fatalf("raw access token = %q, want stored token", accessToken)
		}
		return nil, errors.New("Google Health raw request failed with HTTP 403")
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, stderr)
	if code != 1 {
		t.Fatalf("raw exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 403") {
		t.Fatalf("stderr = %q, want provider status", stderr.String())
	}
	if strings.Contains(stderr.String(), "connect-access-secret") || strings.Contains(stderr.String(), "connect-refresh-secret") {
		t.Fatalf("raw error leaked token material: %s", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestRawDataTypeFailsBeforeProviderWhenScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	var metadata map[string]any
	var metadataJSON string
	if err := db.QueryRow(`SELECT token_metadata_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&metadataJSON); err != nil {
		t.Fatalf("query token metadata: %v", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["scopes"] = []string{googleHealthActivityReadonlyScope}
	updatedMetadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(updatedMetadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token scopes: %v", err)
	}
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("raw provider fetch should not run with missing scope")
		return nil, nil
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"raw", "data-type", "heart-rate", "--from", "2026-01-01", "--config", configPath, "--db", archivePath}, stdout, stderr)
	if code != 1 {
		t.Fatalf("raw exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), googleHealthHealthMetricsReadonlyScope) || !strings.Contains(stderr.String(), "connect") {
		t.Fatalf("stderr = %q, want missing scope reconnect hint", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestBuildGoogleHealthRawRequestUsesProviderNamingConventions(t *testing.T) {
	request, err := buildGoogleHealthRawRequest([]string{"endpoint", "dataTypes.heart-rate.list"}, "2026-01-01", "", 0, "")
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse raw URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/heart-rate/dataPoints" {
		t.Fatalf("path = %q, want kebab-case Data Type path", parsedURL.Path)
	}
	wantFilter := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00Z"`
	if parsedURL.Query().Get("filter") != wantFilter {
		t.Fatalf("filter = %q, want snake-case filter", parsedURL.Query().Get("filter"))
	}
}

func TestBuildGoogleHealthRawRequestRejectsNonListableDataTypes(t *testing.T) {
	_, err := buildGoogleHealthRawRequest([]string{"data-type", "total-calories"}, "2026-01-01", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported Data Type")
	}
	if !strings.Contains(err.Error(), "not supported by dataPoints.list") {
		t.Fatalf("error = %v, want unsupported dataPoints.list", err)
	}
}

func TestGoogleHealthRawFilterFieldsCoverFirstReleaseDataTypes(t *testing.T) {
	for _, test := range []struct {
		dataType string
		from     string
		want     string
	}{
		{
			dataType: "steps",
			from:     "2026-01-01",
			want:     `steps.interval.start_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "oxygen-saturation",
			from:     "2026-01-01",
			want:     `oxygen_saturation.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "daily-resting-heart-rate",
			from:     "2026-01-01",
			want:     `daily_resting_heart_rate.date >= "2026-01-01"`,
		},
		{
			dataType: "exercise",
			from:     "2026-01-01",
			want:     `exercise.interval.civil_start_time >= "2026-01-01"`,
		},
		{
			dataType: "sleep",
			from:     "2026-01-01",
			want:     `sleep.interval.end_time >= "2026-01-01T00:00:00Z"`,
		},
	} {
		t.Run(test.dataType, func(t *testing.T) {
			filter, err := googleHealthDataTypeListFilter(test.dataType, test.from, "")
			if err != nil {
				t.Fatalf("filter: %v", err)
			}
			if filter != test.want {
				t.Fatalf("filter = %q, want %q", filter, test.want)
			}
		})
	}
}

func TestGoogleHealthRawFilterPreservesFractionalRFC3339Bounds(t *testing.T) {
	filter, err := googleHealthDataTypeListFilter("heart-rate", "2026-01-01T00:00:00.500Z", "2026-01-01T01:02:03.123456789+02:00")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	want := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00.5Z" AND heart_rate.sample_time.physical_time < "2025-12-31T23:02:03.123456789Z"`
	if filter != want {
		t.Fatalf("filter = %q, want %q", filter, want)
	}
}

func TestConnectAcceptsGlobalNoInput(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	wantNoInput := true
	installConnectFakes(t, fakeConnectConfig{wantNoInput: &wantNoInput})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"--no-input", "connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("connect exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
}

func TestConnectMigratesLegacyV1ArchiveBeforeStoringIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV1Archive(t, archivePath)
	installConnectFakes(t, fakeConnectConfig{})

	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if userVersion != 2 {
		t.Fatalf("user_version = %d, want 2", userVersion)
	}
	var identityJSON string
	if err := db.QueryRow(`SELECT google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&identityJSON); err != nil {
		t.Fatalf("query migrated connection: %v", err)
	}
	if !strings.Contains(identityJSON, `"healthUserId":"111111256096816351"`) {
		t.Fatalf("identity JSON = %s, want archived identity", identityJSON)
	}
}

func TestDoctorMigratesLegacyV1ArchiveBeforeValidation(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV1Archive(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["schema_version"] != float64(2) {
		t.Fatalf("schema_version = %v, want 2", got["schema_version"])
	}
	assertArchiveUserVersion(t, archivePath, 2)
}

func TestInitMigratesLegacyV1ArchiveBeforeValidation(t *testing.T) {
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
	if got["schema_version"] != float64(2) {
		t.Fatalf("schema_version = %v, want 2", got["schema_version"])
	}
	assertArchiveUserVersion(t, archivePath, 2)
}

func TestConnectRejectsUnsupportedOSNativeStoreBeforeOAuth(t *testing.T) {
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
	config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"os_native\"\nservice = \"gohealthcli\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	originalOS := currentOS
	currentOS = "plan9"
	t.Cleanup(func() { currentOS = originalOS })
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	connectStderr := new(bytes.Buffer)
	code = run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, connectStderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "OS-native Credential Store") {
		t.Fatalf("message = %T(%v), want OS-native preflight failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+connectStderr.String())
}

func TestConnectRejectsFileCredentialStoreCollisionsBeforeOAuth(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(tempDir, configPath, archivePath string) string
	}{
		{
			name: "config",
			path: func(_ string, configPath, _ string) string {
				return configPath
			},
		},
		{
			name: "archive",
			path: func(_ string, _, archivePath string) string {
				return archivePath
			},
		},
		{
			name: "oauth-client",
			path: func(tempDir, _, _ string) string {
				return filepath.Join(tempDir, "client_secret.json")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempDir := t.TempDir()
			if err := os.Chmod(tempDir, 0o700); err != nil {
				t.Fatalf("chmod temp dir: %v", err)
			}
			configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
			collisionPath := test.path(tempDir, configPath, archivePath)
			originalContent, err := os.ReadFile(collisionPath)
			if err != nil {
				t.Fatalf("read protected file: %v", err)
			}
			configBytes, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"file\"\npath = \"" + collisionPath + "\"\n"
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if collisionPath == configPath {
				originalContent = []byte(config)
			}
			installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
			if code != 1 {
				t.Fatalf("connect exit code = %d, want 1", code)
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			message, ok := got["message"].(string)
			if !ok || !strings.Contains(message, "must not match") {
				t.Fatalf("message = %T(%v), want credential collision rejection", got["message"], got["message"])
			}
			afterContent, err := os.ReadFile(collisionPath)
			if err != nil {
				t.Fatalf("read protected file after connect: %v", err)
			}
			if !bytes.Equal(afterContent, originalContent) {
				t.Fatalf("protected file was modified")
			}
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
	}
}

func TestConnectRejectsMissingLinuxCredentialHelperBeforeOAuth(t *testing.T) {
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
	config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"os_native\"\nservice = \"gohealthcli\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	originalOS := currentOS
	originalFindExecutable := findExecutable
	currentOS = "linux"
	findExecutable = func(name string) (string, error) {
		if name != "secret-tool" {
			t.Fatalf("find executable = %q, want secret-tool", name)
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() {
		currentOS = originalOS
		findExecutable = originalFindExecutable
	})
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	connectStderr := new(bytes.Buffer)
	code = run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, connectStderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "secret-tool") || !strings.Contains(message, "type \"file\"") {
		t.Fatalf("message = %T(%v), want secret-tool file fallback guidance", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+connectStderr.String())
}

func TestConnectRejectsWebOAuthClient(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	clientPath := filepath.Join(tempDir, "client_secret.json")
	content := []byte(`{"web":{"client_id":"test-client","client_secret":"test-secret","redirect_uris":["http://127.0.0.1:8080/oauth2callback"]}}`)
	if err := os.WriteFile(clientPath, content, 0o600); err != nil {
		t.Fatalf("write web OAuth client file: %v", err)
	}
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "installed desktop client") {
		t.Fatalf("message = %T(%v), want web client rejection", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestOAuthScopesUseRecognizedGoogleHealthScopes(t *testing.T) {
	scopes := oauthScopesForDataTypes(defaultDataTypes)
	wantScopes := []string{
		googleHealthActivityReadonlyScope,
		googleHealthHealthMetricsReadonlyScope,
		googleHealthSleepReadonlyScope,
		googleHealthProfileReadonlyScope,
	}
	if !slices.Equal(scopes, wantScopes) {
		t.Fatalf("scopes = %v, want configured Google Health readonly scopes %v", scopes, wantScopes)
	}
	for _, scope := range scopes {
		for _, invalid := range []string{"settings.readonly"} {
			if strings.Contains(scope, invalid) {
				t.Fatalf("scopes include unrecognized Google Health scope %q: %v", invalid, scopes)
			}
		}
	}
}

func TestListenForOAuthRedirectPreservesEmptyLoopbackPath(t *testing.T) {
	listener, redirectURI, err := listenForOAuthRedirect([]string{"http://localhost"})
	if err != nil {
		t.Fatalf("listen for OAuth redirect: %v", err)
	}
	defer listener.Close()

	parsed, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect URI: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" {
		t.Fatalf("redirect URI = %s, want dynamic loopback with empty path", redirectURI)
	}
}

func TestParseOAuthTokenResponseRequiresRefreshToken(t *testing.T) {
	_, err := parseOAuthTokenResponse([]byte(`{
		"access_token": "access-secret-value",
		"expires_in": 3600,
		"scope": "https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly",
		"token_type": "Bearer"
	}`), time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "refresh token") {
		t.Fatalf("parse token response error = %v, want missing refresh token", err)
	}
}

func TestFetchGoogleIdentityUsesGetIdentityEndpoint(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })

	var gotURL string
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotURL = request.URL.String()
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"healthUserId":"111111256096816351","legacyUserId":"A1B2C3"}`)),
		}, nil
	})}

	identity, err := fetchGoogleIdentity("access-secret-value")
	if err != nil {
		t.Fatalf("fetch identity: %v", err)
	}
	if gotURL != googleHealthIdentityURL {
		t.Fatalf("identity URL = %q, want %q", gotURL, googleHealthIdentityURL)
	}
	if identity.healthUserID != "111111256096816351" || identity.legacyFitbitUserID != "A1B2C3" {
		t.Fatalf("identity = (%q, %q), want response identity", identity.healthUserID, identity.legacyFitbitUserID)
	}
}

func TestFetchGoogleProfileUsesProfileEndpoint(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })

	var gotURL string
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotURL = request.URL.String()
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"name":"users/111111256096816351/profile","userConfiguredWalkingStrideLengthMm":720}`)),
		}, nil
	})}

	profile, err := fetchGoogleProfile("access-secret-value")
	if err != nil {
		t.Fatalf("fetch profile: %v", err)
	}
	if gotURL != googleHealthProfileURL {
		t.Fatalf("profile URL = %q, want %q", gotURL, googleHealthProfileURL)
	}
	if profile.healthUserID != "111111256096816351" || profile.resourceName != "users/111111256096816351/profile" {
		t.Fatalf("profile = (%q, %q), want response profile", profile.healthUserID, profile.resourceName)
	}
	if !strings.Contains(profile.rawJSON, "userConfiguredWalkingStrideLengthMm") {
		t.Fatalf("profile raw JSON = %s, want profile payload", profile.rawJSON)
	}
}

func TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })

	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != googleHealthIdentityURL {
			t.Fatalf("raw URL = %q, want identity URL", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"access-secret-value rejected"}`)),
		}, nil
	})}

	_, err := fetchGoogleHealthRaw(rawProviderRequest{endpointName: "getIdentity", url: googleHealthIdentityURL}, "access-secret-value")
	if err == nil {
		t.Fatal("fetch raw error = nil, want HTTP failure")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("fetch raw error = %v, want status", err)
	}
	if strings.Contains(err.Error(), "access-secret-value") {
		t.Fatalf("fetch raw error leaked token/body: %v", err)
	}
}

func TestReadLimitedBodyReportsOversize(t *testing.T) {
	body, tooLarge, err := readLimitedBody(strings.NewReader("abcdef"), 5)
	if err != nil {
		t.Fatalf("read limited body: %v", err)
	}
	if !tooLarge {
		t.Fatal("tooLarge = false, want true")
	}
	if body != nil {
		t.Fatalf("body = %q, want nil when oversized", string(body))
	}
}

func TestOSNativeCredentialStoreDoesNotSendTokenAsArgument(t *testing.T) {
	originalOS := currentOS
	originalSecurityCommand := runSecurityAddGenericPassword
	currentOS = "darwin"
	t.Cleanup(func() {
		currentOS = originalOS
		runSecurityAddGenericPassword = originalSecurityCommand
	})

	var gotService string
	var gotKey string
	var gotContent []byte
	runSecurityAddGenericPassword = func(service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStore(credentialStoreConfig{kind: "os_native", service: "gohealthcli"})
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("security command target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("security command content missing token material: %s", string(gotContent))
	}
}

func TestSecurityCredentialStoreFeedsPromptWithoutTokenArgument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake security executable uses POSIX shell")
	}

	tempDir := t.TempDir()
	argvPath := filepath.Join(tempDir, "argv.txt")
	stdinPath := filepath.Join(tempDir, "stdin.txt")
	securityPath := filepath.Join(tempDir, "security")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GOHEALTHCLI_TEST_SECURITY_ARGV\"\ncat > \"$GOHEALTHCLI_TEST_SECURITY_STDIN\"\n"
	if err := os.WriteFile(securityPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake security: %v", err)
	}
	t.Setenv("GOHEALTHCLI_TEST_SECURITY_ARGV", argvPath)
	t.Setenv("GOHEALTHCLI_TEST_SECURITY_STDIN", stdinPath)
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	content := []byte(`{"access_token":"access-secret-value"}`)
	if err := runSecurityAddGenericPasswordCommand("gohealthcli", "googlehealth:111", content); err != nil {
		t.Fatalf("security command: %v", err)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if bytes.Contains(argv, []byte("access-secret-value")) {
		t.Fatalf("security argv contains token material: %s", string(argv))
	}
	if !bytes.Contains(argv, []byte("-w")) {
		t.Fatalf("security argv missing prompt flag: %s", string(argv))
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	wantStdin := string(content) + "\n" + string(content) + "\n"
	if string(stdin) != wantStdin {
		t.Fatalf("security stdin = %q, want password and confirmation", string(stdin))
	}
}

func TestLinuxOSNativeCredentialStoreUsesSecretToolContent(t *testing.T) {
	originalOS := currentOS
	originalSecretToolStore := runSecretToolStore
	currentOS = "linux"
	t.Cleanup(func() {
		currentOS = originalOS
		runSecretToolStore = originalSecretToolStore
	})

	var gotService string
	var gotKey string
	var gotContent []byte
	runSecretToolStore = func(service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStore(credentialStoreConfig{kind: "os_native", service: "gohealthcli"})
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("secret-tool target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("secret-tool content missing token material: %s", string(gotContent))
	}
}

func TestWindowsOSNativeCredentialStoreUsesCredentialManagerContent(t *testing.T) {
	originalOS := currentOS
	originalWindowsCredentialWrite := runWindowsCredentialWrite
	currentOS = "windows"
	t.Cleanup(func() {
		currentOS = originalOS
		runWindowsCredentialWrite = originalWindowsCredentialWrite
	})

	var gotService string
	var gotKey string
	var gotContent []byte
	runWindowsCredentialWrite = func(service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStore(credentialStoreConfig{kind: "os_native", service: "gohealthcli"})
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("Windows Credential Manager target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("Windows Credential Manager content missing token material: %s", string(gotContent))
	}
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
		"schema_version: 2\n",
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

func TestInitRejectsInvalidOAuthClientFileBeforeCreatingSetup(t *testing.T) {
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
		defaultConfigPath(),
		defaultArchivePath(),
		outputMode{},
		stdout,
		stderr,
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

func runCommandInDir(t *testing.T, dir string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runCommandInDirWithEnv(t, dir, nil, args...)
}

func runCommandWithEnv(t *testing.T, env []string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runCommandInDirWithEnv(t, "", env, args...)
}

func runCommandInDirWithEnv(t *testing.T, dir string, env []string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	ensureTestOAuthClientFiles(t, dir, args)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd := exec.Command(testBinaryPath, args...)
	cmd.Dir = dir
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

type fakeConnectConfig struct {
	now                time.Time
	accessToken        string
	refreshToken       string
	refreshExpiresAt   *time.Time
	healthUserID       string
	legacyFitbitUserID string
	wantNoInput        *bool
	failIfCalled       bool
}

type fakeDoctorOnlineConfig struct {
	now                     time.Time
	refreshedAccessToken    string
	wantRefreshToken        string
	wantProviderAccessToken string
	healthUserID            string
	legacyFitbitUserID      string
	refreshErr              error
	providerErr             error
	failRefreshIfCalled     bool
	failProviderIfCalled    bool
}

func installConnectFakes(t *testing.T, config fakeConnectConfig) {
	t.Helper()

	originalOAuthFlow := runOAuthFlow
	originalFetchIdentity := fetchIdentity
	originalCurrentTime := currentTime
	if config.now.IsZero() {
		config.now = time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	}
	if config.accessToken == "" {
		config.accessToken = "access-secret-value"
	}
	if config.refreshToken == "" {
		config.refreshToken = "refresh-secret-value"
	}
	if config.healthUserID == "" {
		config.healthUserID = "111111256096816351"
	}
	runOAuthFlow = func(client oauthClientConfig, scopes []string, noInput bool) (oauthTokenResponse, error) {
		if config.failIfCalled {
			t.Fatalf("OAuth flow should not be called")
		}
		if client.clientID != "test-client" || client.clientSecret != "test-secret" {
			t.Fatalf("OAuth client = (%q, %q), want test client", client.clientID, client.clientSecret)
		}
		if len(scopes) == 0 {
			t.Fatal("OAuth scopes empty")
		}
		if config.wantNoInput != nil && noInput != *config.wantNoInput {
			t.Fatalf("noInput = %v, want %v", noInput, *config.wantNoInput)
		}
		return oauthTokenResponse{
			accessToken:           config.accessToken,
			refreshToken:          config.refreshToken,
			tokenType:             "Bearer",
			scopes:                scopes,
			expiresAt:             config.now.Add(time.Hour),
			refreshTokenExpiresAt: config.refreshExpiresAt,
			rawTokenMaterialObject: map[string]any{
				"access_token":  config.accessToken,
				"refresh_token": config.refreshToken,
				"expires_in":    float64(3600),
				"scope":         strings.Join(scopes, " "),
				"token_type":    "Bearer",
			},
		}, nil
	}
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if config.failIfCalled {
			t.Fatalf("identity fetch should not be called")
		}
		if accessToken != config.accessToken {
			t.Fatalf("identity access token = %q, want fake token", accessToken)
		}
		return googleIdentity{
			healthUserID:       config.healthUserID,
			legacyFitbitUserID: config.legacyFitbitUserID,
			rawJSON:            fmt.Sprintf(`{"healthUserId":%q,"legacyUserId":%q}`, config.healthUserID, config.legacyFitbitUserID),
		}, nil
	}
	currentTime = func() time.Time { return config.now }
	t.Cleanup(func() {
		runOAuthFlow = originalOAuthFlow
		fetchIdentity = originalFetchIdentity
		currentTime = originalCurrentTime
	})
}

func installDoctorOnlineFakes(t *testing.T, config fakeDoctorOnlineConfig) {
	t.Helper()

	originalRefreshOAuthToken := refreshOAuthToken
	originalFetchIdentity := fetchIdentity
	originalCurrentTime := currentTime
	if config.now.IsZero() {
		config.now = time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	}
	if config.refreshedAccessToken == "" {
		config.refreshedAccessToken = "refreshed-access-secret"
	}
	if config.wantRefreshToken == "" {
		config.wantRefreshToken = "refresh-secret-value"
	}
	if config.wantProviderAccessToken == "" {
		config.wantProviderAccessToken = config.refreshedAccessToken
	}
	if config.healthUserID == "" {
		config.healthUserID = "111111256096816351"
	}
	refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		if config.failRefreshIfCalled {
			t.Fatal("token refresh should not be called")
		}
		if refreshToken != config.wantRefreshToken {
			t.Fatalf("refresh token = %q, want configured refresh token", refreshToken)
		}
		if len(fallbackScopes) == 0 {
			t.Fatal("fallback scopes empty")
		}
		if config.refreshErr != nil {
			return oauthTokenResponse{}, config.refreshErr
		}
		return oauthTokenResponse{
			accessToken:  config.refreshedAccessToken,
			refreshToken: refreshToken,
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    config.now.Add(time.Hour),
			rawTokenMaterialObject: map[string]any{
				"access_token":  config.refreshedAccessToken,
				"refresh_token": refreshToken,
				"expires_in":    float64(3600),
				"scope":         strings.Join(fallbackScopes, " "),
				"token_type":    "Bearer",
			},
		}, nil
	}
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if config.failProviderIfCalled {
			t.Fatal("provider reachability check should not be called")
		}
		if accessToken != config.wantProviderAccessToken {
			t.Fatalf("provider access token = %q, want configured token", accessToken)
		}
		if config.providerErr != nil {
			return googleIdentity{}, config.providerErr
		}
		return googleIdentity{
			healthUserID:       config.healthUserID,
			legacyFitbitUserID: config.legacyFitbitUserID,
			rawJSON:            fmt.Sprintf(`{"healthUserId":%q,"legacyUserId":%q}`, config.healthUserID, config.legacyFitbitUserID),
		}, nil
	}
	currentTime = func() time.Time { return config.now }
	t.Cleanup(func() {
		refreshOAuthToken = originalRefreshOAuthToken
		fetchIdentity = originalFetchIdentity
		currentTime = originalCurrentTime
	})
}

func installIdentityFetchFake(t *testing.T, wantAccessToken string, identity googleIdentity) {
	t.Helper()

	originalFetchIdentity := fetchIdentity
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("identity access token = %q, want stored token", accessToken)
		}
		return identity, nil
	}
	t.Cleanup(func() {
		fetchIdentity = originalFetchIdentity
	})
}

func installProfileFetchFake(t *testing.T, wantAccessToken string, profile googleProfile, providerErr error) {
	t.Helper()

	originalFetchProfile := fetchProfile
	fetchProfile = func(accessToken string) (googleProfile, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("profile access token = %q, want stored token", accessToken)
		}
		if providerErr != nil {
			return googleProfile{}, providerErr
		}
		return profile, nil
	}
	t.Cleanup(func() {
		fetchProfile = originalFetchProfile
	})
}

func installRawFetchFake(t *testing.T, wantAccessToken string, response func(rawProviderRequest) []byte) {
	t.Helper()

	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("raw access token = %q, want stored token", accessToken)
		}
		return response(request), nil
	}
	t.Cleanup(func() {
		fetchRawProvider = originalFetchRawProvider
	})
}

func installStepSyncFetchFake(t *testing.T, wantAccessToken string, pages map[string]string) *[]rawProviderRequest {
	t.Helper()

	originalFetchRawProvider := fetchRawProvider
	var requests []rawProviderRequest
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("sync access token = %q, want stored token", accessToken)
		}
		if request.endpointName != "dataTypes.steps.list" || request.dataType != "steps" {
			t.Fatalf("sync request = (%q, %q), want steps list", request.endpointName, request.dataType)
		}
		requests = append(requests, request)
		pageToken := mustURLQuery(t, request.url).Get("pageToken")
		body, ok := pages[pageToken]
		if !ok {
			t.Fatalf("no fake page for pageToken %q", pageToken)
		}
		return []byte(body), nil
	}
	t.Cleanup(func() {
		fetchRawProvider = originalFetchRawProvider
	})
	return &requests
}

func initializeFileCredentialSetup(t *testing.T, tempDir string) (string, string, string) {
	t.Helper()

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
	tokenStorePath := filepath.Join(tempDir, "credential-store", "tokens.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"file\"\npath = \"" + tokenStorePath + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath, archivePath, tokenStorePath
}

func readTestFixture(t *testing.T, name string) []byte {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return content
}

func archivedConnectionIdentityJSON(t *testing.T, archivePath string) string {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var identityJSON string
	if err := db.QueryRow(`SELECT google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&identityJSON); err != nil {
		t.Fatalf("query archived identity JSON: %v", err)
	}
	return identityJSON
}

func archivedConnectionTokenMetadata(t *testing.T, archivePath string) string {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var tokenMetadata string
	if err := db.QueryRow(`SELECT token_metadata_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&tokenMetadata); err != nil {
		t.Fatalf("query archived token metadata: %v", err)
	}
	return tokenMetadata
}

func setConnectionTokenExpiry(t *testing.T, archivePath, expiresAt string) {
	t.Helper()

	var metadata map[string]any
	if err := json.Unmarshal([]byte(archivedConnectionTokenMetadata(t, archivePath)), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["expires_at"] = expiresAt
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(metadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
}

func setConnectionTokenScopes(t *testing.T, archivePath string, scopes []string) {
	t.Helper()

	var metadata map[string]any
	if err := json.Unmarshal([]byte(archivedConnectionTokenMetadata(t, archivePath)), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["scopes"] = scopes
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(metadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
}

func assertArchiveTableCount(t *testing.T, archivePath, table string, want int) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func createLegacyV1Archive(t *testing.T, archivePath string) {
	t.Helper()

	if err := ensureOwnerOnlyDir(filepath.Dir(archivePath)); err != nil {
		t.Fatalf("create archive parent: %v", err)
	}
	file, err := os.OpenFile(archivePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create legacy archive file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close legacy archive file: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open legacy archive: %v", err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin legacy migration: %v", err)
	}
	for _, statement := range initialMigrationStatements() {
		if _, err := tx.Exec(statement); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply legacy migration statement: %v", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, 'initial_archive_schema', ?)`, time.Date(2026, 5, 31, 21, 0, 0, 0, time.UTC).Format(time.RFC3339)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("record legacy migration: %v", err)
	}
	if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("set legacy user_version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit legacy migration: %v", err)
	}
	if usesPOSIXPermissions() {
		if err := os.Chmod(archivePath, 0o600); err != nil {
			t.Fatalf("chmod legacy archive: %v", err)
		}
	}
}

func runConnectCommand(t *testing.T, configPath, archivePath string) int {
	t.Helper()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if stdout.String() != "" {
		var got map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	return code
}

func ensureTestOAuthClientFiles(t *testing.T, dir string, args []string) {
	t.Helper()

	for index, arg := range args {
		if arg != "--oauth-client-file" || index+1 >= len(args) {
			continue
		}
		path := args[index+1]
		if path == "" {
			continue
		}
		if dir != "" && !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
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

func expectedDefaultCredentialStoreKind() string {
	return "os_native"
}

func removeCredentialStoreSection(t *testing.T, config string) string {
	t.Helper()

	start := strings.Index(config, "\n[credential_store]\n")
	if start < 0 {
		t.Fatalf("config missing credential_store section:\n%s", config)
	}
	searchFrom := start + len("\n[credential_store]\n")
	end := strings.Index(config[searchFrom:], "\n[")
	if end < 0 {
		return strings.TrimRight(config[:start], "\n") + "\n"
	}
	end += searchFrom
	return strings.TrimRight(config[:start], "\n") + "\n" + config[end+1:]
}

func assertArchiveUserVersion(t *testing.T, archivePath string, want int) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
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

func assertJSONNumber(t *testing.T, got map[string]any, key string, want float64) {
	t.Helper()

	value, ok := got[key].(float64)
	if !ok {
		t.Fatalf("%s = %T(%v), want number %v", key, got[key], got[key], want)
	}
	if value != want {
		t.Fatalf("%s = %v, want %v", key, value, want)
	}
}

func mustURLQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL %q: %v", rawURL, err)
	}
	return parsed.Query()
}

func assertArchivedStepDataPoint(t *testing.T, archivePath string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var dataType, resourceName, recordKind, startUTC, endUTC, startCivil, endCivil, civilDate, timezoneMetadata, dataSourceJSON, rawJSON string
	if err := db.QueryRow(`SELECT
		data_type,
		upstream_resource_name,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a").Scan(
		&dataType,
		&resourceName,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived step Data Point: %v", err)
	}
	if dataType != "steps" || resourceName != "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a" || recordKind != "interval" {
		t.Fatalf("Data Point identity = (%q, %q, %q), want steps interval resource", dataType, resourceName, recordKind)
	}
	if startUTC != "2026-01-01T07:00:00Z" || endUTC != "2026-01-01T07:15:00Z" {
		t.Fatalf("physical time = (%q, %q), want UTC interval", startUTC, endUTC)
	}
	if startCivil != "2026-01-01T08:00:00" || endCivil != "2026-01-01T08:15:00" || civilDate != "2026-01-01" {
		t.Fatalf("civil time = (%q, %q, %q), want provider civil time", startCivil, endCivil, civilDate)
	}
	if timezoneMetadata != `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}` {
		t.Fatalf("timezone_metadata = %q, want offsets", timezoneMetadata)
	}
	if dataSourceJSON != `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}` {
		t.Fatalf("data_source_json = %q, want compact Data Source", dataSourceJSON)
	}
	if !strings.Contains(rawJSON, `"count":"512"`) {
		t.Fatalf("raw_json = %s, want original steps count", rawJSON)
	}
}

func assertSyncRun(t *testing.T, archivePath string, id int64, wantStatus string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var status, dataTypesJSON, rangeJSON, endpointFamily string
	var seen, newCount, updated int
	var errorSummary sql.NullString
	if err := db.QueryRow(`SELECT
		status,
		data_types_requested,
		range_requested_json,
		endpoint_family,
		seen_count,
		new_count,
		updated_count,
		error_summary
	FROM sync_runs WHERE id = ?`, id).Scan(
		&status,
		&dataTypesJSON,
		&rangeJSON,
		&endpointFamily,
		&seen,
		&newCount,
		&updated,
		&errorSummary,
	); err != nil {
		t.Fatalf("query Sync Run %d: %v", id, err)
	}
	if status != wantStatus || endpointFamily != "list" {
		t.Fatalf("Sync Run status/family = (%q, %q), want (%q, list)", status, endpointFamily, wantStatus)
	}
	if dataTypesJSON != `["steps"]` {
		t.Fatalf("data_types_requested = %q, want steps", dataTypesJSON)
	}
	if !strings.Contains(rangeJSON, `"from":"2026-01-01"`) {
		t.Fatalf("range_requested_json = %q, want from", rangeJSON)
	}
	if seen != wantSeen || newCount != wantNew || updated != wantUpdated {
		t.Fatalf("Sync Run counts = (%d, %d, %d), want (%d, %d, %d)", seen, newCount, updated, wantSeen, wantNew, wantUpdated)
	}
	if wantErrorContains == "" {
		if errorSummary.Valid {
			t.Fatalf("error_summary = %q, want NULL", errorSummary.String)
		}
		return
	}
	if !errorSummary.Valid || !strings.Contains(errorSummary.String, wantErrorContains) {
		t.Fatalf("error_summary = %v(%q), want %q", errorSummary.Valid, errorSummary.String, wantErrorContains)
	}
}

func assertCorrectedStepRevision(t *testing.T, archivePath string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var rawJSON, startUTC, startCivil string
	if err := db.QueryRow(`SELECT raw_json, start_time_utc, start_civil_time FROM data_points WHERE upstream_resource_name = ?`, "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a").Scan(&rawJSON, &startUTC, &startCivil); err != nil {
		t.Fatalf("query corrected Data Point: %v", err)
	}
	if !strings.Contains(rawJSON, `"count":"999"`) {
		t.Fatalf("canonical raw_json = %s, want corrected count", rawJSON)
	}
	if startUTC != "2026-01-01T07:01:00Z" || startCivil != "2026-01-01T08:01:00" {
		t.Fatalf("corrected time = (%q, %q), want updated metadata", startUTC, startCivil)
	}
	var previousRawJSON, reason string
	if err := db.QueryRow(`SELECT previous_raw_json, replacement_reason FROM data_point_revisions`).Scan(&previousRawJSON, &reason); err != nil {
		t.Fatalf("query Data Point Revision: %v", err)
	}
	if !strings.Contains(previousRawJSON, `"count":"512"`) || reason != "provider_correction" {
		t.Fatalf("revision = (%s, %q), want previous count and reason", previousRawJSON, reason)
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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestDoctorJSONReportsMissingSetup(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--no-input",
		"doctor",
		"--plain",
	)

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
	t.Parallel()
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"doctor",
		"--no-input",
		"--plain",
	)

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
	t.Parallel()
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
	t.Parallel()
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

func TestDoctorDefaultPathsAreUsable(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	xdgConfig := filepath.Join(home, "xdg-config")
	xdgData := filepath.Join(home, "xdg-data")

	code, stdout, stderr := runBinaryWithEnv(t,
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

func TestDoctorReportsInitializedSetup(t *testing.T) {
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
	if got["schema_version"] != float64(currentSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", got["schema_version"], currentSchemaVersion)
	}
	if got["connection_count"] != float64(0) {
		t.Fatalf("connection_count = %v, want 0", got["connection_count"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorPlainReportsOfflineHealthCheck(t *testing.T) {
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

	want := fmt.Sprintf("status: ok\nconfig_path: %s\narchive_path: %s\noauth_client_source: file\ncredential_store: %s\nschema_version: %d\nconnection_count: 0\ntoken_status: not_connected\nattachment_root_path: %s.attachments\nattachment_root_mode: 0700\nmessage: local gohealthcli setup is initialized\n", configPath, archivePath, expectedDefaultCredentialStoreKind(), currentSchemaVersion, archivePath)
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorJSONReportsInvalidSetup(t *testing.T) {
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
	t.Parallel()
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
	t.Parallel()
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

	code, _, stderr := runBinaryInDir(t,
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

	code, stdout, stderr := runBinaryInDir(t,
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(googlehealth.DefaultDataTypes(), "\",\n  \"") + "\",\n]"
	inlineDataTypes := "default_data_types = [\"" + strings.Join(googlehealth.DefaultDataTypes(), "\", \"") + "\"]"
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(googlehealth.DefaultDataTypes(), "\",\n  \"") + "\",\n]"
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(googlehealth.DefaultDataTypes(), "\",\n  \"") + "\",\n]"
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(googlehealth.DefaultDataTypes(), "\",\n  \"") + "\",\n]"
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(googlehealth.DefaultDataTypes(), "\",\n  \"") + "\",\n]"
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(googlehealth.DefaultDataTypes(), "\",\n  \"") + "\",\n]\n\n"
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
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
	_, err = db.ExecContext(context.Background(), `UPDATE connections SET token_metadata_json = ? WHERE id = ?`,
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
	_, err = db.ExecContext(context.Background(), `UPDATE connections SET token_metadata_json = ? WHERE id = ?`,
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
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
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
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnect(t, configPath, archivePath, testRuntime)
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T21:00:00Z")
	testRuntime = newDoctorOnlineFakeRuntime(t, fakeDoctorOnlineConfig{
		now:                     time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		refreshedAccessToken:    "refreshed-access-secret",
		wantRefreshToken:        "refresh-secret-value",
		wantProviderAccessToken: "refreshed-access-secret",
		healthUserID:            "111111256096816351",
		legacyFitbitUserID:      "A1B2C3",
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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
	t.Parallel()
	configPath, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T21:00:00Z")
	testRuntime := newDoctorOnlineFakeRuntime(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		wantRefreshToken:     "refresh-secret-value",
		refreshErr:           errors.New("OAuth token refresh failed with HTTP 400"),
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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
	t.Parallel()
	configPath, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "current-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime := newDoctorOnlineFakeRuntime(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 20, 30, 0, 0, time.UTC),
		wantRefreshToken:     "refresh-secret-value",
		refreshErr:           errors.New("OAuth token refresh failed with HTTP 400"),
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnect(t, configPath, archivePath, testRuntime)
	testRuntime = newDoctorOnlineFakeRuntime(t, fakeDoctorOnlineConfig{
		now:                     time.Date(2026, 5, 31, 20, 30, 0, 0, time.UTC),
		refreshedAccessToken:    "refreshed-access-secret",
		wantRefreshToken:        "connect-refresh-secret",
		wantProviderAccessToken: "refreshed-access-secret",
		providerErr:             errors.New("Google Health identity request failed with HTTP 503"),
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnect(t, configPath, archivePath, testRuntime)
	store := fileCredentialStore{path: tokenStorePath}
	if err := store.Store("googlehealth:111111256096816351", map[string]any{"refresh_token": "connect-refresh-secret"}); err != nil {
		t.Fatalf("replace token material: %v", err)
	}
	testRuntime = newDoctorOnlineFakeRuntime(t, fakeDoctorOnlineConfig{
		failRefreshIfCalled:  true,
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnect(t, configPath, archivePath, testRuntime)
	store := fileCredentialStore{path: tokenStorePath}
	if err := store.Store("googlehealth:111111256096816351", map[string]any{"access_token": "connect-access-secret"}); err != nil {
		t.Fatalf("replace token material: %v", err)
	}
	testRuntime = newDoctorOnlineFakeRuntime(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 20, 30, 0, 0, time.UTC),
		failRefreshIfCalled:  true,
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnect(t, configPath, archivePath, testRuntime)
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T21:00:00Z")
	testRuntime = newDoctorOnlineFakeRuntime(t, fakeDoctorOnlineConfig{
		now:                     time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		refreshedAccessToken:    "refreshed-access-secret",
		wantRefreshToken:        "refresh-secret-value",
		wantProviderAccessToken: "refreshed-access-secret",
		healthUserID:            "222222256096816351",
		legacyFitbitUserID:      "DIFFERENT",
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnect(t, configPath, archivePath, testRuntime)
	store := fileCredentialStore{path: tokenStorePath}
	previousTokenMaterial, err := store.Load("googlehealth:111111256096816351")
	if err != nil {
		t.Fatalf("load previous token material: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM connections WHERE id = ?`, "googlehealth:111111256096816351"); err != nil {
		_ = db.Close()
		t.Fatalf("delete connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive after delete: %v", err)
	}
	refreshedToken := oauthTokenResponse{
		accessToken:  "refreshed-access-secret",
		refreshToken: "refresh-secret-value",
		tokenType:    "Bearer",
		scopes:       []string{googlehealth.ScopeActivityReadonly},
		expiresAt:    time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC),
		rawTokenMaterialObject: map[string]any{
			"access_token":  "refreshed-access-secret",
			"refresh_token": "refresh-secret-value",
			"expires_in":    float64(3600),
			"scope":         googlehealth.ScopeActivityReadonly,
			"token_type":    "Bearer",
		},
	}
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		t.Fatalf("open archive API: %v", err)
	}
	defer archive.Close()
	err = persistDoctorOnlineRefreshedTokenWithRuntime(archive, credentialStoreConfig{kind: "file", path: tokenStorePath}, "googlehealth:111111256096816351", refreshedToken, previousTokenMaterial, productionRuntimeAdapters())
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
	t.Parallel()
	configPath, archivePath, _ := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T19:00:00Z")
	testRuntime := newDoctorOnlineFakeRuntime(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		failRefreshIfCalled:  true,
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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

func TestDoctorMigratesLegacyV1ArchiveBeforeValidation(t *testing.T) {
	t.Parallel()
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
	if got["schema_version"] != float64(currentSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", got["schema_version"], currentSchemaVersion)
	}
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
}

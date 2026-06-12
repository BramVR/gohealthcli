package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConnectStoresFileFallbackTokenAndAnchorsIdentity(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	connectNow := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	refreshExpiresAt := connectNow.Add(24 * time.Hour)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                connectNow,
		accessToken:        "access-secret-value",
		refreshToken:       "refresh-secret-value",
		refreshExpiresAt:   &refreshExpiresAt,
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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

	db := openArchiveForTest(t, archivePath)
	var connectionID, providerName, healthUserID, legacyUserID, tokenMetadata, identityJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT id, provider_name, google_health_user_id, legacy_fitbit_user_id, token_metadata_json, google_identity_json FROM connections`).Scan(&connectionID, &providerName, &healthUserID, &legacyUserID, &tokenMetadata, &identityJSON); err != nil {
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
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		accessToken:        "first-access-secret",
		refreshToken:       "first-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime)
	if code != 0 {
		t.Fatalf("first connect exit code = %d, want 0", code)
	}

	testRuntime = newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC),
		accessToken:        "second-access-secret",
		refreshToken:       "second-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	code = runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime)
	if code != 0 {
		t.Fatalf("second connect exit code = %d, want 0", code)
	}

	db := openArchiveForTest(t, archivePath)
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM connections`).Scan(&count); err != nil {
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

func TestConnectArchiveInspectionFailureDoesNotReportCredentialStore(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM schema_migrations WHERE version = ?`, currentSchemaVersion); err != nil {
		_ = db.Close()
		t.Fatalf("delete schema migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connect_failed")
	if _, ok := got["credential_store"]; ok {
		t.Fatalf("credential_store = %v, want omitted for Health Archive check failure", got["credential_store"])
	}
	if !strings.Contains(got["message"].(string), "Health Archive check failed") {
		t.Fatalf("message = %q, want Health Archive check failed", got["message"])
	}
}

func TestConnectRejectsDifferentGoogleIdentity(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		accessToken:        "first-access-secret",
		refreshToken:       "first-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnect(t, configPath, archivePath, testRuntime)

	testRuntime = newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC),
		accessToken:        "other-access-secret",
		refreshToken:       "other-refresh-secret",
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "D4E5F6",
	})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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
	t.Parallel()
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
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{failIfCalled: true})
	stdout := new(bytes.Buffer)
	connectStderr := new(bytes.Buffer)
	code = runWithRuntime([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, connectStderr, testRuntime)
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

func TestConnectAcceptsGlobalNoInput(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	wantNoInput := true
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{wantNoInput: &wantNoInput})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"--no-input", "connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("connect exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
}

func TestConnectMigratesLegacyV1ArchiveBeforeStoringIdentity(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV1Archive(t, archivePath)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{})

	mustConnect(t, configPath, archivePath, testRuntime)

	db := openArchiveForTest(t, archivePath)
	var userVersion int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if userVersion != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", userVersion, currentSchemaVersion)
	}
	var identityJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&identityJSON); err != nil {
		t.Fatalf("query migrated connection: %v", err)
	}
	if !strings.Contains(identityJSON, `"healthUserId":"111111256096816351"`) {
		t.Fatalf("identity JSON = %s, want archived identity", identityJSON)
	}
}

func TestConnectRejectsUnsupportedOSNativeStoreBeforeOAuth(t *testing.T) {
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
	config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"os_native\"\nservice = \"gohealthcli\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{failIfCalled: true})
	testRuntime.currentOS = "plan9"

	stdout := new(bytes.Buffer)
	connectStderr := new(bytes.Buffer)
	code = runWithRuntime([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, connectStderr, testRuntime)
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
	t.Parallel()
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
			testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{failIfCalled: true})

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := runWithRuntime([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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
	config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"os_native\"\nservice = \"gohealthcli\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{failIfCalled: true})
	testRuntime.currentOS = "linux"
	testRuntime.findExecutable = func(name string) (string, error) {
		if name != "secret-tool" {
			t.Fatalf("find executable = %q, want secret-tool", name)
		}
		return "", exec.ErrNotFound
	}

	stdout := new(bytes.Buffer)
	connectStderr := new(bytes.Buffer)
	code = runWithRuntime([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, connectStderr, testRuntime)
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
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	clientPath := filepath.Join(tempDir, "client_secret.json")
	content := []byte(`{"web":{"client_id":"test-client","client_secret":"test-secret","redirect_uris":["http://127.0.0.1:8080/oauth2callback"]}}`)
	if err := os.WriteFile(clientPath, content, 0o600); err != nil {
		t.Fatalf("write web OAuth client file: %v", err)
	}
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
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

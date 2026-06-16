package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOSNativeCredentialStoreDoesNotSendTokenAsArgument(t *testing.T) {
	t.Parallel()
	testRuntime := productionRuntimeAdapters()
	testRuntime.currentOS = "darwin"

	var gotService string
	var gotKey string
	var gotContent []byte
	testRuntime.runSecurityAddGenericPassword = func(_ context.Context, service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStoreWithRuntime(credentialStoreConfig{kind: "os_native", service: "gohealthcli"}, testRuntime)
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
	if err := runSecurityAddGenericPasswordCommand(context.Background(), "gohealthcli", "googlehealth:111", content); err != nil {
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
	t.Parallel()
	testRuntime := productionRuntimeAdapters()
	testRuntime.currentOS = "linux"

	var gotService string
	var gotKey string
	var gotContent []byte
	testRuntime.runSecretToolStore = func(_ context.Context, service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStoreWithRuntime(credentialStoreConfig{kind: "os_native", service: "gohealthcli"}, testRuntime)
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
	t.Parallel()
	testRuntime := productionRuntimeAdapters()
	testRuntime.currentOS = "windows"

	var gotService string
	var gotKey string
	var gotContent []byte
	testRuntime.runWindowsCredentialWrite = func(_ context.Context, service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStoreWithRuntime(credentialStoreConfig{kind: "os_native", service: "gohealthcli"}, testRuntime)
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

func TestFileCredentialStoreFirstWriteIsOwnerOnly(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		t.Fatalf("make temp dir owner-only: %v", err)
	}
	storePath := filepath.Join(tempDir, "tokens.json")
	store := fileCredentialStore{path: storePath}

	if err := store.Store("googlehealth:111", map[string]any{"access_token": "first-access-secret"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	loaded, err := store.Load("googlehealth:111")
	if err != nil {
		t.Fatalf("load token: %v", err)
	}
	if loaded["access_token"] != "first-access-secret" {
		t.Fatalf("access_token = %v, want first token", loaded["access_token"])
	}
	if err := store.Store("googlehealth:222", map[string]any{"refresh_token": "second-refresh-secret"}); err != nil {
		t.Fatalf("store second token: %v", err)
	}
	second, err := store.Load("googlehealth:222")
	if err != nil {
		t.Fatalf("load second token: %v", err)
	}
	if second["refresh_token"] != "second-refresh-secret" {
		t.Fatalf("refresh_token = %v, want second token", second["refresh_token"])
	}
	assertMode(t, storePath, 0o600)
	assertMode(t, tempDir, 0o700)
}

func TestFileCredentialStoreReplacementDoesNotMutateExistingInode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard-link replacement semantics differ on Windows")
	}
	t.Parallel()
	tempDir := t.TempDir()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		t.Fatalf("make temp dir owner-only: %v", err)
	}
	storePath := filepath.Join(tempDir, "tokens.json")
	previousPath := filepath.Join(tempDir, "tokens.previous.json")
	store := fileCredentialStore{path: storePath}

	if err := store.Store("googlehealth:111", map[string]any{"access_token": "first-access-secret"}); err != nil {
		t.Fatalf("store first token: %v", err)
	}
	if err := os.Link(storePath, previousPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "second-access-secret"}); err != nil {
		t.Fatalf("store second token: %v", err)
	}

	current, err := store.Load("googlehealth:111")
	if err != nil {
		t.Fatalf("load current token: %v", err)
	}
	if current["access_token"] != "second-access-secret" {
		t.Fatalf("current access_token = %v, want second token", current["access_token"])
	}
	previous, err := (fileCredentialStore{path: previousPath}).Load("googlehealth:111")
	if err != nil {
		t.Fatalf("load previous token: %v", err)
	}
	if previous["access_token"] != "first-access-secret" {
		t.Fatalf("previous hard link access_token = %v, want first token", previous["access_token"])
	}
	assertMode(t, storePath, 0o600)
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tokens.json.tmp-") {
			t.Fatalf("temporary Credential Store file remains after successful write: %s", entry.Name())
		}
	}
}

func TestFileCredentialStoreFailedReplacementKeepsExistingMaterial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions differ on Windows")
	}
	t.Parallel()
	tempDir := t.TempDir()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		t.Fatalf("make temp dir owner-only: %v", err)
	}
	storePath := filepath.Join(tempDir, "tokens.json")
	store := fileCredentialStore{path: storePath}

	if err := store.Store("googlehealth:111", map[string]any{"access_token": "first-access-secret"}); err != nil {
		t.Fatalf("store first token: %v", err)
	}
	if err := os.Chmod(tempDir, 0o500); err != nil {
		t.Fatalf("make store dir read-only: %v", err)
	}
	defer func() {
		if err := os.Chmod(tempDir, 0o700); err != nil {
			t.Fatalf("restore store dir permissions: %v", err)
		}
	}()

	err := store.Store("googlehealth:111", map[string]any{"access_token": "second-access-secret"})
	if err == nil {
		t.Fatal("store second token succeeded, want replacement failure")
	}
	loaded, loadErr := store.Load("googlehealth:111")
	if loadErr != nil {
		t.Fatalf("load token after failed replacement: %v", loadErr)
	}
	if loaded["access_token"] != "first-access-secret" {
		t.Fatalf("access_token after failed replacement = %v, want first token", loaded["access_token"])
	}
}

func TestFileCredentialStoreRejectsSymlinkOnStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup requires extra privileges on Windows")
	}
	t.Parallel()
	tempDir := t.TempDir()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		t.Fatalf("make temp dir owner-only: %v", err)
	}
	targetPath := filepath.Join(tempDir, "target.json")
	if err := os.WriteFile(targetPath, []byte("do not overwrite\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	storePath := filepath.Join(tempDir, "tokens.json")
	if err := os.Symlink(targetPath, storePath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	err := (fileCredentialStore{path: storePath}).Store("googlehealth:111", map[string]any{"access_token": "secret"})
	assertCredentialStoreSymlinkError(t, err)
	target, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(target) != "do not overwrite\n" {
		t.Fatalf("symlink target was modified: %q", string(target))
	}
	if info, statErr := os.Lstat(storePath); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Credential Store path = mode %v err %v, want symlink still present", info.Mode(), statErr)
	}
}

func TestFileCredentialStoreRejectsSymlinkOnLoad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup requires extra privileges on Windows")
	}
	t.Parallel()
	tempDir := t.TempDir()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		t.Fatalf("make temp dir owner-only: %v", err)
	}
	targetPath := filepath.Join(tempDir, "target.json")
	if err := os.WriteFile(targetPath, []byte(`{"googlehealth:111":{"access_token":"secret"}}`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	storePath := filepath.Join(tempDir, "tokens.json")
	if err := os.Symlink(targetPath, storePath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := (fileCredentialStore{path: storePath}).Load("googlehealth:111")
	assertCredentialStoreSymlinkError(t, err)
}

func TestValidateCredentialStoreConfigRejectsSymlinkFileStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup requires extra privileges on Windows")
	}
	t.Parallel()
	tempDir := t.TempDir()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		t.Fatalf("make temp dir owner-only: %v", err)
	}
	targetPath := filepath.Join(tempDir, "target.json")
	if err := os.WriteFile(targetPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	storePath := filepath.Join(tempDir, "tokens.json")
	if err := os.Symlink(targetPath, storePath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	err := validateCredentialStoreConfig(credentialStoreConfig{kind: "file", path: storePath})
	assertCredentialStoreSymlinkError(t, err)
}

func assertCredentialStoreSymlinkError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want symlink rejection")
	}
	message := err.Error()
	if !strings.Contains(message, "Credential Store file path") || !strings.Contains(message, "symbolic link") {
		t.Fatalf("error = %q, want Credential Store file path symbolic link rejection", message)
	}
}

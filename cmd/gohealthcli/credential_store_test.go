package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
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

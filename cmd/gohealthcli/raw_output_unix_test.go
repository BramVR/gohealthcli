//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRawOutputSupportsWriteSearchOnlyParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "drop-box")
	if err := os.Mkdir(parent, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	outputPath := filepath.Join(parent, "response.json")
	payload := []byte("synthetic-provider-response")
	if _, err := writeRawOutputFile(outputPath, payload); err != nil {
		t.Fatalf("write raw output in write-search-only parent: %v", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("raw output = %q, want %q", got, payload)
	}
	if _, err := os.Lstat(filepath.Join(parent, ".gohealthcli-raw-missing")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect parent after write: %v", err)
	}
}

func TestRawOutputRejectsNonPrivateParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o770); err != nil {
		t.Fatal(err)
	}
	_, err := prepareRawOutputFile(filepath.Join(parent, "response.json"))
	var validationError *rawOutputValidationError
	if !errors.As(err, &validationError) || !strings.Contains(err.Error(), "owned by the effective user") {
		t.Fatalf("prepare raw output error = %v, want private-parent refusal", err)
	}
}

func TestRawOutputRejectsExistingNamedPipe(t *testing.T) {
	t.Parallel()
	outputPath := filepath.Join(t.TempDir(), "response.pipe")
	if err := unix.Mkfifo(outputPath, 0o600); err != nil {
		t.Fatalf("create named pipe: %v", err)
	}
	err := validateRawOutputDestination(outputPath)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("validate named-pipe --output error = %v, want existing-destination refusal", err)
	}
}

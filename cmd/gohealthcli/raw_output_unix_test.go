//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

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

//go:build unix

package main

import (
	"path/filepath"
	"syscall"
	"testing"
)

// TestReadOwnerOnlyOAuthClientFileRejectsFIFO pins the rule that a non-regular
// file (here a FIFO) at the OAuth client path is refused before os.ReadFile,
// which could otherwise block on it. Covers both the validate path and the
// auto-refresh path, which share readOwnerOnlyOAuthClientFile.
func TestReadOwnerOnlyOAuthClientFileRejectsFIFO(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "client.json")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	if _, err := readOwnerOnlyOAuthClientFile(fifoPath); err == nil {
		t.Fatal("readOwnerOnlyOAuthClientFile accepted a FIFO, want a non-regular-file rejection")
	}
	if err := validateOAuthClientFile(fifoPath); err == nil {
		t.Fatal("validateOAuthClientFile accepted a FIFO, want a non-regular-file rejection")
	}
}

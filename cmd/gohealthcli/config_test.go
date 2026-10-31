package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateConfigDoesNotCreateMissingParent(t *testing.T) {
	t.Parallel()
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

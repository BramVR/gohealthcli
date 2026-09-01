//go:build darwin || linux || windows

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRawOutputPublishUsesPinnedParentAfterPathReplacement(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "requested-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(parent, "response.json")
	payload := []byte("synthetic-provider-response")
	destination, err := openStagedRawOutput(outputPath)
	if err != nil {
		t.Fatalf("open staged output: %v", err)
	}
	if _, err := destination.Write(payload); err != nil {
		_ = destination.Abort()
		t.Fatalf("write staged output: %v", err)
	}
	if usesPOSIXPermissions() {
		if err := destination.Chmod(0o600); err != nil {
			_ = destination.Abort()
			t.Fatalf("chmod staged output: %v", err)
		}
	}
	if err := destination.Close(); err != nil {
		_ = destination.Abort()
		t.Fatalf("close staged output: %v", err)
	}

	movedParent := filepath.Join(root, "resolved-parent")
	if err := os.Rename(parent, movedParent); err != nil {
		_ = destination.Abort()
		t.Fatalf("replace parent path: %v", err)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		_ = destination.Abort()
		t.Fatalf("create replacement parent: %v", err)
	}
	if err := destination.Commit(); err != nil {
		_ = destination.Abort()
		t.Fatalf("commit through pinned parent: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(movedParent, "response.json"))
	if err != nil {
		t.Fatalf("read committed output: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("committed output = %q, want %q", got, payload)
	}
	if _, err := os.Lstat(filepath.Join(parent, "response.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement parent received output: %v", err)
	}
}

func TestRawOutputAbortUsesPinnedFileAfterParentPathReplacement(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "requested-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	destination, err := openStagedRawOutput(filepath.Join(parent, "response.json"))
	if err != nil {
		t.Fatalf("open staged output: %v", err)
	}
	stagingFiles, err := filepath.Glob(filepath.Join(parent, ".gohealthcli-raw-*"))
	if err != nil || len(stagingFiles) != 1 {
		_ = destination.Abort()
		t.Fatalf("staging files = %v, err = %v, want one", stagingFiles, err)
	}
	stageLeaf := filepath.Base(stagingFiles[0])

	movedParent := filepath.Join(root, "resolved-parent")
	if err := os.Rename(parent, movedParent); err != nil {
		_ = destination.Abort()
		t.Fatalf("replace parent path: %v", err)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		_ = destination.Abort()
		t.Fatalf("create replacement parent: %v", err)
	}
	replacementStage := filepath.Join(parent, stageLeaf)
	if err := os.WriteFile(replacementStage, []byte("keep"), 0o600); err != nil {
		_ = destination.Abort()
		t.Fatalf("seed replacement staging path: %v", err)
	}
	if err := destination.Abort(); err != nil {
		t.Fatalf("abort through pinned handle: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(movedParent, stageLeaf)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original staging file remains: %v", err)
	}
	got, err := os.ReadFile(replacementStage)
	if err != nil {
		t.Fatalf("read replacement staging file: %v", err)
	}
	if string(got) != "keep" {
		t.Fatalf("replacement staging file = %q, want unchanged", got)
	}
}

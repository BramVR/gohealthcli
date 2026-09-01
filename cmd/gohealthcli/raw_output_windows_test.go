//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsRawOutputRenameBufferHasMinimumSizeForShortLeaf(t *testing.T) {
	buffer, err := windowsRawOutputRenameBuffer(0, "x")
	if err != nil {
		t.Fatal(err)
	}
	minimum := int(unsafe.Sizeof(windowsRawOutputRenameInformation{}))
	if len(buffer) < minimum {
		t.Fatalf("rename buffer length = %d, want at least %d", len(buffer), minimum)
	}
}

func TestWindowsRawOutputRejectsNetworkVolumes(t *testing.T) {
	if !windowsRawOutputFinalPathIsNetwork("\\\\?\\UNC\\server\\share\\parent") {
		t.Fatal("resolved UNC parent was not classified as a network path")
	}
	if windowsRawOutputFinalPathIsNetwork("\\\\?\\C:\\parent") {
		t.Fatal("resolved local parent was classified as a network path")
	}
}

func TestWindowsRawOutputRejectsUnstableWin32Leaves(t *testing.T) {
	for _, leaf := range []string{"response.", "response ", "CON", "CON .json", "CONIN$", "CONOUT$.json", "nul.json", "COM1.txt", "COM1 .txt", "LPT¹", "stream:name", "bad?name"} {
		if err := validateWindowsRawOutputLeaf(leaf); err == nil {
			t.Fatalf("Windows output leaf %q was accepted", leaf)
		}
	}
	if err := validateWindowsRawOutputLeaf("response.json"); err != nil {
		t.Fatalf("ordinary Windows output leaf rejected: %v", err)
	}
}

func TestWindowsRawOutputPreparedParentBlocksPathReplacement(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "requested-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	destination, err := openStagedRawOutput(filepath.Join(parent, "response.json"))
	if err != nil {
		t.Fatalf("open staged output: %v", err)
	}
	movedParent := filepath.Join(root, "replacement-parent")
	if err := os.Rename(parent, movedParent); err == nil {
		_ = destination.Abort()
		t.Fatal("prepared Windows parent was renamed while its handles were pinned")
	}
	if err := destination.Abort(); err != nil {
		t.Fatalf("abort prepared output: %v", err)
	}
	stagingFiles, err := filepath.Glob(filepath.Join(parent, ".gohealthcli-raw-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stagingFiles) != 0 {
		t.Fatalf("abort left staging files: %v", stagingFiles)
	}
}

func TestCleanupWindowsRawOutputSetupRemovesCreatedStage(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "original handle", true: "duplicate handle"}[duplicate], func(t *testing.T) {
			parentPath := t.TempDir()
			parent, err := openWindowsRawOutputParent(parentPath)
			if err != nil {
				t.Fatalf("open parent: %v", err)
			}
			stageLeaf := ".gohealthcli-raw-setup-failure"
			handle, err := openWindowsAttachmentChild(
				parent,
				stageLeaf,
				windows.FILE_WRITE_DATA|windows.FILE_READ_ATTRIBUTES|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE|windows.SYNCHRONIZE,
				windows.FILE_CREATE,
				windows.FILE_NON_DIRECTORY_FILE,
			)
			if err != nil {
				_ = windows.CloseHandle(parent)
				t.Fatalf("create staging file: %v", err)
			}
			stageHandle := windows.InvalidHandle
			if duplicate {
				process := windows.CurrentProcess()
				if err := windows.DuplicateHandle(process, handle, process, &stageHandle, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
					_ = windows.CloseHandle(handle)
					_ = windows.CloseHandle(parent)
					t.Fatalf("duplicate staging handle: %v", err)
				}
			}

			err = cleanupWindowsRawOutputSetup(handle, stageHandle, parent, errSyntheticRawOutputWrite)
			if !errors.Is(err, errSyntheticRawOutputWrite) {
				t.Fatalf("cleanup error = %v, want synthetic setup failure", err)
			}
			if _, err := os.Lstat(filepath.Join(parentPath, stageLeaf)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("setup failure left staging file: %v", err)
			}
		})
	}
}

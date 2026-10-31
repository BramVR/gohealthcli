//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type platformRawOutput struct {
	parent      windows.Handle
	targetLeaf  string
	stageHandle windows.Handle
	file        *os.File
	closed      bool
	committed   bool
}

func rawOutputPlatformSupported() error { return nil }

func openPlatformRawOutput(path string) (rawOutputDestination, error) {
	targetLeaf := filepath.Base(path)
	if err := validateWindowsRawOutputLeaf(targetLeaf); err != nil {
		return nil, &rawOutputValidationError{err: err}
	}
	parent, err := openWindowsRawOutputParent(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open --output parent: %w", err)
	}
	finalParentPath, err := windowsRawOutputFinalPath(parent)
	if err != nil {
		_ = windows.CloseHandle(parent)
		return nil, fmt.Errorf("resolve --output parent: %w", err)
	}
	if windowsRawOutputFinalPathIsNetwork(finalParentPath) {
		_ = windows.CloseHandle(parent)
		return nil, &rawOutputValidationError{err: fmt.Errorf("raw --output on Windows requires a local volume; network paths are unsupported")}
	}
	stageLeaf, err := randomRawOutputLeaf()
	if err != nil {
		_ = windows.CloseHandle(parent)
		return nil, fmt.Errorf("name --output staging file: %w", err)
	}
	// openWindowsAttachmentChild grants FILE_SHARE_DELETE. The duplicate
	// handle can therefore stay open while FileRenameInformation publishes.
	handle, err := openWindowsAttachmentChild(
		parent,
		stageLeaf,
		windows.FILE_WRITE_DATA|windows.FILE_READ_ATTRIBUTES|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE,
	)
	if err != nil {
		_ = windows.CloseHandle(parent)
		return nil, fmt.Errorf("create --output staging file: %w", err)
	}
	stageHandle := windows.InvalidHandle
	process := windows.CurrentProcess()
	if err := windows.DuplicateHandle(process, handle, process, &stageHandle, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, cleanupWindowsRawOutputSetup(handle, windows.InvalidHandle, parent, fmt.Errorf("pin --output staging file: %w", err))
	}
	file := os.NewFile(uintptr(handle), stageLeaf)
	if file == nil {
		return nil, cleanupWindowsRawOutputSetup(handle, stageHandle, parent, fmt.Errorf("create --output staging file"))
	}
	return &platformRawOutput{
		parent:      parent,
		targetLeaf:  targetLeaf,
		stageHandle: stageHandle,
		file:        file,
	}, nil
}

func windowsRawOutputFinalPath(handle windows.Handle) (string, error) {
	size, err := windows.GetFinalPathNameByHandle(handle, nil, 0, 0)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, size)
	written, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if written >= uint32(len(buffer)) {
		return "", fmt.Errorf("resolved --output parent path changed length")
	}
	return windows.UTF16ToString(buffer[:written]), nil
}

func openWindowsRawOutputParent(path string) (windows.Handle, error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	access := uint32(windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.FILE_WRITE_DATA | windows.SYNCHRONIZE)
	return windows.CreateFile(
		pathUTF16,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
}

func cleanupWindowsRawOutputSetup(handle, stageHandle, parent windows.Handle, primary error) error {
	deleteHandle := stageHandle
	if deleteHandle == windows.InvalidHandle {
		deleteHandle = handle
	}
	var deleteErr error
	if deleteHandle != windows.InvalidHandle {
		deleteErr = markWindowsAttachmentDelete(deleteHandle)
	}
	var stageErr error
	if stageHandle != windows.InvalidHandle {
		stageErr = windows.CloseHandle(stageHandle)
	}
	var handleErr error
	if handle != windows.InvalidHandle {
		handleErr = windows.CloseHandle(handle)
	}
	var parentErr error
	if parent != windows.InvalidHandle {
		parentErr = windows.CloseHandle(parent)
	}
	return errors.Join(primary, deleteErr, stageErr, handleErr, parentErr)
}

func (output *platformRawOutput) Write(payload []byte) (int, error) {
	return output.file.Write(payload)
}

func (output *platformRawOutput) Chmod(mode os.FileMode) error {
	return output.file.Chmod(mode)
}

func (output *platformRawOutput) Close() error {
	if output.closed {
		return nil
	}
	output.closed = true
	return output.file.Close()
}

func (output *platformRawOutput) Commit() error {
	if !output.closed {
		return fmt.Errorf("staged output is still open")
	}
	if err := renameWindowsRawOutputHandle(output.stageHandle, output.parent, output.targetLeaf); err != nil {
		return err
	}
	output.committed = true
	// Publication is irreversible and the complete file is now visible. A
	// later handle-close error must not report a failed command that left an
	// apparently complete destination.
	_ = output.closeHandles()
	return nil
}

func (output *platformRawOutput) Abort() error {
	closeErr := output.Close()
	var deleteErr error
	if !output.committed && output.stageHandle != windows.InvalidHandle {
		deleteErr = markWindowsAttachmentDelete(output.stageHandle)
	}
	handleErr := output.closeHandles()
	return errors.Join(closeErr, deleteErr, handleErr)
}

func (output *platformRawOutput) closeHandles() error {
	var stageErr error
	if output.stageHandle != windows.InvalidHandle {
		stageErr = windows.CloseHandle(output.stageHandle)
		output.stageHandle = windows.InvalidHandle
	}
	var parentErr error
	if output.parent != windows.InvalidHandle {
		parentErr = windows.CloseHandle(output.parent)
		output.parent = windows.InvalidHandle
	}
	return errors.Join(stageErr, parentErr)
}

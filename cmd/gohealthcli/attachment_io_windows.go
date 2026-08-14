//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type attachmentRootIdentity struct {
	volumeSerial  uint32
	fileIndexHigh uint32
	fileIndexLow  uint32
}

type windowsAttachmentRenameInformation struct {
	replaceIfExists uint32
	rootDirectory   windows.Handle
	fileNameLength  uint32
	fileName        [1]uint16
}

func readAttachmentFileNoFollow(rootDir, pathRelative string, expectedSize int64, expectedRoot *attachmentRootIdentity) ([]byte, error) {
	if expectedSize < 0 || expectedSize == math.MaxInt64 {
		return nil, fmt.Errorf("attachment path %q has invalid byte_size %d", pathRelative, expectedSize)
	}
	parent, leaf, err := openWindowsAttachmentParentExpected(rootDir, pathRelative, false, expectedRoot)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(parent)
	handle, err := openWindowsAttachmentChild(parent, leaf, windows.FILE_GENERIC_READ, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return nil, windowsAttachmentPathError(pathRelative, err)
	}
	file := os.NewFile(uintptr(handle), leaf)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("open attachment path %q", pathRelative)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attachment path %q is not a regular file", pathRelative)
	}
	if info.Size() != expectedSize {
		return nil, fmt.Errorf("attachment path %q size %d does not match byte_size %d", pathRelative, info.Size(), expectedSize)
	}
	payload, err := io.ReadAll(io.LimitReader(file, expectedSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) != expectedSize {
		return nil, fmt.Errorf("attachment path %q read %d bytes, want %d", pathRelative, len(payload), expectedSize)
	}
	return payload, nil
}

func writeAttachmentFileNoFollow(rootDir, pathRelative string, payload []byte, expectedRoot *attachmentRootIdentity) error {
	parent, leaf, err := openWindowsAttachmentParentExpected(rootDir, pathRelative, true, expectedRoot)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	tempLeaf, err := randomWindowsAttachmentTempLeaf()
	if err != nil {
		return err
	}
	handle, err := openWindowsAttachmentChild(parent, tempLeaf, windows.FILE_GENERIC_WRITE|windows.DELETE, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), tempLeaf)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return fmt.Errorf("create temporary attachment for %q", pathRelative)
	}
	if _, err := file.Write(payload); err != nil {
		_ = markWindowsAttachmentDelete(handle)
		_ = file.Close()
		return err
	}
	if err := renameWindowsAttachmentHandle(handle, parent, leaf); err != nil {
		_ = markWindowsAttachmentDelete(handle)
		_ = file.Close()
		return err
	}
	return file.Close()
}

func captureAttachmentRootIdentity(rootDir string) (attachmentRootIdentity, error) {
	handle, identity, err := openWindowsAttachmentRoot(rootDir, false)
	if err != nil {
		return attachmentRootIdentity{}, err
	}
	_ = windows.CloseHandle(handle)
	return identity, nil
}

func removeAttachmentFileNoFollow(rootDir, pathRelative string, expectedRoot attachmentRootIdentity) error {
	root, identity, err := openWindowsAttachmentRoot(rootDir, true)
	if err != nil {
		return err
	}
	if identity != expectedRoot {
		_ = windows.CloseHandle(root)
		return fmt.Errorf("attachment root identity changed before failed-restore cleanup")
	}
	parent, leaf, err := openWindowsAttachmentParentFromHandle(root, pathRelative, false)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	handle, err := openWindowsAttachmentChild(parent, leaf, windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return windowsAttachmentPathError(pathRelative, err)
	}
	defer windows.CloseHandle(handle)
	return markWindowsAttachmentDelete(handle)
}

func openWindowsAttachmentParent(rootDir, pathRelative string, create bool) (windows.Handle, string, error) {
	return openWindowsAttachmentParentExpected(rootDir, pathRelative, create, nil)
}

func openWindowsAttachmentParentExpected(rootDir, pathRelative string, create bool, expectedRoot *attachmentRootIdentity) (windows.Handle, string, error) {
	root, identity, err := openWindowsAttachmentRoot(rootDir, create)
	if err != nil {
		return windows.InvalidHandle, "", err
	}
	if expectedRoot != nil && identity != *expectedRoot {
		_ = windows.CloseHandle(root)
		return windows.InvalidHandle, "", fmt.Errorf("attachment root identity changed")
	}
	return openWindowsAttachmentParentFromHandle(root, pathRelative, create)
}

func openWindowsAttachmentParentFromHandle(root windows.Handle, pathRelative string, create bool) (windows.Handle, string, error) {
	components := strings.Split(pathRelative, "/")
	if len(components) < 2 {
		_ = windows.CloseHandle(root)
		return windows.InvalidHandle, "", fmt.Errorf("attachment path %q has no parent directory", pathRelative)
	}
	current := root
	for _, component := range components[:len(components)-1] {
		disposition := uint32(windows.FILE_OPEN)
		access := uint32(windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
		if create {
			disposition = windows.FILE_OPEN_IF
			access = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.DELETE
		}
		next, err := openWindowsAttachmentChild(current, component, access, disposition, windows.FILE_DIRECTORY_FILE)
		if err != nil {
			_ = windows.CloseHandle(current)
			return windows.InvalidHandle, "", windowsAttachmentPathError(pathRelative, err)
		}
		_ = windows.CloseHandle(current)
		current = next
	}
	return current, components[len(components)-1], nil
}

func openWindowsAttachmentRoot(rootDir string, write bool) (windows.Handle, attachmentRootIdentity, error) {
	path, err := windows.UTF16PtrFromString(rootDir)
	if err != nil {
		return windows.InvalidHandle, attachmentRootIdentity{}, err
	}
	access := uint32(windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	if write {
		access = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.DELETE
	}
	handle, err := windows.CreateFile(path, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return windows.InvalidHandle, attachmentRootIdentity{}, err
	}
	identity, err := windowsAttachmentHandleIdentity(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, attachmentRootIdentity{}, err
	}
	return handle, identity, nil
}

func openWindowsAttachmentChild(parent windows.Handle, name string, access, disposition, kindOption uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition,
		kindOption|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return windows.InvalidHandle, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, windows.STATUS_REPARSE_POINT_ENCOUNTERED
	}
	return handle, nil
}

func windowsAttachmentHandleIdentity(handle windows.Handle) (attachmentRootIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return attachmentRootIdentity{}, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return attachmentRootIdentity{}, fmt.Errorf("attachment root is a reparse point or not a directory")
	}
	return attachmentRootIdentity{volumeSerial: info.VolumeSerialNumber, fileIndexHigh: info.FileIndexHigh, fileIndexLow: info.FileIndexLow}, nil
}

func renameWindowsAttachmentHandle(handle, parent windows.Handle, leaf string) error {
	leafUTF16, err := windows.UTF16FromString(leaf)
	if err != nil {
		return err
	}
	nameBytes := (len(leafUTF16) - 1) * 2
	var layout windowsAttachmentRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.fileName))+nameBytes)
	info := (*windowsAttachmentRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.replaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.rootDirectory = parent
	info.fileNameLength = uint32(nameBytes)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&info.fileName[0]))[:nameBytes/2:nameBytes/2], leafUTF16)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}

func markWindowsAttachmentDelete(handle windows.Handle) error {
	deleteFile := byte(1)
	return windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &deleteFile, 1)
}

func randomWindowsAttachmentTempLeaf() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".gohealthcli-attachment-" + hex.EncodeToString(value[:]), nil
}

func windowsAttachmentPathError(pathRelative string, err error) error {
	if errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.STATUS_OBJECT_PATH_NOT_FOUND) {
		return fmt.Errorf("%w: attachment path %q", os.ErrNotExist, pathRelative)
	}
	if errors.Is(err, windows.STATUS_REPARSE_POINT_ENCOUNTERED) || errors.Is(err, windows.STATUS_REPARSE_POINT_NOT_RESOLVED) {
		return fmt.Errorf("attachment path %q contains a symbolic link or reparse point: %w", pathRelative, err)
	}
	return err
}

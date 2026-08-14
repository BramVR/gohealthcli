//go:build !windows

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

	"golang.org/x/sys/unix"
)

type attachmentRootIdentity struct {
	device uint64
	inode  uint64
}

func readAttachmentFileNoFollow(rootDir, pathRelative string, expectedSize int64, expectedRoot *attachmentRootIdentity) ([]byte, error) {
	if expectedSize < 0 || expectedSize == math.MaxInt64 {
		return nil, fmt.Errorf("attachment path %q has invalid byte_size %d", pathRelative, expectedSize)
	}
	parentFD, leaf, err := openAttachmentParentFDExpected(rootDir, pathRelative, false, expectedRoot)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)

	var before unix.Stat_t
	if err := unix.Fstatat(parentFD, leaf, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT == unix.S_IFLNK {
		return nil, fmt.Errorf("attachment path %q is a symbolic link", pathRelative)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("attachment path %q is not a regular file", pathRelative)
	}
	if before.Size != expectedSize {
		return nil, fmt.Errorf("attachment path %q size %d does not match byte_size %d", pathRelative, before.Size, expectedSize)
	}

	fd, err := unix.Openat(parentFD, leaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, attachmentPathOpenError(pathRelative, err)
	}
	file := os.NewFile(uintptr(fd), leaf)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open attachment path %q", pathRelative)
	}
	defer file.Close()

	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if after.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("attachment path %q is not a regular file", pathRelative)
	}
	if after.Size != expectedSize {
		return nil, fmt.Errorf("attachment path %q size changed from %d to %d", pathRelative, expectedSize, after.Size)
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
	parentFD, leaf, err := openAttachmentParentFDExpected(rootDir, pathRelative, true, expectedRoot)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)

	tempLeaf, err := randomAttachmentTempLeaf()
	if err != nil {
		return err
	}
	fd, err := unix.Openat(parentFD, tempLeaf, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = unix.Unlinkat(parentFD, tempLeaf, 0)
		}
	}()
	file := os.NewFile(uintptr(fd), tempLeaf)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("create temporary attachment for %q", pathRelative)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write sidecar: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod sidecar: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(parentFD, tempLeaf, parentFD, leaf); err != nil {
		return fmt.Errorf("install attachment path %q: %w", pathRelative, err)
	}
	removeTemp = false
	return nil
}

func openAttachmentParentFD(rootDir, pathRelative string, create bool) (int, string, error) {
	return openAttachmentParentFDExpected(rootDir, pathRelative, create, nil)
}

func openAttachmentParentFDExpected(rootDir, pathRelative string, create bool, expectedRoot *attachmentRootIdentity) (int, string, error) {
	rootFD, err := openAttachmentRootFD(rootDir, pathRelative)
	if err != nil {
		return -1, "", err
	}
	if expectedRoot != nil {
		var stat unix.Stat_t
		if err := unix.Fstat(rootFD, &stat); err != nil {
			_ = unix.Close(rootFD)
			return -1, "", err
		}
		if uint64(stat.Dev) != expectedRoot.device || stat.Ino != expectedRoot.inode {
			_ = unix.Close(rootFD)
			return -1, "", fmt.Errorf("attachment root identity changed")
		}
	}
	return openAttachmentParentFromFD(rootFD, pathRelative, create)
}

func openAttachmentRootFD(rootDir, pathRelative string) (int, error) {
	rootFD, err := unix.Open(rootDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, attachmentPathOpenError(pathRelative, err)
	}
	return rootFD, nil
}

func openAttachmentParentFromFD(rootFD int, pathRelative string, create bool) (int, string, error) {
	components := strings.Split(pathRelative, "/")
	if len(components) < 2 {
		_ = unix.Close(rootFD)
		return -1, "", fmt.Errorf("attachment path %q has no parent directory", pathRelative)
	}
	currentFD := rootFD
	for _, component := range components[:len(components)-1] {
		nextFD, openErr := unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(currentFD, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(currentFD)
				return -1, "", mkdirErr
			}
			nextFD, openErr = unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			_ = unix.Close(currentFD)
			return -1, "", attachmentPathOpenError(pathRelative, openErr)
		}
		if create {
			if err := unix.Fchmod(nextFD, 0o700); err != nil {
				_ = unix.Close(nextFD)
				_ = unix.Close(currentFD)
				return -1, "", err
			}
		}
		_ = unix.Close(currentFD)
		currentFD = nextFD
	}
	return currentFD, components[len(components)-1], nil
}

func captureAttachmentRootIdentity(rootDir string) (attachmentRootIdentity, error) {
	rootFD, err := openAttachmentRootFD(rootDir, rootDir)
	if err != nil {
		return attachmentRootIdentity{}, err
	}
	defer unix.Close(rootFD)
	var stat unix.Stat_t
	if err := unix.Fstat(rootFD, &stat); err != nil {
		return attachmentRootIdentity{}, err
	}
	return attachmentRootIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func removeAttachmentFileNoFollow(rootDir, pathRelative string, expectedRoot attachmentRootIdentity) error {
	rootFD, err := openAttachmentRootFD(rootDir, pathRelative)
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(rootFD, &stat); err != nil {
		_ = unix.Close(rootFD)
		return err
	}
	if uint64(stat.Dev) != expectedRoot.device || stat.Ino != expectedRoot.inode {
		_ = unix.Close(rootFD)
		return fmt.Errorf("attachment root identity changed before failed-restore cleanup")
	}
	parentFD, leaf, err := openAttachmentParentFromFD(rootFD, pathRelative, false)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	return unix.Unlinkat(parentFD, leaf, 0)
}

func randomAttachmentTempLeaf() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".gohealthcli-attachment-" + hex.EncodeToString(value[:]), nil
}

func attachmentPathOpenError(pathRelative string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return fmt.Errorf("attachment path %q contains a symbolic link or non-directory parent: %w", pathRelative, err)
	}
	return err
}

//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type platformRawOutput struct {
	parentFD   int
	targetLeaf string
	stageLeaf  string
	file       *os.File
	closed     bool
	committed  bool
}

func rawOutputPlatformSupported() error { return nil }

func openPlatformRawOutput(path string) (rawOutputDestination, error) {
	parentFD, err := unix.Open(filepath.Dir(path), rawOutputParentOpenFlags(), 0)
	if err != nil {
		return nil, fmt.Errorf("open --output parent: %w", err)
	}
	if err := validateUnixRawOutputParent(parentFD); err != nil {
		_ = unix.Close(parentFD)
		return nil, err
	}
	stageLeaf, err := randomRawOutputLeaf()
	if err != nil {
		_ = unix.Close(parentFD)
		return nil, fmt.Errorf("name --output staging file: %w", err)
	}
	fd, err := unix.Openat(parentFD, stageLeaf, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		_ = unix.Close(parentFD)
		return nil, fmt.Errorf("create --output staging file: %w", err)
	}
	file := os.NewFile(uintptr(fd), stageLeaf)
	if file == nil {
		_ = unix.Close(fd)
		_ = unix.Close(parentFD)
		return nil, fmt.Errorf("create --output staging file")
	}
	return &platformRawOutput{
		parentFD:   parentFD,
		targetLeaf: filepath.Base(path),
		stageLeaf:  stageLeaf,
		file:       file,
	}, nil
}

func validateUnixRawOutputParent(parentFD int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(parentFD, &stat); err != nil {
		return fmt.Errorf("inspect --output parent: %w", err)
	}
	mode := os.FileMode(stat.Mode).Perm()
	if stat.Uid != uint32(os.Geteuid()) || mode&0o022 != 0 {
		return &rawOutputValidationError{err: fmt.Errorf("--output parent must be owned by the effective user and not writable by group or other users; got mode %04o", mode)}
	}
	return validateUnixRawOutputParentPlatform(parentFD)
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
	if err := publishStagedRawOutputAt(output.parentFD, output.stageLeaf, output.targetLeaf); err != nil {
		return err
	}
	output.committed = true
	// Publication is irreversible and the complete file is now visible. A
	// later descriptor-close error must not report a failed command that left
	// an apparently complete destination.
	_ = output.closeParent()
	return nil
}

func (output *platformRawOutput) Abort() error {
	closeErr := output.Close()
	var unlinkErr error
	if !output.committed {
		unlinkErr = unix.Unlinkat(output.parentFD, output.stageLeaf, 0)
		if errors.Is(unlinkErr, unix.ENOENT) {
			unlinkErr = nil
		}
	}
	parentErr := output.closeParent()
	return errors.Join(closeErr, unlinkErr, parentErr)
}

func (output *platformRawOutput) closeParent() error {
	if output.parentFD < 0 {
		return nil
	}
	err := unix.Close(output.parentFD)
	output.parentFD = -1
	return err
}

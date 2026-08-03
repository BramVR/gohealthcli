//go:build !windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockHeadlessClaimFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, fmt.Errorf("headless claim lock is not owner-only: mode %04o, want 0600", info.Mode().Perm())
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func unlockHeadlessClaimFile(file *os.File) error {
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

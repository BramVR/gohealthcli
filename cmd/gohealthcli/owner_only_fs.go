package main

import (
	"fmt"
	"os"
	"runtime"
)

func ensureOwnerOnlyDir(dir string) error {
	if info, err := os.Stat(dir); err == nil {
		return validateOwnerOnlyDirInfo(dir, info)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if !usesPOSIXPermissions() {
		return nil
	}
	return os.Chmod(dir, 0o700)
}

func validateOwnerOnlyDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	return validateOwnerOnlyDirInfo(dir, info)
}

func validateOwnerOnlyDirInfo(dir string, info os.FileInfo) error {
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o700 {
		mode := info.Mode().Perm()
		return fmt.Errorf("%s is not owner-only: mode %04o, want 0700", dir, mode)
	}
	return nil
}

func usesPOSIXPermissions() bool {
	return runtime.GOOS != "windows"
}

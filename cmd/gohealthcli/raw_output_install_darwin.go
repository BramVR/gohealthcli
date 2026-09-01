//go:build darwin

package main

import "golang.org/x/sys/unix"

func publishStagedRawOutput(stagedPath, targetPath string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, stagedPath, unix.AT_FDCWD, targetPath, unix.RENAME_EXCL)
}

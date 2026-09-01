//go:build linux

package main

import "golang.org/x/sys/unix"

func rawOutputParentOpenFlags() int {
	return unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC
}

func validateUnixRawOutputParentPlatform(int) error { return nil }

func publishStagedRawOutputAt(parentFD int, stagedLeaf, targetLeaf string) error {
	return unix.Renameat2(parentFD, stagedLeaf, parentFD, targetLeaf, unix.RENAME_NOREPLACE)
}

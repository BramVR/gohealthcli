//go:build windows

package main

import "golang.org/x/sys/windows"

func publishStagedRawOutput(stagedPath, targetPath string) error {
	staged, err := windows.UTF16PtrFromString(stagedPath)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(staged, target, 0)
}

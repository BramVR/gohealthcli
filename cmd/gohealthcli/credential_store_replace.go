//go:build !windows

package main

import "os"

func replaceCredentialStoreFile(sourcePath, targetPath string) error {
	return os.Rename(sourcePath, targetPath)
}

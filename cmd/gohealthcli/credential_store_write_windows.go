//go:build windows

package main

import "os"

func writeCredentialStoreFile(path string, content []byte) error {
	return os.WriteFile(path, content, 0o600)
}

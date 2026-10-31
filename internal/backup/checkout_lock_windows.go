//go:build windows

package backup

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func lockBackupCheckout(repo string) (*os.File, error) {
	path := filepath.Join(filepath.Dir(repo), "."+filepath.Base(repo)+".gohealthcli-backup.lock")
	return lockBackupFile(path)
}

func lockBackupConfig(configPath string) (*os.File, error) {
	return lockBackupFile(configPath + ".lock")
}

func lockBackupFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func unlockBackupConfig(file *os.File) error { return unlockBackupCheckout(file) }

func unlockBackupCheckout(file *os.File) error {
	overlapped := new(windows.Overlapped)
	unlockErr := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

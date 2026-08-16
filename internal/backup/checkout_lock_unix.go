//go:build !windows

package backup

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockBackupCheckout(repo string) (*os.File, error) {
	path := filepath.Join(filepath.Dir(repo), "."+filepath.Base(repo)+".gohealthcli-backup.lock")
	return lockOwnerOnlyBackupFile(path, "backup checkout")
}

func lockBackupConfig(configPath string) (*os.File, error) {
	return lockOwnerOnlyBackupFile(configPath+".lock", "backup config")
}

func lockOwnerOnlyBackupFile(path, label string) (*os.File, error) {
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
		return nil, fmt.Errorf("%s lock is not owner-only: mode %04o, want 0600", label, info.Mode().Perm())
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func unlockBackupConfig(file *os.File) error { return unlockBackupCheckout(file) }

func unlockBackupCheckout(file *os.File) error {
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

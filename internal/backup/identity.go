package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

func EnsureIdentity(path string) (string, error) {
	path, recipient, found, err := inspectIdentity(path)
	if err != nil {
		return "", err
	}
	if found {
		return recipient, nil
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".backup-age-identity.tmp-*")
	if err != nil {
		return "", err
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", err
	}
	body := "# gohealthcli encrypted backup age identity\n" +
		"# Keep this file private. Losing every configured identity makes the backup unrecoverable.\n" +
		identity.String() + "\n"
	if _, err := io.WriteString(file, body); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return EnsureIdentity(path)
		}
		return "", err
	}
	if err := hardenPrivatePath(path, false); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return identity.Recipient().String(), nil
}

func preflightIdentity(path string) error {
	_, _, _, err := inspectIdentity(path)
	return err
}

func inspectIdentity(path string) (string, string, bool, error) {
	path, err := filepath.Abs(expandHome(path))
	if err != nil {
		return "", "", false, err
	}
	if err := rejectSymlinkedPathComponents(path); err != nil {
		return "", "", false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := validatePrivateParentIfPresent(path); err != nil {
			return "", "", false, err
		}
		return path, "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", false, fmt.Errorf("age identity %s must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return "", "", false, fmt.Errorf("age identity %s must be a regular file", path)
	}
	if err := validatePrivateDir(filepath.Dir(path)); err != nil {
		return "", "", false, err
	}
	if err := validatePrivateMode(path, info, 0o600); err != nil {
		return "", "", false, err
	}
	recipient, err := recipientFromIdentityFile(path)
	if err != nil {
		return "", "", false, err
	}
	return path, recipient, true, nil
}

func ValidateRecipients(values []string) error {
	count := 0
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, err := age.ParseX25519Recipient(value); err != nil {
			return fmt.Errorf("parse age recipient: %w", err)
		}
		count++
	}
	if count == 0 {
		return errors.New("at least one age recipient is required")
	}
	return nil
}

func recipientFromIdentityFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		identity, err := age.ParseX25519Identity(line)
		if err != nil {
			return "", fmt.Errorf("parse age identity: %w", err)
		}
		return identity.Recipient().String(), nil
	}
	return "", errors.New("age identity file is empty")
}

// Package config resolves the local filesystem locations gohealthcli uses.
//
// This slice only needs the effective config and archive paths so that the
// doctor command can report whether a local setup exists. Loading and writing
// config contents belongs to later slices.
package config

import (
	"os"
	"path/filepath"
)

// Paths holds the resolved local locations for one gohealthcli invocation.
type Paths struct {
	// Config is the path to the gohealthcli config file.
	Config string
	// Archive is the path to the local SQLite Health Archive.
	Archive string
}

// Resolve returns effective paths, applying default locations whenever the
// corresponding flag value is empty. Explicit flag values always win so that
// callers (and tests) can point at temporary locations.
func Resolve(configFlag, dbFlag string) (Paths, error) {
	cfg := configFlag
	if cfg == "" {
		def, err := defaultConfigPath()
		if err != nil {
			return Paths{}, err
		}
		cfg = def
	}

	db := dbFlag
	if db == "" {
		def, err := defaultArchivePath()
		if err != nil {
			return Paths{}, err
		}
		db = def
	}

	return Paths{Config: cfg, Archive: db}, nil
}

// defaultConfigPath follows the documented layout:
// ~/.config/gohealthcli/config.toml (honoring XDG_CONFIG_HOME).
func defaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gohealthcli", "config.toml"), nil
}

// defaultArchivePath follows the documented layout:
// ~/.local/share/gohealthcli/gohealthcli.sqlite (honoring XDG_DATA_HOME).
func defaultArchivePath() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "gohealthcli", "gohealthcli.sqlite"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "gohealthcli", "gohealthcli.sqlite"), nil
}

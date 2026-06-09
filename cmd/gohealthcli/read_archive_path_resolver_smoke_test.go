package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadCommandsOpenExplicitDBWithoutConfig pins the PRD #144 slice 1
// (issue #155) acceptance criteria for the four read commands:
//   - `status --db <path>`
//   - `query --db <path> <sql>`
//   - `export --db <path> <dataset> --stdout`
//   - `describe-schema --db <path>`
//
// Each must open the supplied temp archive WITHOUT requiring a config
// file (the read-side relaxation). The default config path is steered
// into a tempdir that holds no config.toml, so the missing-default-
// config branch is the only one that can fire.
func TestReadCommandsOpenExplicitDBWithoutConfig(t *testing.T) {
	tempDir := t.TempDir()
	// Steer defaultConfigPath() at a tempdir that contains no config so
	// the read-side resolver's "no config required" branch is the only
	// path that produces the success exit code below.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "missing-config-xdg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "missing-data-xdg"))

	archivePath := filepath.Join(tempDir, "scratch", "scratch.sqlite")
	if err := createArchive(archivePath); err != nil {
		t.Fatalf("create scratch archive: %v", err)
	}

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "status",
			args: []string{"status", "--db", archivePath, "--json"},
		},
		{
			name: "query",
			args: []string{"query", "--db", archivePath, "--json", "SELECT 1 AS one"},
		},
		{
			name: "export",
			// CSV format prints a header row even for an empty archive, so
			// the success-mode stdout is observably non-empty without
			// needing fixture rows.
			args: []string{"export", "--db", archivePath, "--stdout", "--format", "csv", "daily-steps"},
		},
		{
			name: "describe-schema",
			args: []string{"describe-schema", "--db", archivePath, "--json"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run(tc.args, stdout, stderr)
			if code != 0 {
				t.Fatalf("%s exit code = %d, want 0\nstdout: %s\nstderr: %s",
					tc.name, code, stdout.String(), stderr.String())
			}
			// Every read command's success output must mention SOMETHING —
			// an empty stdout would be a regression. Each command's
			// detailed shape is pinned by its own test file; here we only
			// pin the resolver-honours-it surface.
			if strings.TrimSpace(stdout.String()) == "" {
				t.Fatalf("%s success stdout was empty\nstderr: %s", tc.name, stderr.String())
			}
		})
	}
}

// TestReadCommandsExplicitDBWinsOverDefaultConfig pins the read-side
// relaxation when a default config exists but the user passes --db to
// steer the binary at a different archive. The default config's
// archive_path disagrees on purpose; --db must win without an agreement
// error fires (the agreement check is reserved for the case where both
// --config and --db are passed explicitly).
func TestReadCommandsExplicitDBWinsOverDefaultConfig(t *testing.T) {
	tempDir := t.TempDir()
	// initializeFileCredentialSetup writes a config that points at one
	// archive; we then point XDG_CONFIG_HOME at that config so it
	// becomes the default. The explicit --db steers at a different
	// archive in the same tempdir.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "xdg-data"))

	defaultConfigDir := filepath.Join(tempDir, "xdg-config", "gohealthcli")
	defaultConfig := filepath.Join(defaultConfigDir, "config.toml")
	configArchive := filepath.Join(tempDir, "config-archive", "archive.sqlite")
	if err := createArchive(configArchive); err != nil {
		t.Fatalf("create config archive: %v", err)
	}
	writeOwnerOnlyTOML(t, defaultConfig, "archive_path = \""+configArchive+"\"\n")

	otherArchive := filepath.Join(tempDir, "other", "other.sqlite")
	if err := createArchive(otherArchive); err != nil {
		t.Fatalf("create other archive: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--db", otherArchive, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status --db <other> exit code = %d, want 0 (--db must win over default config)\nstdout: %s\nstderr: %s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), otherArchive) {
		t.Fatalf("status stdout did not mention the --db archive path %q; got:\n%s", otherArchive, stdout.String())
	}
	if strings.Contains(stdout.String(), configArchive) {
		t.Fatalf("status stdout mentioned the config's archive path %q; --db should have won. stdout:\n%s", configArchive, stdout.String())
	}
}

func writeOwnerOnlyTOML(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

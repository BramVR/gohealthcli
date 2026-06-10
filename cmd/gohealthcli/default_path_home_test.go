package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultPathsFailLoudlyWhenHomeUnset pins issue #249: when neither
// HOME nor the XDG_* overrides can anchor the default config / archive
// paths to the user's home directory, the binary must fail with a clear
// error instead of silently falling back to CWD-relative paths and
// writing personal health data under the current working directory.
func TestDefaultPathsFailLoudlyWhenHomeUnset(t *testing.T) {
	// Run from a scratch CWD so any (buggy) CWD-relative creation lands
	// somewhere we can assert about, not the repo checkout.
	tempDir := t.TempDir()
	chdir(t, tempDir)

	// Strip every anchor the default-path resolvers consult.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--json"}, stdout, stderr)
	if code == 0 {
		t.Fatalf("status with HOME/XDG unset exited 0, want a loud failure\nstdout: %s\nstderr: %s",
			stdout.String(), stderr.String())
	}

	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "home directory") {
		t.Fatalf("error output did not mention the home directory; got:\nstdout: %s\nstderr: %s",
			stdout.String(), stderr.String())
	}

	// The whole point: no .config/ or .local/ tree gets created under CWD.
	for _, leaked := range []string{".config", ".local"} {
		if _, err := os.Stat(filepath.Join(tempDir, leaked)); err == nil {
			t.Fatalf("binary created %s under the working directory; it must fail loudly instead", leaked)
		}
	}
}

// TestDefaultPathsRejectRelativeXDG pins the XDG Base Directory spec rule
// that a RELATIVE XDG_CONFIG_HOME / XDG_DATA_HOME value "MUST be ignored".
// With HOME also unset, a relative override must travel the same loud
// failure path as a fully unset environment — never a CWD-relative write.
func TestDefaultPathsRejectRelativeXDG(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configHome string
		dataHome   string
	}{
		{name: "relative XDG_CONFIG_HOME", configHome: "relative/config", dataHome: ""},
		{name: "relative XDG_DATA_HOME", configHome: "", dataHome: "relative/data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			chdir(t, tempDir)
			t.Setenv("HOME", "")
			t.Setenv("XDG_CONFIG_HOME", tc.configHome)
			t.Setenv("XDG_DATA_HOME", tc.dataHome)

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run([]string{"status", "--json"}, stdout, stderr)
			if code == 0 {
				t.Fatalf("status with a relative XDG value exited 0, want a loud failure\nstdout: %s\nstderr: %s",
					stdout.String(), stderr.String())
			}
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, "home directory") {
				t.Fatalf("error output did not mention the home directory; got:\nstdout: %s\nstderr: %s",
					stdout.String(), stderr.String())
			}
			for _, leaked := range []string{".config", ".local", "relative"} {
				if _, err := os.Stat(filepath.Join(tempDir, leaked)); err == nil {
					t.Fatalf("binary created %s under the working directory; it must fail loudly instead", leaked)
				}
			}
		})
	}
}

// TestDefaultPathsFallBackToHomeWhenXDGRelative pins the other half of the
// XDG rule: a relative XDG_* value is ignored, so when HOME is a valid
// absolute directory the resolver must fall through to the HOME-anchored
// default (an absolute path) rather than failing. This guards against an
// over-eager fix that rejects every relative XDG outright even when HOME
// could anchor the path.
func TestDefaultPathsFallBackToHomeWhenXDGRelative(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	t.Setenv("XDG_DATA_HOME", "relative/data")

	gotConfig := defaultConfigPath()
	wantConfig := filepath.Join(home, ".config", "gohealthcli", "config.toml")
	if gotConfig != wantConfig {
		t.Fatalf("defaultConfigPath() = %q, want HOME-anchored %q", gotConfig, wantConfig)
	}

	gotArchive := defaultArchivePath()
	wantArchive := filepath.Join(home, ".local", "share", "gohealthcli", "gohealthcli.sqlite")
	if gotArchive != wantArchive {
		t.Fatalf("defaultArchivePath() = %q, want HOME-anchored %q", gotArchive, wantArchive)
	}
}

// TestExplicitFlagsUnaffectedByUnsetHome pins the issue #249 exemption: a
// user who steers their own absolute --config/--db is unaffected by an
// unset HOME — the loud failure is reserved for the unanchored DEFAULT.
func TestExplicitFlagsUnaffectedByUnsetHome(t *testing.T) {
	tempDir := t.TempDir()
	chdir(t, tempDir)
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	archivePath := filepath.Join(tempDir, "explicit", "archive.sqlite")
	if err := createArchive(archivePath); err != nil {
		t.Fatalf("create archive: %v", err)
	}
	configPath := filepath.Join(tempDir, "explicit", "config.toml")
	writeOwnerOnlyTOML(t, configPath, "archive_path = \""+archivePath+"\"\n")

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status with explicit --config/--db and HOME unset exited %d, want 0\nstdout: %s\nstderr: %s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), archivePath) {
		t.Fatalf("status stdout did not mention the explicit --db path %q; got:\n%s", archivePath, stdout.String())
	}
}

// TestInitFailsLoudlyWhenHomeUnset pins the headline footgun from issue
// #249: `init` is the command that WRITES the config and creates the
// Health Archive, so with HOME/XDG unset it must fail before writing
// anything CWD-relative — no config.toml, no .config/, no .local/ tree.
func TestInitFailsLoudlyWhenHomeUnset(t *testing.T) {
	tempDir := t.TempDir()
	chdir(t, tempDir)
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"init", "--oauth-client-file", "client.json", "--json"}, stdout, stderr)
	if code == 0 {
		t.Fatalf("init with HOME/XDG unset exited 0, want a loud failure\nstdout: %s\nstderr: %s",
			stdout.String(), stderr.String())
	}
	if combined := stdout.String() + stderr.String(); !strings.Contains(combined, "home directory") {
		t.Fatalf("init error did not mention the home directory; got:\nstdout: %s\nstderr: %s",
			stdout.String(), stderr.String())
	}

	// init must not have written a single byte under the working directory.
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("readdir tempDir: %v", err)
	}
	for _, e := range entries {
		t.Fatalf("init created %q under the working directory; it must fail loudly before writing anything", e.Name())
	}
}

// chdir switches into dir for the duration of the test and restores the
// prior working directory afterward.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

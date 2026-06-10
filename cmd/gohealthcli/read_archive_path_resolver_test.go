package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadArchivePathResolverResolves is the table-driven contract for
// PRD #144 slice 1 (issue #155): the read-command Health Archive path
// resolver routes every `(configExists, configArchivePath, configExplicit,
// dbExplicit, dbValue)` shape to a single resolved path or a single
// error that names `--db` and `--config` — never the internal
// `archive_path` field.
//
// Read commands (`status`, `query`, `export`, `describe-schema`) thread
// every flag combination through this one seam, so each row here pins
// the externally observable behaviour every read command relies on.
func TestReadArchivePathResolverResolves(t *testing.T) {
	tempDir := t.TempDir()

	// Two distinct, real-looking archive paths the matrix swaps between.
	configArchivePath := filepath.Join(tempDir, "config-archive.sqlite")
	dbArchivePath := filepath.Join(tempDir, "db-archive.sqlite")

	// A valid config file pointing at configArchivePath; some rows skip
	// it so we exercise "default config does not exist" too. The
	// "missing" config path uses the binary's default so the read-side
	// relaxation (no config required for a read command) fires the same
	// way it would for a real user.
	configPath := filepath.Join(tempDir, "config", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configBody := "archive_path = \"" + configArchivePath + "\"\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Point the default config path at a non-existent file inside the
	// test's tempdir so the "default config missing" branch
	// (`readConfigArchivePath` tolerates it) fires for the rows that
	// pass `missingDefaultConfigPath` below. The path itself is the
	// binary's `defaultConfigPath()` after the XDG override — that is
	// the only shape `readConfigArchivePath` tolerates as "missing OK".
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "missing-config-xdg"))
	missingDefaultConfigPath := defaultConfigPath()
	if _, err := os.Stat(missingDefaultConfigPath); !os.IsNotExist(err) {
		t.Fatalf("test setup: expected missingDefaultConfigPath to not exist, got %v", err)
	}

	tests := []struct {
		name            string
		configPath      string
		configExplicit  bool
		archivePath     string
		archiveExplicit bool
		wantResolved    string
		wantErrContains []string
		wantErrOmits    []string
	}{
		{
			name:            "config missing and --db default uses --db value",
			configPath:      missingDefaultConfigPath,
			configExplicit:  false,
			archivePath:     dbArchivePath,
			archiveExplicit: false,
			wantResolved:    dbArchivePath,
		},
		{
			name:            "config missing and --db explicit uses --db value",
			configPath:      missingDefaultConfigPath,
			configExplicit:  false,
			archivePath:     dbArchivePath,
			archiveExplicit: true,
			wantResolved:    dbArchivePath,
		},
		{
			name:            "config valid and --db default uses config archive_path",
			configPath:      configPath,
			configExplicit:  false,
			archivePath:     dbArchivePath,
			archiveExplicit: false,
			wantResolved:    configArchivePath,
		},
		{
			name:            "config explicit and --db default uses config archive_path",
			configPath:      configPath,
			configExplicit:  true,
			archivePath:     dbArchivePath,
			archiveExplicit: false,
			wantResolved:    configArchivePath,
		},
		{
			name:            "config default and --db explicit lets --db win without agreement check",
			configPath:      configPath,
			configExplicit:  false,
			archivePath:     dbArchivePath,
			archiveExplicit: true,
			wantResolved:    dbArchivePath,
		},
		{
			name:            "config explicit and --db explicit agree returns the shared path",
			configPath:      configPath,
			configExplicit:  true,
			archivePath:     configArchivePath,
			archiveExplicit: true,
			wantResolved:    configArchivePath,
		},
		{
			name:            "config explicit and --db explicit disagree errors naming --db and --config",
			configPath:      configPath,
			configExplicit:  true,
			archivePath:     dbArchivePath,
			archiveExplicit: true,
			wantErrContains: []string{"--db", "--config", dbArchivePath, configPath},
			wantErrOmits:    []string{"archive_path"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := readArchivePathResolver{
				configPath:          tc.configPath,
				configPathExplicit:  tc.configExplicit,
				archivePath:         tc.archivePath,
				archivePathExplicit: tc.archiveExplicit,
			}
			got, err := resolver.Resolve()
			if len(tc.wantErrContains) > 0 {
				if err == nil {
					t.Fatalf("Resolve returned nil error, want one containing %v", tc.wantErrContains)
				}
				for _, want := range tc.wantErrContains {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q missing substring %q", err.Error(), want)
					}
				}
				for _, omit := range tc.wantErrOmits {
					if strings.Contains(err.Error(), omit) {
						t.Errorf("error %q must not mention %q", err.Error(), omit)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if got != tc.wantResolved {
				t.Fatalf("Resolve = %q, want %q", got, tc.wantResolved)
			}
		})
	}
}

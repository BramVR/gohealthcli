package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// migratedCommonFlagSubcommands enumerates the subcommands whose runtime
// flag setup goes through the CommonFlagSet module (issue #166). The
// tests below assert that every entry honours the shared invariants the
// module owns, so a future migration that forgets to wire something up
// fails here rather than at runtime.
var migratedCommonFlagSubcommands = []string{
	"identity",
	"profile",
	"settings",
	"devices",
	"irn-profile",
	"status",
}

// TestMigratedSubcommandsRejectPlainAndJSON locks in acceptance criterion
// (a) of issue #166: every migrated subcommand prints the documented
// mutual-exclusion error and exits 1 when --plain and --json are passed
// together.
func TestMigratedSubcommandsRejectPlainAndJSON(t *testing.T) {
	for _, name := range migratedCommonFlagSubcommands {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, name, "--plain", "--json")
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			const want = "--plain and --json are mutually exclusive"
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), want)
			}
		})
	}
}

// TestMigratedSubcommandsFlowConfigAndDBThrough locks in acceptance
// criterion (b): --config X --db Y flows through to the runtime values.
// We point at non-existent paths inside a uniquely-named parent
// directory; the subcommand's "config check failed" error names the
// directory, proving that RegisterCommon wired the flags onto the
// right variables. The parent-directory name (rather than the leaf
// filename) is the substring we look for because some subcommands
// stat the parent and stop there.
func TestMigratedSubcommandsFlowConfigAndDBThrough(t *testing.T) {
	tempDir := t.TempDir()
	uniqueMarker := "common-flag-set-flowthrough-marker"
	parent := filepath.Join(tempDir, uniqueMarker)
	configPath := filepath.Join(parent, "config.toml")
	dbPath := filepath.Join(parent, "archive.sqlite")

	for _, name := range migratedCommonFlagSubcommands {
		t.Run(name, func(t *testing.T) {
			_, stdout, stderr := runCommand(t, name, "--config", configPath, "--db", dbPath)
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, uniqueMarker) {
				t.Fatalf("flag-supplied path marker %q never surfaced in output, so --config / --db did not flow through; stdout=%q stderr=%q", uniqueMarker, stdout.String(), stderr.String())
			}
		})
	}
}

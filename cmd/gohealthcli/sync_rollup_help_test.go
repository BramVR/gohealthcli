package main

import (
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

// TestSyncHelpRollupUsageListsEveryKind pins issue #147: the --rollup
// flag's `sync --help` description must list every kind the validator
// accepts. The expected kinds derive from
// googlehealth.SupportedRollupKinds — the same list ParseRollupSpec's
// rejection message prints — so a
// future fifth kind that lands without touching the Usage string fails
// here instead of shipping a stale help surface. The registry flagSpec
// (the `schema --json` / docs-regen surface) is held to the same
// contract so the two help surfaces cannot drift apart.
func TestSyncHelpRollupUsageListsEveryKind(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "sync", "--help")
	if code != 0 {
		t.Fatalf("`sync --help` exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	usage := rollupFlagUsageFromHelp(t, stdout.String()+stderr.String())
	for _, kind := range googlehealth.SupportedRollupKinds() {
		if !strings.Contains(usage, kind) {
			t.Errorf("sync --help -rollup usage %q does not list kind %q", usage, kind)
		}
	}
	for _, cmd := range commands {
		if cmd.Name != "sync" {
			continue
		}
		for _, spec := range cmd.Flags {
			if spec.Name != "rollup" {
				continue
			}
			for _, kind := range googlehealth.SupportedRollupKinds() {
				if !strings.Contains(spec.Usage, kind) {
					t.Errorf("sync registry flagSpec rollup usage %q does not list kind %q", spec.Usage, kind)
				}
			}
			return
		}
		t.Fatal("sync commandDef has no rollup flagSpec")
	}
	t.Fatal("command registry has no sync commandDef")
}

// rollupFlagUsageFromHelp extracts the usage text printed under the
// `-rollup` entry of a flag-package help dump (the description sits on
// the indented line after the `-rollup string` header).
func rollupFlagUsageFromHelp(t *testing.T, help string) string {
	t.Helper()
	lines := strings.Split(help, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "-rollup") {
			continue
		}
		if i+1 >= len(lines) {
			break
		}
		return strings.TrimSpace(lines[i+1])
	}
	t.Fatalf("sync --help output has no -rollup entry:\n%s", help)
	return ""
}

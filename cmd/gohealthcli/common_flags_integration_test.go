package main

import (
	"os"
	"path/filepath"
	"regexp"
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
	"query",
	"doctor",
	"init",
	"connect",
	"sync",
}

// TestMigratedSubcommandsRejectPlainAndJSON locks in acceptance criterion
// (a) of issue #166: every migrated subcommand prints the documented
// mutual-exclusion error and exits 1 when --plain and --json are passed
// together.
func TestMigratedSubcommandsRejectPlainAndJSON(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	tempDir := t.TempDir()
	uniqueMarker := "common-flag-set-flowthrough-marker"
	parent := filepath.Join(tempDir, uniqueMarker)
	configPath := filepath.Join(parent, "config.toml")
	dbPath := filepath.Join(parent, "archive.sqlite")

	// Some subcommands need an extra positional or flag before they get
	// as far as surfacing the marker; we feed those so the parser
	// reaches the flag-application stage instead of short-circuiting
	// earlier.
	//
	//   - query: a single SQL positional is required, otherwise the
	//     parser fails with "query requires exactly one SQL statement"
	//     before --db can be resolved.
	//   - connect: --no-input keeps the browser flow from blocking on
	//     stdin during the test; it is still routed through the
	//     migrated common-flag plumbing.
	//   - init: bypasses the OAuth-client preamble by pointing
	//     --oauth-client-file at a stub the test helper creates
	//     automatically (so init reaches the path-resolution stage
	//     where the marker would surface). The marker still appears
	//     because init writes its archive at the --db location.
	extraArgsByCommand := map[string][]string{
		"query":   {"select 1"},
		"connect": {"--no-input"},
		"init":    {"--oauth-client-file", filepath.Join(tempDir, "init-client.json")},
	}

	// Subcommands whose first user-facing failure ("no Connection found",
	// "connect requires browser OAuth") fires upstream of any code that
	// echoes the resolved --config / --db path back into the report.
	// Their wiring is still locked in by TestMigratedSubcommandsReject-
	// PlainAndJSON (the mutual-exclusion error comes from ParseCommon)
	// and the static call-site assertion in TestMigratedSubcommandEntry-
	// PointsCallRegisterCommon, so this stronger flow-through assertion
	// stays scoped to the verbs that surface the path in their output.
	skip := map[string]bool{
		"connect": true,
		"sync":    true,
	}
	for _, name := range migratedCommonFlagSubcommands {
		if skip[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			args := []string{name, "--config", configPath, "--db", dbPath}
			args = append(args, extraArgsByCommand[name]...)
			_, stdout, stderr := runCommand(t, args...)
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, uniqueMarker) {
				t.Fatalf("flag-supplied path marker %q never surfaced in output, so --config / --db did not flow through; stdout=%q stderr=%q", uniqueMarker, stdout.String(), stderr.String())
			}
		})
	}
}

// TestRawRejectsUnsupportedCommonFlags pins acceptance criterion of issue
// #180: `raw`'s flag spec declares only {config, db}, so a stray --plain,
// --json, or --no-input on `raw` yields the Common Flag Set's targeted
// "--<flag> is not supported by raw" wording instead of the legacy
// hand-written walker's "raw: unknown flag" prefix.
func TestRawRejectsUnsupportedCommonFlags(t *testing.T) {
	t.Parallel()
	for _, flagName := range []string{"plain", "json", "no-input"} {
		t.Run(flagName, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, "raw", "--"+flagName, "endpoint", "getIdentity")
			if code == 0 {
				t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			want := "--" + flagName + " is not supported by raw"
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), want)
			}
		})
	}
}

// TestRawRejectsUnknownFlagViaStdlib pins the other half of the acceptance
// criterion: a genuinely unknown flag on `raw` flows through stdlib's
// `flag` package and writes the standard "flag provided but not defined"
// diagnostic, NOT the legacy "raw: unknown flag" wrapper.
func TestRawRejectsUnknownFlagViaStdlib(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "raw", "--bogus")
	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr = %q, want stdlib parse error", stderr.String())
	}
	if strings.Contains(stderr.String(), "raw: unknown flag") {
		t.Fatalf("stderr = %q, still uses legacy hand-written walker wording", stderr.String())
	}
}

// TestMigratedSubcommandEntryPointsCallRegisterCommon asserts that every
// migrated subcommand's parser function contains a RegisterCommon call —
// no remaining inline `flags.String("config", ...)` preamble. This backs
// acceptance criterion #5 of issue #180: a static guarantee that any
// future drift in init / doctor / connect / sync / query / raw (or the
// already-migrated identity / profile / settings / devices / irn-profile
// / status) fails the build. The Identity Snapshot family parses its
// flags inside the shared engine runner (issue #282), so those
// commands anchor on runIdentitySnapshotCommand.
//
// The check is intentionally precise: it pinpoints the function body of
// each subcommand's flag-parser entry point and inspects only that
// region for either RegisterCommon (the migrated form) or the
// inline-preamble signature it replaces. Global state (main.go's
// gohealthcli FlagSet) and divergent subcommands (export, describe-schema,
// schema) are out of scope by design and not consulted.
func TestMigratedSubcommandEntryPointsCallRegisterCommon(t *testing.T) {
	t.Parallel()
	// entry maps each subcommand to the source file and the function-
	// signature substring that anchors its flag-parser entry point. We
	// slice the function body between this anchor and the next top-
	// level `func ` and assert RegisterCommon appears within.
	entries := []struct {
		command string
		file    string
		funcSig string
	}{
		{"init", "init.go", "func runInit("},
		{"doctor", "doctor.go", "func runDoctorWithRuntime("},
		{"connect", "main.go", "func runConnectWithRuntime("},
		{"sync", "main.go", "func runSyncWithRuntime("},
		{"raw", "main.go", "func runRawWithRuntime("},
		{"query", "query.go", "func runQuery("},
		{"identity", "identity_snapshot_command.go", "func runIdentitySnapshotCommand["},
		{"profile", "identity_snapshot_command.go", "func runIdentitySnapshotCommand["},
		{"settings", "identity_snapshot_command.go", "func runIdentitySnapshotCommand["},
		{"devices", "identity_snapshot_command.go", "func runIdentitySnapshotCommand["},
		{"irn-profile", "identity_snapshot_command.go", "func runIdentitySnapshotCommand["},
		{"status", "main.go", "func runStatus("},
	}
	// Per-subcommand legacy preamble lines that would re-appear if a
	// migration was reverted. Each match in the corresponding function
	// body is proof of un-migration.
	preambleSignatures := []*regexp.Regexp{
		regexp.MustCompile(`flags?\.String\("config"`),
		regexp.MustCompile(`flags?\.String\("db"`),
		regexp.MustCompile(`flags?\.Bool\("json"`),
		regexp.MustCompile(`flags?\.Bool\("plain"`),
		regexp.MustCompile(`flags?\.Bool\("no-input"`),
	}
	for _, e := range entries {
		body, err := os.ReadFile(e.file)
		if err != nil {
			t.Fatalf("read %s: %v", e.file, err)
		}
		text := string(body)
		start := strings.Index(text, e.funcSig)
		if start < 0 {
			t.Fatalf("%s: function %q not found", e.file, e.funcSig)
		}
		// The function body ends at the next top-level `\nfunc ` in
		// the same file. If none, it runs to EOF.
		rest := text[start+len(e.funcSig):]
		nextFunc := strings.Index(rest, "\nfunc ")
		end := len(rest)
		if nextFunc >= 0 {
			end = nextFunc
		}
		funcBody := rest[:end]
		if !strings.Contains(funcBody, "RegisterCommon(") {
			t.Errorf("%s: %q does not call RegisterCommon — common flag set migration missing", e.command, e.funcSig)
		}
		for _, re := range preambleSignatures {
			if loc := re.FindIndex([]byte(funcBody)); loc != nil {
				t.Errorf("%s: %q still declares the common flag inline (%q) — should use RegisterCommon instead", e.command, e.funcSig, funcBody[loc[0]:loc[1]])
			}
		}
	}
}

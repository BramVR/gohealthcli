package main

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestEveryCommandHasRunAdapter locks down the single-source-of-truth
// contract introduced by PRD #143 slice 6 (issue #175): every entry in
// the registry — visible OR hidden — must carry a non-nil Run adapter,
// because runWithRuntime dispatches through the registry rather than a
// hand-written switch. A new subcommand that lands without wiring Run
// would be caught by runWithRuntime's defensive nil-Run branch at
// invocation time (clean "internal error" exit instead of a panic);
// this test catches the same omission at build time so the regression
// never ships in the first place.
func TestEveryCommandHasRunAdapter(t *testing.T) {
	t.Parallel()
	for _, cmd := range commands {
		if cmd.Run == nil {
			t.Errorf("command %q has nil Run adapter; every registry entry must implement Run", cmd.Name)
		}
	}
}

// TestEveryVisibleCommandHelpExitsZero is the registry-driven smoke check
// PRD #143 slice 6 requires: iterate every visible registry entry and
// invoke `<cmd> --help`, asserting exit code 0. Because the dispatch
// table and the --help printer now read from the same registry, this
// guarantees there is no entry that documents itself but cannot actually
// dispatch. Hidden entries (the build-time `schema`) are skipped because
// they are intentionally not part of the end-user surface.
func TestEveryVisibleCommandHelpExitsZero(t *testing.T) {
	t.Parallel()
	for _, cmd := range commands {
		if cmd.Hidden {
			continue
		}
		t.Run(cmd.Name, func(t *testing.T) {
			code, _, stderr := runCommand(t, cmd.Name, "--help")
			if code != 0 {
				t.Fatalf("`%s --help` exit code = %d, want 0\nstderr: %s", cmd.Name, code, stderr.String())
			}
		})
	}
}

// TestRunWithRuntimeRejectsNilRunAdapter pins the defensive guard in
// dispatch: a registry entry with a nil Run adapter exits with a
// clean "internal error" message instead of panicking. The guard is
// belt-and-braces (TestEveryCommandHasRunAdapter already pins the same
// invariant at build time), but the test means future maintainers can
// see the failure mode at a glance instead of inferring it from a
// stack trace. The synthetic commandDef exercises dispatchCommand —
// the exact guard runWithRuntime routes every lookup through — without
// mutating the package-level registry (#283).
func TestRunWithRuntimeRejectsNilRunAdapter(t *testing.T) {
	t.Parallel()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := dispatchCommand(commandDef{Name: "schema"}, nil, CommonFlagValues{}, stdout, stderr, runtimeAdapters{})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	want := `internal error: command "schema" has no Run adapter`
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr missing %q\ngot: %s", want, stderr.String())
	}
}

// TestLookupCommandFindsEveryRegistryEntryIncludingHidden pins the lookup
// contract the registry-keyed dispatch depends on (#75): every entry —
// visible OR hidden — is reachable by name, and the returned copy carries
// the wired Run adapter. The Run assertion matters because the `schema`
// entry's adapter is bound in init() after the `commands` literal is
// initialised; an index that snapshotted entry VALUES before that wiring
// would hand dispatch a nil Run for schema, and this test would catch it.
func TestLookupCommandFindsEveryRegistryEntryIncludingHidden(t *testing.T) {
	t.Parallel()
	for _, cmd := range commands {
		got, ok := lookupCommand(cmd.Name)
		if !ok {
			t.Errorf("lookupCommand(%q) reported not found; every registry entry must be reachable by name", cmd.Name)
			continue
		}
		if got.Name != cmd.Name {
			t.Errorf("lookupCommand(%q) returned entry %q", cmd.Name, got.Name)
		}
		if got.Run == nil {
			t.Errorf("lookupCommand(%q) returned an entry with a nil Run adapter; lookup must observe the init()-wired adapters", cmd.Name)
		}
	}
}

// TestLookupCommandRejectsUnknownName pins the miss side of the contract:
// an unknown name reports ok=false so runWithRuntime routes to the
// unknown-command failure path (existing error text + exit code, #75 AC).
func TestLookupCommandRejectsUnknownName(t *testing.T) {
	t.Parallel()
	if got, ok := lookupCommand("definitely-not-a-command"); ok {
		t.Fatalf("lookupCommand(\"definitely-not-a-command\") = %q, want not found", got.Name)
	}
}

// TestCommandNamesAreUnique pins the registry invariant that name-keyed
// dispatch (#75) silently depends on: two entries sharing a Name would
// make one of them unreachable. The init()-time index build is the actual
// enforcement — a duplicate panics during package init, before this test
// ever runs — so this test documents and pins the invariant rather than
// catching it first.
func TestCommandNamesAreUnique(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool, len(commands))
	for _, cmd := range commands {
		if seen[cmd.Name] {
			t.Errorf("duplicate command name %q in registry", cmd.Name)
		}
		seen[cmd.Name] = true
	}
}

// TestSuggestReturnsCloseCommand exercises the canonical one-typo case from
// the PRD: a single transposition in "status" should surface that command as
// the suggestion. This test is intentionally narrow — it locks in the
// Levenshtein ≤ 2 contract through the public Suggest entry point so any
// future tweak to the helper (different metric, different threshold) still
// has to honour the AC.
func TestSuggestReturnsCloseCommand(t *testing.T) {
	t.Parallel()
	got := commandRegistry(commands).Suggest("stauts")
	want := []string{"status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Suggest(\"stauts\") = %v, want %v", got, want)
	}
}

// TestSuggestReturnsNilForDistantTypo pins the upper-bound side of the
// Levenshtein contract: a string with no close match must surface as the
// empty result so the unknown-command path can suppress the "Did you mean"
// line entirely. The AC calls this out explicitly ("xyz" → no suggestion).
func TestSuggestReturnsNilForDistantTypo(t *testing.T) {
	t.Parallel()
	got := commandRegistry(commands).Suggest("xyz")
	if len(got) != 0 {
		t.Fatalf("Suggest(\"xyz\") = %v, want empty slice", got)
	}
}

// TestSuggestExcludesHiddenCommands asserts that the build-time `schema`
// entry (the only Hidden command in the registry today) never surfaces as
// a suggestion. We type a one-character transposition of "schema" so the
// Levenshtein distance is 2 — well inside the cutoff — and verify the name
// is filtered out at the registry layer rather than relying on UX polish at
// the call site. AC: "Hidden commands (schema) are excluded from Suggest".
func TestSuggestExcludesHiddenCommands(t *testing.T) {
	t.Parallel()
	got := commandRegistry(commands).Suggest("shcema")
	for _, name := range got {
		if name == "schema" {
			t.Fatalf("Suggest(\"shcema\") returned hidden command 'schema': %v", got)
		}
	}
}

// TestDispatchCarriesGlobalSlotFlagsIntoRunners pins the flag-passing
// contract between runWithRuntime's dispatch and the per-command runners
// across the issue #285 signature change (runners accept CommonFlagValues
// directly instead of exploded positional parameters). Flags placed at
// the GLOBAL slot (before the subcommand) must reach the runner:
//
//   - --db carries both the path and the ArchivePathExplicit bit, so the
//     read-side resolver opens the explicit archive without a config;
//   - --json carries the output mode for commands that honour it;
//   - export deliberately DROPS the global-slot --json / --plain: its
//     registry adapter never forwarded the output mode (the synonyms are
//     scoped to export's own flag slot), so `--json export` must keep
//     emitting the --format default (CSV), byte-identical to the run
//     without --json.
func TestDispatchCarriesGlobalSlotFlagsIntoRunners(t *testing.T) {
	tempDir := t.TempDir()
	// Steer the default config/archive paths away from the developer's
	// real home so the explicit --db is the only way the runs below can
	// succeed (mirrors read_archive_path_resolver_smoke_test.go).
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "missing-config-xdg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "missing-data-xdg"))

	archivePath := filepath.Join(tempDir, "scratch", "scratch.sqlite")
	if err := createArchive(archivePath); err != nil {
		t.Fatalf("create scratch archive: %v", err)
	}

	runOK := func(t *testing.T, args ...string) string {
		t.Helper()
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		if code := run(args, stdout, stderr); code != 0 {
			t.Fatalf("%v exit code = %d, want 0\nstdout: %s\nstderr: %s",
				args, code, stdout.String(), stderr.String())
		}
		return stdout.String()
	}

	t.Run("status sees global --db and --json", func(t *testing.T) {
		out := runOK(t, "--db", archivePath, "--json", "status")
		if !strings.HasPrefix(strings.TrimSpace(out), "{") {
			t.Fatalf("global --json did not reach status; stdout: %s", out)
		}
		if !strings.Contains(out, archivePath) {
			t.Fatalf("global --db did not reach status; stdout: %s", out)
		}
	})

	t.Run("query sees global --db and --json", func(t *testing.T) {
		out := runOK(t, "--db", archivePath, "--json", "query", "SELECT 1 AS one")
		if !strings.HasPrefix(strings.TrimSpace(out), "{") || !strings.Contains(out, `"one"`) {
			t.Fatalf("global --db/--json did not reach query; stdout: %s", out)
		}
	})

	t.Run("describe-schema sees global --db", func(t *testing.T) {
		out := runOK(t, "--db", archivePath, "describe-schema")
		if !strings.HasPrefix(strings.TrimSpace(out), "{") {
			t.Fatalf("global --db did not reach describe-schema; stdout: %s", out)
		}
	})

	t.Run("export sees global --db but drops global --json", func(t *testing.T) {
		plainRun := runOK(t, "--db", archivePath, "export", "--stdout", "daily-steps")
		jsonRun := runOK(t, "--db", archivePath, "--json", "export", "--stdout", "daily-steps")
		if !strings.Contains(plainRun, ",") {
			t.Fatalf("export CSV header missing; stdout: %s", plainRun)
		}
		if jsonRun != plainRun {
			t.Fatalf("global --json changed export output; without: %q with: %q", plainRun, jsonRun)
		}
	})
}

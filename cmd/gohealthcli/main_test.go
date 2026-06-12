package main

// main_test.go holds the tests of main.go's own feature: top-level
// dispatch — the no-args help, the help verb and --help forms, and the
// unknown-command path. Per-feature command tests live next to their
// features (doctor_test.go, sync_test.go, ...), the shared fixtures and
// drivers live in harness_test.go, and the few tests that need the
// compiled binary live in binary_smoke_test.go (issue #286).

import (
	"strings"
	"testing"
)

// TestNoArgsPrintsHelpToStdout pins the PRD #143 slice 3 behaviour: a bare
// `gohealthcli` invocation now exits 0 and prints the top-level help (the
// "Subcommands:" listing) to stdout, matching `git` / `kubectl` / `docker`
// discoverability conventions. The explicit `--help` path is unchanged and
// keeps writing to stderr per stdlib flag-package convention — see
// TestHelpExitsSuccessfully below.
func TestNoArgsPrintsHelpToStdout(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Subcommands:") {
		t.Fatalf("stdout missing 'Subcommands:' help block; got:\n%s", stdout.String())
	}
	if strings.Contains(stderr.String(), "missing command") {
		t.Fatalf("stderr still emits 'missing command'; got: %q", stderr.String())
	}
}

// TestUnknownCommandPrintsHintAndExitsNonZero covers the unknown-command
// path for an input that has no close Levenshtein match: stderr must carry
// both the "unknown command" line (preserved from the pre-slice-3 shape) and
// the canonical "Run 'gohealthcli --help'" hint introduced by slice 3, and
// the process must exit 1. We deliberately choose "bogus-cmd" because it is
// far from every registered command, ensuring no "Did you mean" line
// interleaves and the hint line is the sole new addition under test.
func TestUnknownCommandPrintsHintAndExitsNonZero(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "bogus-cmd")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "unknown command: bogus-cmd") {
		t.Fatalf("stderr missing 'unknown command: bogus-cmd'; got: %q", out)
	}
	if !strings.Contains(out, "Run 'gohealthcli --help' for a list of commands.") {
		t.Fatalf("stderr missing canonical help hint; got: %q", out)
	}
	if strings.Contains(out, "Did you mean") {
		t.Fatalf("stderr should not suggest for unrelated typo; got: %q", out)
	}
}

// TestUnknownCommandSuggestsCloseMatch closes the loop on the suggestion
// pipeline: when the typo lands within the Levenshtein cutoff, stderr must
// carry a "Did you mean: <name>?" line between the unknown-command banner
// and the help hint. We assert on the full sentence so a future tweak to
// the suggestion phrasing trips this test rather than silently shipping a
// regression in UX.
func TestUnknownCommandSuggestsCloseMatch(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "stauts")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "Did you mean: status?") {
		t.Fatalf("stderr missing 'Did you mean: status?'; got: %q", out)
	}
	if !strings.Contains(out, "Run 'gohealthcli --help' for a list of commands.") {
		t.Fatalf("stderr missing canonical help hint; got: %q", out)
	}
}

func TestHelpExitsSuccessfully(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--help"},
		{"doctor", "--help"},
	} {
		code, stdout, stderr := runCommand(t, args...)

		if code != 0 {
			t.Fatalf("%v exit code = %d, want 0\nstderr: %s", args, code, stderr.String())
		}
		if stdout.String() != "" {
			t.Fatalf("%v stdout = %q, want empty", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "Usage of") {
			t.Fatalf("%v stderr missing usage: %q", args, stderr.String())
		}
	}
}

func TestTopLevelHelpListsVisibleSubcommandsFromRegistry(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	out := stderr.String()
	// Assert each visible command appears on its OWN dedicated line of the form
	// "  <name>   <short>". A bare strings.Contains check would false-pass when
	// the command name shows up inside another command's Short (e.g. "raw" is
	// substring of the "raw" command's own Short "Print raw provider JSON ...").
	lines := strings.Split(out, "\n")
	for _, cmd := range commands {
		if cmd.Hidden {
			continue
		}
		found := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			fields := strings.SplitN(trimmed, " ", 2)
			if len(fields) == 2 && fields[0] == cmd.Name && strings.Contains(line, cmd.Short) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--help missing dedicated line for %q (%q)\noutput:\n%s", cmd.Name, cmd.Short, out)
		}
	}
}

func TestTopLevelHelpOmitsHiddenSubcommands(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	// The "schema" command is the only hidden entry; assert it is filtered.
	// We look for it as a whole word in a "  schema  <short>" line so that
	// substrings inside other words (e.g. "describe-schema") don't false-positive.
	for _, line := range strings.Split(stderr.String(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "schema ") || trimmed == "schema" {
			t.Fatalf("--help should not list hidden subcommand 'schema'; got line %q\nfull output:\n%s", line, stderr.String())
		}
	}
}

func TestTopLevelHelpStillShowsGlobalFlags(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	out := stderr.String()
	for _, flagName := range []string{"-config", "-db", "-json", "-plain", "-no-input", "-version"} {
		if !strings.Contains(out, flagName) {
			t.Errorf("--help missing global flag %q\noutput:\n%s", flagName, out)
		}
	}
}

// TestHelpVerbMatchesTopLevelHelp asserts `gohealthcli help` is an alias for
// `gohealthcli --help`: same exit code, identical stderr bytes. The verb form
// reads more naturally than the flag form and is what users reach for first.
func TestHelpVerbMatchesTopLevelHelp(t *testing.T) {
	t.Parallel()
	codeFlag, _, stderrFlag := runCommand(t, "--help")
	codeVerb, stdoutVerb, stderrVerb := runCommand(t, "help")

	if codeFlag != 0 {
		t.Fatalf("--help exit code = %d, want 0\nstderr: %s", codeFlag, stderrFlag.String())
	}
	if codeVerb != 0 {
		t.Fatalf("help exit code = %d, want 0\nstderr: %s", codeVerb, stderrVerb.String())
	}
	if stdoutVerb.String() != "" {
		t.Fatalf("help stdout = %q, want empty", stdoutVerb.String())
	}
	if stderrVerb.String() != stderrFlag.String() {
		t.Fatalf("help stderr differs from --help stderr\nhelp:\n%s\n--help:\n%s", stderrVerb.String(), stderrFlag.String())
	}
}

// TestHelpVerbWithKnownSubcommand asserts `gohealthcli help status` prints
// the status subcommand's Long description followed by its accepted flags,
// exits 0, and does not write to stdout.
func TestHelpVerbWithKnownSubcommand(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "help", "status")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	// Look up the entry in the same registry the implementation reads.
	var statusDef commandDef
	for _, cmd := range commands {
		if cmd.Name == "status" {
			statusDef = cmd
			break
		}
	}
	if statusDef.Name == "" {
		t.Fatalf("registry missing status entry; cannot validate help output")
	}

	out := stderr.String()
	// The Long body is multi-paragraph; assert on its first sentence so the
	// test stays meaningful without re-encoding the whole prose.
	firstSentence := strings.SplitN(statusDef.Long, ".", 2)[0]
	if !strings.Contains(out, firstSentence) {
		t.Errorf("help status missing Long prefix %q\noutput:\n%s", firstSentence, out)
	}
	// Status accepts the standard common flags — assert each is listed.
	for _, flagName := range []string{"-config", "-db", "-json", "-plain", "-no-input"} {
		if !strings.Contains(out, flagName) {
			t.Errorf("help status missing flag %q\noutput:\n%s", flagName, out)
		}
	}
}

// TestHelpVerbWithUnknownSubcommand asserts `gohealthcli help bogus` exits 1
// with `unknown command: bogus` on stderr. The did-you-mean suggestion list
// is deferred to slice 3 of PRD #143 and explicitly NOT asserted here.
func TestHelpVerbWithUnknownSubcommand(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "help", "bogus")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown command: bogus") {
		t.Fatalf("stderr missing unknown-command message: %q", stderr.String())
	}
}

// TestHelpVerbWithHiddenSubcommand asserts that hidden registry entries (the
// `schema` build-time helper) are still surfaced when looked up explicitly:
// `gohealthcli help schema` prints its Long description and exits 0. Hidden
// only means "filtered from the top-level --help listing"; an explicit help
// lookup must still resolve it.
func TestHelpVerbWithHiddenSubcommand(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "help", "schema")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	var schemaDef commandDef
	for _, cmd := range commands {
		if cmd.Name == "schema" {
			schemaDef = cmd
			break
		}
	}
	if schemaDef.Name == "" || !schemaDef.Hidden {
		t.Fatalf("registry missing hidden schema entry; cannot validate help output")
	}

	firstSentence := strings.SplitN(schemaDef.Long, ".", 2)[0]
	if !strings.Contains(stderr.String(), firstSentence) {
		t.Errorf("help schema missing Long prefix %q\noutput:\n%s", firstSentence, stderr.String())
	}
}

// TestHelpVerbRejectsExtraArguments asserts the `help` verb fails fast when
// given unexpected positional arguments, rather than silently dropping them.
// This mirrors how every other subcommand rejects unknown positionals and
// surfaces typos like `help status extra` instead of masking them.
func TestHelpVerbRejectsExtraArguments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"extras after known cmd", []string{"help", "status", "extra"}},
		{"extras after --help form", []string{"help", "--help", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, tc.args...)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), "unexpected arguments") {
				t.Fatalf("stderr missing 'unexpected arguments' message: %q", stderr.String())
			}
		})
	}
}

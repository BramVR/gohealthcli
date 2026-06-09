package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

var testBinaryPath string

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gohealthcli-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	testBinaryPath = filepath.Join(dir, "gohealthcli")
	build := exec.Command("go", "build", "-o", testBinaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build command: %v\n%s", err, string(output))
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestDoctorJSONReportsMissingSetup(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	archivePath := filepath.Join(tempDir, "gohealthcli.sqlite")

	code, stdout, stderr := runCommand(t,
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}

	if got["status"] != "setup_missing" {
		t.Fatalf("status = %v, want setup_missing", got["status"])
	}
	assertJSONString(t, got, "config_path", configPath)
	assertJSONString(t, got, "archive_path", archivePath)

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestDoctorJSONReportsMissingSetupAfterCommand(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--json",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "setup_missing" {
		t.Fatalf("status = %v, want setup_missing", got["status"])
	}
	if got["config_path"] != filepath.Join(tempDir, "config.toml") {
		t.Fatalf("config_path = %v, want command flag value", got["config_path"])
	}
	if got["archive_path"] != filepath.Join(tempDir, "gohealthcli.sqlite") {
		t.Fatalf("archive_path = %v, want command flag value", got["archive_path"])
	}

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestDoctorRejectsUnknownFlagAfterCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "doctor", "--bogus")

	if code == 0 || code == 2 {
		t.Fatalf("exit code = %d, want flag error", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr missing flag error: %q", stderr.String())
	}
}

func TestDoctorAcceptsNoInputBeforeCommand(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--no-input",
		"doctor",
		"--plain",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "status: setup_missing\n") {
		t.Fatalf("stdout missing setup status: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", stderr.String())
	}
}

func TestDoctorAcceptsNoInputAfterCommand(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"doctor",
		"--no-input",
		"--plain",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "status: setup_missing\n") {
		t.Fatalf("stdout missing setup status: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", stderr.String())
	}
}

func TestDoctorPlainReportsMissingSetup(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--plain",
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	outText := stdout.String()
	for _, want := range []string{
		"status: setup_missing\n",
		"config_path: " + filepath.Join(tempDir, "config.toml") + "\n",
		"archive_path: " + filepath.Join(tempDir, "gohealthcli.sqlite") + "\n",
	} {
		if !strings.Contains(outText, want) {
			t.Fatalf("stdout missing %q:\n%s", want, outText)
		}
	}
	if strings.Contains(outText, "connection_count:") {
		t.Fatalf("stdout reported uninspected connection count:\n%s", outText)
	}

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestDoctorHumanReportsMissingSetup(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	outText := stdout.String()
	if !strings.Contains(outText, "Setup missing") {
		t.Fatalf("stdout missing human setup status: %q", outText)
	}
	if strings.Contains(outText, "run `gohealthcli init`") {
		t.Fatalf("stdout contains human hint that should be stderr: %q", outText)
	}

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestVersionDoesNotCheckSetup(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "missing-config.toml"),
		"--db", filepath.Join(tempDir, "missing.sqlite"),
		"--version",
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	// Without `make build` ldflags, the three package vars all default to
	// "dev", so the canonical plain shape is "gohealthcli dev (dev built dev)".
	// PRD #143 slice 5 (issue #174).
	if got := strings.TrimSpace(stdout.String()); got != "gohealthcli dev (dev built dev)" {
		t.Fatalf("version stdout = %q, want %q", got, "gohealthcli dev (dev built dev)")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionJSON(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "missing-config.toml"),
		"--db", filepath.Join(tempDir, "missing.sqlite"),
		"--version",
		"--json",
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal --version --json: %v\nbody: %s", err, stdout.String())
	}
	for _, key := range []string{"version", "commit", "built"} {
		if got[key] == "" {
			t.Fatalf("--version --json[%q] empty; full body: %s", key, stdout.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionPlainAndJSONMutuallyExclusive(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "missing-config.toml"),
		"--db", filepath.Join(tempDir, "missing.sqlite"),
		"--version",
		"--plain",
		"--json",
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("stderr = %q, want to contain %q", stderr.String(), "mutually exclusive")
	}
}

// TestNoArgsPrintsHelpToStdout pins the PRD #143 slice 3 behaviour: a bare
// `gohealthcli` invocation now exits 0 and prints the top-level help (the
// "Subcommands:" listing) to stdout, matching `git` / `kubectl` / `docker`
// discoverability conventions. The explicit `--help` path is unchanged and
// keeps writing to stderr per stdlib flag-package convention — see
// TestHelpExitsSuccessfully below.
func TestNoArgsPrintsHelpToStdout(t *testing.T) {
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

func TestDoctorDefaultPathsAreUsable(t *testing.T) {
	home := t.TempDir()
	xdgConfig := filepath.Join(home, "xdg-config")
	xdgData := filepath.Join(home, "xdg-data")

	code, stdout, stderr := runCommandWithEnv(t,
		[]string{
			"HOME=" + home,
			"XDG_CONFIG_HOME=" + xdgConfig,
			"XDG_DATA_HOME=" + xdgData,
		},
		"--json",
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2\nstderr: %s", code, stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["config_path"] != filepath.Join(xdgConfig, "gohealthcli", "config.toml") {
		t.Fatalf("config_path = %v, want XDG config path", got["config_path"])
	}
	if got["archive_path"] != filepath.Join(xdgData, "gohealthcli", "gohealthcli.sqlite") {
		t.Fatalf("archive_path = %v, want XDG data path", got["archive_path"])
	}
	if strings.Contains(stdout.String(), "~") {
		t.Fatalf("stdout contains unexpanded home path: %s", stdout.String())
	}
}

func TestInitCreatesConfigAndEmptyHealthArchive(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	oauthClientPath := filepath.Join(tempDir, "client_secret.json")

	code, stdout, stderr := runCommand(t,
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"init",
		"--oauth-client-file", oauthClientPath,
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "initialized" {
		t.Fatalf("status = %v, want initialized", got["status"])
	}
	assertJSONString(t, got, "config_path", configPath)
	assertJSONString(t, got, "archive_path", archivePath)
	assertJSONString(t, got, "oauth_client_source", "file")
	if got["schema_version"] != float64(currentSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", got["schema_version"], currentSchemaVersion)
	}
	dataTypes, ok := got["default_data_types"].([]any)
	if !ok {
		t.Fatalf("default_data_types = %T(%v), want array", got["default_data_types"], got["default_data_types"])
	}
	if len(dataTypes) == 0 || dataTypes[0] != "steps" {
		t.Fatalf("default_data_types = %v, want steps first", dataTypes)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	assertMode(t, filepath.Dir(configPath), 0o700)
	assertMode(t, filepath.Dir(archivePath), 0o700)
	assertMode(t, configPath, 0o600)
	assertMode(t, archivePath, 0o600)

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := string(configBytes)
	for _, want := range []string{
		`archive_path = "` + archivePath + `"`,
		`source = "file"`,
		`path = "` + oauthClientPath + `"`,
		`[credential_store]`,
		`"steps"`,
		`"weight"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
	if expectedDefaultCredentialStoreKind() == "os_native" {
		for _, want := range []string{`type = "os_native"`, `service = "gohealthcli"`} {
			if !strings.Contains(config, want) {
				t.Fatalf("config missing %q:\n%s", want, config)
			}
		}
	} else {
		for _, want := range []string{`type = "file"`, `path = "` + filepath.Join(filepath.Dir(configPath), "tokens.json") + `"`} {
			if !strings.Contains(config, want) {
				t.Fatalf("config missing %q:\n%s", want, config)
			}
		}
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()

	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign key pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	rows, err := db.Query(`SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query schema migrations: %v", err)
	}
	defer rows.Close()
	var migrations []string
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			t.Fatalf("scan schema migration: %v", err)
		}
		migrations = append(migrations, fmt.Sprintf("%d:%s", version, name))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("schema migration rows: %v", err)
	}
	if strings.Join(migrations, ",") != "1:initial_archive_schema,2:add_google_identity_json,3:add_source_family_filter,4:add_daily_steps_view,5:add_first_release_normalized_views,6:add_sync_cursors,7:rename_profile_snapshots_to_identity_snapshots,8:add_current_settings_view,9:add_paired_devices_view,10:add_current_irn_profile_view,11:add_sleep_stages_and_exercise_splits_views,12:fix_exercise_splits_real_shape,13:add_searchable_text_view,14:fix_searchable_text_latest_profile_and_empty_filter,15:add_data_point_attachments,16:add_floors_intervals_view,17:add_tier1_activity_views,18:add_tier1_health_metrics_views,19:add_tier1_daily_hydration_views,20:add_tier2_ecg_irn_views" {
		t.Fatalf("migrations = %v, want all migrations 1..20", migrations)
	}

	for _, table := range []string{
		"connections",
		"data_points",
		"data_point_revisions",
		"rollups",
		"identity_snapshots",
		"sync_runs",
		"sync_cursors",
	} {
		var tableName string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&tableName); err != nil {
			t.Fatalf("missing table %s: %v", table, err)
		}
	}

	db.SetMaxOpenConns(2)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer tx.Rollback()

	var txForeignKeys int
	if err := tx.QueryRow(`PRAGMA foreign_keys`).Scan(&txForeignKeys); err != nil {
		t.Fatalf("query transaction foreign key pragma: %v", err)
	}
	if txForeignKeys != 1 {
		t.Fatalf("transaction foreign_keys = %d, want 1", txForeignKeys)
	}

	_, err = db.Exec(`INSERT INTO data_points (
		provider_name,
		connection_id,
		data_type,
		record_kind,
		data_source_json,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"missing-connection",
		"steps",
		"sample",
		"{}",
		"{}",
		"2026-05-31T00:00:00Z",
		"2026-05-31T00:00:00Z",
	)
	if err == nil {
		t.Fatal("insert orphan Data Point succeeded, want foreign key failure")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("insert orphan Data Point error = %v, want foreign key failure", err)
	}
}

func TestDoctorReportsInitializedSetup(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %v, want ok", got["status"])
	}
	assertJSONString(t, got, "config_path", configPath)
	assertJSONString(t, got, "archive_path", archivePath)
	assertJSONString(t, got, "oauth_client_source", "file")
	assertJSONString(t, got, "credential_store", expectedDefaultCredentialStoreKind())
	assertJSONString(t, got, "token_status", "not_connected")
	if got["schema_version"] != float64(currentSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", got["schema_version"], currentSchemaVersion)
	}
	if got["connection_count"] != float64(0) {
		t.Fatalf("connection_count = %v, want 0", got["connection_count"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorPlainReportsOfflineHealthCheck(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	want := fmt.Sprintf("status: ok\nconfig_path: %s\narchive_path: %s\noauth_client_source: file\ncredential_store: %s\nschema_version: %d\nconnection_count: 0\ntoken_status: not_connected\nattachment_root_path: %s.attachments\nattachment_root_mode: 0700\nmessage: local gohealthcli setup is initialized\n", configPath, archivePath, expectedDefaultCredentialStoreKind(), currentSchemaVersion, archivePath)
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorJSONReportsInvalidSetup(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if err := os.WriteFile(configPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "setup_invalid" {
		t.Fatalf("status = %v, want setup_invalid", got["status"])
	}
	assertJSONString(t, got, "config_path", configPath)
	assertJSONString(t, got, "archive_path", archivePath)
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "config check failed") {
		t.Fatalf("message = %T(%v), want config check failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsMalformedOAuthClientReference(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := strings.Replace(string(configBytes), `path = "`+filepath.Join(tempDir, "client_secret.json")+`"`+"\n", "", 1)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "OAuth client file path") {
		t.Fatalf("message = %T(%v), want OAuth client file path failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsMissingOAuthClientFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	oauthClientPath := filepath.Join(tempDir, "client_secret.json")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", oauthClientPath,
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if err := os.Remove(oauthClientPath); err != nil {
		t.Fatalf("remove OAuth client file: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "OAuth client file") {
		t.Fatalf("message = %T(%v), want OAuth client file failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsRelativeOAuthClientFileAfterInitFromDifferentDirectory(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	initDir := filepath.Join(tempDir, "init-cwd")
	doctorDir := filepath.Join(tempDir, "doctor-cwd")
	if err := os.Mkdir(initDir, 0o700); err != nil {
		t.Fatalf("create init dir: %v", err)
	}
	if err := os.Mkdir(doctorDir, 0o700); err != nil {
		t.Fatalf("create doctor dir: %v", err)
	}

	code, _, stderr := runCommandInDir(t,
		initDir,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", "client_secret.json",
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	oauthClientPath, err := filepath.EvalSymlinks(filepath.Join(initDir, "client_secret.json"))
	if err != nil {
		t.Fatalf("resolve OAuth client path: %v", err)
	}
	if want := `path = "` + oauthClientPath + `"`; !strings.Contains(string(configBytes), want) {
		t.Fatalf("config missing absolute OAuth client path %q:\n%s", want, string(configBytes))
	}

	code, stdout, stderr := runCommandInDir(t,
		doctorDir,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %v, want ok", got["status"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorDefaultsLegacyConfigWithoutCredentialStore(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := removeCredentialStoreSection(t, string(configBytes))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "credential_store", expectedDefaultCredentialStoreKind())
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsInlineConfigComments(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := string(configBytes)
	config = strings.Replace(config, `archive_path = "`+archivePath+`"`, `archive_path = "`+archivePath+`" # local Health Archive`, 1)
	config = strings.Replace(config, `"steps",`, `"steps", # default Data Type`, 1)
	storeType := `type = "` + expectedDefaultCredentialStoreKind() + `"`
	config = strings.Replace(config, storeType, storeType+` # default Credential Store`, 1)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %v, want ok", got["status"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsInlineDefaultDataTypesArray(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	inlineDataTypes := "default_data_types = [\"" + strings.Join(defaultDataTypes, "\", \"") + "\"]"
	config := strings.Replace(string(configBytes), multilineDataTypes, inlineDataTypes, 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %v, want ok", got["status"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsMultivalueDefaultDataTypeRows(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	config := strings.Replace(string(configBytes), multilineDataTypes, "default_data_types = [\n  \"steps\", \"weight\",\n]", 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsOpeningLineMultilineDefaultDataTypesArray(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	config := strings.Replace(string(configBytes), multilineDataTypes, "default_data_types = [\"steps\",\n  \"weight\",\n]", 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorAcceptsConfiguredDefaultDataTypeSubset(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	config := strings.Replace(string(configBytes), multilineDataTypes, `default_data_types = ["steps"]`, 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsUnsupportedDefaultDataType(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]"
	config := strings.Replace(string(configBytes), multilineDataTypes, `default_data_types = ["bogus"]`, 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "unsupported default Data Type") {
		t.Fatalf("message = %T(%v), want unsupported Data Type failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsMissingDefaultDataTypes(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	multilineDataTypes := "default_data_types = [\n  \"" + strings.Join(defaultDataTypes, "\",\n  \"") + "\",\n]\n\n"
	config := strings.Replace(string(configBytes), multilineDataTypes, "", 1)
	if config == string(configBytes) {
		t.Fatalf("config replacement failed:\n%s", string(configBytes))
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "missing default_data_types") {
		t.Fatalf("message = %T(%v), want missing default_data_types failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorReportsMalformedCredentialStoreConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := strings.Replace(string(configBytes), `type = "`+expectedDefaultCredentialStoreKind()+`"`, `type = "1password"`, 1)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "Credential Store") {
		t.Fatalf("message = %T(%v), want Credential Store failure", got["message"], got["message"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorValidatesConnectionTokenMetadata(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.Exec(`INSERT INTO connections (
		id,
		provider_name,
		google_health_user_id,
		token_metadata_json,
		created_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?)`,
		"googlehealth:123",
		"googlehealth",
		"123",
		"{}",
		"2026-05-31T00:00:00Z",
		"2026-05-31T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "missing token metadata") {
		t.Fatalf("message = %T(%v), want missing token metadata failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db, err = openArchive(archivePath)
	if err != nil {
		t.Fatalf("reopen archive: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`,
		`{"credential_store_key":"googlehealth:123","expires_at":"2026-06-01T00:00:00Z","scopes":[""]}`,
		"googlehealth:123",
	)
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	code, stdout, stderr = runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	got = map[string]any{}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok = got["message"].(string)
	if !ok || !strings.Contains(message, "empty strings") {
		t.Fatalf("message = %T(%v), want empty scope failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db, err = openArchive(archivePath)
	if err != nil {
		t.Fatalf("reopen archive: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`,
		`{"credential_store_key":"googlehealth:123","expires_at":"2026-06-01T00:00:00Z","scopes":["health.activity.read"]}`,
		"googlehealth:123",
	)
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	code, stdout, stderr = runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	got = map[string]any{}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["connection_count"] != float64(1) {
		t.Fatalf("connection_count = %v, want 1", got["connection_count"])
	}
	assertJSONString(t, got, "token_status", "metadata_present")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorDoesNotLeakTokenMetadataSecretMaterial(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.Exec(`INSERT INTO connections (
		id,
		provider_name,
		google_health_user_id,
		token_metadata_json,
		created_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?)`,
		"googlehealth:123",
		"googlehealth",
		"123",
		`{"credential_store_key":"googlehealth:123","expires_at":"2026-06-01T00:00:00Z","scopes":["health.activity.read"],"nested":{"idToken":"very-secret-value"}}`,
		"2026-05-31T00:00:00Z",
		"2026-05-31T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "very-secret-value") {
		t.Fatalf("output leaked token value: %s", combined)
	}
	assertNoSecretWords(t, combined)
}

func TestDoctorOnlineRefreshesExpiredTokenAndChecksProvider(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T21:00:00Z")
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                     time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		refreshedAccessToken:    "refreshed-access-secret",
		wantRefreshToken:        "refresh-secret-value",
		wantProviderAccessToken: "refreshed-access-secret",
		healthUserID:            "111111256096816351",
		legacyFitbitUserID:      "A1B2C3",
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("doctor --online exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONString(t, got, "token_status", "online_ok")
	assertJSONString(t, got, "message", "online Google Health check passed")
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "old-access-secret") || strings.Contains(stdout.String()+stderr.String(), "refreshed-access-secret") || strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") {
		t.Fatalf("doctor --online output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if !strings.Contains(string(tokenStoreBytes), "refreshed-access-secret") || !strings.Contains(string(tokenStoreBytes), "refresh-secret-value") {
		t.Fatal("token store was not refreshed")
	}
	tokenMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if strings.Contains(tokenMetadata, "refreshed-access-secret") || strings.Contains(tokenMetadata, "refresh-secret-value") {
		t.Fatalf("token metadata leaked refreshed token material: %s", tokenMetadata)
	}
	if !strings.Contains(tokenMetadata, `"expires_at":"2026-05-31T23:00:00Z"`) {
		t.Fatalf("token metadata = %s, want refreshed expiry", tokenMetadata)
	}
}

func TestDoctorOnlineReportsRefreshFailureAsConnectionHealth(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T21:00:00Z")
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		wantRefreshToken:     "refresh-secret-value",
		refreshErr:           errors.New("OAuth token refresh failed with HTTP 400"),
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "refresh_failed")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "HTTP 400") {
		t.Fatalf("message = %T(%v), want refresh failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") || strings.Contains(stdout.String()+stderr.String(), "old-access-secret") {
		t.Fatalf("doctor --online output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestDoctorOnlineValidatesRefreshWhenAccessTokenIsCurrent(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "current-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 20, 30, 0, 0, time.UTC),
		wantRefreshToken:     "refresh-secret-value",
		refreshErr:           errors.New("OAuth token refresh failed with HTTP 400"),
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "refresh_failed")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "HTTP 400") {
		t.Fatalf("message = %T(%v), want refresh failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") || strings.Contains(stdout.String()+stderr.String(), "current-access-secret") {
		t.Fatalf("doctor --online output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestDoctorOnlineReportsProviderFailureAsConnectionHealth(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                     time.Date(2026, 5, 31, 20, 30, 0, 0, time.UTC),
		refreshedAccessToken:    "refreshed-access-secret",
		wantRefreshToken:        "connect-refresh-secret",
		wantProviderAccessToken: "refreshed-access-secret",
		providerErr:             errors.New("Google Health identity request failed with HTTP 503"),
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "provider_unreachable")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "HTTP 503") {
		t.Fatalf("message = %T(%v), want provider failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if strings.Contains(string(tokenStoreBytes), "refreshed-access-secret") || !strings.Contains(string(tokenStoreBytes), "connect-access-secret") {
		t.Fatal("token store should keep old token material after provider failure")
	}
}

func TestDoctorOnlineReportsMissingTokenAsConnectionHealth(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	store := fileCredentialStore{path: tokenStorePath}
	if err := store.Store("googlehealth:111111256096816351", map[string]any{"refresh_token": "connect-refresh-secret"}); err != nil {
		t.Fatalf("replace token material: %v", err)
	}
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		failRefreshIfCalled:  true,
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "token_missing")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "missing access token") {
		t.Fatalf("message = %T(%v), want missing access token", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorOnlineReportsMissingRefreshTokenBeforeProvider(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	store := fileCredentialStore{path: tokenStorePath}
	if err := store.Store("googlehealth:111111256096816351", map[string]any{"access_token": "connect-access-secret"}); err != nil {
		t.Fatalf("replace token material: %v", err)
	}
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 20, 30, 0, 0, time.UTC),
		failRefreshIfCalled:  true,
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "token_missing")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "missing refresh token") {
		t.Fatalf("message = %T(%v), want missing refresh token", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDoctorOnlineDoesNotPersistRefreshBeforeIdentityMatch(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T21:00:00Z")
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                     time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		refreshedAccessToken:    "refreshed-access-secret",
		wantRefreshToken:        "refresh-secret-value",
		wantProviderAccessToken: "refreshed-access-secret",
		healthUserID:            "222222256096816351",
		legacyFitbitUserID:      "DIFFERENT",
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--online", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor --online exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connection_unhealthy")
	assertJSONString(t, got, "token_status", "identity_mismatch")
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if strings.Contains(string(tokenStoreBytes), "refreshed-access-secret") || !strings.Contains(string(tokenStoreBytes), "old-access-secret") {
		t.Fatal("token store should keep old token material after identity mismatch")
	}
	tokenMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if strings.Contains(tokenMetadata, `"expires_at":"2026-05-31T23:00:00Z"`) || !strings.Contains(tokenMetadata, `"expires_at":"2026-05-31T21:00:00Z"`) {
		t.Fatalf("token metadata should keep old expiry after identity mismatch: %s", tokenMetadata)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "old-access-secret") || strings.Contains(stdout.String()+stderr.String(), "refreshed-access-secret") || strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") {
		t.Fatalf("doctor --online output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestPersistDoctorOnlineRefreshedTokenRollsBackOnMetadataFailure(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "old-access-secret",
		refreshToken:       "refresh-secret-value",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	store := fileCredentialStore{path: tokenStorePath}
	previousTokenMaterial, err := store.Load("googlehealth:111111256096816351")
	if err != nil {
		t.Fatalf("load previous token material: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM connections WHERE id = ?`, "googlehealth:111111256096816351"); err != nil {
		_ = db.Close()
		t.Fatalf("delete connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive after delete: %v", err)
	}
	refreshedToken := oauthTokenResponse{
		accessToken:  "refreshed-access-secret",
		refreshToken: "refresh-secret-value",
		tokenType:    "Bearer",
		scopes:       []string{googleHealthActivityReadonlyScope},
		expiresAt:    time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC),
		rawTokenMaterialObject: map[string]any{
			"access_token":  "refreshed-access-secret",
			"refresh_token": "refresh-secret-value",
			"expires_in":    float64(3600),
			"scope":         googleHealthActivityReadonlyScope,
			"token_type":    "Bearer",
		},
	}
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		t.Fatalf("open archive API: %v", err)
	}
	defer archive.Close()
	err = persistDoctorOnlineRefreshedToken(archive, credentialStoreConfig{kind: "file", path: tokenStorePath}, "googlehealth:111111256096816351", refreshedToken, previousTokenMaterial)
	if err == nil {
		t.Fatal("persist refreshed token succeeded after connection was removed")
	}
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if strings.Contains(string(tokenStoreBytes), "refreshed-access-secret") || !strings.Contains(string(tokenStoreBytes), "old-access-secret") {
		t.Fatal("token store should roll back to previous token material")
	}
}

func TestDoctorDefaultDoesNotRefreshOrCallProvider(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenExpiry(t, archivePath, "2026-05-31T19:00:00Z")
	installDoctorOnlineFakes(t, fakeDoctorOnlineConfig{
		now:                  time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		failRefreshIfCalled:  true,
		failProviderIfCalled: true,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONString(t, got, "token_status", "metadata_present")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestConnectStoresFileFallbackTokenAndAnchorsIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	connectNow := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	refreshExpiresAt := connectNow.Add(24 * time.Hour)
	installConnectFakes(t, fakeConnectConfig{
		now:                connectNow,
		accessToken:        "access-secret-value",
		refreshToken:       "refresh-secret-value",
		refreshExpiresAt:   &refreshExpiresAt,
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("connect exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connected")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "google_health_user_id", "111111256096816351")
	assertJSONString(t, got, "legacy_fitbit_user_id", "A1B2C3")
	assertJSONString(t, got, "credential_store", "file")
	assertJSONString(t, got, "token_status", "metadata_present")
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "access-secret-value") || strings.Contains(stdout.String()+stderr.String(), "refresh-secret-value") {
		t.Fatalf("connect output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}

	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	tokenStore := string(tokenStoreBytes)
	if !strings.Contains(tokenStore, "access-secret-value") || !strings.Contains(tokenStore, "refresh-secret-value") {
		t.Fatalf("token store missing token material: %s", tokenStore)
	}
	assertMode(t, tokenStorePath, 0o600)

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var connectionID, providerName, healthUserID, legacyUserID, tokenMetadata, identityJSON string
	if err := db.QueryRow(`SELECT id, provider_name, google_health_user_id, legacy_fitbit_user_id, token_metadata_json, google_identity_json FROM connections`).Scan(&connectionID, &providerName, &healthUserID, &legacyUserID, &tokenMetadata, &identityJSON); err != nil {
		t.Fatalf("query connection: %v", err)
	}
	if connectionID != "googlehealth:111111256096816351" || providerName != "googlehealth" || healthUserID != "111111256096816351" || legacyUserID != "A1B2C3" {
		t.Fatalf("connection = (%q, %q, %q, %q), want anchored identity", connectionID, providerName, healthUserID, legacyUserID)
	}
	if strings.Contains(tokenMetadata, "access-secret-value") || strings.Contains(tokenMetadata, "refresh-secret-value") || strings.Contains(tokenMetadata, "access_token") || strings.Contains(tokenMetadata, "refresh_token") {
		t.Fatalf("token metadata leaked token material: %s", tokenMetadata)
	}
	for _, want := range []string{"credential_store_key", "expires_at", "scopes"} {
		if !strings.Contains(tokenMetadata, want) {
			t.Fatalf("token metadata missing %q: %s", want, tokenMetadata)
		}
	}
	if strings.Contains(tokenMetadata, "refresh_token_expires_at") {
		t.Fatalf("token metadata uses rejected refresh token key: %s", tokenMetadata)
	}
	if !strings.Contains(tokenMetadata, "refresh_expires_at") {
		t.Fatalf("token metadata missing refresh expiry: %s", tokenMetadata)
	}
	if err := validateTokenMetadata(tokenMetadata); err != nil {
		t.Fatalf("token metadata does not validate: %v", err)
	}
	if !strings.Contains(identityJSON, `"healthUserId":"111111256096816351"`) {
		t.Fatalf("identity JSON not archived: %s", identityJSON)
	}
}

func TestConnectReauthorizesSameIdentityWithoutSecondConnection(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		accessToken:        "first-access-secret",
		refreshToken:       "first-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	code := runConnectCommand(t, configPath, archivePath)
	if code != 0 {
		t.Fatalf("first connect exit code = %d, want 0", code)
	}

	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC),
		accessToken:        "second-access-secret",
		refreshToken:       "second-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	code = runConnectCommand(t, configPath, archivePath)
	if code != 0 {
		t.Fatalf("second connect exit code = %d, want 0", code)
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM connections`).Scan(&count); err != nil {
		t.Fatalf("count connections: %v", err)
	}
	if count != 1 {
		t.Fatalf("connection count = %d, want 1", count)
	}
	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	tokenStore := string(tokenStoreBytes)
	if !strings.Contains(tokenStore, "second-access-secret") || strings.Contains(tokenStore, "first-access-secret") {
		t.Fatalf("token store was not updated on reauth: %s", tokenStore)
	}
}

func TestConnectArchiveInspectionFailureDoesNotReportCredentialStore(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = ?`, currentSchemaVersion); err != nil {
		_ = db.Close()
		t.Fatalf("delete schema migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "connect_failed")
	if _, ok := got["credential_store"]; ok {
		t.Fatalf("credential_store = %v, want omitted for Health Archive check failure", got["credential_store"])
	}
	if !strings.Contains(got["message"].(string), "Health Archive check failed") {
		t.Fatalf("message = %q, want Health Archive check failed", got["message"])
	}
}

func TestConnectRejectsDifferentGoogleIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		accessToken:        "first-access-secret",
		refreshToken:       "first-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("first connect exit code = %d, want 0", code)
	}

	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC),
		accessToken:        "other-access-secret",
		refreshToken:       "other-refresh-secret",
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "D4E5F6",
	})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "different Google Identity") {
		t.Fatalf("message = %T(%v), want different identity failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	tokenStoreBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	if strings.Contains(string(tokenStoreBytes), "other-access-secret") {
		t.Fatalf("different identity token was stored: %s", string(tokenStoreBytes))
	}
}

func TestConnectDoesNotResolveSecretProviderAtRuntime(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--secret-provider", "1password",
		"--oauth-client-item", "Google Health OAuth",
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})
	stdout := new(bytes.Buffer)
	connectStderr := new(bytes.Buffer)
	code = run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, connectStderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "Secret Provider") {
		t.Fatalf("message = %T(%v), want Secret Provider runtime refusal", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+connectStderr.String())
}

func TestIdentityRefreshesArchivedGoogleIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installIdentityFetchFake(t, "connect-access-secret", googleIdentity{
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"111111256096816351","legacyUserId":"Z9Y8X7","refreshed":true}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("identity exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_refreshed")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "google_health_user_id", "111111256096816351")
	assertJSONString(t, got, "legacy_fitbit_user_id", "Z9Y8X7")
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") || strings.Contains(stdout.String()+stderr.String(), "connect-refresh-secret") {
		t.Fatalf("identity output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var legacyUserID, identityJSON string
	if err := db.QueryRow(`SELECT legacy_fitbit_user_id, google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&legacyUserID, &identityJSON); err != nil {
		t.Fatalf("query refreshed identity: %v", err)
	}
	if legacyUserID != "Z9Y8X7" {
		t.Fatalf("legacy_fitbit_user_id = %q, want refreshed value", legacyUserID)
	}
	if !strings.Contains(identityJSON, `"refreshed":true`) {
		t.Fatalf("google_identity_json = %s, want refreshed raw identity", identityJSON)
	}
}

func TestIdentityPlainIncludesStableIdentityFields(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installIdentityFetchFake(t, "connect-access-secret", googleIdentity{
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
		rawJSON:            `{"healthUserId":"111111256096816351","legacyUserId":"A1B2C3"}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("identity exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := "status: identity_refreshed\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ngoogle_health_user_id: 111111256096816351\nlegacy_fitbit_user_id: A1B2C3\nmessage: Google Identity refreshed\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestIdentityHumanOutputDistinguishesFailureStatuses(t *testing.T) {
	for _, test := range []struct {
		status string
		want   string
	}{
		{status: "identity_mismatch", want: "Google Identity mismatch\n"},
		{status: "identity_unavailable", want: "Google Identity unavailable\n"},
		{status: "identity_failed", want: "Google Identity failed\n"},
	} {
		t.Run(test.status, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			err := writeIdentityResult(identityResult{Status: test.status, Message: "message"}, outputMode{}, stdout)
			if err != nil {
				t.Fatalf("write identity result: %v", err)
			}
			if !strings.HasPrefix(stdout.String(), test.want) {
				t.Fatalf("stdout = %q, want prefix %q", stdout.String(), test.want)
			}
		})
	}
}

func TestIdentityRequiresArchivedConnection(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("identity exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_unavailable")
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "gohealthcli connect") {
		t.Fatalf("message = %T(%v), want connect guidance", got["message"], got["message"])
	}
	if _, ok := got["connection_id"]; ok {
		t.Fatalf("connection_id = %v, want omitted when no Connection exists", got["connection_id"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestIdentityReportsExpiredConnectionTokenBeforeProviderFetch(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		t.Fatalf("identity fetch should not be called for expired token")
		return googleIdentity{}, nil
	}
	currentTime = func() time.Time {
		return time.Date(2026, 5, 31, 23, 1, 0, 0, time.UTC)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("identity exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "expired") || !strings.Contains(message, "gohealthcli connect") {
		t.Fatalf("message = %T(%v), want expired-token reconnect guidance", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestIdentityRejectsDifferentGoogleIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installIdentityFetchFake(t, "connect-access-secret", googleIdentity{
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"222222222222222222","legacyUserId":"Z9Y8X7"}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("identity exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_mismatch")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "different Google Identity") {
		t.Fatalf("message = %T(%v), want different identity refusal", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var healthUserID, legacyUserID, identityJSON string
	if err := db.QueryRow(`SELECT google_health_user_id, legacy_fitbit_user_id, google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&healthUserID, &legacyUserID, &identityJSON); err != nil {
		t.Fatalf("query identity after mismatch: %v", err)
	}
	if healthUserID != "111111256096816351" || legacyUserID != "A1B2C3" {
		t.Fatalf("archived identity = (%q, %q), want unchanged", healthUserID, legacyUserID)
	}
	if strings.Contains(identityJSON, "222222222222222222") {
		t.Fatalf("different provider identity was archived: %s", identityJSON)
	}
}

func TestProfileArchivesSnapshotAndPrintsSummary(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{
		healthUserID: "111111256096816351",
		rawJSON:      `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}`,
	}, nil)
	currentTime = func() time.Time {
		return time.Date(2026, 6, 1, 10, 30, 0, 0, time.UTC)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("profile exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_archived")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "google_health_user_id", "111111256096816351")
	assertJSONString(t, got, "legacy_fitbit_user_id", "A1B2C3")
	assertJSONString(t, got, "fetched_at", "2026-06-01T10:30:00Z")
	snapshotID, ok := got["snapshot_id"].(float64)
	if !ok || snapshotID != 1 {
		t.Fatalf("snapshot_id = %T(%v), want 1", got["snapshot_id"], got["snapshot_id"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var providerName, connectionID, rawJSON, fetchedAt string
	if err := db.QueryRow(`SELECT provider_name, connection_id, raw_json, fetched_at FROM identity_snapshots WHERE id = ?`, 1).Scan(&providerName, &connectionID, &rawJSON, &fetchedAt); err != nil {
		t.Fatalf("query profile snapshot: %v", err)
	}
	if providerName != "googlehealth" || connectionID != "googlehealth:111111256096816351" {
		t.Fatalf("snapshot owner = (%q, %q), want archived Connection", providerName, connectionID)
	}
	if rawJSON != `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}` {
		t.Fatalf("raw_json = %s, want provider profile JSON", rawJSON)
	}
	if fetchedAt != "2026-06-01T10:30:00Z" {
		t.Fatalf("fetched_at = %q, want fixed timestamp", fetchedAt)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
}

func TestProfilePlainIncludesStableSnapshotFields(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		now:                time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{
		healthUserID: "111111256096816351",
		rawJSON:      `{"name":"users/111111256096816351/profile"}`,
	}, nil)
	currentTime = func() time.Time {
		return time.Date(2026, 6, 1, 10, 30, 0, 0, time.UTC)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("profile exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := "status: profile_archived\nsnapshot_id: 1\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ngoogle_health_user_id: 111111256096816351\nlegacy_fitbit_user_id: A1B2C3\nfetched_at: 2026-06-01T10:30:00Z\nmessage: Profile Snapshot archived\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestProfileProviderFailureDoesNotArchiveSnapshot(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{}, errors.New("Google Health profile request failed with HTTP 503"))

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_failed")
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "HTTP 503") {
		t.Fatalf("message = %T(%v), want provider status", got["message"], got["message"])
	}
	if _, ok := got["snapshot_id"]; ok {
		t.Fatalf("snapshot_id = %v, want omitted on failure", got["snapshot_id"])
	}
	assertArchiveTableCount(t, archivePath, "identity_snapshots", 0)
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") || strings.Contains(stdout.String()+stderr.String(), "connect-refresh-secret") {
		t.Fatalf("profile output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestProfileFailsBeforeProviderWhenProfileScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenScopes(t, archivePath, []string{googleHealthActivityReadonlyScope})
	originalFetchProfile := fetchProfile
	fetchProfile = func(accessToken string) (googleProfile, error) {
		t.Fatalf("profile fetch should not be called when profile scope is missing")
		return googleProfile{}, nil
	}
	t.Cleanup(func() { fetchProfile = originalFetchProfile })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, googleHealthProfileReadonlyScope) || !strings.Contains(message, "connect") {
		t.Fatalf("message = %T(%v), want profile-scope reconnect guidance", got["message"], got["message"])
	}
	assertArchiveTableCount(t, archivePath, "identity_snapshots", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestProfileRejectsAliasProfileWhenIdentityVerificationDiffers(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{
		rawJSON: `{"name":"users/me/profile","profile":{"unit":"metric"}}`,
	}, nil)
	installIdentityFetchFake(t, "connect-access-secret", googleIdentity{
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"222222222222222222","legacyUserId":"Z9Y8X7"}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_mismatch")
	if _, ok := got["snapshot_id"]; ok {
		t.Fatalf("snapshot_id = %v, want omitted on mismatch", got["snapshot_id"])
	}
	assertArchiveTableCount(t, archivePath, "identity_snapshots", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestProfileRejectsDifferentGoogleIdentityWithoutArchiving(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installProfileFetchFake(t, "connect-access-secret", googleProfile{
		healthUserID: "222222222222222222",
		rawJSON:      `{"name":"users/222222222222222222/profile"}`,
	}, nil)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_mismatch")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	if _, ok := got["snapshot_id"]; ok {
		t.Fatalf("snapshot_id = %v, want omitted on mismatch", got["snapshot_id"])
	}
	assertArchiveTableCount(t, archivePath, "identity_snapshots", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusReportsHealthArchiveCountsAndSyncRunsReadOnly(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	originalFetchIdentity := fetchIdentity
	originalFetchProfile := fetchProfile
	originalFetchRawProvider := fetchRawProvider
	originalRefreshOAuthToken := refreshOAuthToken
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		t.Fatal("status should not call Provider identity")
		return googleIdentity{}, nil
	}
	fetchProfile = func(accessToken string) (googleProfile, error) {
		t.Fatal("status should not call Provider profile")
		return googleProfile{}, nil
	}
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("status should not call Provider raw endpoints")
		return nil, nil
	}
	refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		t.Fatal("status should not refresh tokens")
		return oauthTokenResponse{}, nil
	}
	t.Cleanup(func() {
		fetchIdentity = originalFetchIdentity
		fetchProfile = originalFetchProfile
		fetchRawProvider = originalFetchRawProvider
		refreshOAuthToken = originalRefreshOAuthToken
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONString(t, got, "archive_path", archivePath)
	assertJSONNumber(t, got, "schema_version", currentSchemaVersion)
	assertJSONNumber(t, got, "data_point_count", 3)
	assertJSONNumber(t, got, "rollup_count", 1)
	assertJSONNumber(t, got, "profile_snapshot_count", 1)
	assertJSONNumber(t, got, "sync_run_count", 3)

	heartRateStatus := statusDataTypeFromJSON(t, got, "heart-rate")
	assertJSONNumber(t, heartRateStatus, "data_point_count", 1)
	assertJSONNumber(t, heartRateStatus, "rollup_count", 0)
	assertJSONString(t, heartRateStatus, "newest_data_point_timestamp", "2026-01-03T09:00:00Z")
	if _, ok := heartRateStatus["newest_rollup_timestamp"]; ok {
		t.Fatalf("heart-rate newest_rollup_timestamp = %v, want omitted", heartRateStatus["newest_rollup_timestamp"])
	}

	stepsStatus := statusDataTypeFromJSON(t, got, "steps")
	assertJSONNumber(t, stepsStatus, "data_point_count", 2)
	assertJSONNumber(t, stepsStatus, "rollup_count", 1)
	assertJSONString(t, stepsStatus, "newest_data_point_timestamp", "2026-01-02T08:15:00Z")
	assertJSONString(t, stepsStatus, "newest_rollup_timestamp", "2026-01-04")

	success, ok := got["latest_successful_sync_run"].(map[string]any)
	if !ok {
		t.Fatalf("latest_successful_sync_run = %T(%v), want object", got["latest_successful_sync_run"], got["latest_successful_sync_run"])
	}
	assertJSONNumber(t, success, "id", 2)
	assertJSONString(t, success, "status", "sync_completed")
	assertJSONString(t, success, "from", "2026-01-02")
	assertJSONString(t, success, "to", "2026-01-03T00:00:00Z")
	assertJSONString(t, success, "endpoint_family", "reconcile")
	assertJSONString(t, success, "source_family_filter", "wearable")
	assertJSONNumber(t, success, "seen_count", 2)

	failed, ok := got["latest_failed_sync_run"].(map[string]any)
	if !ok {
		t.Fatalf("latest_failed_sync_run = %T(%v), want object", got["latest_failed_sync_run"], got["latest_failed_sync_run"])
	}
	assertJSONNumber(t, failed, "id", 3)
	assertJSONString(t, failed, "status", "sync_failed")
	assertJSONString(t, failed, "error_summary", "Provider timeout after 30s")
	if strings.Contains(stdout.String(), "gap") || strings.Contains(stdout.String(), "completeness") {
		t.Fatalf("status inferred completeness or gaps:\n%s", stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusPlainReportsEmptyHealthArchive(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--plain",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	wantLines := []string{
		"status: ok\n",
		"archive_path: " + archivePath + "\n",
		fmt.Sprintf("schema_version: %d\n", currentSchemaVersion),
		"data_point_count: 0\n",
		"rollup_count: 0\n",
		"profile_snapshot_count: 0\n",
		"sync_run_count: 0\n",
		"message: Health Archive status summarized\n",
	}
	for _, want := range wantLines {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "latest_successful_sync_run") || strings.Contains(stdout.String(), "known_data_types") {
		t.Fatalf("stdout reported absent archive details:\n%s", stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusMigratesLegacyV3ArchiveBeforeValidation(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV3Archive(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONNumber(t, got, "schema_version", currentSchemaVersion)
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
}

func TestStatusRejectsConfigArchiveMismatch(t *testing.T) {
	tempDir := t.TempDir()
	configPath, _, _ := initializeFileCredentialSetup(t, tempDir)
	otherArchivePath := filepath.Join(tempDir, "other-data", "other.sqlite")
	if err := createArchive(otherArchivePath); err != nil {
		t.Fatalf("create other archive: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--db", otherArchivePath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "status_failed")
	if !strings.Contains(got["message"].(string), "archive_path points") {
		t.Fatalf("message = %q, want config mismatch", got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusRejectsExplicitDefaultArchiveMismatch(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "xdg-data"))
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	defaultArchivePath := defaultArchivePath()
	if defaultArchivePath == archivePath {
		t.Fatalf("default archive path unexpectedly matches config archive path: %s", archivePath)
	}
	if err := createArchive(defaultArchivePath); err != nil {
		t.Fatalf("create default archive: %v", err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "status flag",
			args: []string{"status", "--config", configPath, "--db", defaultArchivePath, "--json"},
		},
		{
			name: "global flag",
			args: []string{"--config", configPath, "--db", defaultArchivePath, "status", "--json"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run(tc.args, stdout, stderr)
			if code != 1 {
				t.Fatalf("status exit code = %d, want 1", code)
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "status_failed")
			wantMessage := fmt.Sprintf("archive_path points to %s, want %s", archivePath, defaultArchivePath)
			if got["message"] != wantMessage {
				t.Fatalf("message = %q, want %q", got["message"], wantMessage)
			}
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
	}
}

// TestStatusReportsNewestElectrocardiogramEventTimestamp pins the
// AC for #104: once any electrocardiogram session is archived, the
// per-Data-Type status entry projects the latest event timestamp
// through the existing readStatusDataTypes loop. No new code path
// is added — the test catches a regression that strips ECG (or any
// future opt-in Data Type) from the per-Data-Type rollup.
func TestStatusReportsNewestElectrocardiogramEventTimestamp(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO data_points (
		provider_name,
		connection_id,
		data_type,
		upstream_resource_name,
		record_kind,
		start_time_utc,
		end_time_utc,
		data_source_json,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		"electrocardiogram",
		"users/me/dataTypes/electrocardiogram/dataPoints/ecg-2026-05-20",
		"session",
		"2026-05-20T10:30:00Z",
		"2026-05-20T10:30:30Z",
		"{}",
		`{"electrocardiogram":{"classification":"SINUS_RHYTHM"}}`,
		"2026-05-21T00:00:00Z",
		"2026-05-21T00:00:00Z",
	); err != nil {
		db.Close()
		t.Fatalf("insert ECG fixture: %v", err)
	}
	db.Close()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	ecgStatus := statusDataTypeFromJSON(t, got, "electrocardiogram")
	assertJSONNumber(t, ecgStatus, "data_point_count", 1)
	assertJSONString(t, ecgStatus, "newest_data_point_timestamp", "2026-05-20T10:30:30Z")
}

func TestStatusReportsMigrationFailureForUnsupportedSchema(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	setArchiveUserVersion(t, archivePath, currentSchemaVersion+1)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "status_failed")
	if _, ok := got["schema_version"]; ok {
		t.Fatalf("schema_version = %v, want omitted before migration succeeds", got["schema_version"])
	}
	if !strings.Contains(got["message"].(string), fmt.Sprintf("schema version %d", currentSchemaVersion+1)) {
		t.Fatalf("message = %q, want schema version", got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusReportsSchemaVersionForArchiveInspectionFailure(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = ?`, currentSchemaVersion); err != nil {
		_ = db.Close()
		t.Fatalf("delete schema migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "status_failed")
	assertJSONNumber(t, got, "schema_version", currentSchemaVersion)
	if !strings.Contains(got["message"].(string), "missing schema migration") {
		t.Fatalf("message = %q, want missing migration", got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestCountArchiveRowsRejectsUnknownTable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open memory archive: %v", err)
	}
	defer db.Close()

	_, err = countArchiveRows(db, "data_points; DROP TABLE data_points")
	if err == nil {
		t.Fatal("countArchiveRows error = nil, want unsupported table")
	}
	if !strings.Contains(err.Error(), "unsupported Health Archive table") {
		t.Fatalf("countArchiveRows error = %v, want unsupported table", err)
	}
}

func TestSyncRejectsInvalidSourceFamilyOptionsBeforeSetup(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		wantMessage string
	}{
		{
			name:        "unsupported source family",
			args:        []string{"sync", "--source-family", "phone", "--from", "2026-01-01", "--json"},
			wantMessage: "supports only wearable",
		},
		{
			name:        "source family with rollup",
			args:        []string{"sync", "--source-family", "wearable", "--rollup", "daily", "--from", "2026-01-01", "--json"},
			wantMessage: "cannot be combined with --rollup",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run(tc.args, stdout, stderr)
			if code != 1 {
				t.Fatalf("sync exit code = %d, want 1", code)
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "sync_failed")
			if !strings.Contains(got["message"].(string), tc.wantMessage) {
				t.Fatalf("message = %q, want %q", got["message"], tc.wantMessage)
			}
			if _, ok := got["sync_run_id"]; ok {
				t.Fatalf("sync_run_id = %v, want omitted before setup", got["sync_run_id"])
			}
		})
	}
}

func TestSyncArchivesStepsIdempotentlyAndTracksRevisions(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })
	firstPage := `{
		"dataPoints": [{
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a",
			"dataSource": {"platform": "FITBIT", "device": {"manufacturer": "Google", "model": "Pixel Watch"}},
			"steps": {
				"interval": {
					"startTime": "2026-01-01T08:00:00+01:00",
					"startUtcOffset": "3600s",
					"endTime": "2026-01-01T08:15:00+01:00",
					"endUtcOffset": "3600s",
					"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}},
					"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 15}}
				},
				"count": "512"
			}
		}],
		"nextPageToken": "page-2"
	}`
	secondPage := `{
		"dataPoints": [{
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-b",
			"dataSource": {"platform": "FITBIT"},
			"steps": {
				"interval": {
					"startTime": "2026-01-01T09:00:00Z",
					"endTime": "2026-01-01T09:05:00Z"
				},
				"count": "200"
			}
		}]
	}`
	requests := installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"":       firstPage,
		"page-2": secondPage,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "from", "2026-01-01")
	assertJSONString(t, got, "to", "2026-01-02T00:00:00Z")
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 2)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertJSONNumber(t, got, "rollups_seen", 0)
	assertJSONNumber(t, got, "rollups_new", 0)
	assertJSONNumber(t, got, "rollups_updated", 0)
	if len(*requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(*requests))
	}
	if (*requests)[0].endpointName != "dataTypes.steps.list" || (*requests)[0].dataType != "steps" {
		t.Fatalf("request target = (%q, %q), want steps list", (*requests)[0].endpointName, (*requests)[0].dataType)
	}
	if strings.Contains((*requests)[0].url, "source") {
		t.Fatalf("sync URL unexpectedly includes source filtering: %s", (*requests)[0].url)
	}
	if pageToken := mustURLQuery(t, (*requests)[1].url).Get("pageToken"); pageToken != "page-2" {
		t.Fatalf("second pageToken = %q, want page-2", pageToken)
	}
	assertArchivedStepDataPoint(t, archivePath)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 1, "sync_completed", 2, 2, 0, "")

	requests = installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"":       firstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--plain",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("second sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	wantPlain := "status: sync_completed\nsync_run_id: 2\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ndata_types: steps\nfrom: 2026-01-01\nto: 2026-01-02T00:00:00Z\nendpoint_family: list\ndata_points_seen: 2\ndata_points_new: 0\ndata_points_updated: 0\nrollups_seen: 0\nrollups_new: 0\nrollups_updated: 0\nmessage: Sync Run archived steps Data Points\n"
	if stdout.String() != wantPlain {
		t.Fatalf("stdout = %q, want %q", stdout.String(), wantPlain)
	}
	if len(*requests) != 2 {
		t.Fatalf("second request count = %d, want 2", len(*requests))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 2, "sync_completed", 2, 0, 0, "")

	semanticallySameFirstPage := `{
		"nextPageToken": "page-2",
		"dataPoints": [{
			"steps": {
				"count": "512",
				"interval": {
					"civilEndTime": {"time": {"minutes": 15, "hours": 8}, "date": {"day": 1, "month": 1, "year": 2026}},
					"civilStartTime": {"time": {"hours": 8}, "date": {"day": 1, "month": 1, "year": 2026}},
					"endUtcOffset": "3600s",
					"endTime": "2026-01-01T08:15:00+01:00",
					"startUtcOffset": "3600s",
					"startTime": "2026-01-01T08:00:00+01:00"
				}
			},
			"dataSource": {"device": {"model": "Pixel Watch", "manufacturer": "Google"}, "platform": "FITBIT"},
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a"
		}]
	}`
	installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"":       semanticallySameFirstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("semantic sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("semantic stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 3, "sync_completed", 2, 0, 0, "")

	correctedFirstPage := strings.Replace(firstPage, `"count": "512"`, `"count": "999"`, 1)
	correctedFirstPage = strings.Replace(correctedFirstPage, `"startTime": "2026-01-01T08:00:00+01:00"`, `"startTime": "2026-01-01T08:01:00+01:00"`, 1)
	correctedFirstPage = strings.Replace(correctedFirstPage, `"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}}`, `"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 1}}`, 1)
	installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"":       correctedFirstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("corrected sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertSyncRun(t, archivePath, 4, "sync_completed", 2, 0, 1, "")
	assertCorrectedStepRevision(t, archivePath)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesSampleDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	heartRatePage := string(readTestFixture(t, "googlehealth_heart_rate_list.json"))
	requests := installDataPointSyncFetchFake(t, "connect-access-secret", "heart-rate", map[string]string{"": heartRatePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00Z" AND heart_rate.sample_time.physical_time < "2026-01-02T00:00:00Z"` {
		t.Fatalf("heart-rate filter = %q", gotFilter)
	}
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a", "heart-rate", "2026-01-01T07:30:00Z", "2026-01-01T08:30:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"beatsPerMinute":"72"`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "heart-rate", "list", 1, 1, 0, "")

	installDataPointSyncFetchFake(t, "connect-access-secret", "heart-rate", map[string]string{"": heartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("idempotent heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "heart-rate", "list", 1, 0, 0, "")

	correctedHeartRatePage := strings.Replace(heartRatePage, `"beatsPerMinute": "72"`, `"beatsPerMinute": "75"`, 1)
	installDataPointSyncFetchFake(t, "connect-access-secret", "heart-rate", map[string]string{"": correctedHeartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("corrected heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a", "heart-rate", "2026-01-01T07:30:00Z", "2026-01-01T08:30:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"beatsPerMinute":"75"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "heart-rate", "list", 1, 0, 1, "")

	oxygenPage := string(readTestFixture(t, "googlehealth_oxygen_saturation_list.json"))
	installDataPointSyncFetchFake(t, "connect-access-secret", "oxygen-saturation", map[string]string{"": oxygenPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "oxygen-saturation",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("oxygen sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("oxygen stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/oxygen-saturation/dataPoints/spo2-2026-01-01-a", "oxygen-saturation", "2026-01-01T22:10:00Z", "2026-01-01T22:10:00", "2026-01-01", "", `"percentage":"97.5"`)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertSyncRunForDataType(t, archivePath, 4, "sync_completed", "oxygen-saturation", "list", 1, 1, 0, "")

	heartRateVariabilityPage := string(readTestFixture(t, "googlehealth_heart_rate_variability_list.json"))
	installDataPointSyncFetchFake(t, "connect-access-secret", "heart-rate-variability", map[string]string{"": heartRateVariabilityPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate-variability",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("heart-rate variability sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("heart-rate variability stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/heart-rate-variability/dataPoints/hrv-2026-01-01-a", "heart-rate-variability", "2026-01-01T05:20:00Z", "2026-01-01T05:20:00", "2026-01-01", "", `"rootMeanSquareOfSuccessiveDifferencesMilliseconds":42.125`)
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertSyncRunForDataType(t, archivePath, 5, "sync_completed", "heart-rate-variability", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesWeightDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	weightPage := string(readTestFixture(t, "googlehealth_weight_list.json"))
	requests := installDataPointSyncFetchFake(t, "connect-access-secret", "weight", map[string]string{"": weightPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `weight.sample_time.physical_time >= "2026-01-01T00:00:00Z" AND weight.sample_time.physical_time < "2026-01-02T00:00:00Z"` {
		t.Fatalf("weight filter = %q", gotFilter)
	}
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/weight/dataPoints/weight-2026-01-01", "weight", "2026-01-01T05:45:00Z", "2026-01-01T06:45:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"weightGrams":71234.5`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "weight", "list", 1, 1, 0, "")

	installDataPointSyncFetchFake(t, "connect-access-secret", "weight", map[string]string{"": weightPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("idempotent weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "weight", "list", 1, 0, 0, "")

	correctedWeightPage := strings.Replace(weightPage, `"weightGrams": 71234.5`, `"weightGrams": 71235.25`, 1)
	installDataPointSyncFetchFake(t, "connect-access-secret", "weight", map[string]string{"": correctedWeightPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("corrected weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/weight/dataPoints/weight-2026-01-01", "weight", "2026-01-01T05:45:00Z", "2026-01-01T06:45:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"weightGrams":71235.25`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "weight", "list", 1, 0, 1, "")

	reconciledWeightPage := `{"dataPoints": [{
		"dataPointName": "users/me/dataTypes/weight/dataPoints/weight-2026-01-01-wearable",
		"weight": {
			"sampleTime": {
				"physicalTime": "2026-01-01T06:45:00+01:00",
				"utcOffset": "3600s",
				"civilTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 6, "minutes": 45}}
			},
			"weightGrams": 71234.5
		}
	}]}`
	reconcileRequests := installDataPointReconcileFetchFake(t, "connect-access-secret", "weight", map[string]string{"": reconciledWeightPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("wearable weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("wearable weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if gotFamily := mustURLQuery(t, (*reconcileRequests)[0].url).Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("weight dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/weight/dataPoints/weight-2026-01-01-wearable", "weight", "2026-01-01T05:45:00Z", "2026-01-01T06:45:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"weightGrams":71234.5`)
	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, 4, "sync_completed", "weight", "reconcile", "wearable", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesDistanceDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	distancePage := string(readTestFixture(t, "googlehealth_distance_list.json"))
	requests := installDataPointSyncFetchFake(t, "connect-access-secret", "distance", map[string]string{"": distancePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `distance.interval.start_time >= "2026-01-01T00:00:00Z" AND distance.interval.start_time < "2026-01-02T00:00:00Z"` {
		t.Fatalf("distance filter = %q", gotFilter)
	}
	assertArchivedIntervalDataPoint(t, archivePath, "users/me/dataTypes/distance/dataPoints/distance-2026-01-01", "distance", "2026-01-01T07:00:00Z", "2026-01-01T07:30:00Z", "2026-01-01T08:00:00", "2026-01-01T08:30:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"millimeters":"2450"`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "distance", "list", 1, 1, 0, "")

	installDataPointSyncFetchFake(t, "connect-access-secret", "distance", map[string]string{"": distancePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("idempotent distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "distance", "list", 1, 0, 0, "")

	correctedDistancePage := strings.Replace(distancePage, `"millimeters": "2450"`, `"millimeters": "2500"`, 1)
	installDataPointSyncFetchFake(t, "connect-access-secret", "distance", map[string]string{"": correctedDistancePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("corrected distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedIntervalDataPoint(t, archivePath, "users/me/dataTypes/distance/dataPoints/distance-2026-01-01", "distance", "2026-01-01T07:00:00Z", "2026-01-01T07:30:00Z", "2026-01-01T08:00:00", "2026-01-01T08:30:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"millimeters":"2500"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "distance", "list", 1, 0, 1, "")

	reconciledDistancePage := `{"dataPoints": [{
		"dataPointName": "users/me/dataTypes/distance/dataPoints/distance-2026-01-01-wearable",
		"distance": {
			"interval": {
				"startTime": "2026-01-01T08:00:00+01:00",
				"startUtcOffset": "3600s",
				"endTime": "2026-01-01T08:30:00+01:00",
				"endUtcOffset": "3600s",
				"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}},
				"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 30}}
			},
			"millimeters": "2450"
		}
	}]}`
	reconcileRequests := installDataPointReconcileFetchFake(t, "connect-access-secret", "distance", map[string]string{"": reconciledDistancePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("wearable distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("wearable distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if gotFamily := mustURLQuery(t, (*reconcileRequests)[0].url).Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("distance dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertArchivedIntervalDataPoint(t, archivePath, "users/me/dataTypes/distance/dataPoints/distance-2026-01-01-wearable", "distance", "2026-01-01T07:00:00Z", "2026-01-01T07:30:00Z", "2026-01-01T08:00:00", "2026-01-01T08:30:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, "{}", `"millimeters":"2450"`)
	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, 4, "sync_completed", "distance", "reconcile", "wearable", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesDailyDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	restingHeartRatePage := string(readTestFixture(t, "googlehealth_daily_resting_heart_rate_list.json"))
	requests := installDataPointSyncFetchFake(t, "connect-access-secret", "daily-resting-heart-rate", map[string]string{"": restingHeartRatePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-resting-heart-rate",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("daily resting heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily resting heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONString(t, got, "to", "2026-01-02")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if (*requests)[0].endpointName != "dataTypes.daily-resting-heart-rate.list" {
		t.Fatalf("endpoint = %q, want daily Data Type list", (*requests)[0].endpointName)
	}
	if strings.Contains((*requests)[0].url, "dailyRollUp") {
		t.Fatalf("daily Data Point sync used Rollup URL: %s", (*requests)[0].url)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `daily_resting_heart_rate.date >= "2026-01-01" AND daily_resting_heart_rate.date < "2026-01-02"` {
		t.Fatalf("daily resting heart-rate filter = %q", gotFilter)
	}
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-resting-heart-rate/dataPoints/rhr-2026-01-01", "daily-resting-heart-rate", "2026-01-01", `"beatsPerMinute":"61"`)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "daily-resting-heart-rate", "list", 1, 1, 0, "")

	installDataPointSyncFetchFake(t, "connect-access-secret", "daily-resting-heart-rate", map[string]string{"": restingHeartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-resting-heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("idempotent daily sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent daily stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "daily-resting-heart-rate", "list", 1, 0, 0, "")

	correctedRestingHeartRatePage := strings.Replace(restingHeartRatePage, `"beatsPerMinute": "61"`, `"beatsPerMinute": "63"`, 1)
	installDataPointSyncFetchFake(t, "connect-access-secret", "daily-resting-heart-rate", map[string]string{"": correctedRestingHeartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-resting-heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("corrected daily sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected daily stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-resting-heart-rate/dataPoints/rhr-2026-01-01", "daily-resting-heart-rate", "2026-01-01", `"beatsPerMinute":"63"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "daily-resting-heart-rate", "list", 1, 0, 1, "")

	dailyOxygenPage := string(readTestFixture(t, "googlehealth_daily_oxygen_saturation_list.json"))
	installDataPointSyncFetchFake(t, "connect-access-secret", "daily-oxygen-saturation", map[string]string{"": dailyOxygenPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-oxygen-saturation",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("daily oxygen sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily oxygen stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-oxygen-saturation/dataPoints/spo2-daily-2026-01-01", "daily-oxygen-saturation", "2026-01-01", `"averagePercentage":96.8`)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 4, "sync_completed", "daily-oxygen-saturation", "list", 1, 1, 0, "")

	dailyHeartRateVariabilityPage := string(readTestFixture(t, "googlehealth_daily_heart_rate_variability_list.json"))
	installDataPointSyncFetchFake(t, "connect-access-secret", "daily-heart-rate-variability", map[string]string{"": dailyHeartRateVariabilityPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-heart-rate-variability",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("daily heart-rate variability sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily heart-rate variability stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-heart-rate-variability/dataPoints/hrv-daily-2026-01-01", "daily-heart-rate-variability", "2026-01-01", `"averageHeartRateVariabilityMilliseconds":45.7`)
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 5, "sync_completed", "daily-heart-rate-variability", "list", 1, 1, 0, "")

	dailyRespiratoryRatePage := string(readTestFixture(t, "googlehealth_daily_respiratory_rate_list.json"))
	installDataPointSyncFetchFake(t, "connect-access-secret", "daily-respiratory-rate", map[string]string{"": dailyRespiratoryRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-respiratory-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("daily respiratory-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily respiratory-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-respiratory-rate/dataPoints/resp-daily-2026-01-01", "daily-respiratory-rate", "2026-01-01", `"breathsPerMinute":14.2`)
	assertArchiveTableCount(t, archivePath, "data_points", 4)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 6, "sync_completed", "daily-respiratory-rate", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesSleepSessionDataPoints(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	sleepPage := string(readTestFixture(t, "googlehealth_sleep_list.json"))
	requests := installDataPointSyncFetchFake(t, "connect-access-secret", "sleep", map[string]string{"": sleepPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "sleep",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("sleep sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("sleep stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONString(t, got, "to", "2026-01-03")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if (*requests)[0].endpointName != "dataTypes.sleep.list" {
		t.Fatalf("endpoint = %q, want sleep Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `sleep.interval.civil_end_time >= "2026-01-01" AND sleep.interval.civil_end_time < "2026-01-03"` {
		t.Fatalf("sleep filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/sleep/dataPoints/sleep-2026-01-01", "sleep", "2026-01-01T21:30:00Z", "2026-01-02T05:45:00Z", "2026-01-01T22:30:00", "2026-01-02T06:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"type":"LIGHT"`)
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "sleep", "list", 1, 1, 0, "")

	installDataPointSyncFetchFake(t, "connect-access-secret", "sleep", map[string]string{"": sleepPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "sleep",
		"--from", "2026-01-01",
		"--to", "2026-01-03",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("idempotent sleep sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent sleep stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "sleep", "list", 1, 0, 0, "")

	correctedSleepPage := strings.Replace(sleepPage, `"type": "LIGHT"`, `"type": "REM"`, 1)
	installDataPointSyncFetchFake(t, "connect-access-secret", "sleep", map[string]string{"": correctedSleepPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "sleep",
		"--from", "2026-01-01",
		"--to", "2026-01-03",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("corrected sleep sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected sleep stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/sleep/dataPoints/sleep-2026-01-01", "sleep", "2026-01-01T21:30:00Z", "2026-01-02T05:45:00Z", "2026-01-01T22:30:00", "2026-01-02T06:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"type":"REM"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "sleep", "list", 1, 0, 1, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesExerciseSessionDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	exercisePage := string(readTestFixture(t, "googlehealth_exercise_list.json"))
	requests := installDataPointSyncFetchFake(t, "connect-access-secret", "exercise", map[string]string{"": exercisePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "exercise",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exercise sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("exercise stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertJSONString(t, got, "to", "2026-01-02")
	if (*requests)[0].endpointName != "dataTypes.exercise.list" {
		t.Fatalf("endpoint = %q, want exercise Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `exercise.interval.civil_start_time >= "2026-01-01" AND exercise.interval.civil_start_time < "2026-01-02"` {
		t.Fatalf("exercise filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/exercise/dataPoints/exercise-2026-01-01", "exercise", "2026-01-01T16:15:00Z", "2026-01-01T16:45:00Z", "2026-01-01T17:15:00", "2026-01-01T17:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"exerciseType":"RUNNING"`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "exercise", "list", 1, 1, 0, "")

	installDataPointSyncFetchFake(t, "connect-access-secret", "exercise", map[string]string{"": exercisePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "exercise",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("idempotent exercise sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent exercise stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "exercise", "list", 1, 0, 0, "")

	correctedExercisePage := strings.Replace(exercisePage, `"activeDuration": "1800s"`, `"activeDuration": "2100s"`, 1)
	installDataPointSyncFetchFake(t, "connect-access-secret", "exercise", map[string]string{"": correctedExercisePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "exercise",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("corrected exercise sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected exercise stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/exercise/dataPoints/exercise-2026-01-01", "exercise", "2026-01-01T16:15:00Z", "2026-01-01T16:45:00Z", "2026-01-01T17:15:00", "2026-01-01T17:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"activeDuration":"2100s"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "exercise", "list", 1, 0, 1, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestSyncArchivesElectrocardiogramSessionDataPoints pins the
// session-parser contract for the Tier 2 ECG Data Type (#104).
// addStoredConnectionScope simulates the user having run
// `connect --add-scopes ecg`; without that the AccessToken call
// would short-circuit on the missing-scope error. The fixture mirrors
// the sleep/exercise session shape because the live probe is deferred
// until the user grants the scope against the live OAuth client.
func TestSyncArchivesElectrocardiogramSessionDataPoints(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	addStoredConnectionScope(t, archivePath, googleHealthEcgReadonlyScope)
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	ecgPage := string(readTestFixture(t, "googlehealth_electrocardiogram_list.json"))
	requests := installDataPointSyncFetchFake(t, "connect-access-secret", "electrocardiogram", map[string]string{"": ecgPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "electrocardiogram",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("electrocardiogram sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("electrocardiogram stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if (*requests)[0].endpointName != "dataTypes.electrocardiogram.list" {
		t.Fatalf("endpoint = %q, want electrocardiogram Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `electrocardiogram.interval.civil_start_time >= "2026-01-01" AND electrocardiogram.interval.civil_start_time < "2026-01-02"` {
		t.Fatalf("electrocardiogram filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/electrocardiogram/dataPoints/ecg-2026-01-01", "electrocardiogram", "2026-01-01T09:30:00Z", "2026-01-01T09:30:30Z", "2026-01-01T10:30:00", "2026-01-01T10:30:30", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"classification":"SINUS_RHYTHM"`)
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "electrocardiogram", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestSyncArchivesIrregularRhythmNotificationSessionDataPoints pins
// the session-parser contract for the Tier 2 IRN Data Type (#104).
// Same harness as the ECG test, with the IRN-specific scope and
// fixture payload.
func TestSyncArchivesIrregularRhythmNotificationSessionDataPoints(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	addStoredConnectionScope(t, archivePath, googleHealthIrnReadonlyScope)
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	irnPage := string(readTestFixture(t, "googlehealth_irregular_rhythm_notification_list.json"))
	requests := installDataPointSyncFetchFake(t, "connect-access-secret", "irregular-rhythm-notification", map[string]string{"": irnPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "irregular-rhythm-notification",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("irregular-rhythm-notification sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("irregular-rhythm-notification stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if (*requests)[0].endpointName != "dataTypes.irregular-rhythm-notification.list" {
		t.Fatalf("endpoint = %q, want irregular-rhythm-notification Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `irregular_rhythm_notification.interval.civil_start_time >= "2026-01-01" AND irregular_rhythm_notification.interval.civil_start_time < "2026-01-02"` {
		t.Fatalf("irregular-rhythm-notification filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/irregular-rhythm-notification/dataPoints/irn-2026-01-01", "irregular-rhythm-notification", "2026-01-01T08:00:00Z", "2026-01-01T08:00:30Z", "2026-01-01T09:00:00", "2026-01-01T09:00:30", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"classification":"ATRIAL_FIBRILLATION_SUGGESTED"`)
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "irregular-rhythm-notification", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesWearableStepsViaReconcile(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	defaultPage := `{"dataPoints": [{
		"name": "users/me/dataTypes/steps/dataPoints/shared-step",
		"dataSource": {"platform": "FITBIT", "device": {"manufacturer": "Google", "model": "Pixel Watch"}},
		"steps": {"interval": {"startTime": "2026-01-01T08:00:00Z", "endTime": "2026-01-01T08:15:00Z"}, "count": "512"}
	}]}`
	listRequests := installStepSyncFetchFake(t, "connect-access-secret", map[string]string{"": defaultPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("default sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if len(*listRequests) != 1 {
		t.Fatalf("default request count = %d, want 1", len(*listRequests))
	}
	if query := mustURLQuery(t, (*listRequests)[0].url); query.Get("dataSourceFamily") != "" {
		t.Fatalf("default sync dataSourceFamily = %q, want empty", query.Get("dataSourceFamily"))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 1, "sync_completed", "list", "", 1, 1, 0, "")

	reconciledPage := `{"dataPoints": [{
		"dataPointName": "users/me/dataTypes/steps/dataPoints/shared-step",
		"steps": {"interval": {"startTime": "2026-01-01T08:00:00Z", "endTime": "2026-01-01T08:15:00Z"}, "count": "512"}
	}]}`
	reconcileRequests := installStepReconcileFetchFake(t, "connect-access-secret", map[string]string{"": reconciledPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("wearable sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("wearable stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if len(*reconcileRequests) != 1 {
		t.Fatalf("reconcile request count = %d, want 1", len(*reconcileRequests))
	}
	query := mustURLQuery(t, (*reconcileRequests)[0].url)
	if gotFamily := query.Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	wantFilter := `steps.interval.start_time >= "2026-01-01T00:00:00Z" AND steps.interval.start_time < "2026-01-02T00:00:00Z"`
	if gotFilter := query.Get("filter"); gotFilter != wantFilter {
		t.Fatalf("filter = %q, want %q", gotFilter, wantFilter)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 2, "sync_completed", "reconcile", "wearable", 1, 1, 0, "")

	installStepReconcileFetchFake(t, "connect-access-secret", map[string]string{"": reconciledPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--plain",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("idempotent wearable sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "source_family: wearable\n") {
		t.Fatalf("plain stdout = %q, want source family", stdout.String())
	}
	if !strings.Contains(stdout.String(), "data_points_new: 0\n") {
		t.Fatalf("plain stdout = %q, want idempotent count", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 3, "sync_completed", "reconcile", "wearable", 1, 0, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesStepsDailyRollupsOnlyWhenRequested(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	listRequests := installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("default sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("default stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 0)
	assertJSONNumber(t, got, "rollups_seen", 0)
	if len(*listRequests) != 1 {
		t.Fatalf("default request count = %d, want 1", len(*listRequests))
	}
	if (*listRequests)[0].endpointName != "dataTypes.steps.list" {
		t.Fatalf("default endpoint = %q, want list", (*listRequests)[0].endpointName)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunWithEndpointFamily(t, archivePath, 1, "sync_completed", "list", 0, 0, 0, "")

	firstRollupPage := `{
		"rollupDataPoints": [{
			"steps": {"countSum": "1234"},
			"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
			"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
		}]
	}`
	rollupRequests := installStepDailyRollupFetchFake(t, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": firstRollupPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "dailyRollUp")
	assertJSONNumber(t, got, "data_points_seen", 0)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertJSONNumber(t, got, "rollups_seen", 1)
	assertJSONNumber(t, got, "rollups_new", 1)
	assertJSONNumber(t, got, "rollups_updated", 0)
	if len(*rollupRequests) != 1 {
		t.Fatalf("rollup request count = %d, want 1", len(*rollupRequests))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertArchivedStepsDailyRollup(t, archivePath, "1234")
	assertSyncRunWithEndpointFamily(t, archivePath, 2, "sync_completed", "dailyRollUp", 1, 1, 0, "")

	installStepDailyRollupFetchFake(t, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": firstRollupPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("idempotent rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "rollups_seen", 1)
	assertJSONNumber(t, got, "rollups_new", 0)
	assertJSONNumber(t, got, "rollups_updated", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertSyncRunWithEndpointFamily(t, archivePath, 3, "sync_completed", "dailyRollUp", 1, 0, 0, "")

	correctedRollupPage := strings.Replace(firstRollupPage, `"countSum": "1234"`, `"countSum": "4321"`, 1)
	installStepDailyRollupFetchFake(t, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": correctedRollupPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("corrected rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "rollups_seen", 1)
	assertJSONNumber(t, got, "rollups_new", 0)
	assertJSONNumber(t, got, "rollups_updated", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertArchivedStepsDailyRollup(t, archivePath, "4321")
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunWithEndpointFamily(t, archivePath, 4, "sync_completed", "dailyRollUp", 1, 0, 1, "")

	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01T12:00:00",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("timed rollup sync exit code = %d, want 1\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	// The gate's preflight message names both supported shapes and the
	// rollup kind so an operator hears exactly what the rollup will
	// accept; this replaces the slice-2 planner-stage "expected
	// YYYY-MM-DD" error with a richer local rejection.
	if !strings.Contains(stdout.String(), "expected YYYY-MM-DD or RFC3339") {
		t.Fatalf("timed rollup stdout = %q, want supported-shapes error", stdout.String())
	}
	if !strings.Contains(stdout.String(), "daily") {
		t.Fatalf("timed rollup stdout = %q, want rollup kind named", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	// PRD #141 slice 3: civil-vs-RFC3339 input-shape errors are caught at
	// the preflight gate before any sync_run row is written. Previously
	// this scenario produced an audit row from the planner-stage parse
	// error; the gate now owns the contract so the archive must show
	// only the 4 rows from the earlier successful invocations.
	assertArchiveTableCount(t, archivePath, "sync_runs", 4)

	longRangeRequests := installStepDailyRollupFetchFake(t, "connect-access-secret", map[string]string{
		"2026-01-01/2026-04-01/": `{"rollupDataPoints": [{
			"steps": {"countSum": "9000"},
			"civilStartTime": {"date": {"year": 2026, "month": 4, "day": 1}},
			"civilEndTime": {"date": {"year": 2026, "month": 4, "day": 2}}
		}]}`,
		"2026-04-01/2026-04-15/": `{"rollupDataPoints": [{
			"steps": {"countSum": "1400"},
			"civilStartTime": {"date": {"year": 2026, "month": 4, "day": 2}},
			"civilEndTime": {"date": {"year": 2026, "month": 4, "day": 3}}
		}]}`,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-04-15",
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("long rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if len(*longRangeRequests) != 2 {
		t.Fatalf("long rollup request count = %d, want 2", len(*longRangeRequests))
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("long rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "rollups_seen", 2)
	assertJSONNumber(t, got, "rollups_new", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 3)
	// Sync Run id is 5 (not 6) because the preceding civil-shape failure
	// is now caught at the gate and does not write a sync_run row.
	assertSyncRunWithEndpointFamily(t, archivePath, 5, "sync_completed", "dailyRollUp", 2, 2, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestParseStepsDailyRollupRequiresCivilEndTime(t *testing.T) {
	_, err := parseGoogleHealthStepsDailyRollup(archivedConnection{
		providerName: "googlehealth",
		id:           "googlehealth:111111256096816351",
	}, json.RawMessage(`{
		"steps": {"countSum": "1234"},
		"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}}
	}`))
	if err == nil || !strings.Contains(err.Error(), "missing civilEndTime") {
		t.Fatalf("parse error = %v, want missing civilEndTime", err)
	}
}

func TestSyncProviderFailureRecordsFailedRun(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != "connect-access-secret" {
			t.Fatalf("sync access token = %q, want stored token", accessToken)
		}
		return nil, errors.New("Google Health raw request failed with HTTP 503")
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	message := got["message"].(string)
	if !strings.Contains(message, "HTTP 503") {
		t.Fatalf("message = %q, want provider status", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, "HTTP 503")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncRefusesDifferentProviderIdentityBeforeArchiving(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installIdentityFetchFake(t, "connect-access-secret", googleIdentity{
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "DIFFERENT",
		rawJSON:            `{"healthUserId":"222222222222222222","legacyUserId":"DIFFERENT"}`,
	})
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run after identity mismatch")
		return nil, nil
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	if message := got["message"].(string); !strings.Contains(message, "different Google Identity") {
		t.Fatalf("message = %q, want identity mismatch", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, "different Google Identity")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncReportsFailedWhenCompletionRecordFails(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:15:00Z"
					},
					"count": "512"
				}
			}]
		}`,
	})
	// Wrap the writer so FinalizeSyncRun (the atomic sync_run+cursor write)
	// fails when called for a sync_completed outcome. This exercises the
	// CLI's "atomic finalize failed → recover-as-sync_failed" path without
	// reaching into the legacy package-level indirection that the executor
	// no longer routes through for completed runs.
	t.Cleanup(func() { healthArchiveWriterOpenerForTest = openHealthArchiveWriter })
	healthArchiveWriterOpenerForTest = func(path string) (healthArchiveWriter, error) {
		inner, err := openHealthArchiveWriter(path)
		if err != nil {
			return nil, err
		}
		return fakeFinalizeWriter{healthArchiveWriter: inner, failOn: failOnCompletedOutcome(errSimulatedFinalizeCompletedFailure)}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	assertJSONNumber(t, got, "data_points_seen", 1)
	if message := got["message"].(string); !strings.Contains(message, "archive finalization failed") {
		t.Fatalf("message = %q, want finalization error", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncFailsBeforeProviderWhenScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenScopes(t, archivePath, []string{googleHealthProfileReadonlyScope})
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run with missing scope")
		return nil, nil
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), googleHealthActivityReadonlyScope) || !strings.Contains(stdout.String(), "connect") {
		t.Fatalf("stdout = %q, want missing scope reconnect hint", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, googleHealthActivityReadonlyScope)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncSampleDataTypeFailsBeforeProviderWhenHealthMetricsScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	setConnectionTokenScopes(t, archivePath, []string{googleHealthProfileReadonlyScope, googleHealthActivityReadonlyScope})
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run with missing health metrics scope")
		return nil, nil
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), googleHealthHealthMetricsReadonlyScope) || !strings.Contains(stdout.String(), "connect") {
		t.Fatalf("stdout = %q, want missing health metrics scope reconnect hint", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_failed", "heart-rate", "list", 0, 0, 0, googleHealthHealthMetricsReadonlyScope)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestRawEndpointIdentityPrintsProviderJSONWithoutArchiving(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	beforeIdentityJSON := archivedConnectionIdentityJSON(t, archivePath)
	installRawFetchFake(t, "connect-access-secret", func(request rawProviderRequest) []byte {
		if request.url != googleHealthIdentityURL {
			t.Fatalf("raw URL = %q, want identity URL", request.url)
		}
		return []byte(`{"healthUserId":"999999999999999999","legacyUserId":"RAW"}`)
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, stderr)
	if code != 0 {
		t.Fatalf("raw exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stdout.String() != `{"healthUserId":"999999999999999999","legacyUserId":"RAW"}` {
		t.Fatalf("stdout = %q, want raw provider JSON", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := archivedConnectionIdentityJSON(t, archivePath); got != beforeIdentityJSON {
		t.Fatalf("raw mutated archived identity JSON: %s", got)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") {
		t.Fatalf("raw output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestRawDataTypeStepsPrintsFixtureJSON(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	fixture := readTestFixture(t, "googlehealth_steps_list.json")
	installRawFetchFake(t, "connect-access-secret", func(request rawProviderRequest) []byte {
		if request.endpointName != "dataTypes.steps.list" || request.dataType != "steps" {
			t.Fatalf("raw request = (%q, %q), want steps list", request.endpointName, request.dataType)
		}
		parsedURL, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("parse raw URL: %v", err)
		}
		if parsedURL.Path != "/v4/users/me/dataTypes/steps/dataPoints" {
			t.Fatalf("raw path = %q, want steps dataPoints path", parsedURL.Path)
		}
		query := parsedURL.Query()
		wantFilter := `steps.interval.start_time >= "2026-01-01T00:00:00Z" AND steps.interval.start_time < "2026-01-02T00:00:00Z"`
		if query.Get("filter") != wantFilter {
			t.Fatalf("filter = %q, want %q", query.Get("filter"), wantFilter)
		}
		if query.Get("pageSize") != "12" || query.Get("pageToken") != "abc123" {
			t.Fatalf("pagination query = %v, want pageSize/pageToken", query)
		}
		return fixture
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"raw",
		"data-type", "steps",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--page-size", "12",
		"--page-token", "abc123",
		"--config", configPath,
		"--db", archivePath,
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("raw exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if !bytes.Equal(stdout.Bytes(), fixture) {
		t.Fatalf("stdout = %q, want fixture JSON", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDailyNamedDataTypeListRequestIsNotRollup(t *testing.T) {
	request, err := buildGoogleHealthDataTypeListRawRequest("daily-resting-heart-rate", "2026-01-01", "2026-01-02", 0, "")
	if err != nil {
		t.Fatalf("build daily-named list request: %v", err)
	}
	if request.endpointName != "dataTypes.daily-resting-heart-rate.list" {
		t.Fatalf("endpointName = %q, want daily Data Type list", request.endpointName)
	}
	if request.method != http.MethodGet {
		t.Fatalf("method = %q, want GET", request.method)
	}
	if len(request.body) != 0 {
		t.Fatalf("request body = %s, want empty list request body", string(request.body))
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/daily-resting-heart-rate/dataPoints" {
		t.Fatalf("path = %q, want Data Points list path", parsedURL.Path)
	}
	if strings.Contains(request.endpointName+parsedURL.Path, "RollUp") || strings.Contains(parsedURL.Path, "rollUp") {
		t.Fatalf("daily Data Type request used Rollup endpoint: %s %s", request.endpointName, parsedURL.Path)
	}
	wantFilter := `daily_resting_heart_rate.date >= "2026-01-01" AND daily_resting_heart_rate.date < "2026-01-02"`
	if got := parsedURL.Query().Get("filter"); got != wantFilter {
		t.Fatalf("filter = %q, want %q", got, wantFilter)
	}
}

func TestRawProviderErrorDoesNotLeakToken(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != "connect-access-secret" {
			t.Fatalf("raw access token = %q, want stored token", accessToken)
		}
		return nil, errors.New("Google Health raw request failed with HTTP 403")
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, stderr)
	if code != 1 {
		t.Fatalf("raw exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 403") {
		t.Fatalf("stderr = %q, want provider status", stderr.String())
	}
	if strings.Contains(stderr.String(), "connect-access-secret") || strings.Contains(stderr.String(), "connect-refresh-secret") {
		t.Fatalf("raw error leaked token material: %s", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestRawDataTypeFailsBeforeProviderWhenScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	var metadata map[string]any
	var metadataJSON string
	if err := db.QueryRow(`SELECT token_metadata_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&metadataJSON); err != nil {
		t.Fatalf("query token metadata: %v", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["scopes"] = []string{googleHealthActivityReadonlyScope}
	updatedMetadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(updatedMetadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token scopes: %v", err)
	}
	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("raw provider fetch should not run with missing scope")
		return nil, nil
	}
	t.Cleanup(func() { fetchRawProvider = originalFetchRawProvider })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"raw", "data-type", "heart-rate", "--from", "2026-01-01", "--config", configPath, "--db", archivePath}, stdout, stderr)
	if code != 1 {
		t.Fatalf("raw exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), googleHealthHealthMetricsReadonlyScope) || !strings.Contains(stderr.String(), "connect") {
		t.Fatalf("stderr = %q, want missing scope reconnect hint", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestBuildGoogleHealthRawRequestUsesProviderNamingConventions(t *testing.T) {
	request, err := buildGoogleHealthRawRequest([]string{"endpoint", "dataTypes.heart-rate.list"}, "2026-01-01", "", 0, "")
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse raw URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/heart-rate/dataPoints" {
		t.Fatalf("path = %q, want kebab-case Data Type path", parsedURL.Path)
	}
	wantFilter := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00Z"`
	if parsedURL.Query().Get("filter") != wantFilter {
		t.Fatalf("filter = %q, want snake-case filter", parsedURL.Query().Get("filter"))
	}
}

func TestBuildGoogleHealthRawRequestRejectsNonListableDataTypes(t *testing.T) {
	_, err := buildGoogleHealthRawRequest([]string{"data-type", "total-calories"}, "2026-01-01", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported Data Type")
	}
	if !strings.Contains(err.Error(), "not supported by dataPoints.list") {
		t.Fatalf("error = %v, want unsupported dataPoints.list", err)
	}
}

// TestBuildGoogleHealthRawRequestEndpointCatalog pins PRD #142 slice 7:
// every identity-style endpoint exposed by `raw endpoint <name>` must
// source its requiredScopes from googleHealthIdentityEndpointScopes so
// the scope contract for `raw` and the introspection commands (devices,
// settings, profile, irn-profile) can never drift apart. When slice 2
// of the PRD revises pairedDevices/getSettings scopes empirically, the
// catalog entry changes and this test follows automatically — no inline
// scope literals to update in main.go.
func TestBuildGoogleHealthRawRequestEndpointCatalog(t *testing.T) {
	tests := []struct {
		name    string
		wantURL string
	}{
		{name: "getIdentity", wantURL: googleHealthIdentityURL},
		{name: "getProfile", wantURL: googleHealthProfileURL},
		{name: "getSettings", wantURL: googleHealthSettingsURL},
		{name: "pairedDevices", wantURL: googleHealthPairedDevicesURL},
		{name: "getIrnProfile", wantURL: googleHealthIRNProfileURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := buildGoogleHealthRawRequest([]string{"endpoint", tt.name}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", tt.name, err)
			}
			if request.endpointName != tt.name {
				t.Fatalf("endpointName = %q, want %q", request.endpointName, tt.name)
			}
			if request.url != tt.wantURL {
				t.Fatalf("url = %q, want %q", request.url, tt.wantURL)
			}
			wantScopes := googleHealthIdentityEndpointScopes[tt.name]
			if len(wantScopes) == 0 {
				t.Fatalf("catalog missing entry for %q — slice 1 contract violated", tt.name)
			}
			if len(request.requiredScopes) != len(wantScopes) {
				t.Fatalf("requiredScopes = %v, want %v (catalog entry)", request.requiredScopes, wantScopes)
			}
			for i, want := range wantScopes {
				if request.requiredScopes[i] != want {
					t.Fatalf("requiredScopes[%d] = %q, want %q (catalog entry)", i, request.requiredScopes[i], want)
				}
			}
		})
	}
}

// TestBuildGoogleHealthRawRequestUnknownEndpoint guards the
// not-found fall-through so a typo (or a renamed endpoint that
// outpaces a catalog update) still surfaces as a clear error rather
// than a nil request.
func TestBuildGoogleHealthRawRequestUnknownEndpoint(t *testing.T) {
	_, err := buildGoogleHealthRawRequest([]string{"endpoint", "nonexistent"}, "", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported raw endpoint")
	}
	if !strings.Contains(err.Error(), `unsupported raw endpoint "nonexistent"`) {
		t.Fatalf("error = %v, want unsupported raw endpoint %q", err, "nonexistent")
	}
}

// TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog pins PRD #142
// slice 7 AC: no `[]string{googleHealthProfileReadonlyScope}` inline
// literal remains in buildGoogleHealthRawRequest. The only source of
// truth for endpoint scopes is the catalog. We verify behaviourally:
// a catalog mutation for the duration of the test flows through to the
// request's requiredScopes — proving the branch did a catalog lookup,
// not a hard-coded literal.
func TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog(t *testing.T) {
	for _, endpoint := range []string{"getIdentity", "getProfile", "getSettings", "pairedDevices", "getIrnProfile"} {
		t.Run(endpoint, func(t *testing.T) {
			original, ok := googleHealthIdentityEndpointScopes[endpoint]
			if !ok {
				t.Fatalf("catalog missing %q — slice 1 contract violated", endpoint)
			}
			sentinel := "https://example.invalid/scope/sentinel-" + endpoint
			googleHealthIdentityEndpointScopes[endpoint] = []string{sentinel}
			t.Cleanup(func() { googleHealthIdentityEndpointScopes[endpoint] = original })

			request, err := buildGoogleHealthRawRequest([]string{"endpoint", endpoint}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", endpoint, err)
			}
			if len(request.requiredScopes) != 1 || request.requiredScopes[0] != sentinel {
				t.Fatalf("requiredScopes = %v, want catalog-driven %q — branch is using an inline scope literal", request.requiredScopes, sentinel)
			}
		})
	}
}

func TestGoogleHealthRawFilterFieldsCoverFirstReleaseDataTypes(t *testing.T) {
	for _, test := range []struct {
		dataType string
		from     string
		want     string
	}{
		{
			dataType: "steps",
			from:     "2026-01-01",
			want:     `steps.interval.start_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "oxygen-saturation",
			from:     "2026-01-01",
			want:     `oxygen_saturation.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "heart-rate-variability",
			from:     "2026-01-01",
			want:     `heart_rate_variability.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "daily-resting-heart-rate",
			from:     "2026-01-01",
			want:     `daily_resting_heart_rate.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-heart-rate-variability",
			from:     "2026-01-01",
			want:     `daily_heart_rate_variability.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-oxygen-saturation",
			from:     "2026-01-01",
			want:     `daily_oxygen_saturation.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-respiratory-rate",
			from:     "2026-01-01",
			want:     `daily_respiratory_rate.date >= "2026-01-01"`,
		},
		{
			dataType: "exercise",
			from:     "2026-01-01",
			want:     `exercise.interval.civil_start_time >= "2026-01-01"`,
		},
		{
			dataType: "sleep",
			from:     "2026-01-01",
			want:     `sleep.interval.civil_end_time >= "2026-01-01"`,
		},
		{
			dataType: "distance",
			from:     "2026-01-01",
			want:     `distance.interval.start_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "weight",
			from:     "2026-01-01",
			want:     `weight.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
	} {
		t.Run(test.dataType, func(t *testing.T) {
			filter, err := googleHealthDataTypeListFilter(test.dataType, test.from, "")
			if err != nil {
				t.Fatalf("filter: %v", err)
			}
			if filter != test.want {
				t.Fatalf("filter = %q, want %q", filter, test.want)
			}
		})
	}
}

func TestGoogleHealthRawFilterPreservesFractionalRFC3339Bounds(t *testing.T) {
	filter, err := googleHealthDataTypeListFilter("heart-rate", "2026-01-01T00:00:00.500Z", "2026-01-01T01:02:03.123456789+02:00")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	want := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00.5Z" AND heart_rate.sample_time.physical_time < "2025-12-31T23:02:03.123456789Z"`
	if filter != want {
		t.Fatalf("filter = %q, want %q", filter, want)
	}
}

func TestConnectAcceptsGlobalNoInput(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	wantNoInput := true
	installConnectFakes(t, fakeConnectConfig{wantNoInput: &wantNoInput})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"--no-input", "connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("connect exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
}

func TestConnectMigratesLegacyV1ArchiveBeforeStoringIdentity(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV1Archive(t, archivePath)
	installConnectFakes(t, fakeConnectConfig{})

	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if userVersion != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", userVersion, currentSchemaVersion)
	}
	var identityJSON string
	if err := db.QueryRow(`SELECT google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&identityJSON); err != nil {
		t.Fatalf("query migrated connection: %v", err)
	}
	if !strings.Contains(identityJSON, `"healthUserId":"111111256096816351"`) {
		t.Fatalf("identity JSON = %s, want archived identity", identityJSON)
	}
}

func TestDoctorMigratesLegacyV1ArchiveBeforeValidation(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV1Archive(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"doctor", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["schema_version"] != float64(currentSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", got["schema_version"], currentSchemaVersion)
	}
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
}

func TestInitMigratesLegacyV1ArchiveBeforeValidation(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV1Archive(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["schema_version"] != float64(currentSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", got["schema_version"], currentSchemaVersion)
	}
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
}

func TestConnectRejectsUnsupportedOSNativeStoreBeforeOAuth(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"os_native\"\nservice = \"gohealthcli\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	originalOS := currentOS
	currentOS = "plan9"
	t.Cleanup(func() { currentOS = originalOS })
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	connectStderr := new(bytes.Buffer)
	code = run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, connectStderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "OS-native Credential Store") {
		t.Fatalf("message = %T(%v), want OS-native preflight failure", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+connectStderr.String())
}

func TestConnectRejectsFileCredentialStoreCollisionsBeforeOAuth(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(tempDir, configPath, archivePath string) string
	}{
		{
			name: "config",
			path: func(_ string, configPath, _ string) string {
				return configPath
			},
		},
		{
			name: "archive",
			path: func(_ string, _, archivePath string) string {
				return archivePath
			},
		},
		{
			name: "oauth-client",
			path: func(tempDir, _, _ string) string {
				return filepath.Join(tempDir, "client_secret.json")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempDir := t.TempDir()
			if err := os.Chmod(tempDir, 0o700); err != nil {
				t.Fatalf("chmod temp dir: %v", err)
			}
			configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
			collisionPath := test.path(tempDir, configPath, archivePath)
			originalContent, err := os.ReadFile(collisionPath)
			if err != nil {
				t.Fatalf("read protected file: %v", err)
			}
			configBytes, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"file\"\npath = \"" + collisionPath + "\"\n"
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if collisionPath == configPath {
				originalContent = []byte(config)
			}
			installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
			if code != 1 {
				t.Fatalf("connect exit code = %d, want 1", code)
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			message, ok := got["message"].(string)
			if !ok || !strings.Contains(message, "must not match") {
				t.Fatalf("message = %T(%v), want credential collision rejection", got["message"], got["message"])
			}
			afterContent, err := os.ReadFile(collisionPath)
			if err != nil {
				t.Fatalf("read protected file after connect: %v", err)
			}
			if !bytes.Equal(afterContent, originalContent) {
				t.Fatalf("protected file was modified")
			}
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
	}
}

func TestConnectRejectsMissingLinuxCredentialHelperBeforeOAuth(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"os_native\"\nservice = \"gohealthcli\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	originalOS := currentOS
	originalFindExecutable := findExecutable
	currentOS = "linux"
	findExecutable = func(name string) (string, error) {
		if name != "secret-tool" {
			t.Fatalf("find executable = %q, want secret-tool", name)
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() {
		currentOS = originalOS
		findExecutable = originalFindExecutable
	})
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	connectStderr := new(bytes.Buffer)
	code = run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, connectStderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "secret-tool") || !strings.Contains(message, "type \"file\"") {
		t.Fatalf("message = %T(%v), want secret-tool file fallback guidance", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+connectStderr.String())
}

func TestConnectRejectsWebOAuthClient(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	clientPath := filepath.Join(tempDir, "client_secret.json")
	content := []byte(`{"web":{"client_id":"test-client","client_secret":"test-secret","redirect_uris":["http://127.0.0.1:8080/oauth2callback"]}}`)
	if err := os.WriteFile(clientPath, content, 0o600); err != nil {
		t.Fatalf("write web OAuth client file: %v", err)
	}
	installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("connect exit code = %d, want 1", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "installed desktop client") {
		t.Fatalf("message = %T(%v), want web client rejection", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestOAuthScopesUseRecognizedGoogleHealthScopes(t *testing.T) {
	scopes := oauthScopesForDataTypes(defaultDataTypes)
	wantScopes := []string{
		googleHealthActivityReadonlyScope,
		googleHealthHealthMetricsReadonlyScope,
		googleHealthSleepReadonlyScope,
		googleHealthProfileReadonlyScope,
	}
	if !slices.Equal(scopes, wantScopes) {
		t.Fatalf("scopes = %v, want configured Google Health readonly scopes %v", scopes, wantScopes)
	}
	for _, scope := range scopes {
		for _, invalid := range []string{"settings.readonly"} {
			if strings.Contains(scope, invalid) {
				t.Fatalf("scopes include unrecognized Google Health scope %q: %v", invalid, scopes)
			}
		}
	}
}

func TestListenForOAuthRedirectPreservesEmptyLoopbackPath(t *testing.T) {
	listener, redirectURI, err := listenForOAuthRedirect([]string{"http://localhost"})
	if err != nil {
		t.Fatalf("listen for OAuth redirect: %v", err)
	}
	defer listener.Close()

	parsed, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect URI: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" {
		t.Fatalf("redirect URI = %s, want dynamic loopback with empty path", redirectURI)
	}
}

func TestParseOAuthTokenResponseRequiresRefreshToken(t *testing.T) {
	_, err := parseOAuthTokenResponse([]byte(`{
		"access_token": "access-secret-value",
		"expires_in": 3600,
		"scope": "https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly",
		"token_type": "Bearer"
	}`), time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "refresh token") {
		t.Fatalf("parse token response error = %v, want missing refresh token", err)
	}
}

func TestFetchGoogleIdentityUsesGetIdentityEndpoint(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })

	var gotURL string
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotURL = request.URL.String()
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"healthUserId":"111111256096816351","legacyUserId":"A1B2C3"}`)),
		}, nil
	})}

	identity, err := fetchGoogleIdentity("access-secret-value")
	if err != nil {
		t.Fatalf("fetch identity: %v", err)
	}
	if gotURL != googleHealthIdentityURL {
		t.Fatalf("identity URL = %q, want %q", gotURL, googleHealthIdentityURL)
	}
	if identity.healthUserID != "111111256096816351" || identity.legacyFitbitUserID != "A1B2C3" {
		t.Fatalf("identity = (%q, %q), want response identity", identity.healthUserID, identity.legacyFitbitUserID)
	}
}

func TestFetchGoogleProfileUsesProfileEndpoint(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })

	var gotURL string
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotURL = request.URL.String()
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"name":"users/111111256096816351/profile","userConfiguredWalkingStrideLengthMm":720}`)),
		}, nil
	})}

	profile, err := fetchGoogleProfile("access-secret-value")
	if err != nil {
		t.Fatalf("fetch profile: %v", err)
	}
	if gotURL != googleHealthProfileURL {
		t.Fatalf("profile URL = %q, want %q", gotURL, googleHealthProfileURL)
	}
	if profile.healthUserID != "111111256096816351" || profile.resourceName != "users/111111256096816351/profile" {
		t.Fatalf("profile = (%q, %q), want response profile", profile.healthUserID, profile.resourceName)
	}
	if !strings.Contains(profile.rawJSON, "userConfiguredWalkingStrideLengthMm") {
		t.Fatalf("profile raw JSON = %s, want profile payload", profile.rawJSON)
	}
}

func TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })

	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != googleHealthIdentityURL {
			t.Fatalf("raw URL = %q, want identity URL", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"access-secret-value rejected"}`)),
		}, nil
	})}

	_, err := fetchGoogleHealthRaw(rawProviderRequest{endpointName: "getIdentity", url: googleHealthIdentityURL}, "access-secret-value")
	if err == nil {
		t.Fatal("fetch raw error = nil, want HTTP failure")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("fetch raw error = %v, want status", err)
	}
	if strings.Contains(err.Error(), "access-secret-value") {
		t.Fatalf("fetch raw error leaked token/body: %v", err)
	}
}

func TestReadLimitedBodyReportsOversize(t *testing.T) {
	body, tooLarge, err := readLimitedBody(strings.NewReader("abcdef"), 5)
	if err != nil {
		t.Fatalf("read limited body: %v", err)
	}
	if !tooLarge {
		t.Fatal("tooLarge = false, want true")
	}
	if body != nil {
		t.Fatalf("body = %q, want nil when oversized", string(body))
	}
}

func TestOSNativeCredentialStoreDoesNotSendTokenAsArgument(t *testing.T) {
	originalOS := currentOS
	originalSecurityCommand := runSecurityAddGenericPassword
	currentOS = "darwin"
	t.Cleanup(func() {
		currentOS = originalOS
		runSecurityAddGenericPassword = originalSecurityCommand
	})

	var gotService string
	var gotKey string
	var gotContent []byte
	runSecurityAddGenericPassword = func(service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStore(credentialStoreConfig{kind: "os_native", service: "gohealthcli"})
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("security command target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("security command content missing token material: %s", string(gotContent))
	}
}

func TestSecurityCredentialStoreFeedsPromptWithoutTokenArgument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake security executable uses POSIX shell")
	}

	tempDir := t.TempDir()
	argvPath := filepath.Join(tempDir, "argv.txt")
	stdinPath := filepath.Join(tempDir, "stdin.txt")
	securityPath := filepath.Join(tempDir, "security")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GOHEALTHCLI_TEST_SECURITY_ARGV\"\ncat > \"$GOHEALTHCLI_TEST_SECURITY_STDIN\"\n"
	if err := os.WriteFile(securityPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake security: %v", err)
	}
	t.Setenv("GOHEALTHCLI_TEST_SECURITY_ARGV", argvPath)
	t.Setenv("GOHEALTHCLI_TEST_SECURITY_STDIN", stdinPath)
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	content := []byte(`{"access_token":"access-secret-value"}`)
	if err := runSecurityAddGenericPasswordCommand("gohealthcli", "googlehealth:111", content); err != nil {
		t.Fatalf("security command: %v", err)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if bytes.Contains(argv, []byte("access-secret-value")) {
		t.Fatalf("security argv contains token material: %s", string(argv))
	}
	if !bytes.Contains(argv, []byte("-w")) {
		t.Fatalf("security argv missing prompt flag: %s", string(argv))
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	wantStdin := string(content) + "\n" + string(content) + "\n"
	if string(stdin) != wantStdin {
		t.Fatalf("security stdin = %q, want password and confirmation", string(stdin))
	}
}

func TestLinuxOSNativeCredentialStoreUsesSecretToolContent(t *testing.T) {
	originalOS := currentOS
	originalSecretToolStore := runSecretToolStore
	currentOS = "linux"
	t.Cleanup(func() {
		currentOS = originalOS
		runSecretToolStore = originalSecretToolStore
	})

	var gotService string
	var gotKey string
	var gotContent []byte
	runSecretToolStore = func(service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStore(credentialStoreConfig{kind: "os_native", service: "gohealthcli"})
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("secret-tool target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("secret-tool content missing token material: %s", string(gotContent))
	}
}

func TestWindowsOSNativeCredentialStoreUsesCredentialManagerContent(t *testing.T) {
	originalOS := currentOS
	originalWindowsCredentialWrite := runWindowsCredentialWrite
	currentOS = "windows"
	t.Cleanup(func() {
		currentOS = originalOS
		runWindowsCredentialWrite = originalWindowsCredentialWrite
	})

	var gotService string
	var gotKey string
	var gotContent []byte
	runWindowsCredentialWrite = func(service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStore(credentialStoreConfig{kind: "os_native", service: "gohealthcli"})
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("Windows Credential Manager target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("Windows Credential Manager content missing token material: %s", string(gotContent))
	}
}

func TestInitStoresSecretProviderReference(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
		"--secret-provider", "1password",
		"--oauth-client-item", "Google Health OAuth",
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	outText := stdout.String()
	for _, want := range []string{
		"status: initialized\n",
		"oauth_client_source: secret_provider\n",
		fmt.Sprintf("schema_version: %d\n", currentSchemaVersion),
	} {
		if !strings.Contains(outText, want) {
			t.Fatalf("stdout missing %q:\n%s", want, outText)
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := string(configBytes)
	for _, want := range []string{
		`source = "secret_provider"`,
		`provider = "1password"`,
		`item = "Google Health OAuth"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
}

func TestInitRequiresExactOAuthClientSource(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	archivePath := filepath.Join(tempDir, "gohealthcli.sqlite")

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	// Slice 7 PRD #143 routes --json failures through the Failure
	// Reporter, which emits a single-line `{"status":...,"message":...}`
	// envelope on stdout (Failure Reporter contract). Stderr stays
	// empty in --json mode; pre-slice-7 the message landed on stderr.
	if !strings.Contains(stdout.String(), "requires --oauth-client-file or --secret-provider") {
		t.Fatalf("stdout missing source error: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"status":"flag_invalid"`) {
		t.Fatalf("stdout missing flag_invalid status: %q", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty in --json failure mode", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive stat err = %v, want not exist", err)
	}
}

func TestInitRejectsInvalidOAuthClientFileBeforeCreatingSetup(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	code := runInit(
		[]string{
			"--config", configPath,
			"--db", archivePath,
			"--oauth-client-file", filepath.Join(tempDir, "missing-client.json"),
		},
		defaultConfigPath(),
		defaultArchivePath(),
		outputMode{},
		stdout,
		stderr,
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "OAuth client file") {
		t.Fatalf("stderr missing OAuth client file error: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive stat err = %v, want not exist", err)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestInitIsIdempotentForExistingSetup(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("initial init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	)
	if code != 0 {
		t.Fatalf("second init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status: already_initialized\n") {
		t.Fatalf("stdout missing already initialized status:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestInitIdempotencyDoesNotRequireHealthyTokenMetadata(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("initial init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.Exec(`INSERT INTO connections (
		id,
		provider_name,
		google_health_user_id,
		token_metadata_json,
		created_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?)`,
		"googlehealth:123",
		"googlehealth",
		"123",
		"{}",
		"2026-05-31T00:00:00Z",
		"2026-05-31T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	)
	if code != 0 {
		t.Fatalf("second init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status: already_initialized\n") {
		t.Fatalf("stdout missing already initialized status:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestInitRejectsExistingInvalidArchive(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("initial init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if err := os.WriteFile(archivePath, []byte{}, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	// Slice 7 PRD #143: --json failure routes through the Failure
	// Reporter envelope on stdout; stderr stays empty in --json mode.
	if !strings.Contains(stdout.String(), "existing Health Archive is not initialized") {
		t.Fatalf("stdout missing archive validation error: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "Health Archive check failed") {
		t.Fatalf("stdout included extra check wrapper: %q", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty in --json failure mode", stderr.String())
	}
}

func TestInitRejectsExistingInvalidConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("initial init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if err := os.WriteFile(configPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	// Slice 7 PRD #143: --plain failure routes the `status:` /
	// `message:` block to stdout AND keeps the `init: <msg>` line on
	// stderr. Both streams now carry the error in --plain mode.
	if !strings.Contains(stdout.String(), "existing config is not initialized") {
		t.Fatalf("stdout missing config validation error block: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "existing config is not initialized") {
		t.Fatalf("stderr missing config validation error line: %q", stderr.String())
	}
}

func TestInitRemovesCreatedConfigWhenArchiveCreationFails(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config", "config.toml")
	archiveParentPath := filepath.Join(tempDir, "not-a-directory")
	archivePath := filepath.Join(archiveParentPath, "gohealthcli.sqlite")
	if err := os.WriteFile(archiveParentPath, []byte{}, 0o600); err != nil {
		t.Fatalf("write archive parent file: %v", err)
	}

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "create Health Archive") {
		t.Fatalf("stderr missing archive creation error: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
}

func TestInitRejectsExistingUnsafeDirectory(t *testing.T) {
	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, "config")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")

	code, stdout, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "not owner-only") {
		t.Fatalf("stderr missing owner-only error: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want not exist", err)
	}
}

func TestInitJSONReportsWriteFailure(t *testing.T) {
	tempDir := t.TempDir()
	oauthClientPath := filepath.Join(tempDir, "client_secret.json")
	if err := os.WriteFile(oauthClientPath, []byte(`{"installed":{"client_id":"test-client","client_secret":"test-secret"}}`), 0o600); err != nil {
		t.Fatalf("write OAuth client file: %v", err)
	}
	stderr := new(bytes.Buffer)

	code := runInit(
		[]string{
			"--config", filepath.Join(tempDir, "config", "config.toml"),
			"--db", filepath.Join(tempDir, "data", "gohealthcli.sqlite"),
			"--json",
			"--oauth-client-file", oauthClientPath,
		},
		defaultConfigPath(),
		defaultArchivePath(),
		outputMode{},
		failingWriter{},
		stderr,
	)

	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "write output") {
		t.Fatalf("stderr missing write error: %q", stderr.String())
	}
}

func TestValidateConfigDoesNotCreateMissingParent(t *testing.T) {
	tempDir := t.TempDir()
	parentPath := filepath.Join(tempDir, "missing")

	err := validateConfig(filepath.Join(parentPath, "config.toml"), filepath.Join(tempDir, "archive.sqlite"))
	if err == nil {
		t.Fatal("validateConfig error = nil, want missing parent failure")
	}
	if _, statErr := os.Stat(parentPath); !os.IsNotExist(statErr) {
		t.Fatalf("parent stat err = %v, want not exist", statErr)
	}
}

func TestArchiveDSNUsesAbsoluteFileURI(t *testing.T) {
	dsn, err := archiveDSN("relative.sqlite", false)
	if err != nil {
		t.Fatalf("archiveDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:///") {
		t.Fatalf("dsn = %q, want absolute file URI", dsn)
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys%3Don") {
		t.Fatalf("dsn = %q, want foreign key pragma", dsn)
	}
	readOnlyDSN, err := archiveDSN("relative.sqlite", true)
	if err != nil {
		t.Fatalf("archiveDSN readonly: %v", err)
	}
	if !strings.Contains(readOnlyDSN, "mode=ro") {
		t.Fatalf("dsn = %q, want readonly mode", readOnlyDSN)
	}
}

func runCommand(t *testing.T, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runCommandWithEnv(t, nil, args...)
}

func runCommandInDir(t *testing.T, dir string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runCommandInDirWithEnv(t, dir, nil, args...)
}

func runCommandWithEnv(t *testing.T, env []string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runCommandInDirWithEnv(t, "", env, args...)
}

func runCommandInDirWithEnv(t *testing.T, dir string, env []string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	ensureTestOAuthClientFiles(t, dir, args)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd := exec.Command(testBinaryPath, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), env...)

	err := cmd.Run()
	if err == nil {
		return 0, stdout, stderr
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), stdout, stderr
	}
	t.Fatalf("run command: %v\nstderr: %s", err, stderr.String())
	return 1, stdout, stderr
}

type fakeConnectConfig struct {
	now                time.Time
	accessToken        string
	refreshToken       string
	refreshExpiresAt   *time.Time
	healthUserID       string
	legacyFitbitUserID string
	wantNoInput        *bool
	failIfCalled       bool
}

type fakeDoctorOnlineConfig struct {
	now                     time.Time
	refreshedAccessToken    string
	wantRefreshToken        string
	wantProviderAccessToken string
	healthUserID            string
	legacyFitbitUserID      string
	refreshErr              error
	providerErr             error
	failRefreshIfCalled     bool
	failProviderIfCalled    bool
}

func installConnectFakes(t *testing.T, config fakeConnectConfig) {
	t.Helper()

	originalOAuthFlow := runOAuthFlow
	originalFetchIdentity := fetchIdentity
	originalCurrentTime := currentTime
	runtime := newConnectFakeRuntime(t, config)
	runOAuthFlow = runtime.runOAuthFlow
	fetchIdentity = runtime.fetchIdentity
	currentTime = runtime.now
	t.Cleanup(func() {
		runOAuthFlow = originalOAuthFlow
		fetchIdentity = originalFetchIdentity
		currentTime = originalCurrentTime
	})
}

func newConnectFakeRuntime(t *testing.T, config fakeConnectConfig) runtimeAdapters {
	t.Helper()

	runtime := productionRuntimeAdapters()
	if config.now.IsZero() {
		config.now = time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	}
	if config.accessToken == "" {
		config.accessToken = "access-secret-value"
	}
	if config.refreshToken == "" {
		config.refreshToken = "refresh-secret-value"
	}
	if config.healthUserID == "" {
		config.healthUserID = "111111256096816351"
	}
	runtime.runOAuthFlow = func(client oauthClientConfig, scopes []string, noInput bool) (oauthTokenResponse, error) {
		if config.failIfCalled {
			t.Fatalf("OAuth flow should not be called")
		}
		if client.clientID != "test-client" || client.clientSecret != "test-secret" {
			t.Fatalf("OAuth client = (%q, %q), want test client", client.clientID, client.clientSecret)
		}
		if len(scopes) == 0 {
			t.Fatal("OAuth scopes empty")
		}
		if config.wantNoInput != nil && noInput != *config.wantNoInput {
			t.Fatalf("noInput = %v, want %v", noInput, *config.wantNoInput)
		}
		return oauthTokenResponse{
			accessToken:           config.accessToken,
			refreshToken:          config.refreshToken,
			tokenType:             "Bearer",
			scopes:                scopes,
			expiresAt:             config.now.Add(time.Hour),
			refreshTokenExpiresAt: config.refreshExpiresAt,
			rawTokenMaterialObject: map[string]any{
				"access_token":  config.accessToken,
				"refresh_token": config.refreshToken,
				"expires_in":    float64(3600),
				"scope":         strings.Join(scopes, " "),
				"token_type":    "Bearer",
			},
		}, nil
	}
	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if config.failIfCalled {
			t.Fatalf("identity fetch should not be called")
		}
		if accessToken != config.accessToken {
			t.Fatalf("identity access token = %q, want fake token", accessToken)
		}
		return googleIdentity{
			healthUserID:       config.healthUserID,
			legacyFitbitUserID: config.legacyFitbitUserID,
			rawJSON:            fmt.Sprintf(`{"healthUserId":%q,"legacyUserId":%q}`, config.healthUserID, config.legacyFitbitUserID),
		}, nil
	}
	runtime.now = func() time.Time { return config.now }
	return runtime
}

func installDoctorOnlineFakes(t *testing.T, config fakeDoctorOnlineConfig) {
	t.Helper()

	originalRefreshOAuthToken := refreshOAuthToken
	originalFetchIdentity := fetchIdentity
	originalCurrentTime := currentTime
	if config.now.IsZero() {
		config.now = time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	}
	if config.refreshedAccessToken == "" {
		config.refreshedAccessToken = "refreshed-access-secret"
	}
	if config.wantRefreshToken == "" {
		config.wantRefreshToken = "refresh-secret-value"
	}
	if config.wantProviderAccessToken == "" {
		config.wantProviderAccessToken = config.refreshedAccessToken
	}
	if config.healthUserID == "" {
		config.healthUserID = "111111256096816351"
	}
	refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		if config.failRefreshIfCalled {
			t.Fatal("token refresh should not be called")
		}
		if refreshToken != config.wantRefreshToken {
			t.Fatalf("refresh token = %q, want configured refresh token", refreshToken)
		}
		if len(fallbackScopes) == 0 {
			t.Fatal("fallback scopes empty")
		}
		if config.refreshErr != nil {
			return oauthTokenResponse{}, config.refreshErr
		}
		return oauthTokenResponse{
			accessToken:  config.refreshedAccessToken,
			refreshToken: refreshToken,
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    config.now.Add(time.Hour),
			rawTokenMaterialObject: map[string]any{
				"access_token":  config.refreshedAccessToken,
				"refresh_token": refreshToken,
				"expires_in":    float64(3600),
				"scope":         strings.Join(fallbackScopes, " "),
				"token_type":    "Bearer",
			},
		}, nil
	}
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if config.failProviderIfCalled {
			t.Fatal("provider reachability check should not be called")
		}
		if accessToken != config.wantProviderAccessToken {
			t.Fatalf("provider access token = %q, want configured token", accessToken)
		}
		if config.providerErr != nil {
			return googleIdentity{}, config.providerErr
		}
		return googleIdentity{
			healthUserID:       config.healthUserID,
			legacyFitbitUserID: config.legacyFitbitUserID,
			rawJSON:            fmt.Sprintf(`{"healthUserId":%q,"legacyUserId":%q}`, config.healthUserID, config.legacyFitbitUserID),
		}, nil
	}
	currentTime = func() time.Time { return config.now }
	t.Cleanup(func() {
		refreshOAuthToken = originalRefreshOAuthToken
		fetchIdentity = originalFetchIdentity
		currentTime = originalCurrentTime
	})
}

func installIdentityFetchFake(t *testing.T, wantAccessToken string, identity googleIdentity) {
	t.Helper()

	originalFetchIdentity := fetchIdentity
	fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("identity access token = %q, want stored token", accessToken)
		}
		return identity, nil
	}
	t.Cleanup(func() {
		fetchIdentity = originalFetchIdentity
	})
}

func installProfileFetchFake(t *testing.T, wantAccessToken string, profile googleProfile, providerErr error) {
	t.Helper()

	originalFetchProfile := fetchProfile
	fetchProfile = func(accessToken string) (googleProfile, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("profile access token = %q, want stored token", accessToken)
		}
		if providerErr != nil {
			return googleProfile{}, providerErr
		}
		return profile, nil
	}
	t.Cleanup(func() {
		fetchProfile = originalFetchProfile
	})
}

func installRawFetchFake(t *testing.T, wantAccessToken string, response func(rawProviderRequest) []byte) {
	t.Helper()

	originalFetchRawProvider := fetchRawProvider
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("raw access token = %q, want stored token", accessToken)
		}
		return response(request), nil
	}
	t.Cleanup(func() {
		fetchRawProvider = originalFetchRawProvider
	})
}

func installStepSyncFetchFake(t *testing.T, wantAccessToken string, pages map[string]string) *[]rawProviderRequest {
	t.Helper()

	originalFetchRawProvider := fetchRawProvider
	runtime, requests := withStepSyncFetchFake(t, productionRuntimeAdapters(), wantAccessToken, pages)
	fetchRawProvider = runtime.fetchRawProvider
	t.Cleanup(func() {
		fetchRawProvider = originalFetchRawProvider
	})
	return requests
}

func withStepSyncFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, pages map[string]string) (runtimeAdapters, *[]rawProviderRequest) {
	t.Helper()

	var requests []rawProviderRequest
	runtime.fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("sync access token = %q, want stored token", accessToken)
		}
		if request.endpointName != "dataTypes.steps.list" || request.dataType != "steps" {
			t.Fatalf("sync request = (%q, %q), want steps list", request.endpointName, request.dataType)
		}
		requests = append(requests, request)
		pageToken := mustURLQuery(t, request.url).Get("pageToken")
		body, ok := pages[pageToken]
		if !ok {
			t.Fatalf("no fake page for pageToken %q", pageToken)
		}
		return []byte(body), nil
	}
	return runtime, &requests
}

func installStepReconcileFetchFake(t *testing.T, wantAccessToken string, pages map[string]string) *[]rawProviderRequest {
	t.Helper()

	originalFetchRawProvider := fetchRawProvider
	runtime, requests := withStepReconcileFetchFake(t, productionRuntimeAdapters(), wantAccessToken, pages)
	fetchRawProvider = runtime.fetchRawProvider
	t.Cleanup(func() {
		fetchRawProvider = originalFetchRawProvider
	})
	return requests
}

func withStepReconcileFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, pages map[string]string) (runtimeAdapters, *[]rawProviderRequest) {
	t.Helper()

	var requests []rawProviderRequest
	runtime.fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("reconcile sync access token = %q, want stored token", accessToken)
		}
		if request.endpointName != "dataTypes.steps.reconcile" || request.dataType != "steps" {
			t.Fatalf("reconcile sync request = (%q, %q), want steps reconcile", request.endpointName, request.dataType)
		}
		if request.sourceFamilyFilter != "wearable" {
			t.Fatalf("source family filter = %q, want wearable", request.sourceFamilyFilter)
		}
		parsedURL, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("parse reconcile URL: %v", err)
		}
		if parsedURL.Path != "/v4/users/me/dataTypes/steps/dataPoints:reconcile" {
			t.Fatalf("reconcile path = %q, want reconcile path", parsedURL.Path)
		}
		requests = append(requests, request)
		pageToken := parsedURL.Query().Get("pageToken")
		body, ok := pages[pageToken]
		if !ok {
			t.Fatalf("no fake reconcile page for pageToken %q", pageToken)
		}
		return []byte(body), nil
	}
	return runtime, &requests
}

func installDataPointReconcileFetchFake(t *testing.T, wantAccessToken, dataType string, pages map[string]string) *[]rawProviderRequest {
	t.Helper()

	originalFetchRawProvider := fetchRawProvider
	var requests []rawProviderRequest
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("reconcile sync access token = %q, want stored token", accessToken)
		}
		if request.endpointName != "dataTypes."+dataType+".reconcile" || request.dataType != dataType {
			t.Fatalf("reconcile sync request = (%q, %q), want %s reconcile", request.endpointName, request.dataType, dataType)
		}
		if request.sourceFamilyFilter != "wearable" {
			t.Fatalf("source family filter = %q, want wearable", request.sourceFamilyFilter)
		}
		parsedURL, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("parse reconcile URL: %v", err)
		}
		wantPath := "/v4/users/me/dataTypes/" + dataType + "/dataPoints:reconcile"
		if parsedURL.Path != wantPath {
			t.Fatalf("reconcile path = %q, want %q", parsedURL.Path, wantPath)
		}
		requests = append(requests, request)
		pageToken := parsedURL.Query().Get("pageToken")
		body, ok := pages[pageToken]
		if !ok {
			t.Fatalf("no fake reconcile page for pageToken %q", pageToken)
		}
		return []byte(body), nil
	}
	t.Cleanup(func() {
		fetchRawProvider = originalFetchRawProvider
	})
	return &requests
}

func installStepDailyRollupFetchFake(t *testing.T, wantAccessToken string, pages map[string]string) *[]rawProviderRequest {
	t.Helper()

	originalFetchRawProvider := fetchRawProvider
	runtime, requests := withStepDailyRollupFetchFake(t, productionRuntimeAdapters(), wantAccessToken, pages)
	fetchRawProvider = runtime.fetchRawProvider
	t.Cleanup(func() {
		fetchRawProvider = originalFetchRawProvider
	})
	return requests
}

func withStepDailyRollupFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, pages map[string]string) (runtimeAdapters, *[]rawProviderRequest) {
	t.Helper()

	var requests []rawProviderRequest
	runtime.fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("rollup sync access token = %q, want stored token", accessToken)
		}
		if request.endpointName != "dataTypes.steps.dailyRollUp" || request.dataType != "steps" {
			t.Fatalf("rollup sync request = (%q, %q), want steps dailyRollUp", request.endpointName, request.dataType)
		}
		if request.method != http.MethodPost {
			t.Fatalf("rollup method = %q, want POST", request.method)
		}
		parsedURL, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("parse rollup URL: %v", err)
		}
		if parsedURL.Path != "/v4/users/me/dataTypes/steps/dataPoints:dailyRollUp" {
			t.Fatalf("rollup path = %q, want dailyRollUp path", parsedURL.Path)
		}
		var body struct {
			Range struct {
				Start struct {
					Date struct {
						Year  int `json:"year"`
						Month int `json:"month"`
						Day   int `json:"day"`
					} `json:"date"`
				} `json:"start"`
				End struct {
					Date struct {
						Year  int `json:"year"`
						Month int `json:"month"`
						Day   int `json:"day"`
					} `json:"date"`
				} `json:"end"`
			} `json:"range"`
			WindowSizeDays int    `json:"windowSizeDays"`
			PageToken      string `json:"pageToken"`
		}
		if err := json.Unmarshal(request.body, &body); err != nil {
			t.Fatalf("rollup body is not valid JSON: %v\nbody: %s", err, string(request.body))
		}
		if body.WindowSizeDays != 1 {
			t.Fatalf("windowSizeDays = %d, want 1", body.WindowSizeDays)
		}
		requests = append(requests, request)
		key := fmt.Sprintf("%04d-%02d-%02d/%04d-%02d-%02d/%s",
			body.Range.Start.Date.Year,
			body.Range.Start.Date.Month,
			body.Range.Start.Date.Day,
			body.Range.End.Date.Year,
			body.Range.End.Date.Month,
			body.Range.End.Date.Day,
			body.PageToken,
		)
		response, ok := pages[key]
		if !ok {
			t.Fatalf("no fake rollup page for key %q", key)
		}
		return []byte(response), nil
	}
	return runtime, &requests
}

// withHeartRateHourlyRollupFetchFake routes the runtime's fetchRawProvider
// to per-page-key canned responses for the hourly heart-rate windowed
// rollUp endpoint. The page-key shape is "<startTime>/<endTime>/<windowSize>/<pageToken>"
// where startTime/endTime are taken VERBATIM from the request body, so a
// test that passes civil dates into the gate-normalized executor proves
// the executor actually used the normalized RFC3339 form (the gate emits
// RFC3339 for hourly per PRD #141 slice 3) rather than the raw civil
// option.from.
func withHeartRateHourlyRollupFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, pages map[string]string) (runtimeAdapters, *[]rawProviderRequest) {
	t.Helper()

	var requests []rawProviderRequest
	runtime.fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("rollup sync access token = %q, want stored token", accessToken)
		}
		if request.endpointName != "dataTypes.heart-rate.rollUp" || request.dataType != "heart-rate" {
			t.Fatalf("rollup sync request = (%q, %q), want heart-rate rollUp", request.endpointName, request.dataType)
		}
		if request.method != http.MethodPost {
			t.Fatalf("rollup method = %q, want POST", request.method)
		}
		parsedURL, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("parse rollup URL: %v", err)
		}
		if parsedURL.Path != "/v4/users/me/dataTypes/heart-rate/dataPoints:rollUp" {
			t.Fatalf("rollup path = %q, want rollUp path", parsedURL.Path)
		}
		var body struct {
			Range struct {
				StartTime string `json:"startTime"`
				EndTime   string `json:"endTime"`
			} `json:"range"`
			WindowSize string `json:"windowSize"`
			PageToken  string `json:"pageToken"`
		}
		if err := json.Unmarshal(request.body, &body); err != nil {
			t.Fatalf("rollup body is not valid JSON: %v\nbody: %s", err, string(request.body))
		}
		requests = append(requests, request)
		key := fmt.Sprintf("%s/%s/%s/%s",
			body.Range.StartTime,
			body.Range.EndTime,
			body.WindowSize,
			body.PageToken,
		)
		response, ok := pages[key]
		if !ok {
			t.Fatalf("no fake rollup page for key %q", key)
		}
		return []byte(response), nil
	}
	return runtime, &requests
}

func installDataPointSyncFetchFake(t *testing.T, wantAccessToken, dataType string, pages map[string]string) *[]rawProviderRequest {
	t.Helper()

	originalFetchRawProvider := fetchRawProvider
	var requests []rawProviderRequest
	fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("sync access token = %q, want stored token", accessToken)
		}
		// The exercise sync path now also hits exportExerciseTcx after
		// each Data Point (#107 slice D, ADR-0009). The fixture archives
		// used by existing exercise sync tests do not carry TCX, so the
		// fake responds with 404 — the production code path treats 404
		// as "no TCX for this Data Point" and continues.
		if request.endpointName == "dataTypes.exercise.exportExerciseTcx" {
			return nil, &googleHealthHTTPError{StatusCode: 404}
		}
		if request.endpointName != "dataTypes."+dataType+".list" || request.dataType != dataType {
			t.Fatalf("sync request = (%q, %q), want %s list", request.endpointName, request.dataType, dataType)
		}
		parsedURL, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("parse sync URL: %v", err)
		}
		wantPath := "/v4/users/me/dataTypes/" + dataType + "/dataPoints"
		if parsedURL.Path != wantPath {
			t.Fatalf("sync path = %q, want %q", parsedURL.Path, wantPath)
		}
		requests = append(requests, request)
		pageToken := parsedURL.Query().Get("pageToken")
		body, ok := pages[pageToken]
		if !ok {
			t.Fatalf("no fake page for pageToken %q", pageToken)
		}
		return []byte(body), nil
	}
	t.Cleanup(func() {
		fetchRawProvider = originalFetchRawProvider
	})
	return &requests
}

func initializeFileCredentialSetup(t *testing.T, tempDir string) (string, string, string) {
	t.Helper()

	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	tokenStorePath := filepath.Join(tempDir, "credential-store", "tokens.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"file\"\npath = \"" + tokenStorePath + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath, archivePath, tokenStorePath
}

func readTestFixture(t *testing.T, name string) []byte {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return content
}

func archivedConnectionIdentityJSON(t *testing.T, archivePath string) string {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var identityJSON string
	if err := db.QueryRow(`SELECT google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&identityJSON); err != nil {
		t.Fatalf("query archived identity JSON: %v", err)
	}
	return identityJSON
}

func archivedConnectionTokenMetadata(t *testing.T, archivePath string) string {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var tokenMetadata string
	if err := db.QueryRow(`SELECT token_metadata_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&tokenMetadata); err != nil {
		t.Fatalf("query archived token metadata: %v", err)
	}
	return tokenMetadata
}

func setConnectionTokenExpiry(t *testing.T, archivePath, expiresAt string) {
	t.Helper()

	var metadata map[string]any
	if err := json.Unmarshal([]byte(archivedConnectionTokenMetadata(t, archivePath)), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["expires_at"] = expiresAt
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(metadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
}

func setConnectionTokenScopes(t *testing.T, archivePath string, scopes []string) {
	t.Helper()

	var metadata map[string]any
	if err := json.Unmarshal([]byte(archivedConnectionTokenMetadata(t, archivePath)), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["scopes"] = scopes
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.Exec(`UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(metadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
}

func assertArchiveTableCount(t *testing.T, archivePath, table string, want int) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func insertStatusFixtureRows(t *testing.T, archivePath string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO connections (
		id,
		provider_name,
		google_health_user_id,
		legacy_fitbit_user_id,
		token_metadata_json,
		google_identity_json,
		created_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth:111111256096816351",
		"googlehealth",
		"111111256096816351",
		"A1B2C3",
		`{"scopes":["https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly"]}`,
		`{"healthUserId":"111111256096816351","legacyUserId":"A1B2C3"}`,
		"2026-01-01T00:00:00Z",
		"2026-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("insert status fixture connection: %v", err)
	}
	dataPoints := []struct {
		dataType     string
		resourceName string
		recordKind   string
		startUTC     any
		endUTC       any
		rawJSON      string
	}{
		{"steps", "users/me/dataTypes/steps/dataPoints/a", "interval", "2026-01-01T08:00:00Z", "2026-01-01T08:15:00Z", `{"steps":{"count":"512"}}`},
		{"steps", "users/me/dataTypes/steps/dataPoints/b", "interval", "2026-01-02T08:00:00Z", "2026-01-02T08:15:00Z", `{"steps":{"count":"1024"}}`},
		{"heart-rate", "users/me/dataTypes/heart-rate/dataPoints/a", "sample", "2026-01-03T09:00:00Z", nil, `{"heartRate":{"bpm":72}}`},
	}
	for _, point := range dataPoints {
		if _, err := db.Exec(`INSERT INTO data_points (
			provider_name,
			connection_id,
			data_type,
			upstream_resource_name,
			record_kind,
			start_time_utc,
			end_time_utc,
			data_source_json,
			raw_json,
			inserted_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"googlehealth",
			"googlehealth:111111256096816351",
			point.dataType,
			point.resourceName,
			point.recordKind,
			point.startUTC,
			point.endUTC,
			"{}",
			point.rawJSON,
			"2026-01-04T00:00:00Z",
			"2026-01-04T00:00:00Z",
		); err != nil {
			t.Fatalf("insert status fixture Data Point %s: %v", point.resourceName, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO rollups (
		provider_name,
		connection_id,
		data_type,
		rollup_kind,
		civil_date,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		"steps",
		"dailyRollUp",
		"2026-01-04",
		`{"steps":{"countSum":"2048"}}`,
		"2026-01-05T00:00:00Z",
		"2026-01-05T00:00:00Z",
	); err != nil {
		t.Fatalf("insert status fixture Rollup: %v", err)
	}
	// Legacy archives (created via createLegacyVxArchive) still carry the
	// pre-#97 table name; the rename ALTER fires only when the migration
	// runs. Pick the right name so this helper works for both fresh-v7
	// and pre-v7 fixtures.
	snapshotTable := "identity_snapshots"
	var legacyName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='profile_snapshots'`).Scan(&legacyName); err == nil {
		snapshotTable = "profile_snapshots"
	}
	if _, err := db.Exec(`INSERT INTO ` + snapshotTable + ` (
		provider_name,
		connection_id,
		raw_json,
		fetched_at
	) VALUES (?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		`{"name":"users/111111256096816351/profile"}`,
		"2026-01-05T00:00:00Z",
	); err != nil {
		t.Fatalf("insert status fixture Profile Snapshot: %v", err)
	}
	syncRuns := []struct {
		status       string
		rangeJSON    string
		endpoint     string
		sourceFamily any
		seen         int
		newCount     int
		updated      int
		startedAt    string
		finishedAt   string
		errorSummary any
	}{
		{"sync_completed", `{"from":"2026-01-01","to":"2026-01-02T00:00:00Z"}`, "list", nil, 1, 1, 0, "2026-01-02T00:00:00Z", "2026-01-02T00:00:10Z", nil},
		{"sync_completed", `{"from":"2026-01-02","to":"2026-01-03T00:00:00Z"}`, "reconcile", "wearable", 2, 2, 0, "2026-01-03T00:00:00Z", "2026-01-03T00:00:10Z", nil},
		{"sync_failed", `{"from":"2026-01-04","to":"2026-01-05T00:00:00Z"}`, "list", nil, 0, 0, 0, "2026-01-05T00:00:00Z", "2026-01-05T00:00:05Z", "Provider timeout after 30s\nretry later"},
	}
	for _, run := range syncRuns {
		if _, err := db.Exec(`INSERT INTO sync_runs (
			provider_name,
			connection_id,
			data_types_requested,
			range_requested_json,
			endpoint_family,
			source_family_filter,
			status,
			seen_count,
			new_count,
			updated_count,
			started_at,
			finished_at,
			error_summary
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"googlehealth",
			"googlehealth:111111256096816351",
			`["steps"]`,
			run.rangeJSON,
			run.endpoint,
			run.sourceFamily,
			run.status,
			run.seen,
			run.newCount,
			run.updated,
			run.startedAt,
			run.finishedAt,
			run.errorSummary,
		); err != nil {
			t.Fatalf("insert status fixture Sync Run: %v", err)
		}
	}
}

func createLegacyV1Archive(t *testing.T, archivePath string) {
	t.Helper()

	createLegacyArchive(t, archivePath, 1)
}

func createLegacyV3Archive(t *testing.T, archivePath string) {
	t.Helper()

	createLegacyArchive(t, archivePath, 3)
}

func createLegacyV4Archive(t *testing.T, archivePath string) {
	t.Helper()

	createLegacyArchive(t, archivePath, 4)
}

func createLegacyArchive(t *testing.T, archivePath string, schemaVersion int) {
	t.Helper()

	if err := ensureOwnerOnlyDir(filepath.Dir(archivePath)); err != nil {
		t.Fatalf("create archive parent: %v", err)
	}
	file, err := os.OpenFile(archivePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create legacy archive file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close legacy archive file: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open legacy archive: %v", err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin legacy migration: %v", err)
	}
	for _, statement := range initialMigrationStatements() {
		if _, err := tx.Exec(statement); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply legacy migration statement: %v", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, 'initial_archive_schema', ?)`, time.Date(2026, 5, 31, 21, 0, 0, 0, time.UTC).Format(time.RFC3339)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("record legacy migration: %v", err)
	}
	if schemaVersion >= 2 {
		if err := applyGoogleIdentityArchiveMigration(tx, time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC).Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply legacy identity migration: %v", err)
		}
	}
	if schemaVersion >= 3 {
		if err := applySourceFamilyArchiveMigration(tx, time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC).Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply legacy source family migration: %v", err)
		}
	}
	if schemaVersion >= 4 {
		if err := applyDailyStepsViewMigration(tx, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply legacy daily steps view migration: %v", err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("set legacy user_version %d: %v", schemaVersion, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit legacy migration: %v", err)
	}
	if usesPOSIXPermissions() {
		if err := os.Chmod(archivePath, 0o600); err != nil {
			t.Fatalf("chmod legacy archive: %v", err)
		}
	}
}

func setArchiveUserVersion(t *testing.T, archivePath string, version int) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatalf("set archive user_version %d: %v", version, err)
	}
}

func runConnectCommand(t *testing.T, configPath, archivePath string) int {
	t.Helper()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if stdout.String() != "" {
		var got map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	return code
}

func ensureTestOAuthClientFiles(t *testing.T, dir string, args []string) {
	t.Helper()

	for index, arg := range args {
		if arg != "--oauth-client-file" || index+1 >= len(args) {
			continue
		}
		path := args[index+1]
		if path == "" {
			continue
		}
		if dir != "" && !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat OAuth client file: %v", err)
		}
		content := []byte(`{"installed":{"client_id":"test-client","client_secret":"test-secret"}}`)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write OAuth client file: %v", err)
		}
	}
}

func expectedDefaultCredentialStoreKind() string {
	return "os_native"
}

func removeCredentialStoreSection(t *testing.T, config string) string {
	t.Helper()

	start := strings.Index(config, "\n[credential_store]\n")
	if start < 0 {
		t.Fatalf("config missing credential_store section:\n%s", config)
	}
	searchFrom := start + len("\n[credential_store]\n")
	end := strings.Index(config[searchFrom:], "\n[")
	if end < 0 {
		return strings.TrimRight(config[:start], "\n") + "\n"
	}
	end += searchFrom
	return strings.TrimRight(config[:start], "\n") + "\n" + config[end+1:]
}

func assertArchiveUserVersion(t *testing.T, archivePath string, want int) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
	}
}

func assertJSONString(t *testing.T, got map[string]any, key, want string) {
	t.Helper()

	value, ok := got[key].(string)
	if !ok {
		t.Fatalf("%s = %T(%v), want string %q", key, got[key], got[key], want)
	}
	if value != want {
		t.Fatalf("%s = %q, want %q", key, value, want)
	}
}

func assertJSONNumber(t *testing.T, got map[string]any, key string, want float64) {
	t.Helper()

	value, ok := got[key].(float64)
	if !ok {
		t.Fatalf("%s = %T(%v), want number %v", key, got[key], got[key], want)
	}
	if value != want {
		t.Fatalf("%s = %v, want %v", key, value, want)
	}
}

func statusDataTypeFromJSON(t *testing.T, got map[string]any, dataType string) map[string]any {
	t.Helper()

	dataTypes, ok := got["data_types"].([]any)
	if !ok {
		t.Fatalf("data_types = %T(%v), want array", got["data_types"], got["data_types"])
	}
	for _, rawItem := range dataTypes {
		item, ok := rawItem.(map[string]any)
		if !ok {
			t.Fatalf("data type item = %T(%v), want object", rawItem, rawItem)
		}
		if item["data_type"] == dataType {
			return item
		}
	}
	t.Fatalf("data_types missing %q: %v", dataType, dataTypes)
	return nil
}

func mustURLQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL %q: %v", rawURL, err)
	}
	return parsed.Query()
}

func assertArchivedStepDataPoint(t *testing.T, archivePath string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var dataType, resourceName, recordKind, startUTC, endUTC, startCivil, endCivil, civilDate, timezoneMetadata, dataSourceJSON, rawJSON string
	if err := db.QueryRow(`SELECT
		data_type,
		upstream_resource_name,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a").Scan(
		&dataType,
		&resourceName,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived step Data Point: %v", err)
	}
	if dataType != "steps" || resourceName != "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a" || recordKind != "interval" {
		t.Fatalf("Data Point identity = (%q, %q, %q), want steps interval resource", dataType, resourceName, recordKind)
	}
	if startUTC != "2026-01-01T07:00:00Z" || endUTC != "2026-01-01T07:15:00Z" {
		t.Fatalf("physical time = (%q, %q), want UTC interval", startUTC, endUTC)
	}
	if startCivil != "2026-01-01T08:00:00" || endCivil != "2026-01-01T08:15:00" || civilDate != "2026-01-01" {
		t.Fatalf("civil time = (%q, %q, %q), want provider civil time", startCivil, endCivil, civilDate)
	}
	if timezoneMetadata != `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}` {
		t.Fatalf("timezone_metadata = %q, want offsets", timezoneMetadata)
	}
	if dataSourceJSON != `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}` {
		t.Fatalf("data_source_json = %q, want compact Data Source", dataSourceJSON)
	}
	if !strings.Contains(rawJSON, `"count":"512"`) {
		t.Fatalf("raw_json = %s, want original steps count", rawJSON)
	}
}

func assertArchivedIntervalDataPoint(t *testing.T, archivePath, resourceName, wantDataType, wantStartUTC, wantEndUTC, wantStartCivil, wantEndCivil, wantCivilDate, wantTimezoneMetadata, wantDataSourceJSON, wantRawContains string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var dataType, recordKind, startUTC, endUTC, dataSourceJSON, rawJSON string
	var startCivil, endCivil, civilDate, timezoneMetadata sql.NullString
	if err := db.QueryRow(`SELECT
		data_type,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, resourceName).Scan(
		&dataType,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived interval Data Point: %v", err)
	}
	if dataType != wantDataType || recordKind != "interval" {
		t.Fatalf("Data Point identity = (%q, %q), want (%q, interval)", dataType, recordKind, wantDataType)
	}
	if startUTC != wantStartUTC || endUTC != wantEndUTC {
		t.Fatalf("physical time = (%q, %q), want (%q, %q)", startUTC, endUTC, wantStartUTC, wantEndUTC)
	}
	if startCivil.String != wantStartCivil || !startCivil.Valid || endCivil.String != wantEndCivil || !endCivil.Valid || civilDate.String != wantCivilDate || !civilDate.Valid {
		t.Fatalf("civil time = (%v(%q), %v(%q), %v(%q)), want start %q end %q date %q", startCivil.Valid, startCivil.String, endCivil.Valid, endCivil.String, civilDate.Valid, civilDate.String, wantStartCivil, wantEndCivil, wantCivilDate)
	}
	if timezoneMetadata.String != wantTimezoneMetadata || !timezoneMetadata.Valid {
		t.Fatalf("timezone_metadata = %v(%q), want %q", timezoneMetadata.Valid, timezoneMetadata.String, wantTimezoneMetadata)
	}
	if dataSourceJSON != wantDataSourceJSON {
		t.Fatalf("data_source_json = %q, want %q", dataSourceJSON, wantDataSourceJSON)
	}
	if !strings.Contains(rawJSON, wantRawContains) {
		t.Fatalf("raw_json = %s, want %s", rawJSON, wantRawContains)
	}
}

func assertArchivedSampleDataPoint(t *testing.T, archivePath, resourceName, wantDataType, wantStartUTC, wantStartCivil, wantCivilDate, wantTimezoneMetadata, wantRawContains string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var dataType, recordKind, startUTC, dataSourceJSON, rawJSON string
	var endUTC, startCivil, endCivil, civilDate, timezoneMetadata sql.NullString
	if err := db.QueryRow(`SELECT
		data_type,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, resourceName).Scan(
		&dataType,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived sample Data Point: %v", err)
	}
	if dataType != wantDataType || recordKind != "sample" {
		t.Fatalf("Data Point identity = (%q, %q), want (%q, sample)", dataType, recordKind, wantDataType)
	}
	if startUTC != wantStartUTC || endUTC.Valid {
		t.Fatalf("physical time = (%q, %v(%q)), want sample start %q only", startUTC, endUTC.Valid, endUTC.String, wantStartUTC)
	}
	if startCivil.String != wantStartCivil || startCivil.Valid != (wantStartCivil != "") || endCivil.Valid || civilDate.String != wantCivilDate || civilDate.Valid != (wantCivilDate != "") {
		t.Fatalf("civil time = (%v(%q), %v(%q), %v(%q)), want start %q date %q", startCivil.Valid, startCivil.String, endCivil.Valid, endCivil.String, civilDate.Valid, civilDate.String, wantStartCivil, wantCivilDate)
	}
	if timezoneMetadata.String != wantTimezoneMetadata || timezoneMetadata.Valid != (wantTimezoneMetadata != "") {
		t.Fatalf("timezone_metadata = %v(%q), want %q", timezoneMetadata.Valid, timezoneMetadata.String, wantTimezoneMetadata)
	}
	if dataSourceJSON == "" {
		t.Fatal("data_source_json is empty")
	}
	if !strings.Contains(rawJSON, wantRawContains) {
		t.Fatalf("raw_json = %s, want %s", rawJSON, wantRawContains)
	}
}

func assertArchivedDailyDataPoint(t *testing.T, archivePath, resourceName, wantDataType, wantCivilDate, wantRawContains string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var dataType, recordKind, dataSourceJSON, rawJSON string
	var startUTC, endUTC, startCivil, endCivil, civilDate, timezoneMetadata sql.NullString
	if err := db.QueryRow(`SELECT
		data_type,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, resourceName).Scan(
		&dataType,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived daily Data Point: %v", err)
	}
	if dataType != wantDataType || recordKind != "daily" {
		t.Fatalf("Data Point identity = (%q, %q), want (%q, daily)", dataType, recordKind, wantDataType)
	}
	if startUTC.Valid || endUTC.Valid || startCivil.Valid || endCivil.Valid {
		t.Fatalf("daily physical/civil times = (%v, %v, %v, %v), want only provider civil date", startUTC.Valid, endUTC.Valid, startCivil.Valid, endCivil.Valid)
	}
	if civilDate.String != wantCivilDate || !civilDate.Valid {
		t.Fatalf("provider_civil_date = %v(%q), want %q", civilDate.Valid, civilDate.String, wantCivilDate)
	}
	if timezoneMetadata.Valid {
		t.Fatalf("timezone_metadata = %q, want omitted for date-only daily Data Point", timezoneMetadata.String)
	}
	if dataSourceJSON == "" {
		t.Fatal("data_source_json is empty")
	}
	if !strings.Contains(rawJSON, wantRawContains) {
		t.Fatalf("raw_json = %s, want %s", rawJSON, wantRawContains)
	}
}

func assertArchivedSessionDataPoint(t *testing.T, archivePath, resourceName, wantDataType, wantStartUTC, wantEndUTC, wantStartCivil, wantEndCivil, wantCivilDate, wantTimezoneMetadata, wantDataSourceJSON, wantRawContains string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var dataType, recordKind, startUTC, endUTC, dataSourceJSON, rawJSON string
	var startCivil, endCivil, civilDate, timezoneMetadata sql.NullString
	if err := db.QueryRow(`SELECT
		data_type,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, resourceName).Scan(
		&dataType,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived session Data Point: %v", err)
	}
	if dataType != wantDataType || recordKind != "session" {
		t.Fatalf("Data Point identity = (%q, %q), want (%q, session)", dataType, recordKind, wantDataType)
	}
	if startUTC != wantStartUTC || endUTC != wantEndUTC {
		t.Fatalf("physical time = (%q, %q), want (%q, %q)", startUTC, endUTC, wantStartUTC, wantEndUTC)
	}
	if startCivil.String != wantStartCivil || !startCivil.Valid || endCivil.String != wantEndCivil || !endCivil.Valid || civilDate.String != wantCivilDate || !civilDate.Valid {
		t.Fatalf("civil time = (%v(%q), %v(%q), %v(%q)), want start %q end %q date %q", startCivil.Valid, startCivil.String, endCivil.Valid, endCivil.String, civilDate.Valid, civilDate.String, wantStartCivil, wantEndCivil, wantCivilDate)
	}
	if timezoneMetadata.String != wantTimezoneMetadata || !timezoneMetadata.Valid {
		t.Fatalf("timezone_metadata = %v(%q), want %q", timezoneMetadata.Valid, timezoneMetadata.String, wantTimezoneMetadata)
	}
	if dataSourceJSON != wantDataSourceJSON {
		t.Fatalf("data_source_json = %q, want %q", dataSourceJSON, wantDataSourceJSON)
	}
	if !strings.Contains(rawJSON, wantRawContains) {
		t.Fatalf("raw_json = %s, want %s", rawJSON, wantRawContains)
	}
}

func assertSyncRun(t *testing.T, archivePath string, id int64, wantStatus string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	assertSyncRunWithEndpointFamily(t, archivePath, id, wantStatus, "list", wantSeen, wantNew, wantUpdated, wantErrorContains)
}

func assertSyncRunWithEndpointFamily(t *testing.T, archivePath string, id int64, wantStatus, wantEndpointFamily string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, id, wantStatus, wantEndpointFamily, "", wantSeen, wantNew, wantUpdated, wantErrorContains)
}

func assertSyncRunWithEndpointFamilyAndSourceFamily(t *testing.T, archivePath string, id int64, wantStatus, wantEndpointFamily, wantSourceFamily string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, id, wantStatus, "steps", wantEndpointFamily, wantSourceFamily, wantSeen, wantNew, wantUpdated, wantErrorContains)
}

func assertSyncRunForDataType(t *testing.T, archivePath string, id int64, wantStatus, wantDataType, wantEndpointFamily string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, id, wantStatus, wantDataType, wantEndpointFamily, "", wantSeen, wantNew, wantUpdated, wantErrorContains)
}

func assertSyncRunForDataTypeWithSourceFamily(t *testing.T, archivePath string, id int64, wantStatus, wantDataType, wantEndpointFamily, wantSourceFamily string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var status, dataTypesJSON, rangeJSON, endpointFamily string
	var sourceFamily sql.NullString
	var seen, newCount, updated int
	var errorSummary sql.NullString
	if err := db.QueryRow(`SELECT
		status,
		data_types_requested,
		range_requested_json,
		endpoint_family,
		source_family_filter,
		seen_count,
		new_count,
		updated_count,
		error_summary
	FROM sync_runs WHERE id = ?`, id).Scan(
		&status,
		&dataTypesJSON,
		&rangeJSON,
		&endpointFamily,
		&sourceFamily,
		&seen,
		&newCount,
		&updated,
		&errorSummary,
	); err != nil {
		t.Fatalf("query Sync Run %d: %v", id, err)
	}
	if status != wantStatus || endpointFamily != wantEndpointFamily {
		t.Fatalf("Sync Run status/family = (%q, %q), want (%q, %s)", status, endpointFamily, wantStatus, wantEndpointFamily)
	}
	if sourceFamily.String != wantSourceFamily || sourceFamily.Valid != (wantSourceFamily != "") {
		t.Fatalf("source_family_filter = %v(%q), want %q", sourceFamily.Valid, sourceFamily.String, wantSourceFamily)
	}
	wantDataTypesJSON := fmt.Sprintf(`["%s"]`, wantDataType)
	if dataTypesJSON != wantDataTypesJSON {
		t.Fatalf("data_types_requested = %q, want %s", dataTypesJSON, wantDataTypesJSON)
	}
	if !strings.Contains(rangeJSON, `"from":"2026-01-01`) {
		t.Fatalf("range_requested_json = %q, want from", rangeJSON)
	}
	if seen != wantSeen || newCount != wantNew || updated != wantUpdated {
		t.Fatalf("Sync Run counts = (%d, %d, %d), want (%d, %d, %d)", seen, newCount, updated, wantSeen, wantNew, wantUpdated)
	}
	if wantErrorContains == "" {
		if errorSummary.Valid {
			t.Fatalf("error_summary = %q, want NULL", errorSummary.String)
		}
		return
	}
	if !errorSummary.Valid || !strings.Contains(errorSummary.String, wantErrorContains) {
		t.Fatalf("error_summary = %v(%q), want %q", errorSummary.Valid, errorSummary.String, wantErrorContains)
	}
}

func assertDataPointSourceFamilyCounts(t *testing.T, archivePath string, want map[string]int) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT IFNULL(source_family_filter, ''), count(*) FROM data_points GROUP BY IFNULL(source_family_filter, '')`)
	if err != nil {
		t.Fatalf("query Data Point source families: %v", err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var sourceFamily string
		var count int
		if err := rows.Scan(&sourceFamily, &count); err != nil {
			t.Fatalf("scan Data Point source family count: %v", err)
		}
		got[sourceFamily] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Data Point source family rows: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Data Point source family counts = %v, want %v", got, want)
	}
}

func assertArchivedStepsDailyRollup(t *testing.T, archivePath, wantCount string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var providerName, connectionID, dataType, rollupKind, civilDate, rawJSON string
	var windowStart, windowEnd, timezoneMetadata sql.NullString
	if err := db.QueryRow(`SELECT
		provider_name,
		connection_id,
		data_type,
		rollup_kind,
		window_start_utc,
		window_end_utc,
		civil_date,
		timezone_metadata,
		raw_json
	FROM rollups`).Scan(
		&providerName,
		&connectionID,
		&dataType,
		&rollupKind,
		&windowStart,
		&windowEnd,
		&civilDate,
		&timezoneMetadata,
		&rawJSON,
	); err != nil {
		t.Fatalf("query Rollup: %v", err)
	}
	if providerName != "googlehealth" || connectionID != "googlehealth:111111256096816351" || dataType != "steps" || rollupKind != "dailyRollUp" {
		t.Fatalf("Rollup identity = (%q, %q, %q, %q), want googlehealth steps dailyRollUp", providerName, connectionID, dataType, rollupKind)
	}
	if windowStart.Valid || windowEnd.Valid {
		t.Fatalf("Rollup UTC window = (%v, %v), want NULL for civil daily Rollup", windowStart, windowEnd)
	}
	if civilDate != "2026-01-01" {
		t.Fatalf("civil_date = %q, want 2026-01-01", civilDate)
	}
	if !timezoneMetadata.Valid || !strings.Contains(timezoneMetadata.String, "civil_start_time") || !strings.Contains(timezoneMetadata.String, "civil_end_time") {
		t.Fatalf("timezone_metadata = %v(%q), want provider civil time metadata", timezoneMetadata.Valid, timezoneMetadata.String)
	}
	if !strings.Contains(rawJSON, `"countSum":"`+wantCount+`"`) {
		t.Fatalf("raw_json = %s, want countSum %s", rawJSON, wantCount)
	}
}

func assertCorrectedStepRevision(t *testing.T, archivePath string) {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var rawJSON, startUTC, startCivil string
	if err := db.QueryRow(`SELECT raw_json, start_time_utc, start_civil_time FROM data_points WHERE upstream_resource_name = ?`, "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a").Scan(&rawJSON, &startUTC, &startCivil); err != nil {
		t.Fatalf("query corrected Data Point: %v", err)
	}
	if !strings.Contains(rawJSON, `"count":"999"`) {
		t.Fatalf("canonical raw_json = %s, want corrected count", rawJSON)
	}
	if startUTC != "2026-01-01T07:01:00Z" || startCivil != "2026-01-01T08:01:00" {
		t.Fatalf("corrected time = (%q, %q), want updated metadata", startUTC, startCivil)
	}
	var previousRawJSON, reason string
	if err := db.QueryRow(`SELECT previous_raw_json, replacement_reason FROM data_point_revisions`).Scan(&previousRawJSON, &reason); err != nil {
		t.Fatalf("query Data Point Revision: %v", err)
	}
	if !strings.Contains(previousRawJSON, `"count":"512"`) || reason != "provider_correction" {
		t.Fatalf("revision = (%s, %q), want previous count and reason", previousRawJSON, reason)
	}
}

func assertNoSecretWords(t *testing.T, text string) {
	t.Helper()
	for _, word := range []string{"access_token", "refresh_token", "client_secret", "id_token", "accessToken", "refreshToken", "clientSecret", "idToken"} {
		if strings.Contains(text, word) {
			t.Fatalf("output leaked %s: %s", word, text)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if !usesPOSIXPermissions() {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %v, want %v", path, got, want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

// TestParseCommonFlowsValuesThrough verifies that --config/--db/--no-input
// land in CommonFlagValues and that ArchivePathExplicit is set whenever
// the user actually typed --db.
func TestParseCommonFlowsValuesThrough(t *testing.T) {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	defaults := CommonFlagValues{
		ConfigPath:  "/default/config",
		ArchivePath: "/default/db",
	}
	spec := CommonFlagSpec{Accepted: []string{"config", "db", "json", "plain", "no-input"}}
	values := RegisterCommon(fs, spec, defaults)

	if err := ParseCommon(fs, values, []string{"--config", "/etc/x.toml", "--db", "/tmp/y.db", "--no-input"}); err != nil {
		t.Fatalf("ParseCommon: %v", err)
	}
	if values.ConfigPath != "/etc/x.toml" {
		t.Errorf("ConfigPath = %q, want %q", values.ConfigPath, "/etc/x.toml")
	}
	if values.ArchivePath != "/tmp/y.db" {
		t.Errorf("ArchivePath = %q, want %q", values.ArchivePath, "/tmp/y.db")
	}
	if !values.ArchivePathExplicit {
		t.Errorf("ArchivePathExplicit = false, want true when --db was passed")
	}
	if !values.NoInput {
		t.Errorf("NoInput = false, want true when --no-input was passed")
	}
}

// TestParseCommonHonoursDefaults verifies that when the user passes no
// flags at all, the values seeded into RegisterCommon survive Parse and
// ArchivePathExplicit stays false.
func TestParseCommonHonoursDefaults(t *testing.T) {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	defaults := CommonFlagValues{
		ConfigPath:  "/default/config",
		ArchivePath: "/default/db",
	}
	spec := CommonFlagSpec{Accepted: []string{"config", "db", "json", "plain", "no-input"}}
	values := RegisterCommon(fs, spec, defaults)

	if err := ParseCommon(fs, values, nil); err != nil {
		t.Fatalf("ParseCommon: %v", err)
	}
	if values.ConfigPath != "/default/config" {
		t.Errorf("ConfigPath = %q, want default", values.ConfigPath)
	}
	if values.ArchivePath != "/default/db" {
		t.Errorf("ArchivePath = %q, want default", values.ArchivePath)
	}
	if values.ArchivePathExplicit {
		t.Errorf("ArchivePathExplicit = true, want false when --db was not passed")
	}
}

// TestParseCommonRejectsUnknownButKnownGlobal verifies the unknown-but-
// known-global error wording: when a subcommand's spec omits a flag that
// IS a known global, the user gets a targeted message ("--plain is not
// supported by <cmd>") instead of stdlib's generic "flag provided but not
// defined" text.
func TestParseCommonRejectsUnknownButKnownGlobal(t *testing.T) {
	fs := flag.NewFlagSet("describe-schema", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	spec := CommonFlagSpec{Accepted: []string{"config", "db", "json"}}
	values := RegisterCommon(fs, spec, CommonFlagValues{})

	err := ParseCommon(fs, values, []string{"--plain"})
	if err == nil {
		t.Fatalf("ParseCommon with --plain should return an error when spec omits 'plain'")
	}
	const want = "--plain is not supported by describe-schema"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// TestParseCommonPreservesParseUsageOnError verifies that when fs.Parse
// rejects an unknown flag, the standard usage block stdlib normally
// prints to fs.Output() still reaches the caller's stderr — the Common
// Flag Set module must not silently swallow it. The error returned to
// the caller carries ErrFlagParseFailed so the caller knows fs.Parse
// already wrote the diagnostic.
func TestParseCommonPreservesParseUsageOnError(t *testing.T) {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	stderr := &bytes.Buffer{}
	fs.SetOutput(stderr)
	values := RegisterCommon(fs, AllCommonFlagsSpec(), CommonFlagValues{})

	err := ParseCommon(fs, values, []string{"--bogus"})
	if err == nil {
		t.Fatalf("ParseCommon should return an error for --bogus")
	}
	if !errors.Is(err, ErrFlagParseFailed) {
		t.Fatalf("error = %v, want ErrFlagParseFailed", err)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Errorf("stderr should contain stdlib parse error; got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage of identity") {
		t.Errorf("stderr should contain stdlib usage block; got %q", stderr.String())
	}
}

// TestParseCommonPreservesHelpUsage verifies that --help still triggers
// the standard `flag` package usage dump that callers rely on. The
// returned error must be flag.ErrHelp so the caller exits 0.
func TestParseCommonPreservesHelpUsage(t *testing.T) {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	stderr := &bytes.Buffer{}
	fs.SetOutput(stderr)
	values := RegisterCommon(fs, AllCommonFlagsSpec(), CommonFlagValues{})

	err := ParseCommon(fs, values, []string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(stderr.String(), "Usage of identity") {
		t.Errorf("stderr should contain stdlib usage block; got %q", stderr.String())
	}
}

// TestParseCommonHandlesDashValueForPriorFlag verifies that a value
// starting with '-' passed to a preceding non-bool flag is NOT mistaken
// for an unknown-but-known-global flag during the pre-Parse scan.
// Example: `--config -weird-path --plain` — `-weird-path` is the
// config value, not a flag.
func TestParseCommonHandlesDashValueForPriorFlag(t *testing.T) {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	values := RegisterCommon(fs, AllCommonFlagsSpec(), CommonFlagValues{})

	if err := ParseCommon(fs, values, []string{"--config", "-weird-path", "--plain"}); err != nil {
		t.Fatalf("ParseCommon: %v", err)
	}
	if values.ConfigPath != "-weird-path" {
		t.Errorf("ConfigPath = %q, want %q", values.ConfigPath, "-weird-path")
	}
	if !values.PlainOutput {
		t.Errorf("PlainOutput = false, want true")
	}
}

// TestParseCommonRejectsPlainAndJSON checks the mutual-exclusion invariant
// that no subcommand should ever have to re-implement: --plain and --json
// cannot both be set.
func TestParseCommonRejectsPlainAndJSON(t *testing.T) {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	spec := CommonFlagSpec{Accepted: []string{"config", "db", "json", "plain", "no-input"}}
	values := RegisterCommon(fs, spec, CommonFlagValues{})

	err := ParseCommon(fs, values, []string{"--plain", "--json"})
	if err == nil {
		t.Fatalf("ParseCommon with --plain --json should return an error")
	}
	const want = "--plain and --json are mutually exclusive"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

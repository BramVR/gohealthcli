package main

import (
	"flag"
	"io"
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

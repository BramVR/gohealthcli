package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// knownGlobalCommonFlags is the canonical set of flag names the Common
// Flag Set module owns. When a subcommand's spec omits one of these but
// the user passes it anyway, ParseCommon swaps stdlib's generic "flag
// provided but not defined" wording for a targeted message that names
// the subcommand.
var knownGlobalCommonFlags = []string{"config", "db", "json", "plain", "no-input"}

// CommonFlagSpec declares which of the five shared flags a subcommand
// accepts. The Common Flag Set module owns the shared parsing surface
// every subcommand crosses (mutual exclusion, unknown-but-known-global
// wording), so the spec is the single seam between subcommand-specific
// flag setup and the shared invariants.
//
// Accepted is a subset of {"config","db","json","plain","no-input"}.
type CommonFlagSpec struct {
	Accepted []string
}

// AllCommonFlagsSpec returns a CommonFlagSpec accepting every shared
// flag. Subcommands whose flag set is exactly the five shared flags
// (identity, profile, settings, devices, irn-profile, status under
// issue #166) use this directly so the call site stays one-line.
func AllCommonFlagsSpec() CommonFlagSpec {
	accepted := make([]string, len(knownGlobalCommonFlags))
	copy(accepted, knownGlobalCommonFlags)
	return CommonFlagSpec{Accepted: accepted}
}

// CommonFlagValues carries the resolved values for the five shared
// flags. ArchivePathExplicit records whether the user actually passed
// --db on the command line; it replaces the per-call flagWasProvided
// dance the subcommands used to do inline.
type CommonFlagValues struct {
	ConfigPath          string
	ArchivePath         string
	JSONOutput          bool
	PlainOutput         bool
	NoInput             bool
	ArchivePathExplicit bool
}

// RegisterCommon registers the subset of shared flags declared in spec
// against fs, seeded from defaults, and returns the values struct that
// will be populated by ParseCommon. Subcommands then register their
// extra (non-common) flags directly on fs as before.
func RegisterCommon(fs *flag.FlagSet, spec CommonFlagSpec, defaults CommonFlagValues) *CommonFlagValues {
	values := &CommonFlagValues{
		ConfigPath:  defaults.ConfigPath,
		ArchivePath: defaults.ArchivePath,
		JSONOutput:  defaults.JSONOutput,
		PlainOutput: defaults.PlainOutput,
		NoInput:     defaults.NoInput,
	}
	for _, name := range spec.Accepted {
		switch name {
		case "config":
			fs.StringVar(&values.ConfigPath, "config", defaults.ConfigPath, "config file path")
		case "db":
			fs.StringVar(&values.ArchivePath, "db", defaults.ArchivePath, "SQLite Health Archive path")
		case "json":
			fs.BoolVar(&values.JSONOutput, "json", defaults.JSONOutput, "write stable JSON to stdout")
		case "plain":
			fs.BoolVar(&values.PlainOutput, "plain", defaults.PlainOutput, "write plain key/value output to stdout")
		case "no-input":
			fs.BoolVar(&values.NoInput, "no-input", defaults.NoInput, "never prompt, never wait for browser input")
		}
	}
	return values
}

// ParseCommon parses args against fs and enforces the shared invariants:
//
//   - If args contains a known global common flag (--config, --db, --json,
//     --plain, --no-input) that the subcommand's spec did NOT declare as
//     accepted, the parse returns a targeted "--<flag> is not supported by
//     <fs.Name()>" error instead of stdlib's generic wording. This check
//     runs BEFORE fs.Parse, because stdlib aborts parsing on the first
//     unknown flag and we never get to inspect Visit().
//   - --plain and --json together return the documented mutual-exclusion
//     error.
//   - ArchivePathExplicit is set to true when --db is on the command line.
func ParseCommon(fs *flag.FlagSet, values *CommonFlagValues, args []string) error {
	accepted := make(map[string]bool, len(knownGlobalCommonFlags))
	for _, name := range knownGlobalCommonFlags {
		accepted[name] = fs.Lookup(name) != nil
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
		name := strings.TrimLeft(arg, "-")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if accepted[name] {
			continue
		}
		if _, isGlobal := accepted[name]; isGlobal {
			return fmt.Errorf("--%s is not supported by %s", name, fs.Name())
		}
	}
	// fs.Parse writes its own diagnostic to fs.Output() AND returns the
	// error. Silence the duplicate by redirecting to io.Discard for the
	// duration of Parse; the caller will print whatever error we return.
	prevOutput := fs.Output()
	fs.SetOutput(io.Discard)
	parseErr := fs.Parse(args)
	fs.SetOutput(prevOutput)
	if parseErr != nil {
		return parseErr
	}
	if values.PlainOutput && values.JSONOutput {
		return errors.New("--plain and --json are mutually exclusive")
	}
	fs.Visit(func(item *flag.Flag) {
		if item.Name == "db" {
			values.ArchivePathExplicit = true
		}
	})
	return nil
}

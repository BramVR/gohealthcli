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

// ErrFlagParseFailed signals that fs.Parse rejected the args and ALREADY
// wrote its full diagnostic (error message + usage block) to fs.Output().
// Call sites should propagate exit code 1 WITHOUT re-printing the error.
//
// Custom errors that ParseCommon returns directly (mutual exclusion,
// unknown-but-known-global) do not wrap this sentinel — callers should
// print those themselves.
var ErrFlagParseFailed = errors.New("flag parse failed")

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
// --db on the command line; ConfigPathExplicit records the same for
// --config. The two explicitness bits replace the per-call
// flagWasProvided dance the subcommands used to do inline, and are
// read by the readArchivePathResolver (PRD #144 slice 1) to decide
// when a --db value should win over the config-recorded archive path
// without firing the agreement check.
type CommonFlagValues struct {
	ConfigPath          string
	ArchivePath         string
	JSONOutput          bool
	PlainOutput         bool
	NoInput             bool
	ArchivePathExplicit bool
	ConfigPathExplicit  bool
}

// RegisterCommon registers the subset of shared flags declared in spec
// against fs, seeded from defaults, and returns the values struct that
// will be populated by ParseCommon. Subcommands then register their
// extra (non-common) flags directly on fs as before.
func RegisterCommon(fs *flag.FlagSet, spec CommonFlagSpec, defaults CommonFlagValues) *CommonFlagValues {
	values := &CommonFlagValues{
		ConfigPath:          defaults.ConfigPath,
		ArchivePath:         defaults.ArchivePath,
		JSONOutput:          defaults.JSONOutput,
		PlainOutput:         defaults.PlainOutput,
		NoInput:             defaults.NoInput,
		ArchivePathExplicit: defaults.ArchivePathExplicit,
		ConfigPathExplicit:  defaults.ConfigPathExplicit,
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

// boolFlag mirrors the unexported interface stdlib's `flag` package uses
// internally to detect "bare" bool flags (those that do NOT consume the
// next arg as a value). We re-declare it here so the pre-Parse scan can
// distinguish `--config foo` (string flag consumes "foo") from `--plain`
// followed by another flag (bool, does not consume the next arg).
type boolFlag interface {
	IsBoolFlag() bool
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
//
// Stdlib parse errors (unknown flags the subcommand never heard of,
// `-h` / `--help`, malformed bool values) flow through unchanged:
// fs.Parse writes its full diagnostic (error + usage) to fs.Output()
// as before, and ParseCommon returns ErrFlagParseFailed (or flag.ErrHelp
// for help requests). Callers should NOT re-print those errors.
func ParseCommon(fs *flag.FlagSet, values *CommonFlagValues, args []string) error {
	if err := preScanUnknownButKnownGlobal(fs, args); err != nil {
		return err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return ErrFlagParseFailed
	}
	if values.PlainOutput && values.JSONOutput {
		return errors.New("--plain and --json are mutually exclusive")
	}
	fs.Visit(func(item *flag.Flag) {
		switch item.Name {
		case "db":
			values.ArchivePathExplicit = true
		case "config":
			values.ConfigPathExplicit = true
		}
	})
	return nil
}

// commonFlagsExitCode collapses the four ParseCommon error shapes the
// migrated subcommands all handle identically into one helper:
//
//   - flag.ErrHelp        → exit 0 (fs.Parse already wrote the usage block)
//   - ErrFlagParseFailed  → exit 1 (fs.Parse already wrote error + usage)
//   - any other error     → route through the unified Failure Reporter
//                           (slice 7, issue #178) so the custom invariant
//                           errors (mutual exclusion, unsupported global)
//                           land in the same `<cmd>: <msg>` shape every
//                           other failure path uses.
//
// fs.Name() supplies the Command prefix; callers do not pass it in. The
// reporter sees no Mode here because the in-flight `--plain` / `--json`
// values are exactly what the mutual-exclusion error is about — default
// mode is the only safe rendering for that branch. Subcommands' own
// `unexpected <cmd> argument` checks run AFTER ParseCommon returns, so
// those failures (which have a known Mode) go through ReportFailure
// directly.
func commonFlagsExitCode(fs *flag.FlagSet, err error, stdout, stderr io.Writer) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if errors.Is(err, ErrFlagParseFailed) {
		return 1
	}
	return ReportFailure(FailureReport{
		Command: fs.Name(),
		Status:  StatusFlagInvalid,
		Message: err.Error(),
	}, stdout, stderr)
}

// preScanUnknownButKnownGlobal walks args BEFORE fs.Parse and returns a
// targeted "--<flag> is not supported by <cmd>" error if the user passed
// one of the five known global common flags that the subcommand's spec
// (i.e. its FlagSet) did not declare. Returns nil otherwise.
//
// The scan respects "--" (end of flags) and skips over non-bool flags'
// values (so `--config -weird-path --plain` is not misread as `--plain`
// being passed positionally).
func preScanUnknownButKnownGlobal(fs *flag.FlagSet, args []string) error {
	knownGlobal := make(map[string]bool, len(knownGlobalCommonFlags))
	for _, name := range knownGlobalCommonFlags {
		knownGlobal[name] = true
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return nil
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return nil
		}
		name := strings.TrimLeft(arg, "-")
		hasEq := strings.IndexByte(name, '=') >= 0
		if hasEq {
			name = name[:strings.IndexByte(name, '=')]
		}
		if knownGlobal[name] && fs.Lookup(name) == nil {
			return fmt.Errorf("--%s is not supported by %s", name, fs.Name())
		}
		// Advance past the value if this is a non-bool flag invoked in
		// "--flag value" form. For bool flags, unknown-to-fs flags, or
		// any flag using "--flag=value" form, the next arg is itself a
		// flag (or positional) — leave it for the next iteration.
		if hasEq {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
			continue
		}
		i++
	}
	return nil
}

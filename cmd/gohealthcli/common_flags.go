package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// commonFlagsSpec is the single source of truth for the five shared
// flags (issue #76): name, type, default, and usage string. Everything
// that mentions a shared flag derives from this slice —
//
//   - RegisterCommon binds the runtime FlagSet entries from it, so every
//     subcommand's --help renders these exact usage strings;
//   - the registry's withCommon / withCommonSubset / withCommonOverrides
//     helpers (commands.go) project it into each commandDef.Flags slice,
//     which `schema --json` publishes and the Project Site renders;
//   - knownGlobalCommonFlags (the pre-Parse scan's vocabulary) is its
//     name projection.
//
// Reword a usage string here and the binary's --help, the published
// schema, and the generated docs move together; the drift test
// (TestEveryCommandFlagSetMatchesRegistryFlags) fails CI if any surface
// is hand-edited out of step. The string-typed Default carries the
// literal default the SCHEMA advertises: config/db are empty because
// their real runtime defaults are platform-dependent paths the schema
// deliberately does not bake in (see flagSpec doc in commands.go) —
// runtime defaults are seeded per-invocation via RegisterCommon's
// defaults argument instead.
var commonFlagsSpec = []flagSpec{
	{Name: "config", Type: "string", Default: "", Usage: "config file path"},
	{Name: "db", Type: "string", Default: "", Usage: "SQLite Health Archive path"},
	{Name: "json", Type: "bool", Default: "false", Usage: "write stable JSON to stdout"},
	{Name: "plain", Type: "bool", Default: "false", Usage: "write plain key/value output to stdout"},
	{Name: "no-input", Type: "bool", Default: "false", Usage: "never prompt, never wait for browser input"},
}

// knownGlobalCommonFlags is the canonical set of flag names the Common
// Flag Set module owns — the name projection of commonFlagsSpec. When a
// subcommand's spec omits one of these but the user passes it anyway,
// ParseCommon swaps stdlib's generic "flag provided but not defined"
// wording for a targeted message that names the subcommand.
var knownGlobalCommonFlags = commonFlagNames()

// flagSetObserver, when non-nil, receives every subcommand's FlagSet
// after all flags (common + subcommand-specific) are registered,
// immediately before parsing. It is the seam the issue #76 schema-drift
// test uses to walk the real runtime FlagSets via flag.VisitAll and
// compare them against the registry's commandDef.Flags — the canonical
// surface `schema --json` and the generated command-reference pages
// advertise. Production leaves the runtimeAdapters field nil; the hook
// fires from ParseCommon (every Common-Flag-Set subcommand funnels
// through it) and from the two bare-Parse build-time verbs (schema,
// docs-export-datasets) so no registry entry escapes the drift check
// (#283: injected through the adapters, not package state).
type flagSetObserver func(fs *flag.FlagSet)

// notifySubcommandFlagSetObserver fires the drift-test seam above. Call
// sites invoke it after the last flag registration and before Parse, so
// the observer always sees the complete flag surface the binary accepts.
func notifySubcommandFlagSetObserver(observe flagSetObserver, fs *flag.FlagSet) {
	if observe != nil {
		observe(fs)
	}
}

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
//
// UsageOverrides replaces the canonical commonFlagsSpec usage string for
// the named flags. A subcommand whose shared-flag semantics genuinely
// diverge (export's --json is a --format synonym) declares the wording
// ONCE in a package-level map that both its registry entry (via
// withCommonOverrides) and this runtime spec consume — the two surfaces
// cannot drift because they read the same map. Nil means "canonical
// wording everywhere", which is every other subcommand.
type CommonFlagSpec struct {
	Accepted       []string
	UsageOverrides map[string]string
}

// commonFlagUsage returns the canonical usage string commonFlagsSpec
// declares for the named shared flag. The top-level gohealthcli FlagSet
// (runWithRuntime) registers the shared flags directly — it carries
// --version, which the Common spec deliberately does not — so it reads
// the wording through this helper instead of duplicating the literals.
// Unknown names return "".
func commonFlagUsage(name string) string {
	for _, f := range commonFlagsSpec {
		if f.Name == name {
			return f.Usage
		}
	}
	return ""
}

// usageFor resolves the usage string RegisterCommon binds for the named
// shared flag: the subcommand's override when declared, the canonical
// commonFlagsSpec wording otherwise. Unknown names return "" — the
// RegisterCommon switch below never binds a flag commonFlagsSpec does
// not declare, so the empty string is unreachable in practice.
func (spec CommonFlagSpec) usageFor(name string) string {
	if usage, ok := spec.UsageOverrides[name]; ok {
		return usage
	}
	return commonFlagUsage(name)
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
		// Usage strings come from commonFlagsSpec (the issue #76 single
		// source of truth) via usageFor, never from string literals here:
		// the same spec feeds the registry's commandDef.Flags projection,
		// so --help and the published schema stay in lockstep.
		switch name {
		case "config":
			fs.StringVar(&values.ConfigPath, "config", defaults.ConfigPath, spec.usageFor("config"))
		case "db":
			fs.StringVar(&values.ArchivePath, "db", defaults.ArchivePath, spec.usageFor("db"))
		case "json":
			fs.BoolVar(&values.JSONOutput, "json", defaults.JSONOutput, spec.usageFor("json"))
		case "plain":
			fs.BoolVar(&values.PlainOutput, "plain", defaults.PlainOutput, spec.usageFor("plain"))
		case "no-input":
			fs.BoolVar(&values.NoInput, "no-input", defaults.NoInput, spec.usageFor("no-input"))
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
func ParseCommon(fs *flag.FlagSet, values *CommonFlagValues, args []string, observe flagSetObserver) error {
	notifySubcommandFlagSetObserver(observe, fs)
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
	if err := requireAnchoredDefaultPaths(values); err != nil {
		return err
	}
	return nil
}

// errUnanchoredDefaultPath is the issue #249 loud failure: a default
// config/archive path could not be anchored to the user's home directory
// (HOME unset and no usable absolute XDG_* override), so it resolved to a
// CWD-relative path. Writing personal health data under the current
// working directory is a silent privacy footgun, so ParseCommon rejects
// it before any command touches the filesystem. The message names the two
// escape hatches: set HOME, or pass --config/--db explicitly. It routes
// through commonFlagsExitCode like the other custom ParseCommon errors.
var errUnanchoredDefaultPath = errors.New(
	"cannot determine home directory; set HOME or pass --config/--db explicitly")

// requireAnchoredDefaultPaths enforces that a --config/--db value the user
// did NOT pass explicitly resolves to an ABSOLUTE path. The gate fires
// only when the in-flight value is BOTH the home-anchored default
// (defaultConfigPath / defaultArchivePath) AND relative — the single shape
// that means os.UserHomeDir() failed and no usable absolute XDG_* override
// stepped in, so the default would otherwise resolve against the current
// working directory. An explicit flag is the user's own choice and is
// exempt (it may legitimately be relative); a non-default value seeded by a
// caller (e.g. a unit-test harness) is likewise none of this gate's
// business, so the equality check against the real default keeps the gate
// inert outside the genuine HOME-less footgun.
func requireAnchoredDefaultPaths(values *CommonFlagValues) error {
	if !values.ConfigPathExplicit &&
		values.ConfigPath == defaultConfigPath() &&
		!filepath.IsAbs(values.ConfigPath) {
		return errUnanchoredDefaultPath
	}
	if !values.ArchivePathExplicit &&
		values.ArchivePath == defaultArchivePath() &&
		!filepath.IsAbs(values.ArchivePath) {
		return errUnanchoredDefaultPath
	}
	return nil
}

// commonFlagsExitCode collapses the four ParseCommon error shapes the
// migrated subcommands all handle identically into one helper:
//
//   - flag.ErrHelp        → exit 0 (fs.Parse already wrote the usage block)
//   - ErrFlagParseFailed  → exit 1 (fs.Parse already wrote error + usage)
//   - any other error     → route through the unified Failure Reporter
//     (slice 7, issue #178) so the custom invariant
//     errors (mutual exclusion, unsupported global)
//     land in the same `<cmd>: <msg>` shape every
//     other failure path uses.
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

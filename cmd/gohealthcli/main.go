package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithRuntime(args, stdout, stderr, productionRuntimeAdapters())
}

// printTopLevelUsage renders the top-level --help block: a Subcommands list
// sourced from the registry (hidden entries filtered out) followed by the
// standard FlagSet defaults. The "Usage of gohealthcli:" header matches Go's
// default flag output so that callers (and tests) keying off it keep working.
// flags.PrintDefaults writes to the FlagSet's configured output, so we point
// it at w for the duration of the call to keep the whole block on one stream.
func printTopLevelUsage(flags *flag.FlagSet, w io.Writer) {
	prev := flags.Output()
	flags.SetOutput(w)
	defer flags.SetOutput(prev)

	fmt.Fprintln(w, "Usage of gohealthcli:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	for _, cmd := range commands {
		if cmd.Hidden {
			continue
		}
		fmt.Fprintf(w, "  %-16s %s\n", cmd.Name, cmd.Short)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags:")
	flags.PrintDefaults()
}

func runWithRuntime(args []string, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	runtime = runtime.withDefaults()
	flags := flag.NewFlagSet("gohealthcli", flag.ContinueOnError)
	flags.SetOutput(stderr)

	// The shared flags' usage strings come from commonFlagsSpec via
	// commonFlagUsage — the top-level set registers them directly (it
	// carries --version, which the Common spec deliberately does not)
	// but must render the same wording as every subcommand's --help.
	configPath := flags.String("config", defaultConfigPath(), commonFlagUsage("config"))
	archivePath := flags.String("db", defaultArchivePath(), commonFlagUsage("db"))
	jsonOutput := flags.Bool("json", false, commonFlagUsage("json"))
	plainOutput := flags.Bool("plain", false, commonFlagUsage("plain"))
	noInput := flags.Bool("no-input", false, commonFlagUsage("no-input"))
	versionOutput := flags.Bool("version", false, "print version and exit")

	flags.Usage = func() { printTopLevelUsage(flags, stderr) }

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}

	// Mutual exclusion of --plain and --json applies to every top-level
	// invocation that emits structured output, --version included. The
	// Common Flag Set module owns the same check for the subcommand
	// surface (see common_flags.go ParseCommon); enforcing it here keeps
	// the top-level parse consistent without routing the global flags
	// through ParseCommon (the global flag set has --version, which the
	// Common spec deliberately does not). PRD #143 slice 5 (issue #174).
	if *plainOutput && *jsonOutput {
		// Default mode is the only safe one: the user asked for both shapes
		// at once, so we cannot honor either — the bare `gohealthcli: <msg>`
		// line on stderr is unambiguous about the binary rejecting the
		// invocation. Slice 7 PRD #143 (issue #178).
		return ReportFailure(FailureReport{
			Status:  StatusFlagInvalid,
			Message: "--plain and --json are mutually exclusive",
		}, stdout, stderr)
	}

	if *versionOutput {
		RenderVersion(outputMode{json: *jsonOutput, plain: *plainOutput}, stdout)
		return 0
	}

	if flags.NArg() == 0 {
		// PRD #143 slice 3: a bare `gohealthcli` invocation prints the
		// top-level help to stdout and exits 0, matching git / kubectl /
		// docker discoverability conventions. Explicit `--help` continues
		// to write to stderr via flags.Usage (stdlib flag-package
		// convention), so the two paths intentionally diverge by stream.
		printTopLevelUsage(flags, stdout)
		return 0
	}

	// `help` verb dispatch sits BEFORE the subcommand switch so it doesn't
	// fall through to the unknown-command fallthrough. The verb form is an
	// alias for the equivalent flag form: `help` ≡ `--help`, `help <cmd>` ≡
	// `<cmd> --help` (with the registry's `Long` prose prepended). Slice 2 of
	// PRD #143.
	if flags.Arg(0) == "help" {
		globalMode := outputMode{json: *jsonOutput, plain: *plainOutput}
		if flags.NArg() == 1 || flags.Arg(1) == "--help" || flags.Arg(1) == "-help" {
			// Top-level help form rejects trailing positionals so a typo like
			// `gohealthcli help --help status` fails loudly instead of being
			// silently dropped.
			if flags.NArg() > 2 {
				return ReportFailure(FailureReport{
					Command: "help",
					Status:  StatusUnexpectedArgument,
					Message: fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args()[2:], " ")),
					Mode:    globalMode,
				}, stdout, stderr)
			}
			printTopLevelUsage(flags, stderr)
			return 0
		}
		// Per-command help takes exactly one positional (the subcommand). Reject
		// extras so `help status extra` fails fast like every other subcommand.
		if flags.NArg() > 2 {
			return ReportFailure(FailureReport{
				Command: "help",
				Status:  StatusUnexpectedArgument,
				Message: fmt.Sprintf("unexpected arguments after %s: %s", flags.Arg(1), strings.Join(flags.Args()[2:], " ")),
				Mode:    globalMode,
			}, stdout, stderr)
		}
		target := flags.Arg(1)
		def, ok := lookupCommand(target)
		if !ok {
			return ReportFailure(FailureReport{
				Status:  StatusFlagInvalid,
				Message: fmt.Sprintf("unknown command: %s", target),
				Mode:    globalMode,
			}, stdout, stderr)
		}
		// Print the registry Long prose, then re-dispatch through the runtime
		// with `--help` so the flag-set portion is identical to the bytes a
		// user gets from `gohealthcli <cmd> --help` directly. This satisfies
		// both AC bullets: Long description AND its accepted flags.
		fmt.Fprintln(stderr, def.Long)
		fmt.Fprintln(stderr)
		return runWithRuntime([]string{target, "--help"}, stdout, stderr, runtime)
	}

	// PRD #143 slice 6 (issue #175): dispatch reads from the same registry
	// the --help printer reads, so a new subcommand registered in commands.go
	// is automatically callable — no parallel switch to forget to update.
	// The diverged signatures from the previous hand-written switch are
	// folded into each commandDef.Run adapter:
	//   - status / query / export read ArchivePathExplicit out of common.
	//   - init / status / query / export / schema take but ignore the
	//     runtime adapter bundle (their underlying entry points pre-date
	//     the runtime injection).
	//   - raw takes the global outputMode but its underlying function
	//     ignores it (declared as `_`); the adapter still passes the
	//     value through for call-site uniformity.
	// CommonFlagValues is the single seam that carries the global flag
	// state down to all of those adapters.
	common := CommonFlagValues{
		ConfigPath:          *configPath,
		ArchivePath:         *archivePath,
		JSONOutput:          *jsonOutput,
		PlainOutput:         *plainOutput,
		NoInput:             *noInput,
		ArchivePathExplicit: flagWasProvided(flags, "db"),
		ConfigPathExplicit:  flagWasProvided(flags, "config"),
	}
	cmd, ok := lookupCommand(flags.Arg(0))
	if !ok {
		// PRD #143 slice 3: every unknown-command exit prints the canonical
		// help hint so users always know how to discover the catalog. The
		// "Did you mean" line (when a Levenshtein suggestion exists) lives
		// between the two and is handled by the runUnknownCommand helper.
		return runUnknownCommand(flags.Arg(0), outputMode{json: *jsonOutput, plain: *plainOutput}, stdout, stderr)
	}
	return dispatchCommand(cmd, flags.Args()[1:], common, stdout, stderr, runtime)
}

// dispatchCommand invokes a registry entry's Run adapter, guarding the
// nil case: TestEveryCommandHasRunAdapter pins the invariant that every
// entry's Run is wired, but if a future change slips a registry entry
// through without an adapter we exit cleanly with a targeted stderr
// message instead of panicking on the nil call. The error wording names
// the offending command so an operator can flag the bug without
// inspecting a stack trace.
func dispatchCommand(cmd commandDef, args []string, common CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	if cmd.Run == nil {
		fmt.Fprintf(stderr, "internal error: command %q has no Run adapter\n", cmd.Name)
		return 1
	}
	return cmd.Run(args, common, stdout, stderr, runtime)
}

// runUnknownCommand renders the unknown-command failure: the canonical
// `gohealthcli: unknown command: <typo>` line routed through the unified
// Failure Reporter (slice 7, issue #178) so the bytes match the rest of
// the binary's failure surface in every mode, plus the slice-3 hint
// block ("Did you mean: <name>?" when a Levenshtein suggestion exists,
// and "Run 'gohealthcli --help' for a list of commands.") that lands on
// stderr.
//
// In --json mode the hint block is suppressed: scripts parsing the
// stdout envelope shouldn't see human-targeted stderr noise. In
// default and --plain modes the hint lines stay so terminal users
// still get the discoverability nudge the slice-3 AC requires.
func runUnknownCommand(typo string, mode outputMode, stdout, stderr io.Writer) int {
	exit := ReportFailure(FailureReport{
		Status:  StatusFlagInvalid,
		Message: fmt.Sprintf("unknown command: %s", typo),
		Mode:    mode,
	}, stdout, stderr)
	if mode.json {
		return exit
	}
	if suggestions := commandRegistry(commands).Suggest(typo); len(suggestions) > 0 {
		fmt.Fprintf(stderr, "Did you mean: %s?\n", strings.Join(suggestions, ", "))
	}
	fmt.Fprintln(stderr, "Run 'gohealthcli --help' for a list of commands.")
	return exit
}

type outputMode struct {
	json  bool
	plain bool
}

func flagWasProvided(flags *flag.FlagSet, name string) bool {
	provided := false
	flags.Visit(func(item *flag.Flag) {
		if item.Name == name {
			provided = true
		}
	})
	return provided
}

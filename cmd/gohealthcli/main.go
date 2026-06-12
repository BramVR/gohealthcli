package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

type rawCommandOptions struct {
	configPath  string
	archivePath string
	from        string
	to          string
	pageSize    int64
	pageToken   string
	target      []string
}

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

func runRawWithRuntime(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("raw", flag.ContinueOnError)
	flags.SetOutput(stderr)
	// raw's success output is the provider's raw bytes on stdout — it
	// does not honour --plain / --json / --no-input. The Common Flag
	// Set's pre-Parse scan turns those known-global flags into a
	// targeted "--<flag> is not supported by raw" message when the user
	// passes them on raw, instead of letting them silently lose values
	// or fall through to stdlib's generic wording.
	common := RegisterCommon(flags, CommonFlagSpec{Accepted: rawCommonFlagNames()}, CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
	})
	rawFrom := flags.String("from", "", "inclusive time-range start (where supported by the endpoint)")
	rawTo := flags.String("to", "", "exclusive time-range end (where supported by the endpoint)")
	rawPageSize := flags.Int64("page-size", 0, "pagination page size (positive integer; where supported by the endpoint)")
	rawPageToken := flags.String("page-token", "", "pagination page token from a prior response")

	// raw uses a bespoke usage block (`raw endpoint getIdentity` etc.)
	// rather than the auto-generated stdlib one, because its first
	// positional is a verb (`endpoint`/`data-type`) — not a flag.
	// Override fs.Usage so a parse failure or -h prints raw's own
	// three-line block on stderr instead of the stdlib auto-listing.
	// On --help we additionally mirror it to stdout below; the duplicate
	// stderr copy would surface twice otherwise, so we route only on
	// genuine parse errors here.
	rawUsage := func(w io.Writer) {
		fmt.Fprintln(w, "usage: gohealthcli raw endpoint getIdentity")
		fmt.Fprintln(w, "usage: gohealthcli raw endpoint dataTypes.<data-type>.list --from YYYY-MM-DD [--to YYYY-MM-DD]")
		fmt.Fprintln(w, "usage: gohealthcli raw data-type <data-type> --from YYYY-MM-DD [--to YYYY-MM-DD]")
	}
	// stdlib's flag package calls fs.Usage on BOTH `-h` and a parse
	// error. Suppress that auto-call entirely and emit the bespoke
	// block from the error branches below so each failure mode picks
	// the right destination (stdout for --help, stderr for parse error)
	// and the user never sees the block twice.
	flags.Usage = func() {}

	// raw's subverbs (`endpoint <name>`, `data-type <data-type>`) are
	// positional arguments that may appear BEFORE the flag block —
	// stdlib's `flag` package stops parsing at the first non-flag, so
	// `raw endpoint getIdentity --config X` would otherwise leave
	// `--config X` unread. Reorder the args so flags come first and
	// positionals trail; the FlagSet then parses everything, and
	// fs.Args() returns just the trailing positionals.
	reordered, target := partitionRawFlagArgs(flags, args)
	if err := ParseCommon(flags, common, reordered, runtime.observeSubcommandFlagSet); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// --help / -h flowed through; raw's success output is the
			// provider's raw bytes on stdout, so the bespoke usage
			// block follows the same convention when the user
			// explicitly asks for it.
			rawUsage(stdout)
			return 0
		}
		// Genuine parse failures (unknown flag, malformed value): the
		// error message went to stderr via fs.SetOutput already; append
		// the bespoke usage block on the same stream so the user sees
		// "what to use instead" without a second copy on stdout.
		if errors.Is(err, ErrFlagParseFailed) {
			rawUsage(stderr)
		}
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	if *rawPageSize < 0 || (flagWasProvided(flags, "page-size") && *rawPageSize <= 0) {
		return ReportFailure(FailureReport{Command: "raw", Status: StatusFlagInvalid, Message: "--page-size must be a positive integer", Mode: mode}, stdout, stderr)
	}
	options := rawCommandOptions{
		configPath:  common.ConfigPath,
		archivePath: common.ArchivePath,
		from:        *rawFrom,
		to:          *rawTo,
		pageSize:    *rawPageSize,
		pageToken:   *rawPageToken,
		target:      target,
	}
	request, err := buildGoogleHealthRawRequest(options.target, options.from, options.to, options.pageSize, options.pageToken)
	if err != nil {
		return ReportFailure(FailureReport{Command: "raw", Status: StatusFlagInvalid, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	body, err := rawSetupWithRuntime(options.configPath, options.archivePath, request, runtime)
	if err != nil {
		// Provider outage (non-auth HTTP failure or network error) maps
		// to the documented provider_unreachable failure status so JSON
		// consumers can tell it apart from local misconfiguration
		// (issue #272); everything else stays operation_failed.
		status := StatusOperationFailed
		if isProviderUnreachableError(err) {
			status = StatusProviderUnreachable
		}
		return ReportFailure(FailureReport{Command: "raw", Status: status, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	if _, err := stdout.Write(body); err != nil {
		return reportWriteFailure("raw", err, mode, stdout, stderr)
	}
	return 0
}

// partitionRawFlagArgs splits raw's argv so flags come first and the
// subverb-positional tail (e.g. `endpoint getIdentity`,
// `data-type steps`) trails. stdlib's `flag.Parse` stops at the first
// non-flag, so without this reorder the legacy invocation
// `raw endpoint getIdentity --config X` would leave `--config X` unread.
//
// The walk respects `--` (end of flags), recognises string flags
// registered on fs so their value argument is not mistaken for a
// positional, and leaves bool / unknown flags' subsequent token to be
// parsed in the usual way. Returns the flag-first arg list and the
// trailing positional slice (which the caller uses verbatim as raw's
// `target`, bypassing fs.Args() so an early `--` still surfaces the
// subverb).
func partitionRawFlagArgs(fs *flag.FlagSet, args []string) ([]string, []string) {
	flagArgs := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	endOfFlags := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if endOfFlags {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "--" {
			endOfFlags = true
			flagArgs = append(flagArgs, arg)
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		// If the flag is registered as a non-bool on fs, the next arg
		// is its value — pull it along (unless this is the `--name=value`
		// form, which is self-contained).
		name := strings.TrimLeft(arg, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	// fs.Parse consumes only the leading flag block; appending the
	// positionals lets fs.Args() report them too, while the explicit
	// `positionals` return survives even if a future change pre-trims
	// args before fs.Parse sees them.
	out := append(flagArgs, positionals...)
	return out, positionals
}

func rawSetupWithRuntime(configPath, archivePath string, request rawProviderRequest, runtime runtimeAdapters) ([]byte, error) {
	runtime = runtime.withDefaults()
	config, err := inspectIdentityConfig(configPath, archivePath)
	if err != nil {
		return nil, fmt.Errorf("config check failed: %w", err)
	}
	// PRD #142 slice 6 (issue #179): open the archive in writable mode
	// via openHealthArchiveConnectionAPI so the handle satisfies
	// connectionTokenWriter — WithAutoRefresh below can then persist a
	// refreshed token's metadata the same way sync and irn-profile
	// already do. ADR-0002 is not violated: the only write `raw`
	// performs is updating connections.token_metadata_json, the same
	// kind of write `sync` already performs on the same path.
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("no Connection found; run `gohealthcli connect` first")
		}
		return nil, err
	}
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{configPath, archivePath}, runtime)
	if config.oauthClient.kind == "file" {
		connectionAccess = connectionAccess.WithAutoRefresh(config.oauthClient, archive)
	}
	accessToken, err := connectionAccess.AccessToken(request.requiredScopes)
	if err != nil {
		return nil, err
	}
	// raw is an interactive exploration command with no SIGINT
	// instrumentation; Background keeps the request context-scoped (the
	// shared timeout client still bounds it) without wiring a handler.
	body, err := runtime.fetchRawProvider(context.Background(), request, accessToken)
	if err != nil {
		return nil, normalizeProviderError(err)
	}
	return body, nil
}

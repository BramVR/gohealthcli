package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	_ "modernc.org/sqlite"
)

type syncResult struct {
	Status            string   `json:"status"`
	SyncRunID         int64    `json:"sync_run_id,omitempty"`
	ConnectionID      string   `json:"connection_id,omitempty"`
	ProviderName      string   `json:"provider_name,omitempty"`
	DataTypes         []string `json:"data_types,omitempty"`
	From              string   `json:"from,omitempty"`
	ResumedFromCursor bool     `json:"resumed_from_cursor,omitempty"`
	To                string   `json:"to,omitempty"`
	EndpointFamily    string   `json:"endpoint_family,omitempty"`
	SourceFamily      string   `json:"source_family,omitempty"`
	DataPointsSeen    int      `json:"data_points_seen"`
	DataPointsNew     int      `json:"data_points_new"`
	DataPointsUpdated int      `json:"data_points_updated"`
	RollupsSeen       int      `json:"rollups_seen"`
	RollupsNew        int      `json:"rollups_new"`
	RollupsUpdated    int      `json:"rollups_updated"`
	Message           string   `json:"message"`
}

type rawCommandOptions struct {
	configPath  string
	archivePath string
	from        string
	to          string
	pageSize    int64
	pageToken   string
	target      []string
}

type syncCommandOptions struct {
	configPath   string
	archivePath  string
	dataTypes    []string
	allTypes     bool
	from         string
	to           string
	rollup       string
	sourceFamily string
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

func runSyncWithRuntime(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		JSONOutput:  mode.json,
		PlainOutput: mode.plain,
	})
	syncTypes := flags.String("types", "", "comma-separated Data Types; defaults to \"steps\" when neither --types nor --all is set")
	syncAll := flags.Bool("all", false, "sync every default Data Type")
	syncFrom := flags.String("from", "", "inclusive sync range start; optional once a Sync Cursor exists")
	syncTo := flags.String("to", "", "exclusive sync range end")
	syncRollup := flags.String("rollup", "", "rollup kind to sync; supported: daily | hourly | weekly | window=<duration>")
	syncSourceFamily := flags.String("source-family", "", "source family filter; supported: wearable")
	syncStatus := flags.Bool("status", false, "list recent Sync Runs from the local archive instead of syncing")
	syncWindow := flags.String("window", "", "with --status: how far back to list finished Sync Runs (Go duration, default 15m, max 24h)")

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "sync",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected sync argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}
	// --status flips sync into a read-only view of recent Sync Runs
	// (#236); any flag that shapes an actual sync is a usage error
	// alongside it (silent ignoring would read as "my filters applied").
	// --window inverts the rule: it only means something WITH --status.
	if err := validateSyncStatusFlagSet(flags, *syncStatus); err != nil {
		return ReportFailure(FailureReport{
			Command: "sync",
			Status:  StatusFlagInvalid,
			Message: err.Error(),
			Mode:    mode,
		}, stdout, stderr)
	}
	if *syncStatus {
		return runSyncStatusWithRuntime(*common, *syncWindow, mode, stdout, stderr, runtime)
	}

	dataTypes := parseCommaList(*syncTypes)
	if !*syncAll && len(dataTypes) == 0 {
		// Preserve the historical default for single-type invocations
		// (`gohealthcli sync` with no flags).
		dataTypes = []string{"steps"}
	}
	options := syncCommandOptions{
		configPath:   common.ConfigPath,
		archivePath:  common.ArchivePath,
		dataTypes:    dataTypes,
		allTypes:     *syncAll,
		from:         *syncFrom,
		to:           *syncTo,
		rollup:       *syncRollup,
		sourceFamily: *syncSourceFamily,
	}
	ctx, stopSignalHandler := installSyncCancelContext()
	defer stopSignalHandler()

	orchestrator := newSyncOrchestrator(runtime)
	results, err := orchestrator.Sync(ctx, options)
	if err != nil {
		// Preflight rejections from the orchestrator (gate-routed) surface
		// here. status is always sync_failed (never the empty string),
		// SyncRunID is unset so the JSON envelope omits sync_run_id, and
		// the user-supplied --from/--to round-trip so tooling that pivots
		// on the failed-range can still see what was attempted. The
		// no-audit-row contract is preserved upstream of this branch.
		fallback := syncResult{
			Status:    "sync_failed",
			DataTypes: dataTypes,
			From:      options.from,
			To:        options.to,
			Message:   err.Error(),
		}
		if writeErr := writeSyncResult(fallback, mode, stdout); writeErr != nil {
			return reportWriteFailure("sync", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	// Shape is determined by what the user requested, not by how many
	// results came back: --all and --types csv (more than one) always
	// emit the wrapped fan-out shape so downstream tooling can rely on
	// the envelope being predictable from the invocation flags. Bare
	// single-type sync (--types steps, or no flags) keeps the legacy
	// flat shape for backwards compatibility.
	fanOut := options.allTypes || len(dataTypes) > 1
	if !fanOut {
		// orchestrator.Sync returns an empty slice only when SIGINT
		// arrived before the first Data Type started — report that as
		// sync_canceled, not the misleading "sync produced no results"
		// the legacy path would surface as sync_failed.
		single := syncResult{Status: "sync_canceled", DataTypes: dataTypes, Message: "sync canceled before any Data Type started"}
		if len(results) > 0 {
			single = results[0]
		}
		if writeErr := writeSyncResult(single, mode, stdout); writeErr != nil {
			return reportWriteFailure("sync", writeErr, mode, stdout, stderr)
		}
		if single.Status != "sync_completed" {
			return 1
		}
		return 0
	}
	if writeErr := writeSyncFanOutResult(results, options, mode, stdout); writeErr != nil {
		return reportWriteFailure("sync", writeErr, mode, stdout, stderr)
	}
	// fanOutStatus folds the same empty-results case to sync_canceled
	// (see fanOutStatus), so an empty fan-out should also exit non-zero
	// rather than the silent exit-0 the per-result loop would produce.
	if fanOutStatus(results) != "sync_completed" {
		return 1
	}
	return 0
}

// installSyncCancelContext wires a SIGINT handler whose cancellation is
// delivered via the returned context. The context cancels when SIGINT
// fires or when the returned stop function is called (whichever is
// first); the stop function is idempotent and races cleanly with an
// in-flight signal. Because the context scopes every Provider HTTP
// request in the run (#284), SIGINT aborts the in-flight call rather
// than waiting for the next page boundary. Tests construct a
// context.WithCancel directly instead.
func installSyncCancelContext() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
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

func writeSyncResult(result syncResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeSyncPlain(writer, result)
	} else {
		writeSyncHuman(writer, result)
	}
	return writer.Err()
}

func writeSyncPlain(writer *stickyWriter, result syncResult) {
	writer.Printf("status: %s\n", result.Status)
	if result.SyncRunID != 0 {
		writer.Printf("sync_run_id: %d\n", result.SyncRunID)
	}
	if result.ConnectionID != "" {
		writer.Printf("connection_id: %s\n", result.ConnectionID)
	}
	if result.ProviderName != "" {
		writer.Printf("provider_name: %s\n", result.ProviderName)
	}
	if len(result.DataTypes) != 0 {
		writer.Printf("data_types: %s\n", strings.Join(result.DataTypes, ","))
	}
	if result.From != "" {
		writer.Printf("from: %s\n", result.From)
	}
	if result.ResumedFromCursor {
		writer.Println("resumed_from_cursor: true")
	}
	if result.To != "" {
		writer.Printf("to: %s\n", result.To)
	}
	if result.EndpointFamily != "" {
		writer.Printf("endpoint_family: %s\n", result.EndpointFamily)
	}
	if result.SourceFamily != "" {
		writer.Printf("source_family: %s\n", result.SourceFamily)
	}
	writer.Printf("data_points_seen: %d\n", result.DataPointsSeen)
	writer.Printf("data_points_new: %d\n", result.DataPointsNew)
	writer.Printf("data_points_updated: %d\n", result.DataPointsUpdated)
	writer.Printf("rollups_seen: %d\n", result.RollupsSeen)
	writer.Printf("rollups_new: %d\n", result.RollupsNew)
	writer.Printf("rollups_updated: %d\n", result.RollupsUpdated)
	writer.Printf("message: %s\n", result.Message)
}

func writeSyncHuman(writer *stickyWriter, result syncResult) {
	switch result.Status {
	case "sync_completed":
		writer.Println("Sync Run completed")
	default:
		writer.Println("Sync Run failed")
	}
	if result.SyncRunID != 0 {
		writer.Printf("Sync Run: %d\n", result.SyncRunID)
	}
	if result.ConnectionID != "" {
		writer.Printf("Connection: %s\n", result.ConnectionID)
	}
	if len(result.DataTypes) != 0 {
		writer.Printf("Data Types: %s\n", strings.Join(result.DataTypes, ","))
	}
	if result.From != "" || result.To != "" {
		writer.Printf("Range: %s to %s\n", result.From, result.To)
	}
	if result.ResumedFromCursor {
		writer.Println("Resumed from Sync Cursor")
	}
	if result.SourceFamily != "" {
		writer.Printf("Source family: %s\n", result.SourceFamily)
	}
	writer.Printf("Data Points: seen %d, new %d, updated %d\n", result.DataPointsSeen, result.DataPointsNew, result.DataPointsUpdated)
	writer.Printf("Rollups: seen %d, new %d, updated %d\n", result.RollupsSeen, result.RollupsNew, result.RollupsUpdated)
	writer.Printf("Message: %s\n", result.Message)
}

// syncFanOutResult is the JSON/Plain wire shape for a multi-Data-Type
// sync. Single-type syncs keep emitting a flat syncResult to preserve
// backwards compatibility for downstream tooling that parses sync output.
type syncFanOutResult struct {
	Status  string            `json:"status"`
	Summary syncFanOutSummary `json:"summary"`
	Results []syncResult      `json:"results"`
	Message string            `json:"message"`
}

func writeSyncFanOutResult(results []syncResult, options syncCommandOptions, mode outputMode, stdout io.Writer) error {
	summary := summarizeSyncFanOut(results, options.from, options.to)
	status := fanOutStatus(results)
	message := fanOutMessage(status, results)
	if mode.json {
		envelope := syncFanOutResult{
			Status:  status,
			Summary: summary,
			Results: results,
			Message: message,
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(envelope)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeSyncFanOutPlain(writer, results, summary, status, message)
	} else {
		writeSyncFanOutHuman(writer, results, summary, status, message)
	}
	return writer.Err()
}

func writeSyncFanOutPlain(writer *stickyWriter, results []syncResult, summary syncFanOutSummary, status, message string) {
	writer.Printf("status: %s\n", status)
	for index, result := range results {
		writeSyncFanOutResultPlain(writer, fmt.Sprintf("results.%d.", index), result)
	}
	writer.Printf("totals.data_points_seen: %d\n", summary.DataPointsSeen)
	writer.Printf("totals.data_points_new: %d\n", summary.DataPointsNew)
	writer.Printf("totals.data_points_updated: %d\n", summary.DataPointsUpdated)
	writer.Printf("totals.rollups_seen: %d\n", summary.RollupsSeen)
	writer.Printf("totals.rollups_new: %d\n", summary.RollupsNew)
	writer.Printf("totals.rollups_updated: %d\n", summary.RollupsUpdated)
	writer.Printf("message: %s\n", message)
}

func writeSyncFanOutResultPlain(writer *stickyWriter, prefix string, result syncResult) {
	writer.Printf("%sstatus: %s\n", prefix, result.Status)
	if len(result.DataTypes) > 0 {
		writer.Printf("%sdata_type: %s\n", prefix, result.DataTypes[0])
	}
	if result.SyncRunID != 0 {
		writer.Printf("%ssync_run_id: %d\n", prefix, result.SyncRunID)
	}
	for _, counter := range []struct {
		key   string
		value int
	}{
		{"data_points_seen", result.DataPointsSeen},
		{"data_points_new", result.DataPointsNew},
		{"data_points_updated", result.DataPointsUpdated},
		{"rollups_seen", result.RollupsSeen},
		{"rollups_new", result.RollupsNew},
		{"rollups_updated", result.RollupsUpdated},
	} {
		writer.Printf("%s%s: %d\n", prefix, counter.key, counter.value)
	}
	if result.Message != "" {
		writer.Printf("%smessage: %s\n", prefix, result.Message)
	}
}

func writeSyncFanOutHuman(writer *stickyWriter, results []syncResult, summary syncFanOutSummary, status, message string) {
	switch status {
	case "sync_completed":
		writer.Printf("Sync Run fan-out completed across %d Data Types\n", len(results))
	case "sync_canceled":
		writer.Printf("Sync Run fan-out canceled across %d Data Types\n", len(results))
	default:
		writer.Printf("Sync Run fan-out failed across %d Data Types\n", len(results))
	}
	for _, result := range results {
		dataType := "?"
		if len(result.DataTypes) > 0 {
			dataType = result.DataTypes[0]
		}
		writer.Printf("- %s: %s — Data Points new=%d updated=%d, Rollups new=%d updated=%d\n", dataType, result.Status, result.DataPointsNew, result.DataPointsUpdated, result.RollupsNew, result.RollupsUpdated)
	}
	writer.Printf("Totals: Data Points seen=%d new=%d updated=%d, Rollups seen=%d new=%d updated=%d\n", summary.DataPointsSeen, summary.DataPointsNew, summary.DataPointsUpdated, summary.RollupsSeen, summary.RollupsNew, summary.RollupsUpdated)
	writer.Println(message)
}

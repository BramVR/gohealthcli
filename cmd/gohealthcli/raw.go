package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
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

func runRawWithRuntime(args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("raw", flag.ContinueOnError)
	flags.SetOutput(stderr)
	// raw does not accept --json / --plain on its own slot, so failure
	// rendering honours the GLOBAL output mode only.
	mode := commonOutputMode(globals)
	// raw's success output is the provider's raw bytes on stdout — it
	// does not honour --plain / --json / --no-input. The Common Flag
	// Set's pre-Parse scan turns those known-global flags into a
	// targeted "--<flag> is not supported by raw" message when the user
	// passes them on raw, instead of letting them silently lose values
	// or fall through to stdlib's generic wording.
	common := RegisterCommon(flags, CommonFlagSpec{Accepted: rawCommonFlagNames()}, CommonFlagValues{
		ConfigPath:  globals.ConfigPath,
		ArchivePath: globals.ArchivePath,
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

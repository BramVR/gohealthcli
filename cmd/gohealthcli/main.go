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

const googleHealthIdentityURL = "https://health.googleapis.com/v4/users/me/identity"
const googleHealthProfileURL = "https://health.googleapis.com/v4/users/me/profile"

type connectResult struct {
	Status             string `json:"status"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	LegacyFitbitUserID string `json:"legacy_fitbit_user_id,omitempty"`
	CredentialStore    string `json:"credential_store,omitempty"`
	TokenStatus        string `json:"token_status,omitempty"`
	Message            string `json:"message"`
}

type identityResult struct {
	Status             string `json:"status"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	LegacyFitbitUserID string `json:"legacy_fitbit_user_id,omitempty"`
	Message            string `json:"message"`
}

type profileResult struct {
	Status             string `json:"status"`
	SnapshotID         int64  `json:"snapshot_id,omitempty"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	LegacyFitbitUserID string `json:"legacy_fitbit_user_id,omitempty"`
	FetchedAt          string `json:"fetched_at,omitempty"`
	Message            string `json:"message"`
}

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

type googleIdentity struct {
	healthUserID       string
	legacyFitbitUserID string
	rawJSON            string
}

type googleProfile struct {
	healthUserID string
	resourceName string
	rawJSON      string
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

func runConnectWithRuntime(args []string, configPath, archivePath string, globalNoInput bool, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		JSONOutput:  mode.json,
		PlainOutput: mode.plain,
		NoInput:     globalNoInput,
	})
	// The keyword list is rendered from connectAddScopeKeywords so the
	// --help text can never drift from what expandConnectAddScopes
	// accepts again (#148: `nutrition` was accepted but invisible).
	connectAddScopes := flags.String("add-scopes", "", connectAddScopesUsage())

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "connect",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected connect argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	additionalScopes, err := expandConnectAddScopes(parseCommaList(*connectAddScopes))
	if err != nil {
		return ReportFailure(FailureReport{
			Command: "connect --add-scopes",
			Status:  StatusFlagInvalid,
			Message: err.Error(),
			Mode:    mode,
		}, stdout, stderr)
	}

	result, err := connectSetupWithRuntimeAndExtraScopes(common.ConfigPath, common.ArchivePath, common.NoInput, additionalScopes, runtime)
	if err != nil {
		result.Status = "connect_failed"
		result.Message = err.Error()
		if writeErr := writeConnectResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure("connect", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	if err := writeConnectResult(result, mode, stdout); err != nil {
		return reportWriteFailure("connect", err, mode, stdout, stderr)
	}
	return 0
}

// identityCommand is identity's Identity Snapshot engine spec (issue
// #282). identity joined the engine via the #273 parity decision: it
// shares the whole pipeline through Connection access (auto-refresh
// for file-based OAuth Connections, the getIdentity scope pre-check —
// the same catalog entry `raw endpoint getIdentity` consumes). Its
// genuinely-unique decoration is the act override: instead of
// archiving a snapshot, identity re-fetches the verified Google
// Identity and refreshes the metadata stored alongside the Connection
// — which is also why its spec name carries no "Snapshot" and it sets
// no snapshotKind.
var identityCommand = identitySnapshotCommandSpec[identityResult, googleIdentity]{
	command:            "identity",
	commonFlags:        AllCommonFlagsSpec,
	statusFailed:       "identity_failed",
	statusUnavailable:  "identity_unavailable",
	statusScopeMissing: "identity_scope_missing",
	scopeEndpointKey:   "getIdentity",
	seedResult: func(connection archivedConnection) identityResult {
		return identityResult{
			ConnectionID:       connection.id,
			ProviderName:       connection.providerName,
			GoogleHealthUserID: connection.googleHealthUserID,
			LegacyFitbitUserID: connection.legacyFitbitUserID,
		}
	},
	status:      func(result *identityResult) string { return result.Status },
	setStatus:   func(result *identityResult, status string) { result.Status = status },
	setMessage:  func(result *identityResult, message string) { result.Message = message },
	writeResult: writeIdentityResult,
	act: func(engine identitySnapshotCommandContext, result *identityResult) error {
		identity, err := engine.connectionAccess.FetchVerifiedIdentity(engine.accessToken)
		if err != nil {
			if isCurrentConnectionIdentityMismatch(err) {
				result.Status = "identity_mismatch"
			} else if isProviderUnreachableError(err) {
				// Provider outage (non-auth HTTP failure or network error)
				// gets its own documented JSON failure status so automation
				// can tell it apart from local misconfiguration (issue #272).
				result.Status = "provider_unreachable"
			}
			return err
		}
		if err := engine.archive.RefreshConnectionIdentity(context.Background(), engine.connection, identity, engine.runtime.now()); err != nil {
			return err
		}
		result.Status = "identity_refreshed"
		result.GoogleHealthUserID = identity.healthUserID
		if identity.legacyFitbitUserID != "" {
			result.LegacyFitbitUserID = identity.legacyFitbitUserID
		}
		result.Message = "Google Identity refreshed"
		return nil
	},
}

// profileSnapshotCommand is profile's Identity Snapshot engine spec
// (issue #282). Its genuinely-unique decoration is the profile ID
// verification: verifyPayload confirms the fetched payload belongs to
// the archived Google Identity — falling back to the verified-identity
// endpoint when the profile payload carries no user ID — before any
// snapshot is archived, mapping a mismatch to "profile_mismatch".
// fetchPayload rides the runtime.fetchProfile adapter, the same seam
// tests already fake.
var profileSnapshotCommand = identitySnapshotCommandSpec[profileResult, googleProfile]{
	command:            "profile",
	commonFlags:        AllCommonFlagsSpec,
	statusFailed:       "profile_failed",
	statusUnavailable:  "profile_unavailable",
	statusScopeMissing: "profile_scope_missing",
	scopeEndpointKey:   "getProfile",
	seedResult: func(connection archivedConnection) profileResult {
		return profileResult{
			ConnectionID:       connection.id,
			ProviderName:       connection.providerName,
			GoogleHealthUserID: connection.googleHealthUserID,
			LegacyFitbitUserID: connection.legacyFitbitUserID,
		}
	},
	status:       func(result *profileResult) string { return result.Status },
	setStatus:    func(result *profileResult, status string) { result.Status = status },
	setMessage:   func(result *profileResult, message string) { result.Message = message },
	writeResult:  writeProfileResult,
	snapshotKind: snapshotKindProfile,
	fetchPayload: func(runtime runtimeAdapters, accessToken string) (googleProfile, error) {
		return runtime.fetchProfile(accessToken)
	},
	payloadRawJSON: func(payload googleProfile) string { return payload.rawJSON },
	verifyPayload: func(engine identitySnapshotCommandContext, result *profileResult, payload googleProfile) error {
		profileHealthUserID := payload.healthUserID
		if profileHealthUserID == "" {
			identity, err := engine.connectionAccess.FetchVerifiedIdentity(engine.accessToken)
			if err != nil {
				if isCurrentConnectionIdentityMismatch(err) {
					result.Status = "profile_mismatch"
				} else if isProviderUnreachableError(err) {
					result.Status = "provider_unreachable"
				}
				return err
			}
			profileHealthUserID = identity.healthUserID
		}
		if err := engine.connectionAccess.RequireMatchingHealthUserID(profileHealthUserID); err != nil {
			result.Status = "profile_mismatch"
			return err
		}
		return nil
	},
	finishArchived: func(result *profileResult, snapshotID int64, fetchedAt string) {
		result.Status = "profile_archived"
		result.SnapshotID = snapshotID
		result.FetchedAt = fetchedAt
		result.Message = "Profile Snapshot archived"
	},
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

func connectSetupWithRuntimeAndExtraScopes(configPath, archivePath string, noInput bool, extraScopes []string, runtime runtimeAdapters) (connectResult, error) {
	runtime = runtime.withDefaults()
	config, err := inspectFullConfig(configPath, archivePath)
	if err != nil {
		return connectResult{}, fmt.Errorf("config check failed: %w", err)
	}
	if config.oauthClient.kind != "file" {
		return connectResult{CredentialStore: config.credentialStore.kind}, errors.New("connect requires an OAuth client file source; Secret Provider references are setup-only")
	}
	if _, err := (healthArchiveLifecycle{path: archivePath}).MigrateAndInspect(context.Background(), false); err != nil {
		var checkErr healthArchiveOpenError
		if errors.As(err, &checkErr) {
			return connectResult{}, err
		}
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	store, err := newCredentialStoreWithRuntime(config.credentialStore, runtime)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := validateCredentialStoreRuntimeWithRuntime(config.credentialStore, []string{configPath, archivePath, config.oauthClient.path}, runtime); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	client, err := loadOAuthClientConfig(config.oauthClient.path)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	requestedScopes := unionScopes(oauthScopesForDataTypes(config.defaultDataTypes), extraScopes)
	token, err := runtime.runOAuthFlow(client, requestedScopes, noInput)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	identity, err := runtime.fetchIdentity(token.accessToken)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	connectionID := "googlehealth:" + identity.healthUserID

	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	defer archive.Close()
	// context.Background(): connect is a synchronous interactive flow
	// with no cancellation path today (its OAuth POST rides
	// context.Background() the same way, #284); the context keeps the
	// Connection writes on the Context API (#305) without changing
	// behavior.
	if err := archive.EnsureSameGoogleIdentity(context.Background(), identity.healthUserID); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := store.Store(connectionID, token.rawTokenMaterialObject); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := archive.UpsertConnection(context.Background(), connectionID, identity, token, runtime.now()); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	return connectResult{
		Status:             "connected",
		ConnectionID:       connectionID,
		ProviderName:       "googlehealth",
		GoogleHealthUserID: identity.healthUserID,
		LegacyFitbitUserID: identity.legacyFitbitUserID,
		CredentialStore:    config.credentialStore.kind,
		TokenStatus:        "metadata_present",
		Message:            "Google Identity connected",
	}, nil
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

// fetchGoogleIdentity is a thin call site over the shared Provider GET
// module (provider_get.go, issue #280), which owns the transport
// behavior: bearer auth, size limit, timeout, typed labeled status
// errors, JSON validity, and retry/Retry-After. The module value
// carries the HTTP doer (#281). Identity-shape validation stays in
// parseGoogleIdentity.
func fetchGoogleIdentity(get providerGET, accessToken string) (googleIdentity, error) {
	body, err := fetchProviderJSON(context.Background(), get, googleHealthIdentityURL, "identity", accessToken)
	if err != nil {
		return googleIdentity{}, err
	}
	return parseGoogleIdentity(body)
}

// fetchGoogleProfile is a thin call site over the shared Provider GET
// module (provider_get.go, issue #280), which owns the transport
// behavior: bearer auth, size limit, timeout, typed labeled status
// errors, JSON validity, and retry/Retry-After. The module value
// carries the HTTP doer (#281). Profile-shape validation stays in
// parseGoogleProfile.
func fetchGoogleProfile(get providerGET, accessToken string) (googleProfile, error) {
	body, err := fetchProviderJSON(context.Background(), get, googleHealthProfileURL, "profile", accessToken)
	if err != nil {
		return googleProfile{}, err
	}
	return parseGoogleProfile(body)
}

func parseGoogleProfile(body []byte) (googleProfile, error) {
	var raw struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return googleProfile{}, errors.New("Google Health profile response is not valid JSON")
	}
	if raw.Name == "" {
		return googleProfile{}, errors.New("Google Health profile response missing name")
	}
	return googleProfile{
		healthUserID: googleHealthUserIDFromProfileName(raw.Name),
		resourceName: raw.Name,
		rawJSON:      string(body),
	}, nil
}

func googleHealthUserIDFromProfileName(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) != 3 || parts[0] != "users" || parts[2] != "profile" || parts[1] == "me" {
		return ""
	}
	return parts[1]
}

func parseGoogleIdentity(body []byte) (googleIdentity, error) {
	var raw struct {
		HealthUserID string `json:"healthUserId"`
		LegacyUserID string `json:"legacyUserId"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return googleIdentity{}, errors.New("Google Health identity response is not valid JSON")
	}
	if raw.HealthUserID == "" {
		return googleIdentity{}, errors.New("Google Health identity response missing healthUserId")
	}
	return googleIdentity{
		healthUserID:       raw.HealthUserID,
		legacyFitbitUserID: raw.LegacyUserID,
		rawJSON:            string(body),
	}, nil
}

func parseCommaList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func writeConnectResult(result connectResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if result.ConnectionID != "" {
			if _, err := fmt.Fprintf(stdout, "connection_id: %s\n", result.ConnectionID); err != nil {
				return err
			}
		}
		if result.ProviderName != "" {
			if _, err := fmt.Fprintf(stdout, "provider_name: %s\n", result.ProviderName); err != nil {
				return err
			}
		}
		if result.GoogleHealthUserID != "" {
			if _, err := fmt.Fprintf(stdout, "google_health_user_id: %s\n", result.GoogleHealthUserID); err != nil {
				return err
			}
		}
		if result.LegacyFitbitUserID != "" {
			if _, err := fmt.Fprintf(stdout, "legacy_fitbit_user_id: %s\n", result.LegacyFitbitUserID); err != nil {
				return err
			}
		}
		if result.CredentialStore != "" {
			if _, err := fmt.Fprintf(stdout, "credential_store: %s\n", result.CredentialStore); err != nil {
				return err
			}
		}
		if result.TokenStatus != "" {
			if _, err := fmt.Fprintf(stdout, "token_status: %s\n", result.TokenStatus); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}
	if result.Status == "connected" {
		if _, err := fmt.Fprintln(stdout, "Connected Google Identity"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(stdout, "Connect failed"); err != nil {
		return err
	}
	if result.ConnectionID != "" {
		if _, err := fmt.Fprintf(stdout, "Connection: %s\n", result.ConnectionID); err != nil {
			return err
		}
	}
	if result.GoogleHealthUserID != "" {
		if _, err := fmt.Fprintf(stdout, "Google Health user ID: %s\n", result.GoogleHealthUserID); err != nil {
			return err
		}
	}
	if result.CredentialStore != "" {
		if _, err := fmt.Fprintf(stdout, "Credential Store: %s\n", result.CredentialStore); err != nil {
			return err
		}
	}
	if result.TokenStatus != "" {
		if _, err := fmt.Fprintf(stdout, "Token status: %s\n", result.TokenStatus); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
}

func writeIdentityResult(result identityResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if result.ConnectionID != "" {
			if _, err := fmt.Fprintf(stdout, "connection_id: %s\n", result.ConnectionID); err != nil {
				return err
			}
		}
		if result.ProviderName != "" {
			if _, err := fmt.Fprintf(stdout, "provider_name: %s\n", result.ProviderName); err != nil {
				return err
			}
		}
		if result.GoogleHealthUserID != "" {
			if _, err := fmt.Fprintf(stdout, "google_health_user_id: %s\n", result.GoogleHealthUserID); err != nil {
				return err
			}
		}
		if result.LegacyFitbitUserID != "" {
			if _, err := fmt.Fprintf(stdout, "legacy_fitbit_user_id: %s\n", result.LegacyFitbitUserID); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}
	switch result.Status {
	case "identity_refreshed":
		if _, err := fmt.Fprintln(stdout, "Google Identity refreshed"); err != nil {
			return err
		}
	case "identity_mismatch":
		if _, err := fmt.Fprintln(stdout, "Google Identity mismatch"); err != nil {
			return err
		}
	case "identity_unavailable":
		if _, err := fmt.Fprintln(stdout, "Google Identity unavailable"); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintln(stdout, "Google Identity failed"); err != nil {
			return err
		}
	}
	if result.ConnectionID != "" {
		if _, err := fmt.Fprintf(stdout, "Connection: %s\n", result.ConnectionID); err != nil {
			return err
		}
	}
	if result.GoogleHealthUserID != "" {
		if _, err := fmt.Fprintf(stdout, "Google Health user ID: %s\n", result.GoogleHealthUserID); err != nil {
			return err
		}
	}
	if result.LegacyFitbitUserID != "" {
		if _, err := fmt.Fprintf(stdout, "Legacy Fitbit user ID: %s\n", result.LegacyFitbitUserID); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
}

func writeProfileResult(result profileResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if result.SnapshotID != 0 {
			if _, err := fmt.Fprintf(stdout, "snapshot_id: %d\n", result.SnapshotID); err != nil {
				return err
			}
		}
		if result.ConnectionID != "" {
			if _, err := fmt.Fprintf(stdout, "connection_id: %s\n", result.ConnectionID); err != nil {
				return err
			}
		}
		if result.ProviderName != "" {
			if _, err := fmt.Fprintf(stdout, "provider_name: %s\n", result.ProviderName); err != nil {
				return err
			}
		}
		if result.GoogleHealthUserID != "" {
			if _, err := fmt.Fprintf(stdout, "google_health_user_id: %s\n", result.GoogleHealthUserID); err != nil {
				return err
			}
		}
		if result.LegacyFitbitUserID != "" {
			if _, err := fmt.Fprintf(stdout, "legacy_fitbit_user_id: %s\n", result.LegacyFitbitUserID); err != nil {
				return err
			}
		}
		if result.FetchedAt != "" {
			if _, err := fmt.Fprintf(stdout, "fetched_at: %s\n", result.FetchedAt); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}
	switch result.Status {
	case "profile_archived":
		if _, err := fmt.Fprintln(stdout, "Profile Snapshot archived"); err != nil {
			return err
		}
	case "profile_mismatch":
		if _, err := fmt.Fprintln(stdout, "Profile Snapshot mismatch"); err != nil {
			return err
		}
	case "profile_unavailable":
		if _, err := fmt.Fprintln(stdout, "Profile Snapshot unavailable"); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintln(stdout, "Profile Snapshot failed"); err != nil {
			return err
		}
	}
	if result.SnapshotID != 0 {
		if _, err := fmt.Fprintf(stdout, "Snapshot: %d\n", result.SnapshotID); err != nil {
			return err
		}
	}
	if result.ConnectionID != "" {
		if _, err := fmt.Fprintf(stdout, "Connection: %s\n", result.ConnectionID); err != nil {
			return err
		}
	}
	if result.GoogleHealthUserID != "" {
		if _, err := fmt.Fprintf(stdout, "Google Health user ID: %s\n", result.GoogleHealthUserID); err != nil {
			return err
		}
	}
	if result.FetchedAt != "" {
		if _, err := fmt.Fprintf(stdout, "Fetched at: %s\n", result.FetchedAt); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
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

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const setupMissingExitCode = 2

const googleHealthBaseURL = "https://health.googleapis.com/v4"
const googleHealthIdentityURL = "https://health.googleapis.com/v4/users/me/identity"
const googleHealthProfileURL = "https://health.googleapis.com/v4/users/me/profile"
const googleHealthRawResponseLimit = 10 << 20

type doctorResult struct {
	Status             string                  `json:"status"`
	ConfigPath         string                  `json:"config_path"`
	ArchivePath        string                  `json:"archive_path"`
	OAuthClientSource  string                  `json:"oauth_client_source"`
	CredentialStore    string                  `json:"credential_store"`
	SchemaVersion      *int                    `json:"schema_version"`
	ConnectionCount    *int                    `json:"connection_count"`
	TokenStatus        string                  `json:"token_status"`
	AttachmentRootPath string                  `json:"attachment_root_path,omitempty"`
	AttachmentRootMode string                  `json:"attachment_root_mode,omitempty"`
	Attachments        *doctorAttachmentReport `json:"attachments,omitempty"`
	Message            string                  `json:"message"`
}

type doctorAttachmentReport struct {
	// Always emit both arrays so downstream tools can rely on the
	// shape; nil slices would encode as null, so the constructor
	// initialises them to []. omitempty would break the contract when
	// only one side has orphans.
	OrphanRows  []doctorOrphanRow  `json:"orphan_rows"`
	OrphanFiles []doctorOrphanFile `json:"orphan_files"`
}

type doctorOrphanRow struct {
	SHA256       string `json:"sha256"`
	PathRelative string `json:"path_relative"`
	DataPointID  int64  `json:"data_point_id"`
}

type doctorOrphanFile struct {
	AbsolutePath string `json:"absolute_path"`
}

type initResult struct {
	Status            string   `json:"status"`
	ConfigPath        string   `json:"config_path"`
	ArchivePath       string   `json:"archive_path"`
	OAuthClientSource string   `json:"oauth_client_source,omitempty"`
	DefaultDataTypes  []string `json:"default_data_types"`
	SchemaVersion     int      `json:"schema_version"`
	Message           string   `json:"message,omitempty"`
}

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

type statusResult struct {
	Status                string `json:"status"`
	ArchivePath           string `json:"archive_path"`
	SchemaVersion         int    `json:"schema_version,omitempty"`
	DataPointCount        int    `json:"data_point_count"`
	RollupCount           int    `json:"rollup_count"`
	ProfileSnapshotCount  int    `json:"profile_snapshot_count"`
	IdentitySnapshotCount int    `json:"identity_snapshot_count"`
	SyncRunCount          int    `json:"sync_run_count"`
	// KnownDataTypes is the flat array of Data Type names also
	// emitted as the `known_data_types: a,b,c` plain line (PRD #144
	// slice 9). Computed once in StatusSummary from DataTypes so the
	// plain and JSON writers share a single source. omitempty keeps
	// the JSON shape clean on a brand-new archive.
	KnownDataTypes []string         `json:"known_data_types,omitempty"`
	DataTypes      []statusDataType `json:"data_types,omitempty"`
	// PairedDeviceCount is a top-level mirror of
	// IdentitySnapshotsFreshness.PairedDeviceCount so `--json` carries
	// the same key as `--plain` (PRD #144 slice 9). The nested
	// location is preserved for back-compat. omitempty so a clean
	// archive does not advertise a misleading zero.
	PairedDeviceCount          int                      `json:"paired_device_count,omitempty"`
	IdentitySnapshotsFreshness *statusSnapshotFreshness `json:"identity_snapshots_freshness,omitempty"`
	Tier2                      *statusTier2             `json:"tier_2,omitempty"`
	LatestSuccessfulRun        *statusSyncRun           `json:"latest_successful_sync_run,omitempty"`
	LatestFailedRun            *statusSyncRun           `json:"latest_failed_sync_run,omitempty"`
	Message                    string                   `json:"message"`
}

// statusSnapshotFreshness summarises identity-level metadata recency:
// when each kind's latest snapshot was fetched, and how many devices
// are in the current paired-devices snapshot. Helps an operator (and
// an LLM) verify metadata freshness before running analysis.
type statusSnapshotFreshness struct {
	PairedDeviceCount int               `json:"paired_device_count"`
	LatestFetchedAt   map[string]string `json:"latest_fetched_at,omitempty"`
}

// statusTier2 summarises Tier 2 (ECG + IRN) coverage for the archive
// (#111). Both counts default to 0 when the scopes have not been
// granted — the AC: "defaulting to 0 when the scopes are not
// granted". The scope_granted flags let the plain writer decide
// whether to emit the corresponding line, and let downstream tooling
// distinguish "0 because no rows yet" from "0 because no scope".
type statusTier2 struct {
	ElectrocardiogramEventCount             int  `json:"electrocardiogram_event_count"`
	ElectrocardiogramScopeGranted           bool `json:"electrocardiogram_scope_granted"`
	IrregularRhythmNotificationCount        int  `json:"irregular_rhythm_notification_count"`
	IrregularRhythmNotificationScopeGranted bool `json:"irregular_rhythm_notification_scope_granted"`
}

type statusDataType struct {
	DataType                 string             `json:"data_type"`
	DataPointCount           int                `json:"data_point_count"`
	RollupCount              int                `json:"rollup_count"`
	NewestDataPointTimestamp string             `json:"newest_data_point_timestamp,omitempty"`
	NewestRollupTimestamp    string             `json:"newest_rollup_timestamp,omitempty"`
	SyncCursors              []statusSyncCursor `json:"sync_cursors,omitempty"`
}

type statusSyncCursor struct {
	SourceFamilyFilter string `json:"source_family_filter,omitempty"`
	RollupKind         string `json:"rollup_kind"`
	CursorTime         string `json:"cursor_time"`
	AdvancedAt         string `json:"advanced_at"`
}

type statusSyncRun struct {
	ID                 int64    `json:"id"`
	Status             string   `json:"status"`
	DataTypes          []string `json:"data_types,omitempty"`
	From               string   `json:"from,omitempty"`
	To                 string   `json:"to,omitempty"`
	EndpointFamily     string   `json:"endpoint_family,omitempty"`
	SourceFamilyFilter string   `json:"source_family_filter,omitempty"`
	SeenCount          int      `json:"seen_count"`
	NewCount           int      `json:"new_count"`
	UpdatedCount       int      `json:"updated_count"`
	StartedAt          string   `json:"started_at,omitempty"`
	FinishedAt         string   `json:"finished_at,omitempty"`
	ErrorSummary       string   `json:"error_summary,omitempty"`
}

type archivedConnection struct {
	id                 string
	providerName       string
	googleHealthUserID string
	legacyFitbitUserID string
	tokenMetadataJSON  string
}

type rawProviderRequest struct {
	endpointName       string
	dataType           string
	method             string
	url                string
	body               []byte
	requiredScopes     []string
	sourceFamilyFilter string
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

type archiveCheck struct {
	schemaVersion   int
	connectionCount int
	tokenStatus     string
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

type googleHealthDataPointList struct {
	dataPoints    []json.RawMessage
	nextPageToken string
}

type archivedDataPoint struct {
	providerName         string
	connectionID         string
	dataType             string
	upstreamResourceName string
	recordKind           string
	startTimeUTC         string
	endTimeUTC           string
	startCivilTime       string
	endCivilTime         string
	providerCivilDate    string
	timezoneMetadataJSON string
	dataSourceJSON       string
	sourceFamilyFilter   string
	rawJSON              string
}

type googleHealthDataPointEnvelope struct {
	name           string
	dataPointName  string
	dataSourceJSON string
	fields         map[string]json.RawMessage
}

type googleHealthIntervalFields struct {
	StartTime      string          `json:"startTime"`
	StartUTCOffset string          `json:"startUtcOffset"`
	EndTime        string          `json:"endTime"`
	EndUTCOffset   string          `json:"endUtcOffset"`
	CivilStartTime json.RawMessage `json:"civilStartTime"`
	CivilEndTime   json.RawMessage `json:"civilEndTime"`
}

type parsedGoogleHealthInterval struct {
	startTimeUTC         string
	endTimeUTC           string
	startCivilTime       string
	endCivilTime         string
	providerCivilDate    string
	timezoneMetadataJSON string
}

type archivedRollup struct {
	providerName         string
	connectionID         string
	dataType             string
	rollupKind           string
	windowStartUTC       string
	windowEndUTC         string
	civilDate            string
	timezoneMetadataJSON string
	rawJSON              string
}

// productionFetchIdentity and productionFetchProfile bind the real
// fetchers over the production Provider GET module (shared timeout
// client as the HTTP doer, #281). Plain functions, not package vars:
// tests fake these dependencies through runtimeAdapters fields (#283).
func productionFetchIdentity(accessToken string) (googleIdentity, error) {
	return fetchGoogleIdentity(productionProviderGET(), accessToken)
}

func productionFetchProfile(accessToken string) (googleProfile, error) {
	return fetchGoogleProfile(productionProviderGET(), accessToken)
}

// productionFetchRawProvider binds the real raw Provider fetch over the
// shared timeout client. ctx scopes the HTTP request so a canceled Sync
// Run aborts the in-flight call (#284).
func productionFetchRawProvider(ctx context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
	return fetchGoogleHealthRaw(ctx, providerHTTPClient, request, accessToken)
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

func runDoctorWithRuntime(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		JSONOutput:  mode.json,
		PlainOutput: mode.plain,
	})
	doctorOnline := flags.Bool("online", false, "refresh tokens and check provider reachability")

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "doctor",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected doctor argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	if fileExists(common.ConfigPath) && fileExists(common.ArchivePath) {
		if *doctorOnline {
			return runDoctorOnlineWithRuntime(common.ConfigPath, common.ArchivePath, mode, stdout, stderr, runtime)
		}
		config, err := inspectConfig(common.ConfigPath, common.ArchivePath)
		if err != nil {
			return runDoctorInvalid(common.ConfigPath, common.ArchivePath, fmt.Sprintf("config check failed: %v", err), mode, stdout, stderr)
		}
		archive, err := (healthArchiveLifecycle{path: common.ArchivePath}).MigrateAndInspect(context.Background(), true)
		if err != nil {
			return runDoctorInvalid(common.ConfigPath, common.ArchivePath, err.Error(), mode, stdout, stderr)
		}
		result := doctorResult{
			Status:            "ok",
			ConfigPath:        common.ConfigPath,
			ArchivePath:       common.ArchivePath,
			OAuthClientSource: config.oauthClientSource,
			CredentialStore:   config.credentialStore,
			SchemaVersion:     &archive.schemaVersion,
			ConnectionCount:   &archive.connectionCount,
			TokenStatus:       archive.tokenStatus,
			Message:           "local gohealthcli setup is initialized",
		}
		attachmentRoot, attachmentMode, attachmentErr := inspectAttachmentRoot(common.ArchivePath)
		if attachmentErr != nil {
			return runDoctorInvalid(common.ConfigPath, common.ArchivePath, attachmentErr.Error(), mode, stdout, stderr)
		}
		result.AttachmentRootPath = attachmentRoot
		result.AttachmentRootMode = attachmentMode
		attachments, attachmentsErr := collectAttachmentOrphans(context.Background(), common.ArchivePath)
		if attachmentsErr != nil {
			return runDoctorInvalid(common.ConfigPath, common.ArchivePath, attachmentsErr.Error(), mode, stdout, stderr)
		}
		result.Attachments = attachments
		if err := writeDoctorResult(result, mode, stdout); err != nil {
			return reportWriteFailure("doctor", err, mode, stdout, stderr)
		}
		return 0
	}
	if fileExists(common.ConfigPath) || fileExists(common.ArchivePath) {
		return runDoctorInvalid(common.ConfigPath, common.ArchivePath, "partial local setup found; run `gohealthcli init` after moving existing config or Health Archive", mode, stdout, stderr)
	}

	result := doctorResult{
		Status:      "setup_missing",
		ConfigPath:  common.ConfigPath,
		ArchivePath: common.ArchivePath,
		TokenStatus: "unknown",
		Message:     "local gohealthcli setup not found",
	}

	if err := writeDoctorResult(result, mode, stdout); err != nil {
		return reportWriteFailure("doctor", err, mode, stdout, stderr)
	}

	// The structured envelope already landed on stdout via
	// writeDoctorResult above; the hint line stays as a plain stderr
	// write so JSON-mode callers get the envelope on stdout AND the
	// human hint on stderr. failureExitCode routes through the
	// Failure Reporter module's status→exit-code map so no site
	// references setupMissingExitCode directly (#178).
	fmt.Fprintln(stderr, "run `gohealthcli init` to create local config and Health Archive")
	return failureExitCode(StatusSetupMissing)
}

func runDoctorInvalid(configPath, archivePath, message string, mode outputMode, stdout, stderr io.Writer) int {
	result := doctorResult{
		Status:      "setup_invalid",
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		TokenStatus: "unknown",
		Message:     message,
	}
	if err := writeDoctorResult(result, mode, stdout); err != nil {
		return reportWriteFailure("doctor", err, mode, stdout, stderr)
	}
	return 1
}

func runDoctorOnlineWithRuntime(configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	result, err := doctorOnlineSetupWithRuntime(configPath, archivePath, runtime)
	if err != nil {
		if result.Status == "" || result.Status == "ok" {
			result.Status = "connection_unhealthy"
		}
		result.Message = err.Error()
		if writeErr := writeDoctorResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure("doctor", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	if err := writeDoctorResult(result, mode, stdout); err != nil {
		return reportWriteFailure("doctor", err, mode, stdout, stderr)
	}
	return 0
}

func runStatus(args []string, configPath, archivePath string, configPathExplicit, archivePathExplicit bool, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)

	// Seed the two explicitness bits from the dispatch-time CommonFlagValues
	// so the readArchivePathResolver below sees the correct shape even when
	// the user passed --db / --config at the global slot (before the
	// subcommand). ParseCommon's Visit pass below ORs in subcommand-local
	// flag values too, so passing them on either side of the subcommand
	// works the same way.
	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:          configPath,
		ArchivePath:         archivePath,
		JSONOutput:          mode.json,
		PlainOutput:         mode.plain,
		ArchivePathExplicit: archivePathExplicit,
		ConfigPathExplicit:  configPathExplicit,
	})

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "status",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected status argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	resolvedArchivePath, err := resolveReadArchivePath(*common)
	if err != nil {
		result := statusResult{Status: "status_failed", ArchivePath: common.ArchivePath, Message: err.Error()}
		if writeErr := writeStatusResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure("status", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	// context.Background(): status is a synchronous read command with no
	// cancellation path today; the context keeps the archive reads on the
	// Context API (#305) without changing behavior.
	result, err := statusSetup(context.Background(), resolvedArchivePath, runtime.withDefaults().now().UTC())
	if err != nil {
		if result.Status == "" {
			result.Status = "status_failed"
		}
		result.Message = err.Error()
		if writeErr := writeStatusResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure("status", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	if err := writeStatusResult(result, mode, stdout); err != nil {
		return reportWriteFailure("status", err, mode, stdout, stderr)
	}
	return 0
}

func runInit(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		JSONOutput:  mode.json,
		PlainOutput: mode.plain,
	})
	oauthClientFile := flags.String("oauth-client-file", "", "OAuth client JSON file reference")
	secretProvider := flags.String("secret-provider", "", "Secret Provider name for OAuth client setup")
	oauthClientItem := flags.String("oauth-client-item", "", "Secret Provider item name for OAuth client setup")

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "init",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected init argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	if fileExists(common.ConfigPath) && fileExists(common.ArchivePath) {
		if err := validateConfig(common.ConfigPath, common.ArchivePath); err != nil {
			return ReportFailure(FailureReport{
				Command: "init",
				Status:  StatusOperationFailed,
				Message: fmt.Sprintf("existing config is not initialized: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		lifecycle := healthArchiveLifecycle{path: common.ArchivePath}
		if err := lifecycle.Migrate(context.Background()); err != nil {
			return ReportFailure(FailureReport{
				Command: "init",
				Status:  StatusOperationFailed,
				Message: fmt.Sprintf("existing Health Archive is not initialized: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		if _, err := lifecycle.Inspect(context.Background(), false); err != nil {
			return ReportFailure(FailureReport{
				Command: "init",
				Status:  StatusOperationFailed,
				Message: fmt.Sprintf("existing Health Archive is not initialized: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		result := initResult{
			Status:           "already_initialized",
			ConfigPath:       common.ConfigPath,
			ArchivePath:      common.ArchivePath,
			DefaultDataTypes: defaultDataTypes,
			SchemaVersion:    currentSchemaVersion,
			Message:          "local gohealthcli setup already exists",
		}
		if err := writeInitResult(result, mode, stdout); err != nil {
			return reportWriteFailure("init", err, mode, stdout, stderr)
		}
		return 0
	}
	if fileExists(common.ConfigPath) || fileExists(common.ArchivePath) {
		return ReportFailure(FailureReport{
			Command: "init",
			Status:  StatusOperationFailed,
			Message: "refusing to overwrite partial local setup; move existing config or Health Archive first",
			Mode:    mode,
		}, stdout, stderr)
	}

	source, err := parseOAuthClientSource(*oauthClientFile, *secretProvider, *oauthClientItem)
	if err != nil {
		return ReportFailure(FailureReport{Command: "init", Status: StatusFlagInvalid, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	if err := validateOAuthClientConfig(source); err != nil {
		return ReportFailure(FailureReport{Command: "init", Status: StatusFlagInvalid, Message: err.Error(), Mode: mode}, stdout, stderr)
	}

	if err := createConfigFile(common.ConfigPath, common.ArchivePath, source); err != nil {
		return ReportFailure(FailureReport{
			Command: "init",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("create config: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	if err := createArchive(common.ArchivePath); err != nil {
		_ = os.Remove(common.ConfigPath)
		_ = os.Remove(common.ArchivePath)
		return ReportFailure(FailureReport{
			Command: "init",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("create Health Archive: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}

	result := initResult{
		Status:            "initialized",
		ConfigPath:        common.ConfigPath,
		ArchivePath:       common.ArchivePath,
		OAuthClientSource: source.kind,
		DefaultDataTypes:  defaultDataTypes,
		SchemaVersion:     currentSchemaVersion,
	}
	if err := writeInitResult(result, mode, stdout); err != nil {
		return reportWriteFailure("init", err, mode, stdout, stderr)
	}
	return 0
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

func doctorOnlineSetupWithRuntime(configPath, archivePath string, runtime runtimeAdapters) (doctorResult, error) {
	runtime = runtime.withDefaults()
	config, err := inspectFullConfig(configPath, archivePath)
	if err != nil {
		return doctorResult{Status: "setup_invalid", ConfigPath: configPath, ArchivePath: archivePath, TokenStatus: "unknown"}, fmt.Errorf("config check failed: %w", err)
	}
	result := doctorResult{
		Status:            "ok",
		ConfigPath:        configPath,
		ArchivePath:       archivePath,
		OAuthClientSource: config.oauthClient.kind,
		CredentialStore:   config.credentialStore.kind,
	}
	archive, err := (healthArchiveLifecycle{path: archivePath}).MigrateAndInspect(context.Background(), true)
	if err != nil {
		result.Status = "setup_invalid"
		result.TokenStatus = "unknown"
		return result, err
	}
	result.SchemaVersion = &archive.schemaVersion
	result.ConnectionCount = &archive.connectionCount
	result.TokenStatus = archive.tokenStatus
	attachmentRoot, attachmentMode, attachmentErr := inspectAttachmentRoot(archivePath)
	if attachmentErr != nil {
		result.Status = "setup_invalid"
		return result, attachmentErr
	}
	result.AttachmentRootPath = attachmentRoot
	result.AttachmentRootMode = attachmentMode
	attachments, attachmentsErr := collectAttachmentOrphans(context.Background(), archivePath)
	if attachmentsErr != nil {
		result.Status = "setup_invalid"
		return result, attachmentsErr
	}
	result.Attachments = attachments
	if archive.connectionCount == 0 {
		result.TokenStatus = "not_connected"
		return result, errors.New("no Connection found; run `gohealthcli connect` first")
	}
	archiveAPI, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		result.Status = "setup_invalid"
		result.TokenStatus = "archive_unavailable"
		return result, err
	}
	defer archiveAPI.Close()
	connection, err := archiveAPI.CurrentConnection()
	if err != nil {
		result.TokenStatus = "connection_unavailable"
		return result, err
	}
	protectedPaths := []string{configPath, archivePath, config.oauthClient.path}
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, protectedPaths, runtime)
	tokenCheck, err := connectionAccess.RefreshableAccessToken(config.oauthClient)
	if err != nil {
		if result.TokenStatus == archive.tokenStatus {
			if isCurrentConnectionTokenMissing(err) {
				result.TokenStatus = "token_missing"
			} else {
				result.TokenStatus = "refresh_failed"
			}
		}
		return result, err
	}
	if tokenCheck.refreshedToken == nil {
		result.TokenStatus = "metadata_present"
	}
	if _, err := connectionAccess.FetchVerifiedIdentity(tokenCheck.accessToken); err != nil {
		result.TokenStatus = "provider_unreachable"
		if isCurrentConnectionIdentityMismatch(err) {
			result.TokenStatus = "identity_mismatch"
		}
		return result, err
	}
	if tokenCheck.refreshedToken != nil {
		if err := persistDoctorOnlineRefreshedTokenWithRuntime(archiveAPI, config.credentialStore, connection.id, *tokenCheck.refreshedToken, tokenCheck.previousTokenMaterial, runtime); err != nil {
			result.TokenStatus = "refresh_failed"
			return result, err
		}
	}
	result.TokenStatus = "online_ok"
	result.Message = "online Google Health check passed"
	return result, nil
}

func persistDoctorOnlineRefreshedTokenWithRuntime(archive connectionTokenWriter, credentialStore credentialStoreConfig, connectionID string, token oauthTokenResponse, previousTokenMaterial map[string]any, runtime runtimeAdapters) error {
	runtime = runtime.withDefaults()
	store, err := newCredentialStoreWithRuntime(credentialStore, runtime)
	if err != nil {
		return err
	}
	if err := store.Store(connectionID, token.rawTokenMaterialObject); err != nil {
		return err
	}
	if err := archive.UpdateConnectionTokenMetadata(connectionID, token, runtime.now()); err != nil {
		if rollbackErr := store.Store(connectionID, previousTokenMaterial); rollbackErr != nil {
			// The secondary rollback error is deliberately %v, not %w: only
			// the primary archive error may carry the typed-error chain
			// callers branch on (#272 translation layer).
			return fmt.Errorf("%w; rollback Credential Store token material: %v", err, rollbackErr) //nolint:errorlint // deliberate non-wrapping %v for the secondary error
		}
		return err
	}
	return nil
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

func createArchive(archivePath string) (err error) {
	if err := (healthArchiveLifecycle{path: archivePath}).Create(context.Background()); err != nil {
		return err
	}
	// Pre-create the attachment root so users running init see the
	// full archive shape without waiting for a sync to lazily create
	// it. Owner-only mode follows the rest of the archive.
	return ensureOwnerOnlyDir(attachmentRootDirForArchive(archivePath))
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

func buildGoogleHealthRawRequest(target []string, from, to string, pageSize int64, pageToken string) (rawProviderRequest, error) {
	if len(target) < 2 {
		return rawProviderRequest{}, errors.New("requires `endpoint <name>` or `data-type <name>`")
	}
	switch target[0] {
	case "endpoint":
		if len(target) != 2 {
			return rawProviderRequest{}, errors.New("endpoint mode requires exactly one endpoint name")
		}
		// Identity-style endpoints route through the catalog: URL
		// lookup comes from googleHealthIdentityEndpointURLs, scopes
		// from googleHealthIdentityEndpointScopes. PRD #142 slice 7
		// makes `raw endpoint <name>` and the matching introspection
		// command (`profile`, `settings`, `devices`, `irn-profile`)
		// share one source of truth, so a scope revision (slice 2) is
		// a one-row change.
		if endpointURL, ok := googleHealthIdentityEndpointURLs[target[1]]; ok {
			requiredScopes, hasScopes := googleHealthIdentityEndpointScopes[target[1]]
			if !hasScopes || len(requiredScopes) == 0 {
				return rawProviderRequest{}, fmt.Errorf("internal: identity endpoint %q present in URL catalog but missing from scope catalog", target[1])
			}
			return rawProviderRequest{
				endpointName:   target[1],
				url:            endpointURL,
				requiredScopes: requiredScopes,
			}, nil
		}
		if strings.HasPrefix(target[1], "dataTypes.") && strings.HasSuffix(target[1], ".list") {
			dataType := strings.TrimSuffix(strings.TrimPrefix(target[1], "dataTypes."), ".list")
			return buildGoogleHealthDataTypeListRawRequest(dataType, from, to, pageSize, pageToken)
		}
		return rawProviderRequest{}, fmt.Errorf("unsupported raw endpoint %q", target[1])
	case "data-type":
		if len(target) != 2 {
			return rawProviderRequest{}, errors.New("data-type mode requires exactly one Data Type")
		}
		return buildGoogleHealthDataTypeListRawRequest(target[1], from, to, pageSize, pageToken)
	default:
		return rawProviderRequest{}, fmt.Errorf("unsupported raw target %q", target[0])
	}
}

func buildGoogleHealthDataTypeListRawRequest(dataType, from, to string, pageSize int64, pageToken string) (rawProviderRequest, error) {
	if err := validateRawGoogleHealthDataType(dataType); err != nil {
		return rawProviderRequest{}, err
	}
	if from == "" {
		return rawProviderRequest{}, errors.New("Data Type list raw calls require --from")
	}
	query := url.Values{}
	filter, err := googleHealthDataTypeListFilter(dataType, from, to)
	if err != nil {
		return rawProviderRequest{}, err
	}
	query.Set("filter", filter)
	if pageSize > 0 {
		query.Set("pageSize", strconv.FormatInt(pageSize, 10))
	}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	requestURL := googleHealthBaseURL + "/users/me/dataTypes/" + url.PathEscape(dataType) + "/dataPoints"
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}
	return rawProviderRequest{
		endpointName:   "dataTypes." + dataType + ".list",
		dataType:       dataType,
		method:         http.MethodGet,
		url:            requestURL,
		requiredScopes: googleHealthScopesForDataType(dataType),
	}, nil
}

func validateRawGoogleHealthDataType(dataType string) error {
	if dataType == "" {
		return errors.New("Data Type must not be empty")
	}
	for _, char := range dataType {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return fmt.Errorf("Data Type %q must use kebab-case provider names", dataType)
	}
	return nil
}

func googleHealthDataTypeListFilter(dataType, from, to string) (string, error) {
	field, err := googleHealthDataTypeListFilterField(dataType)
	if err != nil {
		return "", err
	}
	filterFrom, err := googleHealthFilterValue(field, from)
	if err != nil {
		return "", fmt.Errorf("--from: %w", err)
	}
	clauses := []string{fmt.Sprintf("%s >= %s", field, filterFrom)}
	if to != "" {
		filterTo, err := googleHealthFilterValue(field, to)
		if err != nil {
			return "", fmt.Errorf("--to: %w", err)
		}
		clauses = append(clauses, fmt.Sprintf("%s < %s", field, filterTo))
	}
	return strings.Join(clauses, " AND "), nil
}

func googleHealthFilterValue(field, value string) (string, error) {
	if strings.HasSuffix(field, ".date") {
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return "", errors.New("expected YYYY-MM-DD")
		}
		return strconv.Quote(value), nil
	}
	if strings.Contains(field, ".civil_") {
		if _, err := time.Parse("2006-01-02", value); err == nil {
			return strconv.Quote(value), nil
		}
		if _, err := time.Parse("2006-01-02T15:04:05", value); err == nil {
			return strconv.Quote(value), nil
		}
		return "", errors.New("expected YYYY-MM-DD or YYYY-MM-DDTHH:mm:ss")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return strconv.Quote(parsed.UTC().Format(time.RFC3339Nano)), nil
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return strconv.Quote(parsed.UTC().Format("2006-01-02T00:00:00Z")), nil
	}
	return "", errors.New("expected YYYY-MM-DD or RFC3339")
}

// fetchGoogleHealthRaw is the single-attempt raw Provider fetch. The
// HTTP doer is injected (#281): production binds the shared timeout
// client via the fetchRawProvider seam and the runtime adapters; tests
// bind a fake doer to exercise this body directly. The request is
// scoped to ctx (#284), so canceling it aborts the in-flight call.
func fetchGoogleHealthRaw(ctx context.Context, doer httpDoer, request rawProviderRequest, accessToken string) ([]byte, error) {
	method := request.method
	if method == "" {
		method = http.MethodGet
	}
	var requestBody io.Reader
	if len(request.body) != 0 {
		requestBody = bytes.NewReader(request.body)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, request.url, requestBody)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+accessToken)
	httpRequest.Header.Set("Accept", "application/json")
	if len(request.body) != 0 {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := doer.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, tooLarge, err := readLimitedBody(response.Body, googleHealthRawResponseLimit)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &googleHealthHTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
			Body:       body,
		}
	}
	if tooLarge {
		return nil, fmt.Errorf("Google Health raw response exceeds %d bytes; narrow the raw request", googleHealthRawResponseLimit)
	}
	return body, nil
}

// googleHealthHTTPError carries the upstream status code plus an optional
// Retry-After hint. The ingestion retry middleware uses these to decide
// whether to retry transient failures (429, 5xx) and how long to wait
// before doing so; the Provider error translation layer
// (provider_error_normalization.go) reads StatusCode via errors.As to
// detect auth rejections and provider_unreachable failures without
// matching on message text (issue #272). Other callers can still read
// the error string.
type googleHealthHTTPError struct {
	StatusCode int
	RetryAfter time.Duration
	Body       []byte
	// endpoint labels which Provider request failed ("identity",
	// "pairedDevices", ...) so each fetcher keeps its historical
	// user-facing message verbatim. Empty means the raw Provider fetch
	// path, whose message predates the label.
	endpoint string
}

func (err *googleHealthHTTPError) Error() string {
	// Deliberately omit the response body — Google Health echoes the
	// bearer token in some error responses (covered by
	// TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody). Callers that
	// need the body can read err.Body directly.
	label := err.endpoint
	if label == "" {
		label = "raw"
	}
	return fmt.Sprintf("Google Health %s request failed with HTTP %d", label, err.StatusCode)
}

// parseRetryAfter parses the Retry-After header. RFC 7231 allows either
// an HTTP-date or a delta-seconds. We accept the delta-seconds form (the
// only form Google Health emits in practice) and ignore the date form so
// the middleware never blocks for hours on a malformed header.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

func readLimitedBody(reader io.Reader, limit int64) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > limit {
		return nil, true, nil
	}
	return body, false, nil
}

func parseGoogleHealthDataPointList(body []byte) (googleHealthDataPointList, error) {
	var raw struct {
		DataPoints    []json.RawMessage `json:"dataPoints"`
		NextPageToken string            `json:"nextPageToken"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return googleHealthDataPointList{}, errors.New("Google Health Data Point list response is not valid JSON")
	}
	return googleHealthDataPointList{dataPoints: raw.DataPoints, nextPageToken: raw.NextPageToken}, nil
}

func parseGoogleHealthDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter string) (archivedDataPoint, error) {
	if jsonField, recordKind, ok := googleHealthIntervalShapedDataPointShape(dataType); ok {
		return parseGoogleHealthIntervalShapedDataPoint(connection, dataType, rawPoint, sourceFamilyFilter, jsonField, recordKind)
	}
	if googleHealthSampleDataPointJSONField(dataType) != "" {
		return parseGoogleHealthSampleDataPoint(connection, dataType, rawPoint, sourceFamilyFilter)
	}
	if googleHealthDailyDataPointJSONField(dataType) != "" {
		return parseGoogleHealthDailyDataPoint(connection, dataType, rawPoint, sourceFamilyFilter)
	}
	return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point is not supported", dataType)
}

func parseGoogleHealthDataPointEnvelope(dataType string, rawPoint json.RawMessage) (googleHealthDataPointEnvelope, error) {
	var raw struct {
		Name          string                     `json:"name"`
		DataPointName string                     `json:"dataPointName"`
		DataSource    json.RawMessage            `json:"dataSource"`
		Fields        map[string]json.RawMessage `json:"-"`
	}
	if err := json.Unmarshal(rawPoint, &raw.Fields); err != nil {
		return googleHealthDataPointEnvelope{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	if err := json.Unmarshal(rawPoint, &raw); err != nil {
		return googleHealthDataPointEnvelope{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	dataSourceJSON := "{}"
	if len(raw.DataSource) != 0 && string(raw.DataSource) != "null" {
		var err error
		dataSourceJSON, err = compactJSONString(raw.DataSource)
		if err != nil {
			return googleHealthDataPointEnvelope{}, fmt.Errorf("Google Health %s Data Point dataSource is not valid JSON", dataType)
		}
	}
	return googleHealthDataPointEnvelope{
		name:           raw.Name,
		dataPointName:  raw.DataPointName,
		dataSourceJSON: dataSourceJSON,
		fields:         raw.Fields,
	}, nil
}

func (envelope googleHealthDataPointEnvelope) upstreamResourceName() string {
	if envelope.name != "" {
		return envelope.name
	}
	return envelope.dataPointName
}

// requiredField returns the named value object, or the missing-value
// error every Data Point parser shape reports for an absent field.
func (envelope googleHealthDataPointEnvelope) requiredField(dataType, jsonField string) (json.RawMessage, error) {
	rawValue, ok := envelope.fields[jsonField]
	if !ok || len(rawValue) == 0 || string(rawValue) == "null" {
		return nil, fmt.Errorf("Google Health %s Data Point missing %s value", dataType, jsonField)
	}
	return rawValue, nil
}

// parseGoogleHealthDataPointHead performs the envelope decode shared
// by every Data Point parser shape: the canonical raw JSON archived on
// the row plus the name / dataSource / field-map envelope.
func parseGoogleHealthDataPointHead(dataType string, rawPoint json.RawMessage) (string, googleHealthDataPointEnvelope, error) {
	canonicalRaw, err := compactJSONString(rawPoint)
	if err != nil {
		return "", googleHealthDataPointEnvelope{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	envelope, err := parseGoogleHealthDataPointEnvelope(dataType, rawPoint)
	if err != nil {
		return "", googleHealthDataPointEnvelope{}, err
	}
	return canonicalRaw, envelope, nil
}

func parseGoogleHealthIntervalMetadata(dataType string, interval googleHealthIntervalFields) (parsedGoogleHealthInterval, error) {
	if interval.StartTime == "" || interval.EndTime == "" {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point missing interval startTime or endTime", dataType)
	}
	startTimeUTC, err := normalizeGoogleTimestamp(interval.StartTime)
	if err != nil {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point startTime: %w", dataType, err)
	}
	endTimeUTC, err := normalizeGoogleTimestamp(interval.EndTime)
	if err != nil {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point endTime: %w", dataType, err)
	}
	startCivilTime, providerCivilDate, err := googleCivilDateTimeText(interval.CivilStartTime)
	if err != nil {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point civilStartTime: %w", dataType, err)
	}
	endCivilTime, _, err := googleCivilDateTimeText(interval.CivilEndTime)
	if err != nil {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point civilEndTime: %w", dataType, err)
	}
	timezoneMetadata, err := googleIntervalTimezoneMetadataJSON(interval.StartUTCOffset, interval.EndUTCOffset)
	if err != nil {
		return parsedGoogleHealthInterval{}, err
	}
	return parsedGoogleHealthInterval{
		startTimeUTC:         startTimeUTC,
		endTimeUTC:           endTimeUTC,
		startCivilTime:       startCivilTime,
		endCivilTime:         endCivilTime,
		providerCivilDate:    providerCivilDate,
		timezoneMetadataJSON: timezoneMetadata,
	}, nil
}

// parseGoogleHealthIntervalShapedDataPoint is the single parser for
// the interval-shaped Data Point kinds (steps, interval, session).
// The Data Type catalog supplies the two values the kinds differ in:
// the JSON field holding the upstream value object and the record
// kind stored on the archived row (#278).
func parseGoogleHealthIntervalShapedDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter, jsonField, recordKind string) (archivedDataPoint, error) {
	canonicalRaw, envelope, err := parseGoogleHealthDataPointHead(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	rawValue, err := envelope.requiredField(dataType, jsonField)
	if err != nil {
		return archivedDataPoint{}, err
	}
	var value struct {
		Interval googleHealthIntervalFields `json:"interval"`
	}
	if err := json.Unmarshal(rawValue, &value); err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point %s is not valid JSON", dataType, jsonField)
	}
	interval, err := parseGoogleHealthIntervalMetadata(dataType, value.Interval)
	if err != nil {
		return archivedDataPoint{}, err
	}
	return archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             dataType,
		upstreamResourceName: envelope.upstreamResourceName(),
		recordKind:           recordKind,
		startTimeUTC:         interval.startTimeUTC,
		endTimeUTC:           interval.endTimeUTC,
		startCivilTime:       interval.startCivilTime,
		endCivilTime:         interval.endCivilTime,
		providerCivilDate:    interval.providerCivilDate,
		timezoneMetadataJSON: interval.timezoneMetadataJSON,
		dataSourceJSON:       envelope.dataSourceJSON,
		sourceFamilyFilter:   sourceFamilyFilter,
		rawJSON:              canonicalRaw,
	}, nil
}

func parseGoogleHealthSampleDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter string) (archivedDataPoint, error) {
	canonicalRaw, envelope, err := parseGoogleHealthDataPointHead(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	jsonField := googleHealthSampleDataPointJSONField(dataType)
	rawSample, err := envelope.requiredField(dataType, jsonField)
	if err != nil {
		return archivedDataPoint{}, err
	}
	var sample struct {
		SampleTime struct {
			PhysicalTime string          `json:"physicalTime"`
			UTCOffset    string          `json:"utcOffset"`
			CivilTime    json.RawMessage `json:"civilTime"`
		} `json:"sampleTime"`
	}
	if err := json.Unmarshal(rawSample, &sample); err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point %s is not valid JSON", dataType, jsonField)
	}
	if sample.SampleTime.PhysicalTime == "" {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point missing sampleTime physicalTime", dataType)
	}
	startTimeUTC, err := normalizeGoogleTimestamp(sample.SampleTime.PhysicalTime)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point sampleTime physicalTime: %w", dataType, err)
	}
	startCivilTime, providerCivilDate, err := googleCivilDateTimeText(sample.SampleTime.CivilTime)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point sampleTime civilTime: %w", dataType, err)
	}
	timezoneMetadata, err := googleSampleTimezoneMetadataJSON(sample.SampleTime.UTCOffset)
	if err != nil {
		return archivedDataPoint{}, err
	}
	return archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             dataType,
		upstreamResourceName: envelope.upstreamResourceName(),
		recordKind:           "sample",
		startTimeUTC:         startTimeUTC,
		startCivilTime:       startCivilTime,
		providerCivilDate:    providerCivilDate,
		timezoneMetadataJSON: timezoneMetadata,
		dataSourceJSON:       envelope.dataSourceJSON,
		sourceFamilyFilter:   sourceFamilyFilter,
		rawJSON:              canonicalRaw,
	}, nil
}

func parseGoogleHealthDailyDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter string) (archivedDataPoint, error) {
	canonicalRaw, envelope, err := parseGoogleHealthDataPointHead(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	shape, ok := googleHealthDailyDataPointShapeForDataType(dataType)
	if !ok {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point is not supported", dataType)
	}
	rawDaily, err := envelope.requiredField(dataType, shape.jsonField)
	if err != nil {
		return archivedDataPoint{}, err
	}
	var daily struct {
		Date json.RawMessage `json:"date"`
	}
	if err := json.Unmarshal(rawDaily, &daily); err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point %s is not valid JSON", dataType, shape.jsonField)
	}
	providerCivilDate, err := googleDateText(daily.Date)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point date: %w", dataType, err)
	}
	return archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             dataType,
		upstreamResourceName: envelope.upstreamResourceName(),
		recordKind:           "daily",
		providerCivilDate:    providerCivilDate,
		dataSourceJSON:       envelope.dataSourceJSON,
		sourceFamilyFilter:   sourceFamilyFilter,
		rawJSON:              canonicalRaw,
	}, nil
}

func compactJSONString(raw json.RawMessage) (string, error) {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return "", err
	}
	return out.String(), nil
}

func normalizeGoogleTimestamp(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", errors.New("expected RFC3339 timestamp")
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func googleCivilDateTimeText(raw json.RawMessage) (string, string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", nil
	}
	var value struct {
		Date struct {
			Year  int `json:"year"`
			Month int `json:"month"`
			Day   int `json:"day"`
		} `json:"date"`
		Time *struct {
			Hours   int `json:"hours"`
			Minutes int `json:"minutes"`
			Seconds int `json:"seconds"`
			Nanos   int `json:"nanos"`
		} `json:"time"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", "", errors.New("not valid JSON")
	}
	if value.Date.Year == 0 || value.Date.Month == 0 || value.Date.Day == 0 {
		return "", "", errors.New("missing date")
	}
	date := fmt.Sprintf("%04d-%02d-%02d", value.Date.Year, value.Date.Month, value.Date.Day)
	if value.Time == nil {
		return date, date, nil
	}
	text := fmt.Sprintf("%sT%02d:%02d:%02d", date, value.Time.Hours, value.Time.Minutes, value.Time.Seconds)
	if value.Time.Nanos != 0 {
		fraction := fmt.Sprintf("%09d", value.Time.Nanos)
		fraction = strings.TrimRight(fraction, "0")
		text += "." + fraction
	}
	return text, date, nil
}

func googleDateText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("missing date")
	}
	var value struct {
		Year  int `json:"year"`
		Month int `json:"month"`
		Day   int `json:"day"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("not valid JSON")
	}
	if value.Year == 0 || value.Month == 0 || value.Day == 0 {
		return "", errors.New("missing date")
	}
	return fmt.Sprintf("%04d-%02d-%02d", value.Year, value.Month, value.Day), nil
}

func googleIntervalTimezoneMetadataJSON(startUTCOffset, endUTCOffset string) (string, error) {
	metadata := map[string]string{}
	if startUTCOffset != "" {
		metadata["start_utc_offset"] = startUTCOffset
	}
	if endUTCOffset != "" {
		metadata["end_utc_offset"] = endUTCOffset
	}
	if len(metadata) == 0 {
		return "", nil
	}
	content, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func googleSampleTimezoneMetadataJSON(utcOffset string) (string, error) {
	if utcOffset == "" {
		return "", nil
	}
	content, err := json.Marshal(map[string]string{"utc_offset": utcOffset})
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// googleDailyRollupTimeMetadataJSON is Data Type-agnostic: callers
// wrap the returned error with the active Data Type so the message
// reflects the row that failed (steps vs floors vs …). Keeping the
// helper itself generic avoids stringly-typed plumbing of the Data
// Type through one more layer.
func googleDailyRollupTimeMetadataJSON(civilStartTime, civilEndTime json.RawMessage) (string, error) {
	metadata := map[string]json.RawMessage{}
	if len(civilStartTime) != 0 && string(civilStartTime) != "null" {
		start, err := compactJSONString(civilStartTime)
		if err != nil {
			return "", errors.New("daily Rollup civilStartTime is not valid JSON")
		}
		metadata["civil_start_time"] = json.RawMessage(start)
	}
	if len(civilEndTime) != 0 && string(civilEndTime) != "null" {
		end, err := compactJSONString(civilEndTime)
		if err != nil {
			return "", errors.New("daily Rollup civilEndTime is not valid JSON")
		}
		metadata["civil_end_time"] = json.RawMessage(end)
	}
	if len(metadata) == 0 {
		return "", nil
	}
	content, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
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

func ensureOwnerOnlyDir(dir string) error {
	if info, err := os.Stat(dir); err == nil {
		return validateOwnerOnlyDirInfo(dir, info)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if !usesPOSIXPermissions() {
		return nil
	}
	return os.Chmod(dir, 0o700)
}

func validateOwnerOnlyDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	return validateOwnerOnlyDirInfo(dir, info)
}

func validateOwnerOnlyDirInfo(dir string, info os.FileInfo) error {
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o700 {
		mode := info.Mode().Perm()
		return fmt.Errorf("%s is not owner-only: mode %04o, want 0700", dir, mode)
	}
	return nil
}

func usesPOSIXPermissions() bool {
	return runtime.GOOS != "windows"
}

func openArchive(archivePath string) (*sql.DB, error) {
	dsn, err := archiveDSN(archivePath, false)
	if err != nil {
		return nil, err
	}
	return openArchiveDSN(dsn)
}

func openArchiveReadOnly(archivePath string) (*sql.DB, error) {
	dsn, err := archiveDSN(archivePath, true)
	if err != nil {
		return nil, err
	}
	return openArchiveDSN(dsn)
}

func openArchiveDSN(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func archiveDSN(archivePath string, readOnly bool) (string, error) {
	absPath, err := filepath.Abs(archivePath)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(absPath)
	if runtime.GOOS == "windows" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := url.Values{}
	query.Add("_pragma", "foreign_keys=on")
	// busy_timeout makes SQLite block internally for up to 5 seconds
	// when it encounters a lock contention (SQLITE_BUSY) instead of
	// immediately surfacing the error. This is what lets two concurrent
	// sync invocations against the same Health Archive coexist without
	// the second one failing at StartSyncRun — modernc's driver does
	// not implement an automatic global busy_timeout the way mattn's
	// does, so the DSN must opt in explicitly. The finalize-time retry
	// (retryFinalizeSyncRunOnBusy) still guards the terminal-write
	// path for the rare case where the wait elapses.
	query.Add("_pragma", "busy_timeout=5000")
	if readOnly {
		query.Add("mode", "ro")
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}).String(), nil
}

func writeStatusResult(result statusResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeStatusPlain(writer, result)
	} else {
		writeStatusHuman(writer, result)
	}
	return writer.Err()
}

func writeStatusPlain(writer *stickyWriter, result statusResult) {
	writer.Printf("status: %s\n", result.Status)
	if result.ArchivePath != "" {
		writer.Printf("archive_path: %s\n", result.ArchivePath)
	}
	if result.SchemaVersion != 0 {
		writer.Printf("schema_version: %d\n", result.SchemaVersion)
	}
	writeStatusCounts(writer, result)
	if len(result.DataTypes) != 0 {
		// Read the shared KnownDataTypes field instead of
		// re-synthesising from DataTypes — PRD #144 slice 9
		// guarantees both modes carry the same array.
		writer.Printf("known_data_types: %s\n", strings.Join(result.KnownDataTypes, ","))
	}
	for _, dataType := range result.DataTypes {
		writeStatusDataTypePlain(writer, dataType)
	}
	writeStatusSnapshotFreshnessPlain(writer, result.IdentitySnapshotsFreshness)
	writeStatusTier2Plain(writer, result.Tier2)
	writeStatusSyncRunPlain(writer, "latest_successful_sync_run", result.LatestSuccessfulRun)
	writeStatusSyncRunPlain(writer, "latest_failed_sync_run", result.LatestFailedRun)
	writer.Printf("message: %s\n", result.Message)
}

func writeStatusDataTypePlain(writer *stickyWriter, dataType statusDataType) {
	prefix := "data_type." + dataType.DataType + "."
	writer.Printf("%sdata_point_count: %d\n", prefix, dataType.DataPointCount)
	writer.Printf("%srollup_count: %d\n", prefix, dataType.RollupCount)
	if dataType.NewestDataPointTimestamp != "" {
		writer.Printf("%snewest_data_point_timestamp: %s\n", prefix, dataType.NewestDataPointTimestamp)
	}
	if dataType.NewestRollupTimestamp != "" {
		writer.Printf("%snewest_rollup_timestamp: %s\n", prefix, dataType.NewestRollupTimestamp)
	}
	for index, cursor := range dataType.SyncCursors {
		cursorPrefix := fmt.Sprintf("%ssync_cursor.%d.", prefix, index)
		writer.Printf("%srollup_kind: %s\n", cursorPrefix, cursor.RollupKind)
		if cursor.SourceFamilyFilter != "" {
			writer.Printf("%ssource_family_filter: %s\n", cursorPrefix, cursor.SourceFamilyFilter)
		}
		writer.Printf("%scursor_time: %s\n", cursorPrefix, cursor.CursorTime)
		writer.Printf("%sadvanced_at: %s\n", cursorPrefix, cursor.AdvancedAt)
	}
}

func writeStatusSnapshotFreshnessPlain(writer *stickyWriter, freshness *statusSnapshotFreshness) {
	if freshness == nil {
		return
	}
	if freshness.PairedDeviceCount > 0 {
		writer.Printf("paired_device_count: %d\n", freshness.PairedDeviceCount)
	}
	// Emit kinds in a stable order so the output is reproducible.
	for _, kind := range identitySnapshotKinds {
		if ts, ok := freshness.LatestFetchedAt[kind]; ok {
			writer.Printf("identity_snapshot.%s.fetched_at: %s\n", kind, ts)
		}
	}
}

func writeStatusTier2Plain(writer *stickyWriter, tier2 *statusTier2) {
	if tier2 == nil {
		return
	}
	// Plain output omits Tier 2 lines when the scope has not been
	// granted — matches PR #128's omitted-when-missing convention for
	// snapshot kinds. JSON always carries the block so downstream
	// tooling sees a stable shape.
	if tier2.ElectrocardiogramScopeGranted {
		writer.Printf("electrocardiogram_event_count: %d\n", tier2.ElectrocardiogramEventCount)
	}
	if tier2.IrregularRhythmNotificationScopeGranted {
		writer.Printf("irregular_rhythm_notification_count: %d\n", tier2.IrregularRhythmNotificationCount)
	}
}

func writeStatusHuman(writer *stickyWriter, result statusResult) {
	if result.Status == "ok" {
		writer.Println("Health Archive status")
	} else {
		writer.Println("Health Archive status failed")
	}
	if result.ArchivePath != "" {
		writer.Printf("Health Archive: %s\n", result.ArchivePath)
	}
	if result.SchemaVersion != 0 {
		writer.Printf("Schema version: %d\n", result.SchemaVersion)
	}
	writer.Printf("Counts: %d Data Points, %d Rollups, %d Identity Snapshots (%d Profile), %d Sync Runs\n", result.DataPointCount, result.RollupCount, result.IdentitySnapshotCount, result.ProfileSnapshotCount, result.SyncRunCount)
	if len(result.DataTypes) != 0 {
		writer.Printf("Known Data Types: %s\n", strings.Join(statusDataTypeNames(result.DataTypes), ", "))
	}
	for _, dataType := range result.DataTypes {
		writeStatusDataTypeHuman(writer, dataType)
	}
	if result.LatestSuccessfulRun != nil {
		writer.Printf("Latest successful Sync Run: %d (%s to %s)\n", result.LatestSuccessfulRun.ID, result.LatestSuccessfulRun.From, result.LatestSuccessfulRun.To)
	}
	if result.LatestFailedRun != nil {
		writer.Printf("Latest failed Sync Run: %d (%s)\n", result.LatestFailedRun.ID, result.LatestFailedRun.ErrorSummary)
	}
	writer.Printf("Message: %s\n", result.Message)
}

func writeStatusDataTypeHuman(writer *stickyWriter, dataType statusDataType) {
	writer.Printf("- %s: %d Data Points, %d Rollups", dataType.DataType, dataType.DataPointCount, dataType.RollupCount)
	if dataType.NewestDataPointTimestamp != "" {
		writer.Printf(", newest Data Point %s", dataType.NewestDataPointTimestamp)
	}
	if dataType.NewestRollupTimestamp != "" {
		writer.Printf(", newest Rollup %s", dataType.NewestRollupTimestamp)
	}
	for _, cursor := range dataType.SyncCursors {
		label := cursor.RollupKind
		if cursor.SourceFamilyFilter != "" {
			label = label + "/" + cursor.SourceFamilyFilter
		}
		writer.Printf(", Sync Cursor (%s) %s", label, cursor.CursorTime)
	}
	writer.Println()
}

func writeStatusCounts(writer *stickyWriter, result statusResult) {
	for _, item := range []struct {
		key   string
		count int
	}{
		{"data_point_count", result.DataPointCount},
		{"rollup_count", result.RollupCount},
		{"profile_snapshot_count", result.ProfileSnapshotCount},
		{"identity_snapshot_count", result.IdentitySnapshotCount},
		{"sync_run_count", result.SyncRunCount},
	} {
		writer.Printf("%s: %d\n", item.key, item.count)
	}
}

func statusDataTypeNames(dataTypes []statusDataType) []string {
	names := make([]string, 0, len(dataTypes))
	for _, dataType := range dataTypes {
		names = append(names, dataType.DataType)
	}
	return names
}

func writeStatusSyncRunPlain(writer *stickyWriter, prefix string, run *statusSyncRun) {
	if run == nil {
		return
	}
	writer.Printf("%s_id: %d\n", prefix, run.ID)
	writer.Printf("%s_status: %s\n", prefix, run.Status)
	if len(run.DataTypes) != 0 {
		writer.Printf("%s_data_types: %s\n", prefix, strings.Join(run.DataTypes, ","))
	}
	if run.From != "" {
		writer.Printf("%s_from: %s\n", prefix, run.From)
	}
	if run.To != "" {
		writer.Printf("%s_to: %s\n", prefix, run.To)
	}
	if run.EndpointFamily != "" {
		writer.Printf("%s_endpoint_family: %s\n", prefix, run.EndpointFamily)
	}
	if run.SourceFamilyFilter != "" {
		writer.Printf("%s_source_family_filter: %s\n", prefix, escapePlainControlChars(run.SourceFamilyFilter))
	}
	writer.Printf("%s_seen_count: %d\n", prefix, run.SeenCount)
	writer.Printf("%s_new_count: %d\n", prefix, run.NewCount)
	writer.Printf("%s_updated_count: %d\n", prefix, run.UpdatedCount)
	if run.StartedAt != "" {
		writer.Printf("%s_started_at: %s\n", prefix, run.StartedAt)
	}
	if run.FinishedAt != "" {
		writer.Printf("%s_finished_at: %s\n", prefix, run.FinishedAt)
	}
	if run.ErrorSummary != "" {
		writer.Printf("%s_error_summary: %s\n", prefix, escapePlainControlChars(run.ErrorSummary))
	}
}

func writeDoctorResult(result doctorResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeDoctorPlain(writer, result)
	} else {
		writeDoctorHuman(writer, result)
	}
	return writer.Err()
}

func writeDoctorPlain(writer *stickyWriter, result doctorResult) {
	writer.Printf("status: %s\n", result.Status)
	writer.Printf("config_path: %s\n", result.ConfigPath)
	writer.Printf("archive_path: %s\n", result.ArchivePath)
	if result.OAuthClientSource != "" {
		writer.Printf("oauth_client_source: %s\n", result.OAuthClientSource)
	}
	if result.CredentialStore != "" {
		writer.Printf("credential_store: %s\n", result.CredentialStore)
	}
	if result.SchemaVersion != nil {
		writer.Printf("schema_version: %d\n", *result.SchemaVersion)
	}
	if result.ConnectionCount != nil {
		writer.Printf("connection_count: %d\n", *result.ConnectionCount)
	}
	if result.TokenStatus != "" {
		writer.Printf("token_status: %s\n", result.TokenStatus)
	}
	if result.AttachmentRootPath != "" {
		writer.Printf("attachment_root_path: %s\n", result.AttachmentRootPath)
		if result.AttachmentRootMode != "" {
			writer.Printf("attachment_root_mode: %s\n", result.AttachmentRootMode)
		}
	}
	if result.Attachments != nil {
		if n := len(result.Attachments.OrphanFiles); n > 0 {
			writer.Printf("attachments_orphan_files: %d\n", n)
		}
		if n := len(result.Attachments.OrphanRows); n > 0 {
			writer.Printf("attachments_orphan_rows: %d\n", n)
		}
	}
	writer.Printf("message: %s\n", result.Message)
}

func writeDoctorHuman(writer *stickyWriter, result doctorResult) {
	switch result.Status {
	case "ok":
		writer.Println("Setup ok")
	case "connection_unhealthy":
		writer.Println("Connection unhealthy")
	case "setup_invalid":
		writer.Println("Setup invalid")
	default:
		writer.Println("Setup missing")
	}
	writer.Printf("Config: %s\n", result.ConfigPath)
	writer.Printf("Health Archive: %s\n", result.ArchivePath)
	if result.OAuthClientSource != "" {
		writer.Printf("OAuth client source: %s\n", result.OAuthClientSource)
	}
	if result.CredentialStore != "" {
		writer.Printf("Credential Store: %s\n", result.CredentialStore)
	}
	if result.SchemaVersion != nil {
		writer.Printf("Schema version: %d\n", *result.SchemaVersion)
	}
	if result.ConnectionCount != nil {
		writer.Printf("Connections: %d\n", *result.ConnectionCount)
	}
	if result.TokenStatus != "" {
		writer.Printf("Token status: %s\n", result.TokenStatus)
	}
	writer.Printf("Message: %s\n", result.Message)
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

func writeInitResult(result initResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "config_path: %s\n", result.ConfigPath); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "archive_path: %s\n", result.ArchivePath); err != nil {
			return err
		}
		if result.OAuthClientSource != "" {
			if _, err := fmt.Fprintf(stdout, "oauth_client_source: %s\n", result.OAuthClientSource); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "default_data_types: %s\n", strings.Join(result.DefaultDataTypes, ",")); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "schema_version: %d\n", result.SchemaVersion); err != nil {
			return err
		}
		if result.Message != "" {
			if _, err := fmt.Fprintf(stdout, "message: %s\n", result.Message); err != nil {
				return err
			}
		}
		return nil
	}

	if result.Status == "already_initialized" {
		if _, err := fmt.Fprintln(stdout, "Already initialized"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(stdout, "Initialized gohealthcli"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "Config: %s\n", result.ConfigPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "Health Archive: %s\n", result.ArchivePath); err != nil {
		return err
	}
	if result.OAuthClientSource != "" {
		if _, err := fmt.Fprintf(stdout, "OAuth client source: %s\n", result.OAuthClientSource); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "Default Data Types: %s\n", strings.Join(result.DefaultDataTypes, ", ")); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "Schema version: %d\n", result.SchemaVersion)
	return err
}

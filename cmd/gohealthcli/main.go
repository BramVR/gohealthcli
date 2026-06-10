package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const setupMissingExitCode = 2
const currentSchemaVersion = 21
const googleHealthActivityReadonlyScope = "https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly"
const googleHealthHealthMetricsReadonlyScope = "https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly"
const googleHealthSleepReadonlyScope = "https://www.googleapis.com/auth/googlehealth.sleep.readonly"
const googleHealthNutritionReadonlyScope = "https://www.googleapis.com/auth/googlehealth.nutrition.readonly"
const googleHealthProfileReadonlyScope = "https://www.googleapis.com/auth/googlehealth.profile.readonly"

// Tier 2 opt-in scopes (#104, #176). Users grant these via
// `gohealthcli connect --add-scopes ecg,irn,settings`. String
// literals match connectAddScopeKeywords["ecg"/"irn"/"settings"];
// connect_add_scopes.go owns the keyword→scope mapping, this file
// owns the constant the catalog references. `settings.readonly`
// (#176) is what Google's `users.getSettings` and
// `users.pairedDevices.list` actually require — `profile.readonly`
// alone returns HTTP 403 for those.
const googleHealthEcgReadonlyScope = "https://www.googleapis.com/auth/googlehealth.electrocardiogram.readonly"
const googleHealthIrnReadonlyScope = "https://www.googleapis.com/auth/googlehealth.irn.readonly"
const googleHealthSettingsReadonlyScope = "https://www.googleapis.com/auth/googlehealth.settings.readonly"

// Tier 2 optional scope #140: `googlehealth.location.readonly` is the
// scope Google requires (on top of `activity_and_fitness.readonly`) to
// authorise `users.dataTypes.dataPoints.exportExerciseTcx`. Users opt
// in via `gohealthcli connect --add-scopes tcx`; the exercise sync
// then archives TCX route bytes as a `tcx`-kind Attachment per
// ADR-0009. Without it, exercise sync skips the TCX hook cleanly (no
// 403 round-trip) — see attachExerciseTcxIfAvailable.
const googleHealthLocationReadonlyScope = "https://www.googleapis.com/auth/googlehealth.location.readonly"

const googleHealthBaseURL = "https://health.googleapis.com/v4"
const googleHealthIdentityURL = "https://health.googleapis.com/v4/users/me/identity"
const googleHealthProfileURL = "https://health.googleapis.com/v4/users/me/profile"
const googleHealthRawResponseLimit = 10 << 20

type doctorResult struct {
	Status              string                  `json:"status"`
	ConfigPath          string                  `json:"config_path"`
	ArchivePath         string                  `json:"archive_path"`
	OAuthClientSource   string                  `json:"oauth_client_source"`
	CredentialStore     string                  `json:"credential_store"`
	SchemaVersion       *int                    `json:"schema_version"`
	ConnectionCount     *int                    `json:"connection_count"`
	TokenStatus         string                  `json:"token_status"`
	AttachmentRootPath  string                  `json:"attachment_root_path,omitempty"`
	AttachmentRootMode  string                  `json:"attachment_root_mode,omitempty"`
	Attachments         *doctorAttachmentReport `json:"attachments,omitempty"`
	Message             string                  `json:"message"`
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
	// cancelCh, when closed, asks the Sync Run to stop cleanly between
	// pagination pages. Closed by the orchestrator's SIGINT handler. nil
	// disables cancellation (legacy single-type entry point).
	cancelCh <-chan struct{}
}

type oauthClientSource struct {
	kind     string
	path     string
	provider string
	item     string
}

type credentialStoreConfig struct {
	kind    string
	service string
	path    string
}

type parsedConfig struct {
	archivePath         string
	defaultDataTypes    []string
	oauthClient         oauthClientSource
	credentialStore     credentialStoreConfig
	credentialStoreSeen bool
}

type configCheck struct {
	oauthClientSource string
	credentialStore   string
}

type fullConfigCheck struct {
	archivePath      string
	defaultDataTypes []string
	oauthClient      oauthClientSource
	credentialStore  credentialStoreConfig
}

type archiveCheck struct {
	schemaVersion   int
	connectionCount int
	tokenStatus     string
}

type oauthClientConfig struct {
	kind         string
	clientID     string
	clientSecret string
	authURI      string
	tokenURI     string
	redirectURIs []string
}

type oauthTokenResponse struct {
	accessToken            string
	refreshToken           string
	tokenType              string
	scopes                 []string
	expiresAt              time.Time
	refreshTokenExpiresAt  *time.Time
	rawTokenMaterialObject map[string]any
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

var runOAuthFlow = runBrowserOAuthFlow
var refreshOAuthToken = refreshGoogleOAuthToken
var fetchIdentity = fetchGoogleIdentity
var fetchProfile = fetchGoogleProfile
var fetchRawProvider = fetchGoogleHealthRaw
var currentTime = func() time.Time { return time.Now().UTC() }
var currentOS = runtime.GOOS
var runSecurityAddGenericPassword = runSecurityAddGenericPasswordCommand
var runSecurityFindGenericPassword = runSecurityFindGenericPasswordCommand
var runSecretToolStore = runSecretToolStoreCommand
var runSecretToolLookup = runSecretToolLookupCommand
var runWindowsCredentialWrite = runWindowsCredentialWriteCommand
var runWindowsCredentialRead = runWindowsCredentialReadCommand
var findExecutable = exec.LookPath

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

	configPath := flags.String("config", defaultConfigPath(), "config file path")
	archivePath := flags.String("db", defaultArchivePath(), "SQLite Health Archive path")
	jsonOutput := flags.Bool("json", false, "write stable JSON to stdout")
	plainOutput := flags.Bool("plain", false, "write plain key/value output to stdout")
	noInput := flags.Bool("no-input", false, "never prompt, never wait for browser input")
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
	if cmd.Run == nil {
		// Defensive guard: TestEveryCommandHasRunAdapter pins the invariant
		// that every entry's Run is wired, but if a future change slips a
		// registry entry through without an adapter we exit cleanly with a
		// targeted stderr message instead of panicking on the nil call.
		// The error wording names the offending command so an operator can
		// flag the bug without inspecting a stack trace.
		fmt.Fprintf(stderr, "internal error: command %q has no Run adapter\n", cmd.Name)
		return 1
	}
	return cmd.Run(flags.Args()[1:], common, stdout, stderr, runtime)
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

func runDoctor(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	return runDoctorWithRuntime(args, configPath, archivePath, mode, stdout, stderr, productionRuntimeAdapters())
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

	if err := ParseCommon(flags, common, args); err != nil {
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
		archive, err := (healthArchiveLifecycle{path: common.ArchivePath}).MigrateAndInspect(true)
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
		attachments, attachmentsErr := collectAttachmentOrphans(common.ArchivePath)
		if attachmentsErr != nil {
			return runDoctorInvalid(common.ConfigPath, common.ArchivePath, attachmentsErr.Error(), mode, stdout, stderr)
		}
		result.Attachments = attachments
		if err := writeDoctorResult(result, mode, stdout); err != nil {
			return ReportFailure(FailureReport{
				Command: "doctor",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", err),
				Mode:    mode,
			}, stdout, stderr)
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
		return ReportFailure(FailureReport{
			Command: "doctor",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
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
		return ReportFailure(FailureReport{
			Command: "doctor",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 1
}

func runDoctorOnline(configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	return runDoctorOnlineWithRuntime(configPath, archivePath, mode, stdout, stderr, productionRuntimeAdapters())
}

func runDoctorOnlineWithRuntime(configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	result, err := doctorOnlineSetupWithRuntime(configPath, archivePath, runtime)
	if err != nil {
		if result.Status == "" || result.Status == "ok" {
			result.Status = "connection_unhealthy"
		}
		result.Message = err.Error()
		if writeErr := writeDoctorResult(result, mode, stdout); writeErr != nil {
			return ReportFailure(FailureReport{
				Command: "doctor",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	if err := writeDoctorResult(result, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "doctor",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func runStatus(args []string, configPath, archivePath string, configPathExplicit, archivePathExplicit bool, mode outputMode, stdout, stderr io.Writer) int {
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

	if err := ParseCommon(flags, common, args); err != nil {
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
			return ReportFailure(FailureReport{
				Command: "status",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	result, err := statusSetup(resolvedArchivePath)
	if err != nil {
		if result.Status == "" {
			result.Status = "status_failed"
		}
		result.Message = err.Error()
		if writeErr := writeStatusResult(result, mode, stdout); writeErr != nil {
			return ReportFailure(FailureReport{
				Command: "status",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	if err := writeStatusResult(result, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "status",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func runInit(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
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

	if err := ParseCommon(flags, common, args); err != nil {
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
		if err := lifecycle.Migrate(); err != nil {
			return ReportFailure(FailureReport{
				Command: "init",
				Status:  StatusOperationFailed,
				Message: fmt.Sprintf("existing Health Archive is not initialized: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		if _, err := lifecycle.Inspect(false); err != nil {
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
			return ReportFailure(FailureReport{
				Command: "init",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", err),
				Mode:    mode,
			}, stdout, stderr)
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
		return ReportFailure(FailureReport{
			Command: "init",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func runConnect(args []string, configPath, archivePath string, globalNoInput bool, mode outputMode, stdout, stderr io.Writer) int {
	return runConnectWithRuntime(args, configPath, archivePath, globalNoInput, mode, stdout, stderr, productionRuntimeAdapters())
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
	connectAddScopes := flags.String("add-scopes", "", "extend the OAuth grant with optional scope keywords (csv): irn, ecg, nutrition, tcx, settings")

	if err := ParseCommon(flags, common, args); err != nil {
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
			return ReportFailure(FailureReport{
				Command: "connect",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	if err := writeConnectResult(result, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "connect",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func runIdentity(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	return runIdentityWithRuntime(args, configPath, archivePath, mode, stdout, stderr, productionRuntimeAdapters())
}

func runIdentityWithRuntime(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("identity", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		JSONOutput:  mode.json,
		PlainOutput: mode.plain,
	})

	if err := ParseCommon(flags, common, args); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "identity",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected identity argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	result, err := identitySetupWithRuntime(common.ConfigPath, common.ArchivePath, runtime)
	if err != nil {
		if result.Status == "" {
			result.Status = "identity_failed"
		}
		result.Message = err.Error()
		if writeErr := writeIdentityResult(result, mode, stdout); writeErr != nil {
			return ReportFailure(FailureReport{
				Command: "identity",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	if err := writeIdentityResult(result, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "identity",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func runProfile(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	return runProfileWithRuntime(args, configPath, archivePath, mode, stdout, stderr, productionRuntimeAdapters())
}

func runProfileWithRuntime(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("profile", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		JSONOutput:  mode.json,
		PlainOutput: mode.plain,
	})

	if err := ParseCommon(flags, common, args); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "profile",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected profile argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	result, err := profileSetupWithRuntime(common.ConfigPath, common.ArchivePath, runtime)
	if err != nil {
		if result.Status == "" {
			result.Status = "profile_failed"
		}
		result.Message = err.Error()
		if writeErr := writeProfileResult(result, mode, stdout); writeErr != nil {
			return ReportFailure(FailureReport{
				Command: "profile",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	if err := writeProfileResult(result, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "profile",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func runSync(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	return runSyncWithRuntime(args, configPath, archivePath, mode, stdout, stderr, productionRuntimeAdapters())
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
	syncFrom := flags.String("from", "", "inclusive sync range start")
	syncTo := flags.String("to", "", "exclusive sync range end")
	syncRollup := flags.String("rollup", "", "rollup kind to sync; supported: daily")
	syncSourceFamily := flags.String("source-family", "", "source family filter; supported: wearable")

	if err := ParseCommon(flags, common, args); err != nil {
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
	cancelCh, stopSignalHandler := installSyncCancelChannel()
	defer stopSignalHandler()
	options.cancelCh = cancelCh

	orchestrator := newSyncOrchestrator(runtime, cancelCh)
	results, err := orchestrator.Sync(options)
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
			return ReportFailure(FailureReport{
				Command: "sync",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
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
			return ReportFailure(FailureReport{
				Command: "sync",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		if single.Status != "sync_completed" {
			return 1
		}
		return 0
	}
	if writeErr := writeSyncFanOutResult(results, options, mode, stdout); writeErr != nil {
		return ReportFailure(FailureReport{
			Command: "sync",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", writeErr),
			Mode:    mode,
		}, stdout, stderr)
	}
	// fanOutStatus folds the same empty-results case to sync_canceled
	// (see fanOutStatus), so an empty fan-out should also exit non-zero
	// rather than the silent exit-0 the per-result loop would produce.
	if fanOutStatus(results) != "sync_completed" {
		return 1
	}
	return 0
}

// installSyncCancelChannel wires a SIGINT handler whose cancellation is
// delivered via the returned read-only channel. The channel closes when
// SIGINT fires or when the returned stop function is called (whichever
// is first); the stop function is idempotent and races cleanly with an
// in-flight signal. Tests construct the channel directly and pass nil
// as the runtime stop.
func installSyncCancelChannel() (<-chan struct{}, func()) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	return ctx.Done(), stop
}

func runRaw(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	return runRawWithRuntime(args, configPath, archivePath, mode, stdout, stderr, productionRuntimeAdapters())
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
	common := RegisterCommon(flags, CommonFlagSpec{Accepted: []string{"config", "db"}}, CommonFlagValues{
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
	if err := ParseCommon(flags, common, reordered); err != nil {
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
		return ReportFailure(FailureReport{Command: "raw", Status: StatusOperationFailed, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	if _, err := stdout.Write(body); err != nil {
		return ReportFailure(FailureReport{
			Command: "raw",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
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

func connectSetup(configPath, archivePath string, noInput bool) (connectResult, error) {
	return connectSetupWithRuntime(configPath, archivePath, noInput, productionRuntimeAdapters())
}

func connectSetupWithRuntime(configPath, archivePath string, noInput bool, runtime runtimeAdapters) (connectResult, error) {
	return connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, noInput, nil, runtime)
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
	if _, err := (healthArchiveLifecycle{path: archivePath}).MigrateAndInspect(false); err != nil {
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
	if err := archive.EnsureSameGoogleIdentity(identity.healthUserID); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := store.Store(connectionID, token.rawTokenMaterialObject); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := archive.UpsertConnection(connectionID, identity, token, runtime.now()); err != nil {
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

func identitySetup(configPath, archivePath string) (identityResult, error) {
	return identitySetupWithRuntime(configPath, archivePath, productionRuntimeAdapters())
}

func identitySetupWithRuntime(configPath, archivePath string, runtime runtimeAdapters) (identityResult, error) {
	runtime = runtime.withDefaults()
	config, err := inspectIdentityConfig(configPath, archivePath)
	if err != nil {
		return identityResult{}, fmt.Errorf("config check failed: %w", err)
	}
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return identityResult{}, err
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identityResult{Status: "identity_unavailable"}, errors.New("no Connection found; run `gohealthcli connect` first")
		}
		return identityResult{}, err
	}
	result := identityResult{
		ConnectionID:       connection.id,
		ProviderName:       connection.providerName,
		GoogleHealthUserID: connection.googleHealthUserID,
		LegacyFitbitUserID: connection.legacyFitbitUserID,
	}
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{configPath, archivePath}, runtime)
	accessToken, err := connectionAccess.AccessToken(nil)
	if err != nil {
		return result, err
	}
	identity, err := connectionAccess.FetchVerifiedIdentity(accessToken)
	if err != nil {
		if isCurrentConnectionIdentityMismatch(err) {
			result.Status = "identity_mismatch"
		}
		return result, err
	}
	if err := archive.RefreshConnectionIdentity(connection, identity, runtime.now()); err != nil {
		return result, err
	}
	result.Status = "identity_refreshed"
	result.GoogleHealthUserID = identity.healthUserID
	if identity.legacyFitbitUserID != "" {
		result.LegacyFitbitUserID = identity.legacyFitbitUserID
	}
	result.Message = "Google Identity refreshed"
	return result, nil
}

func profileSetup(configPath, archivePath string) (profileResult, error) {
	return profileSetupWithRuntime(configPath, archivePath, productionRuntimeAdapters())
}

func profileSetupWithRuntime(configPath, archivePath string, runtime runtimeAdapters) (profileResult, error) {
	runtime = runtime.withDefaults()
	config, err := inspectIdentityConfig(configPath, archivePath)
	if err != nil {
		return profileResult{}, fmt.Errorf("config check failed: %w", err)
	}
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return profileResult{}, err
	}
	// archive is closed either by writeIdentitySnapshotHandoff (success
	// path) or by this deferred guard (any error before handoff).
	archiveClosed := false
	defer func() {
		if !archiveClosed {
			_ = archive.Close()
		}
	}()
	connection, err := archive.CurrentConnection()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return profileResult{Status: "profile_unavailable"}, errors.New("no Connection found; run `gohealthcli connect` first")
		}
		return profileResult{}, err
	}
	result := profileResult{
		ConnectionID:       connection.id,
		ProviderName:       connection.providerName,
		GoogleHealthUserID: connection.googleHealthUserID,
		LegacyFitbitUserID: connection.legacyFitbitUserID,
	}
	// The deepened currentConnectionAccess pattern (PRD #142): wire
	// WithAutoRefresh when the OAuth client is a file source — the
	// archive handle openHealthArchiveConnectionAPI returned already
	// satisfies connectionTokenWriter — so an expired access token
	// refreshes and persists transparently, the way
	// sync_run_lifecycle.go already does. The required scope comes
	// from googleHealthIdentityEndpointScopes["getProfile"] so a
	// slice-2 revision of the catalog (PRD #142 #176) flows into
	// profile automatically. The scope pre-check happens inside
	// AccessToken via the errCurrentConnectionScopeMissing sentinel,
	// so we set the per-command status without re-implementing the
	// scope-list comparison locally.
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{configPath, archivePath}, runtime)
	if config.oauthClient.kind == "file" {
		connectionAccess = connectionAccess.WithAutoRefresh(config.oauthClient, archive)
	}
	accessToken, err := connectionAccess.AccessToken(googleHealthIdentityEndpointScopes["getProfile"])
	if err != nil {
		if errors.Is(err, errCurrentConnectionScopeMissing) {
			result.Status = "profile_scope_missing"
		}
		return result, err
	}
	profile, err := runtime.fetchProfile(accessToken)
	if err != nil {
		return result, currentConnectionProviderError(err)
	}
	profileHealthUserID := profile.healthUserID
	if profileHealthUserID == "" {
		identity, err := connectionAccess.FetchVerifiedIdentity(accessToken)
		if err != nil {
			if isCurrentConnectionIdentityMismatch(err) {
				result.Status = "profile_mismatch"
			}
			return result, err
		}
		profileHealthUserID = identity.healthUserID
	}
	if err := connectionAccess.RequireMatchingHealthUserID(profileHealthUserID); err != nil {
		result.Status = "profile_mismatch"
		return result, err
	}
	fetchedAt := runtime.now().UTC().Format(time.RFC3339)
	snapshotID, err := writeIdentitySnapshotHandoff(archive, archivePath, connection, "profile", profile.rawJSON, fetchedAt)
	archiveClosed = true // handoff owns archive's lifecycle now
	if err != nil {
		return result, err
	}
	result.Status = "profile_archived"
	result.SnapshotID = snapshotID
	result.FetchedAt = fetchedAt
	result.Message = "Profile Snapshot archived"
	return result, nil
}

// resolveConfiguredArchivePath was the legacy read+write resolver. PRD
// #144 slice 1 (issue #155) moved the four read commands to
// readArchivePathResolver — which owns the read-side relaxation
// (`--db` alone wins over a missing or default config) plus the
// user-facing error wording (names `--db` and `--config`, not the
// internal `archive_path` field). Write commands keep the stricter
// `archive_path` agreement via inspectFullConfig / inspectIdentityConfig
// (this file) so the no-callers `resolveConfiguredArchivePath` symbol
// was deleted; consult `readArchivePathResolver` (read commands) or the
// inspect* helpers (write commands) when reasoning about either side.

func readConfigArchivePath(configPath string) (string, bool, error) {
	info, err := os.Stat(configPath)
	if errors.Is(err, os.ErrNotExist) && configPath == defaultConfigPath() {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if err := validateOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return "", false, err
	}
	if info.IsDir() {
		return "", false, fmt.Errorf("%s is a directory", configPath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		mode := info.Mode().Perm()
		return "", false, fmt.Errorf("%s is not owner-only: mode %04o, want 0600", configPath, mode)
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return "", false, err
	}
	config, err := parseConfig(string(configBytes))
	if err != nil {
		return "", false, err
	}
	if config.archivePath == "" {
		return "", false, errors.New("missing archive_path")
	}
	return config.archivePath, true, nil
}

func doctorOnlineSetup(configPath, archivePath string) (doctorResult, error) {
	return doctorOnlineSetupWithRuntime(configPath, archivePath, productionRuntimeAdapters())
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
	archive, err := (healthArchiveLifecycle{path: archivePath}).MigrateAndInspect(true)
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
	attachments, attachmentsErr := collectAttachmentOrphans(archivePath)
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

func persistDoctorOnlineRefreshedToken(archive connectionTokenWriter, credentialStore credentialStoreConfig, connectionID string, token oauthTokenResponse, previousTokenMaterial map[string]any) error {
	return persistDoctorOnlineRefreshedTokenWithRuntime(archive, credentialStore, connectionID, token, previousTokenMaterial, productionRuntimeAdapters())
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
			return fmt.Errorf("%w; rollback Credential Store token material: %v", err, rollbackErr)
		}
		return err
	}
	return nil
}

func rawSetup(configPath, archivePath string, request rawProviderRequest) ([]byte, error) {
	return rawSetupWithRuntime(configPath, archivePath, request, productionRuntimeAdapters())
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
	body, err := runtime.fetchRawProvider(request, accessToken)
	if err != nil {
		return nil, currentConnectionProviderError(err)
	}
	return body, nil
}

func requireConnectionScopes(metadata string, requiredScopes []string) error {
	if len(requiredScopes) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		return errors.New("Connection token metadata is not valid JSON; run `gohealthcli connect` again")
	}
	value, ok := raw["scopes"]
	if !ok {
		return errors.New("Connection token metadata is missing scopes; run `gohealthcli connect` again")
	}
	var grantedScopes []string
	if err := json.Unmarshal(value, &grantedScopes); err != nil {
		return errors.New("Connection token metadata scopes are invalid; run `gohealthcli connect` again")
	}
	granted := make(map[string]struct{}, len(grantedScopes))
	for _, scope := range grantedScopes {
		granted[scope] = struct{}{}
	}
	// Collect every required scope that is not granted so the hint
	// path below can decide whether all missing scopes are
	// `--add-scopes` keywords (and therefore worth combining into a
	// single `ecg,irn`-style recovery hint). The error message itself
	// still names the first missing scope; the keyword join is what
	// changes between single-scope and multi-scope misses.
	var missing []string
	for _, requiredScope := range requiredScopes {
		if _, ok := granted[requiredScope]; !ok {
			missing = append(missing, requiredScope)
		}
	}
	if len(missing) > 0 {
		// Wrap the typed sentinel so callers can switch on
		// errors.Is(err, errCurrentConnectionScopeMissing) to set
		// per-command "<command>_scope_missing" status without
		// duplicating this pre-check. The user-facing message keeps
		// naming the precise `--add-scopes <keyword>` recovery (or the
		// generic `connect` fallback for non-keyword scopes) — only the
		// error type changes.
		if keywords := addScopeKeywordsForScopes(missing); len(keywords) == len(missing) {
			// Every missing scope is an opt-in Tier 2 scope — point
			// the user at the lightweight `connect --add-scopes` flow
			// rather than re-running the full base-set connect.
			return fmt.Errorf("%w %s; run `gohealthcli connect --add-scopes %s`", errCurrentConnectionScopeMissing, missing[0], strings.Join(keywords, ","))
		}
		return fmt.Errorf("%w %s; run `gohealthcli connect` again", errCurrentConnectionScopeMissing, missing[0])
	}
	return nil
}

func parseOAuthClientSource(oauthClientFile, secretProvider, oauthClientItem string) (oauthClientSource, error) {
	if oauthClientFile != "" {
		if secretProvider != "" || oauthClientItem != "" {
			return oauthClientSource{}, errors.New("use either --oauth-client-file or --secret-provider with --oauth-client-item")
		}
		absPath, err := filepath.Abs(oauthClientFile)
		if err != nil {
			return oauthClientSource{}, errors.New("resolve OAuth client file path")
		}
		return oauthClientSource{kind: "file", path: absPath}, nil
	}
	if secretProvider != "" || oauthClientItem != "" {
		// Name the flag the user actually provided first, then the
		// missing one, so the error reads in the right dependency
		// direction (issue #150).
		if secretProvider == "" {
			return oauthClientSource{}, errors.New("--oauth-client-item requires --secret-provider")
		}
		if oauthClientItem == "" {
			return oauthClientSource{}, errors.New("--secret-provider requires --oauth-client-item")
		}
		return oauthClientSource{kind: "secret_provider", provider: secretProvider, item: oauthClientItem}, nil
	}
	return oauthClientSource{}, errors.New("requires --oauth-client-file or --secret-provider with --oauth-client-item")
}

func createConfigFile(configPath, archivePath string, source oauthClientSource) (err error) {
	if err := ensureOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return err
	}

	file, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(configPath)
		}
	}()

	if _, err := fmt.Fprint(file, configContent(configPath, archivePath, source)); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if !usesPOSIXPermissions() {
		return nil
	}
	return os.Chmod(configPath, 0o600)
}

func configContent(configPath, archivePath string, source oauthClientSource) string {
	var builder strings.Builder
	builder.WriteString("# gohealthcli config\n\n")
	builder.WriteString("archive_path = ")
	builder.WriteString(strconv.Quote(archivePath))
	builder.WriteString("\n")
	builder.WriteString("default_data_types = [\n")
	for _, dataType := range defaultDataTypes {
		builder.WriteString("  ")
		builder.WriteString(strconv.Quote(dataType))
		builder.WriteString(",\n")
	}
	builder.WriteString("]\n\n")
	builder.WriteString("[oauth_client]\n")
	builder.WriteString("source = ")
	builder.WriteString(strconv.Quote(source.kind))
	builder.WriteString("\n")
	switch source.kind {
	case "file":
		builder.WriteString("path = ")
		builder.WriteString(strconv.Quote(source.path))
		builder.WriteString("\n")
	case "secret_provider":
		builder.WriteString("provider = ")
		builder.WriteString(strconv.Quote(source.provider))
		builder.WriteString("\nitem = ")
		builder.WriteString(strconv.Quote(source.item))
		builder.WriteString("\n")
	}
	store := defaultCredentialStoreConfig()
	builder.WriteString("\n[credential_store]\n")
	builder.WriteString("type = ")
	builder.WriteString(strconv.Quote(store.kind))
	switch store.kind {
	case "os_native":
		builder.WriteString("\nservice = ")
		builder.WriteString(strconv.Quote(store.service))
	case "file":
		builder.WriteString("\npath = ")
		builder.WriteString(strconv.Quote(store.path))
	}
	builder.WriteString("\n")
	return builder.String()
}

func createArchive(archivePath string) (err error) {
	if err := (healthArchiveLifecycle{path: archivePath}).Create(); err != nil {
		return err
	}
	// Pre-create the attachment root so users running init see the
	// full archive shape without waiting for a sync to lazily create
	// it. Owner-only mode follows the rest of the archive.
	return ensureOwnerOnlyDir(attachmentRootDirForArchive(archivePath))
}

func validateConfig(configPath, archivePath string) error {
	_, err := inspectConfig(configPath, archivePath)
	return err
}

func inspectConfig(configPath, archivePath string) (configCheck, error) {
	config, err := inspectFullConfig(configPath, archivePath)
	if err != nil {
		return configCheck{}, err
	}
	return configCheck{
		oauthClientSource: config.oauthClient.kind,
		credentialStore:   config.credentialStore.kind,
	}, nil
}

func inspectFullConfig(configPath, archivePath string) (fullConfigCheck, error) {
	if err := validateOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return fullConfigCheck{}, err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return fullConfigCheck{}, err
	}
	if info.IsDir() {
		return fullConfigCheck{}, fmt.Errorf("%s is a directory", configPath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		mode := info.Mode().Perm()
		return fullConfigCheck{}, fmt.Errorf("%s is not owner-only: mode %04o, want 0600", configPath, mode)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return fullConfigCheck{}, err
	}
	config, err := parseConfig(string(configBytes))
	if err != nil {
		return fullConfigCheck{}, err
	}
	if config.archivePath == "" {
		return fullConfigCheck{}, errors.New("missing archive_path")
	}
	if config.archivePath != archivePath {
		return fullConfigCheck{}, fmt.Errorf("archive_path points to %s, want %s", config.archivePath, archivePath)
	}
	if err := validateDefaultDataTypes(config.defaultDataTypes); err != nil {
		return fullConfigCheck{}, err
	}
	if err := validateOAuthClientConfig(config.oauthClient); err != nil {
		return fullConfigCheck{}, err
	}
	if !config.credentialStoreSeen && config.credentialStore.kind == "" {
		config.credentialStore = defaultCredentialStoreConfig()
	}
	if err := validateCredentialStoreConfig(config.credentialStore); err != nil {
		return fullConfigCheck{}, err
	}
	return fullConfigCheck{
		archivePath:      config.archivePath,
		defaultDataTypes: config.defaultDataTypes,
		oauthClient:      config.oauthClient,
		credentialStore:  config.credentialStore,
	}, nil
}

func inspectIdentityConfig(configPath, archivePath string) (fullConfigCheck, error) {
	if err := validateOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return fullConfigCheck{}, err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return fullConfigCheck{}, err
	}
	if info.IsDir() {
		return fullConfigCheck{}, fmt.Errorf("%s is a directory", configPath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		mode := info.Mode().Perm()
		return fullConfigCheck{}, fmt.Errorf("%s is not owner-only: mode %04o, want 0600", configPath, mode)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return fullConfigCheck{}, err
	}
	config, err := parseConfig(string(configBytes))
	if err != nil {
		return fullConfigCheck{}, err
	}
	if config.archivePath == "" {
		return fullConfigCheck{}, errors.New("missing archive_path")
	}
	if config.archivePath != archivePath {
		return fullConfigCheck{}, fmt.Errorf("archive_path points to %s, want %s", config.archivePath, archivePath)
	}
	if err := validateDefaultDataTypes(config.defaultDataTypes); err != nil {
		return fullConfigCheck{}, err
	}
	if !config.credentialStoreSeen && config.credentialStore.kind == "" {
		config.credentialStore = defaultCredentialStoreConfig()
	}
	if err := validateCredentialStoreConfig(config.credentialStore); err != nil {
		return fullConfigCheck{}, err
	}
	return fullConfigCheck{
		archivePath:      config.archivePath,
		defaultDataTypes: config.defaultDataTypes,
		// oauthClient is returned unvalidated so the sync auto-refresh
		// path can reach it without forcing every identity-only command
		// to validate the OAuth client file. validateOAuthClientFile is
		// still triggered inside loadOAuthClientConfig when a refresh
		// actually runs, so an invalid file fails the refresh, not the
		// happy-path read.
		oauthClient:     config.oauthClient,
		credentialStore: config.credentialStore,
	}, nil
}

func parseConfig(content string) (parsedConfig, error) {
	var config parsedConfig
	section := ""
	lines := strings.Split(content, "\n")
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(stripInlineComment(lines[index]))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			if section == "credential_store" {
				config.credentialStoreSeen = true
			}
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return parsedConfig{}, fmt.Errorf("malformed config line %d", index+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if section == "" && key == "default_data_types" {
			dataTypes, nextIndex, err := parseStringArray(lines, index, value)
			if err != nil {
				return parsedConfig{}, err
			}
			config.defaultDataTypes = dataTypes
			index = nextIndex
			continue
		}

		parsedValue, err := parseQuotedValue(value, key)
		if err != nil {
			return parsedConfig{}, err
		}
		switch section {
		case "":
			if key == "archive_path" {
				config.archivePath = parsedValue
			}
		case "oauth_client":
			switch key {
			case "source":
				config.oauthClient.kind = parsedValue
			case "path":
				config.oauthClient.path = parsedValue
			case "provider":
				config.oauthClient.provider = parsedValue
			case "item":
				config.oauthClient.item = parsedValue
			}
		case "credential_store":
			switch key {
			case "type":
				config.credentialStore.kind = parsedValue
			case "service":
				config.credentialStore.service = parsedValue
			case "path":
				config.credentialStore.path = parsedValue
			}
		}
	}
	return config, nil
}

func parseStringArray(lines []string, startIndex int, firstValue string) ([]string, int, error) {
	if strings.HasPrefix(firstValue, "[") && firstValue != "[" {
		if strings.HasSuffix(firstValue, "]") {
			values, err := parseInlineStringArray(firstValue)
			if err != nil {
				return nil, startIndex, err
			}
			return values, startIndex, nil
		}
		firstLine := strings.TrimSpace(strings.TrimPrefix(firstValue, "["))
		values, err := parseStringArrayItems(firstLine)
		if err != nil {
			return nil, startIndex, err
		}
		return parseStringArrayContinuation(lines, startIndex, values)
	}
	if firstValue != "[" {
		return nil, startIndex, errors.New("default_data_types must be a string array")
	}
	return parseStringArrayContinuation(lines, startIndex, nil)
}

func parseStringArrayContinuation(lines []string, startIndex int, values []string) ([]string, int, error) {
	for index := startIndex + 1; index < len(lines); index++ {
		line := strings.TrimSpace(stripInlineComment(lines[index]))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "]" {
			return values, index, nil
		}
		closeArray := strings.HasSuffix(line, "]")
		if closeArray {
			line = strings.TrimSpace(strings.TrimSuffix(line, "]"))
		}
		lineValues, err := parseStringArrayItems(line)
		if err != nil {
			return nil, startIndex, err
		}
		values = append(values, lineValues...)
		if closeArray {
			return values, index, nil
		}
	}
	return nil, startIndex, errors.New("default_data_types array is not closed")
}

func parseInlineStringArray(value string) ([]string, error) {
	if !strings.HasSuffix(value, "]") {
		return nil, errors.New("default_data_types array is not closed")
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
	if inner == "" {
		return []string{}, nil
	}
	return parseStringArrayItems(inner)
}

func parseStringArrayItems(value string) ([]string, error) {
	var values []string
	start := 0
	inString := false
	escaped := false
	for index, char := range value {
		switch {
		case escaped:
			escaped = false
		case char == '\\' && inString:
			escaped = true
		case char == '"':
			inString = !inString
		case char == ',' && !inString:
			parsedValue, err := parseInlineStringArrayValue(value[start:index])
			if err != nil {
				return nil, err
			}
			values = append(values, parsedValue)
			start = index + 1
		}
	}
	tail := strings.TrimSpace(value[start:])
	if tail == "" {
		return values, nil
	}
	parsedValue, err := parseInlineStringArrayValue(tail)
	if err != nil {
		return nil, err
	}
	values = append(values, parsedValue)
	return values, nil
}

func parseInlineStringArrayValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("default_data_types array contains an empty value")
	}
	parsed, err := parseQuotedValue(value, "default_data_types")
	if err != nil {
		return "", err
	}
	return parsed, nil
}

func stripInlineComment(line string) string {
	inString := false
	escaped := false
	for index, char := range line {
		switch {
		case escaped:
			escaped = false
		case char == '\\' && inString:
			escaped = true
		case char == '"':
			inString = !inString
		case char == '#' && !inString:
			return line[:index]
		}
	}
	return line
}

func parseQuotedValue(value, key string) (string, error) {
	parsed, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("%s must be a quoted string", key)
	}
	return parsed, nil
}

func validateOAuthClientConfig(source oauthClientSource) error {
	switch source.kind {
	case "file":
		if source.path == "" {
			return errors.New("missing OAuth client file path")
		}
		if err := validateOAuthClientFile(source.path); err != nil {
			return err
		}
	case "secret_provider":
		if source.provider == "" || source.item == "" {
			return errors.New("missing Secret Provider reference")
		}
	case "":
		return errors.New("missing OAuth client source")
	default:
		return errors.New("unsupported OAuth client source")
	}
	return nil
}

func validateOAuthClientFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("OAuth client file is missing")
		}
		return errors.New("OAuth client file cannot be checked")
	}
	if info.IsDir() {
		return errors.New("OAuth client file path is a directory")
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		return fmt.Errorf("OAuth client file is not owner-only: mode %04o, want 0600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return errors.New("OAuth client file cannot be read")
	}
	if _, err := parseOAuthClientConfigContent(content); err != nil {
		return err
	}
	return nil
}

func defaultCredentialStoreConfig() credentialStoreConfig {
	return credentialStoreConfig{kind: "os_native", service: "gohealthcli"}
}

func validateCredentialStoreConfig(store credentialStoreConfig) error {
	switch store.kind {
	case "os_native":
		if store.service == "" {
			return errors.New("missing Credential Store service name")
		}
	case "file":
		if store.path == "" {
			return errors.New("missing Credential Store file path")
		}
		parent := filepath.Dir(store.path)
		if _, err := os.Stat(parent); err == nil {
			if err := validateOwnerOnlyDir(parent); err != nil {
				return fmt.Errorf("Credential Store file parent: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if info, err := os.Stat(store.path); err == nil {
			if info.IsDir() {
				return fmt.Errorf("%s is a directory", store.path)
			}
			if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
				return fmt.Errorf("%s is not owner-only: mode %04o, want 0600", store.path, info.Mode().Perm())
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	case "":
		return errors.New("missing Credential Store configuration")
	default:
		return errors.New("unsupported Credential Store type")
	}
	return nil
}

func validateCredentialStoreRuntime(store credentialStoreConfig, protectedPaths []string) error {
	return validateCredentialStoreRuntimeWithRuntime(store, protectedPaths, productionRuntimeAdapters())
}

func validateCredentialStoreRuntimeWithRuntime(store credentialStoreConfig, protectedPaths []string, runtime runtimeAdapters) error {
	runtime = runtime.withDefaults()
	switch store.kind {
	case "file":
		storePath, err := canonicalCredentialPath(store.path)
		if err != nil {
			return err
		}
		for _, protectedPath := range protectedPaths {
			if protectedPath == "" {
				continue
			}
			checkedPath, err := canonicalCredentialPath(protectedPath)
			if err != nil {
				return err
			}
			if storePath == checkedPath {
				return errors.New("Credential Store file path must not match config, archive, or OAuth client files")
			}
		}
	case "os_native":
		switch runtime.currentOS {
		case "darwin":
			if _, err := runtime.findExecutable("security"); err != nil {
				return errors.New("OS-native Credential Store requires the security command; configure credential_store type \"file\"")
			}
		case "linux":
			if _, err := runtime.findExecutable("secret-tool"); err != nil {
				return errors.New("OS-native Credential Store requires secret-tool; install libsecret tooling or configure credential_store type \"file\"")
			}
		case "windows":
			if _, err := runtime.findExecutable("powershell"); err != nil {
				if _, err := runtime.findExecutable("powershell.exe"); err != nil {
					return errors.New("OS-native Credential Store requires PowerShell; configure credential_store type \"file\"")
				}
			}
		}
	}
	return nil
}

func canonicalCredentialPath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolvedPath, err := filepath.EvalSymlinks(absolutePath); err == nil {
		return filepath.Clean(resolvedPath), nil
	}
	parent := filepath.Dir(absolutePath)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		resolvedParent = parent
	}
	return filepath.Clean(filepath.Join(resolvedParent, filepath.Base(absolutePath))), nil
}

func validateDefaultDataTypes(dataTypes []string) error {
	if dataTypes == nil {
		return errors.New("missing default_data_types")
	}
	if len(dataTypes) == 0 {
		return errors.New("default Data Types must include at least one Data Type")
	}
	seen := make(map[string]struct{}, len(dataTypes))
	for _, dataType := range dataTypes {
		entry, ok := googleHealthDataTypes.Lookup(dataType)
		if !ok || !entry.DefaultConfigType {
			return fmt.Errorf("unsupported default Data Type %s", dataType)
		}
		if _, ok := seen[dataType]; ok {
			return fmt.Errorf("duplicate default Data Type %s", dataType)
		}
		seen[dataType] = struct{}{}
	}
	return nil
}

func loadOAuthClientConfig(path string) (oauthClientConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return oauthClientConfig{}, errors.New("OAuth client file cannot be read")
	}
	return parseOAuthClientConfigContent(content)
}

func parseOAuthClientConfigContent(content []byte) (oauthClientConfig, error) {
	var raw map[string]json.RawMessage
	// A JSON "null" unmarshals into a nil map without error, so it is
	// rejected here together with non-object input.
	if err := json.Unmarshal(content, &raw); err != nil || raw == nil {
		return oauthClientConfig{}, errors.New("OAuth client file must contain a JSON object")
	}
	var client struct {
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		AuthURI      string   `json:"auth_uri"`
		TokenURI     string   `json:"token_uri"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	clientKind := ""
	for _, key := range []string{"installed", "web"} {
		if nested, ok := raw[key]; ok {
			if err := json.Unmarshal(nested, &client); err != nil {
				return oauthClientConfig{}, errors.New("OAuth client file has malformed client details")
			}
			clientKind = key
			break
		}
	}
	if clientKind == "" {
		return oauthClientConfig{}, errors.New(`OAuth client file is missing the "installed" object (Google Desktop client JSON shape: {"installed": {"client_id": "...", "client_secret": "..."}})`)
	}
	if clientKind == "web" {
		return oauthClientConfig{}, errors.New("OAuth client file must be an installed desktop client, not a web client")
	}
	if client.ClientID == "" || client.ClientSecret == "" {
		return oauthClientConfig{}, errors.New("OAuth client file is missing client_id or client_secret")
	}
	if client.AuthURI == "" {
		client.AuthURI = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	if client.TokenURI == "" {
		client.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return oauthClientConfig{
		kind:         clientKind,
		clientID:     client.ClientID,
		clientSecret: client.ClientSecret,
		authURI:      client.AuthURI,
		tokenURI:     client.TokenURI,
		redirectURIs: client.RedirectURIs,
	}, nil
}

func oauthScopesForDataTypes(dataTypes []string) []string {
	needed := make(map[string]struct{})
	needed[googleHealthProfileReadonlyScope] = struct{}{}
	for _, dataType := range dataTypes {
		for _, scope := range googleHealthScopesForDataType(dataType) {
			needed[scope] = struct{}{}
		}
	}
	if len(needed) == 0 {
		needed[googleHealthActivityReadonlyScope] = struct{}{}
	}
	ordered := []string{
		googleHealthActivityReadonlyScope,
		googleHealthHealthMetricsReadonlyScope,
		googleHealthSleepReadonlyScope,
		googleHealthNutritionReadonlyScope,
		googleHealthProfileReadonlyScope,
	}
	scopes := make([]string, 0, len(needed))
	for _, scope := range ordered {
		if _, ok := needed[scope]; ok {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

func runBrowserOAuthFlow(client oauthClientConfig, scopes []string, noInput bool) (oauthTokenResponse, error) {
	return runBrowserOAuthFlowWithRuntime(client, scopes, noInput, runtimeAdapters{openBrowser: openBrowser})
}

func runBrowserOAuthFlowWithRuntime(client oauthClientConfig, scopes []string, noInput bool, runtime runtimeAdapters) (oauthTokenResponse, error) {
	if runtime.openBrowser == nil {
		runtime.openBrowser = openBrowser
	}
	if runtime.now == nil {
		runtime.now = currentTime
	}
	if noInput {
		return oauthTokenResponse{}, errors.New("connect requires browser OAuth; rerun without --no-input")
	}
	listener, redirectURI, err := listenForOAuthRedirect(client.redirectURIs)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	defer listener.Close()

	state, err := randomURLToken(32)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	verifier, err := randomURLToken(64)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	challenge := pkceChallenge(verifier)
	authURL, err := buildOAuthAuthURL(client, redirectURI, scopes, state, challenge)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	if err := runtime.openBrowser(authURL); err != nil {
		return oauthTokenResponse{}, fmt.Errorf("open browser: %w", err)
	}
	code, err := waitForOAuthCode(listener, state)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	return exchangeOAuthCodeWithRuntime(client, redirectURI, code, verifier, runtime)
}

func listenForOAuthRedirect(redirectURIs []string) (net.Listener, string, error) {
	redirectPath := "/oauth2callback"
	for _, candidate := range redirectURIs {
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Scheme != "http" {
			continue
		}
		host := parsed.Hostname()
		if host != "127.0.0.1" && host != "localhost" {
			continue
		}
		redirectPath = parsed.EscapedPath()
		break
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return listener, fmt.Sprintf("http://127.0.0.1:%d%s", port, redirectPath), nil
}

func buildOAuthAuthURL(client oauthClientConfig, redirectURI string, scopes []string, state, challenge string) (string, error) {
	authURL, err := url.Parse(client.authURI)
	if err != nil {
		return "", errors.New("OAuth auth_uri is invalid")
	}
	query := authURL.Query()
	query.Set("client_id", client.clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", strings.Join(scopes, " "))
	query.Set("access_type", "offline")
	// include_granted_scopes=true tells Google to grant the union of the
	// requested scopes and any scopes the user has previously consented
	// to under this client, so `connect --add-scopes irn` extends an
	// existing grant rather than replacing it.
	query.Set("include_granted_scopes", "true")
	query.Set("prompt", "consent")
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	authURL.RawQuery = query.Encode()
	return authURL.String(), nil
}

func waitForOAuthCode(listener net.Listener, wantState string) (string, error) {
	result := make(chan struct {
		code string
		err  error
	}, 1)
	server := &http.Server{}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("state") != wantState {
			http.Error(w, "invalid OAuth state", http.StatusBadRequest)
			result <- struct {
				code string
				err  error
			}{err: errors.New("OAuth state mismatch")}
			return
		}
		if errText := query.Get("error"); errText != "" {
			http.Error(w, "OAuth failed", http.StatusBadRequest)
			result <- struct {
				code string
				err  error
			}{err: fmt.Errorf("OAuth failed: %s", errText)}
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(w, "missing OAuth code", http.StatusBadRequest)
			result <- struct {
				code string
				err  error
			}{err: errors.New("OAuth redirect missing code")}
			return
		}
		fmt.Fprintln(w, "gohealthcli connected. You can close this tab.")
		result <- struct {
			code string
			err  error
		}{code: code}
	})
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			result <- struct {
				code string
				err  error
			}{err: err}
		}
	}()
	outcome := <-result
	_ = server.Close()
	return outcome.code, outcome.err
}

func exchangeOAuthCode(client oauthClientConfig, redirectURI, code, verifier string) (oauthTokenResponse, error) {
	return exchangeOAuthCodeWithRuntime(client, redirectURI, code, verifier, runtimeAdapters{now: currentTime})
}

func exchangeOAuthCodeWithRuntime(client oauthClientConfig, redirectURI, code, verifier string, runtime runtimeAdapters) (oauthTokenResponse, error) {
	if runtime.now == nil {
		runtime.now = currentTime
	}
	values := url.Values{}
	values.Set("client_id", client.clientID)
	values.Set("client_secret", client.clientSecret)
	values.Set("code", code)
	values.Set("code_verifier", verifier)
	values.Set("grant_type", "authorization_code")
	values.Set("redirect_uri", redirectURI)
	response, err := http.PostForm(client.tokenURI, values)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return oauthTokenResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return oauthTokenResponse{}, fmt.Errorf("OAuth token exchange failed with HTTP %d", response.StatusCode)
	}
	return parseOAuthTokenResponse(body, runtime.now())
}

func refreshGoogleOAuthToken(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
	return refreshGoogleOAuthTokenWithRuntime(client, refreshToken, fallbackScopes, runtimeAdapters{now: currentTime})
}

func refreshGoogleOAuthTokenWithRuntime(client oauthClientConfig, refreshToken string, fallbackScopes []string, runtime runtimeAdapters) (oauthTokenResponse, error) {
	if runtime.now == nil {
		runtime.now = currentTime
	}
	values := url.Values{}
	values.Set("client_id", client.clientID)
	values.Set("client_secret", client.clientSecret)
	values.Set("refresh_token", refreshToken)
	values.Set("grant_type", "refresh_token")
	response, err := http.PostForm(client.tokenURI, values)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return oauthTokenResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return oauthTokenResponse{}, fmt.Errorf("OAuth token refresh failed with HTTP %d", response.StatusCode)
	}
	return parseOAuthRefreshTokenResponse(body, runtime.now(), refreshToken, fallbackScopes)
}

func parseOAuthTokenResponse(body []byte, now time.Time) (oauthTokenResponse, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return oauthTokenResponse{}, errors.New("OAuth token response is not valid JSON")
	}
	accessToken, _ := raw["access_token"].(string)
	if accessToken == "" {
		return oauthTokenResponse{}, errors.New("OAuth token response missing access token")
	}
	refreshToken, _ := raw["refresh_token"].(string)
	if refreshToken == "" {
		return oauthTokenResponse{}, errors.New("OAuth token response missing refresh token; rerun connect and grant offline access")
	}
	tokenType, _ := raw["token_type"].(string)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	expiresIn, _ := raw["expires_in"].(float64)
	if expiresIn <= 0 {
		return oauthTokenResponse{}, errors.New("OAuth token response missing expiry")
	}
	scopeText, _ := raw["scope"].(string)
	scopes := strings.Fields(scopeText)
	if len(scopes) == 0 {
		return oauthTokenResponse{}, errors.New("OAuth token response missing scopes")
	}
	var refreshExpiresAt *time.Time
	if refreshExpiresIn, ok := raw["refresh_token_expires_in"].(float64); ok && refreshExpiresIn > 0 {
		value := now.Add(time.Duration(refreshExpiresIn) * time.Second).UTC()
		refreshExpiresAt = &value
	}
	return oauthTokenResponse{
		accessToken:            accessToken,
		refreshToken:           refreshToken,
		tokenType:              tokenType,
		scopes:                 scopes,
		expiresAt:              now.Add(time.Duration(expiresIn) * time.Second).UTC(),
		refreshTokenExpiresAt:  refreshExpiresAt,
		rawTokenMaterialObject: raw,
	}, nil
}

func parseOAuthRefreshTokenResponse(body []byte, now time.Time, fallbackRefreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return oauthTokenResponse{}, errors.New("OAuth token response is not valid JSON")
	}
	accessToken, _ := raw["access_token"].(string)
	if accessToken == "" {
		return oauthTokenResponse{}, errors.New("OAuth token response missing access token")
	}
	refreshToken, _ := raw["refresh_token"].(string)
	if refreshToken == "" {
		refreshToken = fallbackRefreshToken
		raw["refresh_token"] = fallbackRefreshToken
	}
	if refreshToken == "" {
		return oauthTokenResponse{}, errors.New("OAuth token response missing refresh token; run `gohealthcli connect` again")
	}
	tokenType, _ := raw["token_type"].(string)
	if tokenType == "" {
		tokenType = "Bearer"
		raw["token_type"] = tokenType
	}
	expiresIn, _ := raw["expires_in"].(float64)
	if expiresIn <= 0 {
		return oauthTokenResponse{}, errors.New("OAuth token response missing expiry")
	}
	scopeText, _ := raw["scope"].(string)
	scopes := strings.Fields(scopeText)
	if len(scopes) == 0 {
		scopes = fallbackScopes
		raw["scope"] = strings.Join(scopes, " ")
	}
	if len(scopes) == 0 {
		return oauthTokenResponse{}, errors.New("OAuth token response missing scopes")
	}
	var refreshExpiresAt *time.Time
	if refreshExpiresIn, ok := raw["refresh_token_expires_in"].(float64); ok && refreshExpiresIn > 0 {
		value := now.Add(time.Duration(refreshExpiresIn) * time.Second).UTC()
		refreshExpiresAt = &value
	}
	return oauthTokenResponse{
		accessToken:            accessToken,
		refreshToken:           refreshToken,
		tokenType:              tokenType,
		scopes:                 scopes,
		expiresAt:              now.Add(time.Duration(expiresIn) * time.Second).UTC(),
		refreshTokenExpiresAt:  refreshExpiresAt,
		rawTokenMaterialObject: raw,
	}, nil
}

func fetchGoogleIdentity(accessToken string) (googleIdentity, error) {
	request, err := http.NewRequest(http.MethodGet, googleHealthIdentityURL, nil)
	if err != nil {
		return googleIdentity{}, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return googleIdentity{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return googleIdentity{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return googleIdentity{}, fmt.Errorf("Google Health identity request failed with HTTP %d", response.StatusCode)
	}
	return parseGoogleIdentity(body)
}

func fetchGoogleProfile(accessToken string) (googleProfile, error) {
	request, err := http.NewRequest(http.MethodGet, googleHealthProfileURL, nil)
	if err != nil {
		return googleProfile{}, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return googleProfile{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return googleProfile{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return googleProfile{}, fmt.Errorf("Google Health profile request failed with HTTP %d", response.StatusCode)
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

func fetchGoogleHealthRaw(request rawProviderRequest, accessToken string) ([]byte, error) {
	method := request.method
	if method == "" {
		method = http.MethodGet
	}
	var requestBody io.Reader
	if len(request.body) != 0 {
		requestBody = bytes.NewReader(request.body)
	}
	httpRequest, err := http.NewRequest(method, request.url, requestBody)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+accessToken)
	httpRequest.Header.Set("Accept", "application/json")
	if len(request.body) != 0 {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(httpRequest)
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
// before doing so. Other callers can still read the error string.
type googleHealthHTTPError struct {
	StatusCode int
	RetryAfter time.Duration
	Body       []byte
}

func (err *googleHealthHTTPError) Error() string {
	// Deliberately omit the response body — Google Health echoes the
	// bearer token in some error responses (covered by
	// TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody). Callers that
	// need the body can read err.Body directly.
	return fmt.Sprintf("Google Health raw request failed with HTTP %d", err.StatusCode)
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
	if dataType == "steps" {
		return parseGoogleHealthStepsDataPoint(connection, rawPoint, sourceFamilyFilter)
	}
	if googleHealthIntervalDataPointJSONField(dataType) != "" {
		return parseGoogleHealthIntervalDataPoint(connection, dataType, rawPoint, sourceFamilyFilter)
	}
	if googleHealthSampleDataPointJSONField(dataType) != "" {
		return parseGoogleHealthSampleDataPoint(connection, dataType, rawPoint, sourceFamilyFilter)
	}
	if googleHealthDailyDataPointJSONField(dataType) != "" {
		return parseGoogleHealthDailyDataPoint(connection, dataType, rawPoint, sourceFamilyFilter)
	}
	if googleHealthSessionDataPointJSONField(dataType) != "" {
		return parseGoogleHealthSessionDataPoint(connection, dataType, rawPoint, sourceFamilyFilter)
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

func parseGoogleHealthStepsDataPoint(connection archivedConnection, rawPoint json.RawMessage, sourceFamilyFilter string) (archivedDataPoint, error) {
	canonicalRaw, err := compactJSONString(rawPoint)
	if err != nil {
		return archivedDataPoint{}, errors.New("Google Health steps Data Point is not valid JSON")
	}
	envelope, err := parseGoogleHealthDataPointEnvelope("steps", rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	var raw struct {
		Steps struct {
			Interval googleHealthIntervalFields `json:"interval"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(rawPoint, &raw); err != nil {
		return archivedDataPoint{}, errors.New("Google Health steps Data Point is not valid JSON")
	}
	interval, err := parseGoogleHealthIntervalMetadata("steps", raw.Steps.Interval)
	if err != nil {
		return archivedDataPoint{}, err
	}
	return archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "steps",
		upstreamResourceName: envelope.upstreamResourceName(),
		recordKind:           "interval",
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

func parseGoogleHealthIntervalDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter string) (archivedDataPoint, error) {
	canonicalRaw, err := compactJSONString(rawPoint)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	envelope, err := parseGoogleHealthDataPointEnvelope(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	jsonField := googleHealthIntervalDataPointJSONField(dataType)
	rawInterval, ok := envelope.fields[jsonField]
	if !ok || len(rawInterval) == 0 || string(rawInterval) == "null" {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point missing %s value", dataType, jsonField)
	}
	var value struct {
		Interval googleHealthIntervalFields `json:"interval"`
	}
	if err := json.Unmarshal(rawInterval, &value); err != nil {
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
		recordKind:           "interval",
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
	canonicalRaw, err := compactJSONString(rawPoint)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	envelope, err := parseGoogleHealthDataPointEnvelope(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	jsonField := googleHealthSampleDataPointJSONField(dataType)
	rawSample, ok := envelope.fields[jsonField]
	if !ok || len(rawSample) == 0 || string(rawSample) == "null" {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point missing %s value", dataType, jsonField)
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
	canonicalRaw, err := compactJSONString(rawPoint)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	envelope, err := parseGoogleHealthDataPointEnvelope(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	shape, ok := googleHealthDailyDataPointShapeForDataType(dataType)
	if !ok {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point is not supported", dataType)
	}
	rawDaily, ok := envelope.fields[shape.jsonField]
	if !ok || len(rawDaily) == 0 || string(rawDaily) == "null" {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point missing %s value", dataType, shape.jsonField)
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

func parseGoogleHealthSessionDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter string) (archivedDataPoint, error) {
	canonicalRaw, err := compactJSONString(rawPoint)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	envelope, err := parseGoogleHealthDataPointEnvelope(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	jsonField := googleHealthSessionDataPointJSONField(dataType)
	rawSession, ok := envelope.fields[jsonField]
	if !ok || len(rawSession) == 0 || string(rawSession) == "null" {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point missing %s value", dataType, jsonField)
	}
	var session struct {
		Interval googleHealthIntervalFields `json:"interval"`
	}
	if err := json.Unmarshal(rawSession, &session); err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point %s is not valid JSON", dataType, jsonField)
	}
	interval, err := parseGoogleHealthIntervalMetadata(dataType, session.Interval)
	if err != nil {
		return archivedDataPoint{}, err
	}
	return archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             dataType,
		upstreamResourceName: envelope.upstreamResourceName(),
		recordKind:           "session",
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

// parseGoogleHealthStepsDailyRollup is the legacy steps-only entry
// point retained for callers (existing tests, the daily-rollup
// executor) that still address it by name. It delegates to the
// generic parseGoogleHealthRollup, which preserves byte-identical
// output for the steps-daily shape (the #106 AC regression guard).
func parseGoogleHealthStepsDailyRollup(connection archivedConnection, rawRollup json.RawMessage) (archivedRollup, error) {
	return parseGoogleHealthRollup(connection, "steps", "dailyRollUp", rawRollup)
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

func randomURLToken(byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func openBrowser(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}

type credentialStore interface {
	Store(key string, tokenMaterial map[string]any) error
	Load(key string) (map[string]any, error)
}

func newCredentialStore(config credentialStoreConfig) (credentialStore, error) {
	return newCredentialStoreWithRuntime(config, productionRuntimeAdapters())
}

func newCredentialStoreWithRuntime(config credentialStoreConfig, runtime runtimeAdapters) (credentialStore, error) {
	runtime = runtime.withDefaults()
	switch config.kind {
	case "file":
		return fileCredentialStore{path: config.path}, nil
	case "os_native":
		if runtime.currentOS != "darwin" && runtime.currentOS != "linux" && runtime.currentOS != "windows" {
			return nil, errors.New("OS-native Credential Store is not available on this platform; configure credential_store type \"file\"")
		}
		return osNativeCredentialStore{service: config.service, runtime: runtime}, nil
	default:
		return nil, errors.New("unsupported Credential Store type")
	}
}

type fileCredentialStore struct {
	path string
}

func (store fileCredentialStore) Store(key string, tokenMaterial map[string]any) error {
	if err := ensureOwnerOnlyDir(filepath.Dir(store.path)); err != nil {
		return err
	}
	existing := map[string]any{}
	if content, err := os.ReadFile(store.path); err == nil && len(content) > 0 {
		if err := json.Unmarshal(content, &existing); err != nil {
			return errors.New("Credential Store file is not valid JSON")
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	existing[key] = tokenMaterial
	content, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := os.WriteFile(store.path, content, 0o600); err != nil {
		return err
	}
	if !usesPOSIXPermissions() {
		return nil
	}
	return os.Chmod(store.path, 0o600)
}

func (store fileCredentialStore) Load(key string) (map[string]any, error) {
	content, err := os.ReadFile(store.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("Credential Store token material not found; run `gohealthcli connect` first")
		}
		return nil, err
	}
	var existing map[string]json.RawMessage
	if err := json.Unmarshal(content, &existing); err != nil {
		return nil, errors.New("Credential Store file is not valid JSON")
	}
	raw, ok := existing[key]
	if !ok {
		return nil, errors.New("Credential Store token material not found; run `gohealthcli connect` first")
	}
	var tokenMaterial map[string]any
	if err := json.Unmarshal(raw, &tokenMaterial); err != nil {
		return nil, errors.New("Credential Store token material is not valid JSON")
	}
	return tokenMaterial, nil
}

type osNativeCredentialStore struct {
	service string
	runtime runtimeAdapters
}

func (store osNativeCredentialStore) Store(key string, tokenMaterial map[string]any) error {
	content, err := json.Marshal(tokenMaterial)
	if err != nil {
		return err
	}
	runtime := store.runtime.withDefaults()
	switch runtime.currentOS {
	case "darwin":
		return runtime.runSecurityAddGenericPassword(store.service, key, content)
	case "linux":
		return runtime.runSecretToolStore(store.service, key, content)
	case "windows":
		return runtime.runWindowsCredentialWrite(store.service, key, content)
	default:
		return errors.New("OS-native Credential Store is not available on this platform; configure credential_store type \"file\"")
	}
}

func (store osNativeCredentialStore) Load(key string) (map[string]any, error) {
	var content []byte
	var err error
	runtime := store.runtime.withDefaults()
	switch runtime.currentOS {
	case "darwin":
		content, err = runtime.runSecurityFindGenericPassword(store.service, key)
	case "linux":
		content, err = runtime.runSecretToolLookup(store.service, key)
	case "windows":
		content, err = runtime.runWindowsCredentialRead(store.service, key)
	default:
		return nil, errors.New("OS-native Credential Store is not available on this platform; configure credential_store type \"file\"")
	}
	if err != nil {
		return nil, err
	}
	var tokenMaterial map[string]any
	if err := json.Unmarshal(content, &tokenMaterial); err != nil {
		return nil, errors.New("Credential Store token material is not valid JSON")
	}
	return tokenMaterial, nil
}

func runSecurityAddGenericPasswordCommand(service, key string, content []byte) error {
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", service, "-a", key, "-w")
	password := string(content)
	cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")
	return cmd.Run()
}

func runSecurityFindGenericPasswordCommand(service, key string) ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", service, "-a", key, "-w")
	output, err := cmd.Output()
	if err != nil {
		return nil, errors.New("Credential Store token material not found; run `gohealthcli connect` first")
	}
	return []byte(strings.TrimSpace(string(output))), nil
}

func runSecretToolStoreCommand(service, key string, content []byte) error {
	cmd := exec.Command("secret-tool", "store", "--label", service, "service", service, "account", key)
	cmd.Stdin = strings.NewReader(string(content))
	return cmd.Run()
}

func runSecretToolLookupCommand(service, key string) ([]byte, error) {
	cmd := exec.Command("secret-tool", "lookup", "service", service, "account", key)
	output, err := cmd.Output()
	if err != nil {
		return nil, errors.New("Credential Store token material not found; run `gohealthcli connect` first")
	}
	return []byte(strings.TrimSpace(string(output))), nil
}

func runWindowsCredentialWriteCommand(service, key string, content []byte) error {
	target := service + ":" + key
	script := `
$secret = [Console]::In.ReadToEnd()
$code = @"
using System;
using System.Runtime.InteropServices;
public static class NativeCredential {
  [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
  public struct CREDENTIAL {
    public UInt32 Flags;
    public UInt32 Type;
    public string TargetName;
    public string Comment;
    public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
    public UInt32 CredentialBlobSize;
    public IntPtr CredentialBlob;
    public UInt32 Persist;
    public UInt32 AttributeCount;
    public IntPtr Attributes;
    public string TargetAlias;
    public string UserName;
  }
  [DllImport("advapi32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
  public static extern bool CredWrite(ref CREDENTIAL credential, UInt32 flags);
}
"@
Add-Type $code
$bytes = [Text.Encoding]::Unicode.GetBytes($secret)
$blob = [Runtime.InteropServices.Marshal]::AllocHGlobal($bytes.Length)
try {
  [Runtime.InteropServices.Marshal]::Copy($bytes, 0, $blob, $bytes.Length)
  $credential = New-Object NativeCredential+CREDENTIAL
  $credential.Type = 1
  $credential.TargetName = $env:GOHEALTHCLI_CREDENTIAL_TARGET
  $credential.UserName = $env:GOHEALTHCLI_CREDENTIAL_ACCOUNT
  $credential.CredentialBlobSize = $bytes.Length
  $credential.CredentialBlob = $blob
  $credential.Persist = 2
  if (-not [NativeCredential]::CredWrite([ref]$credential, 0)) {
    throw [ComponentModel.Win32Exception][Runtime.InteropServices.Marshal]::GetLastWin32Error()
  }
} finally {
  [Runtime.InteropServices.Marshal]::FreeHGlobal($blob)
}
`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "GOHEALTHCLI_CREDENTIAL_TARGET="+target, "GOHEALTHCLI_CREDENTIAL_ACCOUNT="+key)
	cmd.Stdin = strings.NewReader(string(content))
	return cmd.Run()
}

func runWindowsCredentialReadCommand(service, key string) ([]byte, error) {
	target := service + ":" + key
	script := `
$code = @"
using System;
using System.Runtime.InteropServices;
public static class NativeCredential {
  [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
  public struct CREDENTIAL {
    public UInt32 Flags;
    public UInt32 Type;
    public string TargetName;
    public string Comment;
    public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
    public UInt32 CredentialBlobSize;
    public IntPtr CredentialBlob;
    public UInt32 Persist;
    public UInt32 AttributeCount;
    public IntPtr Attributes;
    public string TargetAlias;
    public string UserName;
  }
  [DllImport("advapi32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
  public static extern bool CredRead(string target, UInt32 type, UInt32 reservedFlag, out IntPtr credentialPtr);
  [DllImport("advapi32.dll", SetLastError = true)]
  public static extern void CredFree(IntPtr buffer);
}
"@
Add-Type $code
$utf8 = [Text.UTF8Encoding]::new($false)
$credentialPtr = [IntPtr]::Zero
if (-not [NativeCredential]::CredRead($env:GOHEALTHCLI_CREDENTIAL_TARGET, 1, 0, [ref]$credentialPtr)) {
  throw [ComponentModel.Win32Exception][Runtime.InteropServices.Marshal]::GetLastWin32Error()
}
try {
  $credential = [Runtime.InteropServices.Marshal]::PtrToStructure($credentialPtr, [type][NativeCredential+CREDENTIAL])
  $bytes = New-Object byte[] $credential.CredentialBlobSize
  [Runtime.InteropServices.Marshal]::Copy($credential.CredentialBlob, $bytes, 0, $credential.CredentialBlobSize)
  $credentialJson = [Text.Encoding]::Unicode.GetString($bytes)
  $stdout = [Console]::OpenStandardOutput()
  $outputBytes = $utf8.GetBytes($credentialJson)
  $stdout.Write($outputBytes, 0, $outputBytes.Length)
} finally {
  [NativeCredential]::CredFree($credentialPtr)
}
`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "GOHEALTHCLI_CREDENTIAL_TARGET="+target)
	output, err := cmd.Output()
	if err != nil {
		return nil, errors.New("Credential Store token material not found; run `gohealthcli connect` first")
	}
	return []byte(strings.TrimSpace(string(output))), nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func validateArchive(archivePath string) error {
	_, err := inspectArchive(archivePath, false)
	return err
}

func inspectArchive(archivePath string, validateTokens bool) (archiveCheck, error) {
	return (healthArchiveLifecycle{path: archivePath}).Inspect(validateTokens)
}

func validateTokenMetadata(metadata string) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		return errors.New("token metadata is not valid JSON")
	}
	if len(raw) == 0 {
		return errors.New("missing token metadata")
	}
	if metadataContainsSecretKeys(raw) {
		return errors.New("token metadata contains forbidden secret material")
	}
	if _, err := requireJSONString(raw, "credential_store_key"); err != nil {
		return err
	}
	expiresAt, err := requireJSONString(raw, "expires_at")
	if err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
		return errors.New("token metadata expiry is not RFC3339")
	}
	if err := requireJSONStringArray(raw, "scopes"); err != nil {
		return err
	}
	return nil
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

func requireUsableConnectionAccessToken(metadata string, now time.Time) error {
	expiresAt, _, err := connectionTokenExpiryAndScopes(metadata)
	if err != nil {
		return err
	}
	if !expiresAt.After(now.UTC()) {
		return errCurrentConnectionTokenExpired
	}
	return nil
}

func connectionTokenExpiryAndScopes(metadata string) (time.Time, []string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		return time.Time{}, nil, errors.New("Connection token metadata is not valid JSON; run `gohealthcli connect` again")
	}
	expiresAtText, err := requireJSONString(raw, "expires_at")
	if err != nil {
		return time.Time{}, nil, errors.New("Connection token metadata is incomplete; run `gohealthcli connect` again")
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtText)
	if err != nil {
		return time.Time{}, nil, errors.New("Connection token expiry is invalid; run `gohealthcli connect` again")
	}
	value, ok := raw["scopes"]
	if !ok {
		return time.Time{}, nil, errors.New("Connection token metadata is missing scopes; run `gohealthcli connect` again")
	}
	var scopes []string
	if err := json.Unmarshal(value, &scopes); err != nil || len(scopes) == 0 {
		return time.Time{}, nil, errors.New("Connection token metadata scopes are invalid; run `gohealthcli connect` again")
	}
	for _, scope := range scopes {
		if strings.TrimSpace(scope) == "" {
			return time.Time{}, nil, errors.New("Connection token metadata scopes are invalid; run `gohealthcli connect` again")
		}
	}
	return expiresAt, scopes, nil
}

func metadataContainsSecretKeys(value any) bool {
	switch typed := value.(type) {
	case map[string]json.RawMessage:
		for key, nested := range typed {
			if secretMetadataKey(key) {
				return true
			}
			var decoded any
			if err := json.Unmarshal(nested, &decoded); err == nil && metadataContainsSecretKeys(decoded) {
				return true
			}
		}
	case map[string]any:
		for key, nested := range typed {
			if secretMetadataKey(key) || metadataContainsSecretKeys(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if metadataContainsSecretKeys(nested) {
				return true
			}
		}
	}
	return false
}

func secretMetadataKey(key string) bool {
	lower := strings.ToLower(key)
	normalized := strings.NewReplacer("_", "", "-", "").Replace(lower)
	return strings.Contains(normalized, "accesstoken") ||
		strings.Contains(normalized, "refreshtoken") ||
		strings.Contains(normalized, "clientsecret") ||
		strings.Contains(normalized, "idtoken")
}

func requireJSONString(raw map[string]json.RawMessage, key string) (string, error) {
	value, ok := raw[key]
	if !ok {
		return "", fmt.Errorf("missing token metadata %s", key)
	}
	var parsed string
	if err := json.Unmarshal(value, &parsed); err != nil || parsed == "" {
		return "", fmt.Errorf("token metadata %s must be a non-empty string", key)
	}
	return parsed, nil
}

func requireJSONStringArray(raw map[string]json.RawMessage, key string) error {
	value, ok := raw[key]
	if !ok {
		return fmt.Errorf("missing token metadata %s", key)
	}
	var parsed []string
	if err := json.Unmarshal(value, &parsed); err != nil || len(parsed) == 0 {
		return fmt.Errorf("token metadata %s must be a non-empty string array", key)
	}
	for _, item := range parsed {
		if strings.TrimSpace(item) == "" {
			return fmt.Errorf("token metadata %s must not contain empty strings", key)
		}
	}
	return nil
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

func applyMigrations(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, statement := range initialMigrationStatements() {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, 'initial_archive_schema', ?)`, now); err != nil {
		return err
	}
	if err := applyGoogleIdentityArchiveMigration(tx, now); err != nil {
		return err
	}
	if err := applySourceFamilyArchiveMigration(tx, now); err != nil {
		return err
	}
	if err := applyDailyStepsViewMigration(tx, now); err != nil {
		return err
	}
	if err := applyFirstReleaseNormalizedViewsMigration(tx, now); err != nil {
		return err
	}
	if err := applySyncCursorsMigration(tx, now); err != nil {
		return err
	}
	if err := applyIdentitySnapshotsMigration(tx, now); err != nil {
		return err
	}
	if err := applyCurrentSettingsViewMigration(tx, now); err != nil {
		return err
	}
	if err := applyPairedDevicesViewMigration(tx, now); err != nil {
		return err
	}
	if err := applyCurrentIRNProfileViewMigration(tx, now); err != nil {
		return err
	}
	if err := applySleepExerciseViewsMigration(tx, now); err != nil {
		return err
	}
	if err := applyExerciseSplitsRealShapeMigration(tx, now); err != nil {
		return err
	}
	if err := applySearchableTextViewMigration(tx, now); err != nil {
		return err
	}
	if err := applySearchableTextLatestProfileMigration(tx, now); err != nil {
		return err
	}
	if err := applyDataPointAttachmentsMigration(tx, now); err != nil {
		return err
	}
	if err := applyFloorsIntervalsViewMigration(tx, now); err != nil {
		return err
	}
	if err := applyTier1ActivityViewsMigration(tx, now); err != nil {
		return err
	}
	if err := applyTier1HealthMetricsViewsMigration(tx, now); err != nil {
		return err
	}
	if err := applyTier1DailyHydrationViewsMigration(tx, now); err != nil {
		return err
	}
	if err := applyTier2EcgIrnViewsMigration(tx, now); err != nil {
		return err
	}
	if err := applyHydrationLogSessionsViewMigration(tx, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 21`); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateArchiveIfNeeded(archivePath string) error {
	// healthArchiveLifecycle.Migrate already backfills the attachment
	// root, so this thin wrapper just forwards.
	return (healthArchiveLifecycle{path: archivePath}).Migrate()
}

func applyPendingMigrations(db *sql.DB) error {
	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		return err
	}
	switch userVersion {
	case currentSchemaVersion:
		return nil
	case 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20:
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		now := time.Now().UTC().Format(time.RFC3339)
		if userVersion == 1 {
			if err := applyGoogleIdentityArchiveMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 2 {
			if err := applySourceFamilyArchiveMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 3 {
			if err := applyDailyStepsViewMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 4 {
			if err := applyFirstReleaseNormalizedViewsMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 5 {
			if err := applySyncCursorsMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 6 {
			if err := applyIdentitySnapshotsMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 7 {
			if err := applyCurrentSettingsViewMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 8 {
			if err := applyPairedDevicesViewMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 9 {
			if err := applyCurrentIRNProfileViewMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 10 {
			if err := applySleepExerciseViewsMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 11 {
			if err := applyExerciseSplitsRealShapeMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 12 {
			if err := applySearchableTextViewMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 13 {
			if err := applySearchableTextLatestProfileMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 14 {
			if err := applyDataPointAttachmentsMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 15 {
			if err := applyFloorsIntervalsViewMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 16 {
			if err := applyTier1ActivityViewsMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 17 {
			if err := applyTier1HealthMetricsViewsMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 18 {
			if err := applyTier1DailyHydrationViewsMigration(tx, now); err != nil {
				return err
			}
		}
		if userVersion <= 19 {
			if err := applyTier2EcgIrnViewsMigration(tx, now); err != nil {
				return err
			}
		}
		if err := applyHydrationLogSessionsViewMigration(tx, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`PRAGMA user_version = 21`); err != nil {
			return err
		}
		return tx.Commit()
	default:
		return fmt.Errorf("schema version %d, want %d", userVersion, currentSchemaVersion)
	}
}

func applyGoogleIdentityArchiveMigration(tx *sql.Tx, appliedAt string) error {
	if _, err := tx.Exec(`ALTER TABLE connections ADD COLUMN google_identity_json TEXT NOT NULL DEFAULT '{}'`); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (2, 'add_google_identity_json', ?)`, appliedAt)
	return err
}

func applySourceFamilyArchiveMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range []string{
		`ALTER TABLE data_points ADD COLUMN source_family_filter TEXT`,
		`ALTER TABLE sync_runs ADD COLUMN source_family_filter TEXT`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (3, 'add_source_family_filter', ?)`, appliedAt)
	return err
}

func applyDailyStepsViewMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range dailyStepsViewMigrationStatements() {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (4, 'add_daily_steps_view', ?)`, appliedAt)
	return err
}

func applyFirstReleaseNormalizedViewsMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range firstReleaseNormalizedViewMigrationStatements() {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (5, 'add_first_release_normalized_views', ?)`, appliedAt)
	return err
}

func applySyncCursorsMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range syncCursorsMigrationStatements() {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (6, 'add_sync_cursors', ?)`, appliedAt)
	return err
}

func applyTier1ActivityViewsMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(17) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (17, 'add_tier1_activity_views', ?)`, appliedAt)
	return err
}

func applyTier1HealthMetricsViewsMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(18) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (18, 'add_tier1_health_metrics_views', ?)`, appliedAt)
	return err
}

// applyTier1DailyHydrationViewsMigration installs the four daily/sample
// Normalized Views for #103: daily_vo2_max, daily_heart_rate_zones,
// daily_sleep_temperature_derivations, respiratory_rate_sleep_summary.
// The session-shaped hydration_log_sessions view ships separately at
// schema version 21 (applyHydrationLogSessionsViewMigration) so the
// migration row history records the two payload shapes independently.
func applyTier1DailyHydrationViewsMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(19) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (19, 'add_tier1_daily_hydration_views', ?)`, appliedAt)
	return err
}

// applyHydrationLogSessionsViewMigration installs the session-shaped
// hydration_log_sessions Normalized View (#103). The view projects
// $.hydrationLog.volume.liters (TEXT for precision) plus the standard
// session timing columns. Pinned to schema version 21 — the daily/sample
// Tier 1 daily+hydration views shipped at v19; this seals the slice.
func applyHydrationLogSessionsViewMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(21) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (21, 'add_hydration_log_sessions_view', ?)`, appliedAt)
	return err
}

// applyTier2EcgIrnViewsMigration registers the Tier 2 ECG and IRN
// Normalized Views (#104) — electrocardiogram_sessions and
// irregular_rhythm_notifications. The view SQL itself lives in the
// shared exportDatasetDefinitions registry; this migration just
// runs the registered CREATE VIEW statements pinned to schema
// version 20 and records the migration row.
func applyTier2EcgIrnViewsMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(20) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (20, 'add_tier2_ecg_irn_views', ?)`, appliedAt)
	return err
}

func applyFloorsIntervalsViewMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(16) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (16, 'add_floors_intervals_view', ?)`, appliedAt)
	return err
}

func applyDataPointAttachmentsMigration(tx *sql.Tx, appliedAt string) error {
	if _, err := tx.Exec(`CREATE TABLE data_point_attachments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		data_point_id INTEGER NOT NULL,
		kind TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		path_relative TEXT NOT NULL,
		byte_size INTEGER NOT NULL,
		fetched_at TEXT NOT NULL,
		FOREIGN KEY (data_point_id) REFERENCES data_points(id)
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX data_point_attachments_dp_sha ON data_point_attachments (data_point_id, sha256)`); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (15, 'add_data_point_attachments', ?)`, appliedAt)
	return err
}

// applySearchableTextLatestProfileMigration drops the migration-13
// searchable_text view and recreates it from the Registry. The new
// definition restricts the profile kind to the latest snapshot per
// Connection and filters empty-string values from data_source and
// exercise_type rows (Copilot findings on PR #121).
func applySearchableTextLatestProfileMigration(tx *sql.Tx, appliedAt string) error {
	if _, err := tx.Exec(`DROP VIEW IF EXISTS searchable_text`); err != nil {
		return err
	}
	spec, ok := normalizedViewsRegistry().View("searchable-text")
	if !ok {
		return fmt.Errorf("searchable-text view missing from registry; cannot recreate")
	}
	if _, err := tx.Exec(exportDatasetViewMigrationStatement(spec)); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (14, 'fix_searchable_text_latest_profile_and_empty_filter', ?)`, appliedAt)
	return err
}

func applySearchableTextViewMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(13) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (13, 'add_searchable_text_view', ?)`, appliedAt)
	return err
}

// applyExerciseSplitsRealShapeMigration drops the migration-11 view that
// extracted distance from $.distanceMeters (a path Google Health API
// does not emit) and recreates it against the real shape:
// $.metricsSummary.distanceMillimeters in millimeters. Live testing in
// the original #105 PR returned all-NULL distances; this is the
// follow-up that pins the view to what the upstream actually returns.
func applyExerciseSplitsRealShapeMigration(tx *sql.Tx, appliedAt string) error {
	if _, err := tx.Exec(`DROP VIEW IF EXISTS exercise_splits`); err != nil {
		return err
	}
	spec, ok := normalizedViewsRegistry().View("exercise-splits")
	if !ok {
		return fmt.Errorf("exercise-splits view missing from registry; cannot recreate")
	}
	if _, err := tx.Exec(exportDatasetViewMigrationStatement(spec)); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (12, 'fix_exercise_splits_real_shape', ?)`, appliedAt)
	return err
}

func applySleepExerciseViewsMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(11) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (11, 'add_sleep_stages_and_exercise_splits_views', ?)`, appliedAt)
	return err
}

func applyCurrentIRNProfileViewMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(10) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (10, 'add_current_irn_profile_view', ?)`, appliedAt)
	return err
}

func applyPairedDevicesViewMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(9) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (9, 'add_paired_devices_view', ?)`, appliedAt)
	return err
}

func applyCurrentSettingsViewMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range normalizedViewsRegistry().MigrationStatements(8) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (8, 'add_current_settings_view', ?)`, appliedAt)
	return err
}

func applyIdentitySnapshotsMigration(tx *sql.Tx, appliedAt string) error {
	for _, statement := range identitySnapshotsMigrationStatements() {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (7, 'rename_profile_snapshots_to_identity_snapshots', ?)`, appliedAt)
	return err
}

// identitySnapshotsMigrationStatements renames profile_snapshots to
// identity_snapshots and adds the snapshot_kind discriminator. All
// existing rows keep snapshot_kind = 'profile' via the column default,
// preserving every prior profile snapshot's identity without a
// parallel-table-with-view shim (per the PRD §"identity_snapshots
// migration: explicit strategy").
func identitySnapshotsMigrationStatements() []string {
	return []string{
		`ALTER TABLE profile_snapshots RENAME TO identity_snapshots`,
		`ALTER TABLE identity_snapshots ADD COLUMN snapshot_kind TEXT NOT NULL DEFAULT 'profile'`,
	}
}

func syncCursorsMigrationStatements() []string {
	return []string{
		`CREATE TABLE sync_cursors (
			connection_id TEXT NOT NULL,
			data_type TEXT NOT NULL,
			source_family_filter TEXT NOT NULL DEFAULT '',
			rollup_kind TEXT NOT NULL DEFAULT 'none',
			cursor_time TEXT NOT NULL,
			advanced_at TEXT NOT NULL,
			PRIMARY KEY (connection_id, data_type, source_family_filter, rollup_kind),
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
	}
}

func expectedSchemaMigrations() map[int]string {
	return map[int]string{
		1: "initial_archive_schema",
		2: "add_google_identity_json",
		3: "add_source_family_filter",
		4: "add_daily_steps_view",
		5: "add_first_release_normalized_views",
		6: "add_sync_cursors",
		7: "rename_profile_snapshots_to_identity_snapshots",
		8: "add_current_settings_view",
		9:  "add_paired_devices_view",
		10: "add_current_irn_profile_view",
		11: "add_sleep_stages_and_exercise_splits_views",
		12: "fix_exercise_splits_real_shape",
		13: "add_searchable_text_view",
		14: "fix_searchable_text_latest_profile_and_empty_filter",
		15: "add_data_point_attachments",
		16: "add_floors_intervals_view",
		17: "add_tier1_activity_views",
		18: "add_tier1_health_metrics_views",
		19: "add_tier1_daily_hydration_views",
		20: "add_tier2_ecg_irn_views",
		21: "add_hydration_log_sessions_view",
	}
}

func initialMigrationStatements() []string {
	return []string{
		`CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`,
		`CREATE TABLE connections (
			id TEXT PRIMARY KEY,
			provider_name TEXT NOT NULL,
			google_health_user_id TEXT NOT NULL,
			legacy_fitbit_user_id TEXT,
			token_metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE data_points (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_name TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			data_type TEXT NOT NULL,
			upstream_resource_name TEXT,
			record_kind TEXT NOT NULL,
			start_time_utc TEXT,
			end_time_utc TEXT,
			start_civil_time TEXT,
			end_civil_time TEXT,
			provider_civil_date TEXT,
			timezone_metadata TEXT,
			data_source_json TEXT NOT NULL,
			raw_json TEXT NOT NULL,
			inserted_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
		`CREATE TABLE data_point_revisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			data_point_id INTEGER NOT NULL,
			previous_raw_json TEXT NOT NULL,
			replaced_at TEXT NOT NULL,
			replacement_reason TEXT,
			FOREIGN KEY (data_point_id) REFERENCES data_points(id)
		)`,
		`CREATE TABLE rollups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_name TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			data_type TEXT NOT NULL,
			rollup_kind TEXT NOT NULL,
			window_start_utc TEXT,
			window_end_utc TEXT,
			civil_date TEXT,
			timezone_metadata TEXT,
			raw_json TEXT NOT NULL,
			inserted_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
		`CREATE TABLE profile_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_name TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			raw_json TEXT NOT NULL,
			fetched_at TEXT NOT NULL,
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
		`CREATE TABLE sync_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_name TEXT NOT NULL,
			connection_id TEXT,
			data_types_requested TEXT NOT NULL,
			range_requested_json TEXT NOT NULL,
			endpoint_family TEXT NOT NULL,
			status TEXT NOT NULL,
			seen_count INTEGER NOT NULL DEFAULT 0,
			new_count INTEGER NOT NULL DEFAULT 0,
			updated_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			error_summary TEXT,
			FOREIGN KEY (connection_id) REFERENCES connections(id)
		)`,
	}
}

func dailyStepsViewMigrationStatements() []string {
	return normalizedViewsRegistry().MigrationStatements(4)
}

func firstReleaseNormalizedViewMigrationStatements() []string {
	return normalizedViewsRegistry().MigrationStatements(5)
}

func writeStatusResult(result statusResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if result.ArchivePath != "" {
			if _, err := fmt.Fprintf(stdout, "archive_path: %s\n", result.ArchivePath); err != nil {
				return err
			}
		}
		if result.SchemaVersion != 0 {
			if _, err := fmt.Fprintf(stdout, "schema_version: %d\n", result.SchemaVersion); err != nil {
				return err
			}
		}
		if err := writeStatusCounts(result, stdout); err != nil {
			return err
		}
		if len(result.DataTypes) != 0 {
			// Read the shared KnownDataTypes field instead of
			// re-synthesising from DataTypes — PRD #144 slice 9
			// guarantees both modes carry the same array.
			if _, err := fmt.Fprintf(stdout, "known_data_types: %s\n", strings.Join(result.KnownDataTypes, ",")); err != nil {
				return err
			}
			for _, dataType := range result.DataTypes {
				prefix := "data_type." + dataType.DataType + "."
				if _, err := fmt.Fprintf(stdout, "%sdata_point_count: %d\n", prefix, dataType.DataPointCount); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(stdout, "%srollup_count: %d\n", prefix, dataType.RollupCount); err != nil {
					return err
				}
				if dataType.NewestDataPointTimestamp != "" {
					if _, err := fmt.Fprintf(stdout, "%snewest_data_point_timestamp: %s\n", prefix, dataType.NewestDataPointTimestamp); err != nil {
						return err
					}
				}
				if dataType.NewestRollupTimestamp != "" {
					if _, err := fmt.Fprintf(stdout, "%snewest_rollup_timestamp: %s\n", prefix, dataType.NewestRollupTimestamp); err != nil {
						return err
					}
				}
				for index, cursor := range dataType.SyncCursors {
					cursorPrefix := fmt.Sprintf("%ssync_cursor.%d.", prefix, index)
					if _, err := fmt.Fprintf(stdout, "%srollup_kind: %s\n", cursorPrefix, cursor.RollupKind); err != nil {
						return err
					}
					if cursor.SourceFamilyFilter != "" {
						if _, err := fmt.Fprintf(stdout, "%ssource_family_filter: %s\n", cursorPrefix, cursor.SourceFamilyFilter); err != nil {
							return err
						}
					}
					if _, err := fmt.Fprintf(stdout, "%scursor_time: %s\n", cursorPrefix, cursor.CursorTime); err != nil {
						return err
					}
					if _, err := fmt.Fprintf(stdout, "%sadvanced_at: %s\n", cursorPrefix, cursor.AdvancedAt); err != nil {
						return err
					}
				}
			}
		}
		if result.IdentitySnapshotsFreshness != nil {
			if result.IdentitySnapshotsFreshness.PairedDeviceCount > 0 {
				if _, err := fmt.Fprintf(stdout, "paired_device_count: %d\n", result.IdentitySnapshotsFreshness.PairedDeviceCount); err != nil {
					return err
				}
			}
			// Emit kinds in a stable order so the output is reproducible.
			for _, kind := range []string{"profile", "settings", "paired-devices", "irn-profile"} {
				if ts, ok := result.IdentitySnapshotsFreshness.LatestFetchedAt[kind]; ok {
					if _, err := fmt.Fprintf(stdout, "identity_snapshot.%s.fetched_at: %s\n", kind, ts); err != nil {
						return err
					}
				}
			}
		}
		if result.Tier2 != nil {
			// Plain output omits Tier 2 lines when the scope has not
			// been granted — matches PR #128's omitted-when-missing
			// convention for snapshot kinds. JSON always carries the
			// block so downstream tooling sees a stable shape.
			if result.Tier2.ElectrocardiogramScopeGranted {
				if _, err := fmt.Fprintf(stdout, "electrocardiogram_event_count: %d\n", result.Tier2.ElectrocardiogramEventCount); err != nil {
					return err
				}
			}
			if result.Tier2.IrregularRhythmNotificationScopeGranted {
				if _, err := fmt.Fprintf(stdout, "irregular_rhythm_notification_count: %d\n", result.Tier2.IrregularRhythmNotificationCount); err != nil {
					return err
				}
			}
		}
		if err := writeStatusSyncRunPlain(stdout, "latest_successful_sync_run", result.LatestSuccessfulRun); err != nil {
			return err
		}
		if err := writeStatusSyncRunPlain(stdout, "latest_failed_sync_run", result.LatestFailedRun); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}

	if result.Status == "ok" {
		if _, err := fmt.Fprintln(stdout, "Health Archive status"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(stdout, "Health Archive status failed"); err != nil {
			return err
		}
	}
	if result.ArchivePath != "" {
		if _, err := fmt.Fprintf(stdout, "Health Archive: %s\n", result.ArchivePath); err != nil {
			return err
		}
	}
	if result.SchemaVersion != 0 {
		if _, err := fmt.Fprintf(stdout, "Schema version: %d\n", result.SchemaVersion); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "Counts: %d Data Points, %d Rollups, %d Identity Snapshots (%d Profile), %d Sync Runs\n", result.DataPointCount, result.RollupCount, result.IdentitySnapshotCount, result.ProfileSnapshotCount, result.SyncRunCount); err != nil {
		return err
	}
	if len(result.DataTypes) != 0 {
		if _, err := fmt.Fprintf(stdout, "Known Data Types: %s\n", strings.Join(statusDataTypeNames(result.DataTypes), ", ")); err != nil {
			return err
		}
		for _, dataType := range result.DataTypes {
			if _, err := fmt.Fprintf(stdout, "- %s: %d Data Points, %d Rollups", dataType.DataType, dataType.DataPointCount, dataType.RollupCount); err != nil {
				return err
			}
			if dataType.NewestDataPointTimestamp != "" {
				if _, err := fmt.Fprintf(stdout, ", newest Data Point %s", dataType.NewestDataPointTimestamp); err != nil {
					return err
				}
			}
			if dataType.NewestRollupTimestamp != "" {
				if _, err := fmt.Fprintf(stdout, ", newest Rollup %s", dataType.NewestRollupTimestamp); err != nil {
					return err
				}
			}
			for _, cursor := range dataType.SyncCursors {
				label := cursor.RollupKind
				if cursor.SourceFamilyFilter != "" {
					label = label + "/" + cursor.SourceFamilyFilter
				}
				if _, err := fmt.Fprintf(stdout, ", Sync Cursor (%s) %s", label, cursor.CursorTime); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(stdout); err != nil {
				return err
			}
		}
	}
	if result.LatestSuccessfulRun != nil {
		if _, err := fmt.Fprintf(stdout, "Latest successful Sync Run: %d (%s to %s)\n", result.LatestSuccessfulRun.ID, result.LatestSuccessfulRun.From, result.LatestSuccessfulRun.To); err != nil {
			return err
		}
	}
	if result.LatestFailedRun != nil {
		if _, err := fmt.Fprintf(stdout, "Latest failed Sync Run: %d (%s)\n", result.LatestFailedRun.ID, result.LatestFailedRun.ErrorSummary); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
}

func writeStatusCounts(result statusResult, stdout io.Writer) error {
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
		if _, err := fmt.Fprintf(stdout, "%s: %d\n", item.key, item.count); err != nil {
			return err
		}
	}
	return nil
}

func statusDataTypeNames(dataTypes []statusDataType) []string {
	names := make([]string, 0, len(dataTypes))
	for _, dataType := range dataTypes {
		names = append(names, dataType.DataType)
	}
	return names
}

func writeStatusSyncRunPlain(stdout io.Writer, prefix string, run *statusSyncRun) error {
	if run == nil {
		return nil
	}
	if _, err := fmt.Fprintf(stdout, "%s_id: %d\n", prefix, run.ID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "%s_status: %s\n", prefix, run.Status); err != nil {
		return err
	}
	if len(run.DataTypes) != 0 {
		if _, err := fmt.Fprintf(stdout, "%s_data_types: %s\n", prefix, strings.Join(run.DataTypes, ",")); err != nil {
			return err
		}
	}
	if run.From != "" {
		if _, err := fmt.Fprintf(stdout, "%s_from: %s\n", prefix, run.From); err != nil {
			return err
		}
	}
	if run.To != "" {
		if _, err := fmt.Fprintf(stdout, "%s_to: %s\n", prefix, run.To); err != nil {
			return err
		}
	}
	if run.EndpointFamily != "" {
		if _, err := fmt.Fprintf(stdout, "%s_endpoint_family: %s\n", prefix, run.EndpointFamily); err != nil {
			return err
		}
	}
	if run.SourceFamilyFilter != "" {
		if _, err := fmt.Fprintf(stdout, "%s_source_family_filter: %s\n", prefix, run.SourceFamilyFilter); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "%s_seen_count: %d\n", prefix, run.SeenCount); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "%s_new_count: %d\n", prefix, run.NewCount); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "%s_updated_count: %d\n", prefix, run.UpdatedCount); err != nil {
		return err
	}
	if run.StartedAt != "" {
		if _, err := fmt.Fprintf(stdout, "%s_started_at: %s\n", prefix, run.StartedAt); err != nil {
			return err
		}
	}
	if run.FinishedAt != "" {
		if _, err := fmt.Fprintf(stdout, "%s_finished_at: %s\n", prefix, run.FinishedAt); err != nil {
			return err
		}
	}
	if run.ErrorSummary != "" {
		if _, err := fmt.Fprintf(stdout, "%s_error_summary: %s\n", prefix, run.ErrorSummary); err != nil {
			return err
		}
	}
	return nil
}

func writeDoctorResult(result doctorResult, mode outputMode, stdout io.Writer) error {
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
		if result.CredentialStore != "" {
			if _, err := fmt.Fprintf(stdout, "credential_store: %s\n", result.CredentialStore); err != nil {
				return err
			}
		}
		if result.SchemaVersion != nil {
			if _, err := fmt.Fprintf(stdout, "schema_version: %d\n", *result.SchemaVersion); err != nil {
				return err
			}
		}
		if result.ConnectionCount != nil {
			if _, err := fmt.Fprintf(stdout, "connection_count: %d\n", *result.ConnectionCount); err != nil {
				return err
			}
		}
		if result.TokenStatus != "" {
			if _, err := fmt.Fprintf(stdout, "token_status: %s\n", result.TokenStatus); err != nil {
				return err
			}
		}
		if result.AttachmentRootPath != "" {
			if _, err := fmt.Fprintf(stdout, "attachment_root_path: %s\n", result.AttachmentRootPath); err != nil {
				return err
			}
			if result.AttachmentRootMode != "" {
				if _, err := fmt.Fprintf(stdout, "attachment_root_mode: %s\n", result.AttachmentRootMode); err != nil {
					return err
				}
			}
		}
		if result.Attachments != nil {
			if n := len(result.Attachments.OrphanFiles); n > 0 {
				if _, err := fmt.Fprintf(stdout, "attachments_orphan_files: %d\n", n); err != nil {
					return err
				}
			}
			if n := len(result.Attachments.OrphanRows); n > 0 {
				if _, err := fmt.Fprintf(stdout, "attachments_orphan_rows: %d\n", n); err != nil {
					return err
				}
			}
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}

	switch result.Status {
	case "ok":
		if _, err := fmt.Fprintln(stdout, "Setup ok"); err != nil {
			return err
		}
	case "connection_unhealthy":
		if _, err := fmt.Fprintln(stdout, "Connection unhealthy"); err != nil {
			return err
		}
	case "setup_invalid":
		if _, err := fmt.Fprintln(stdout, "Setup invalid"); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintln(stdout, "Setup missing"); err != nil {
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
	if result.CredentialStore != "" {
		if _, err := fmt.Fprintf(stdout, "Credential Store: %s\n", result.CredentialStore); err != nil {
			return err
		}
	}
	if result.SchemaVersion != nil {
		if _, err := fmt.Fprintf(stdout, "Schema version: %d\n", *result.SchemaVersion); err != nil {
			return err
		}
	}
	if result.ConnectionCount != nil {
		if _, err := fmt.Fprintf(stdout, "Connections: %d\n", *result.ConnectionCount); err != nil {
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
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if result.SyncRunID != 0 {
			if _, err := fmt.Fprintf(stdout, "sync_run_id: %d\n", result.SyncRunID); err != nil {
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
		if len(result.DataTypes) != 0 {
			if _, err := fmt.Fprintf(stdout, "data_types: %s\n", strings.Join(result.DataTypes, ",")); err != nil {
				return err
			}
		}
		if result.From != "" {
			if _, err := fmt.Fprintf(stdout, "from: %s\n", result.From); err != nil {
				return err
			}
		}
		if result.ResumedFromCursor {
			if _, err := fmt.Fprintln(stdout, "resumed_from_cursor: true"); err != nil {
				return err
			}
		}
		if result.To != "" {
			if _, err := fmt.Fprintf(stdout, "to: %s\n", result.To); err != nil {
				return err
			}
		}
		if result.EndpointFamily != "" {
			if _, err := fmt.Fprintf(stdout, "endpoint_family: %s\n", result.EndpointFamily); err != nil {
				return err
			}
		}
		if result.SourceFamily != "" {
			if _, err := fmt.Fprintf(stdout, "source_family: %s\n", result.SourceFamily); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "data_points_seen: %d\n", result.DataPointsSeen); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "data_points_new: %d\n", result.DataPointsNew); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "data_points_updated: %d\n", result.DataPointsUpdated); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "rollups_seen: %d\n", result.RollupsSeen); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "rollups_new: %d\n", result.RollupsNew); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "rollups_updated: %d\n", result.RollupsUpdated); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}
	switch result.Status {
	case "sync_completed":
		if _, err := fmt.Fprintln(stdout, "Sync Run completed"); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintln(stdout, "Sync Run failed"); err != nil {
			return err
		}
	}
	if result.SyncRunID != 0 {
		if _, err := fmt.Fprintf(stdout, "Sync Run: %d\n", result.SyncRunID); err != nil {
			return err
		}
	}
	if result.ConnectionID != "" {
		if _, err := fmt.Fprintf(stdout, "Connection: %s\n", result.ConnectionID); err != nil {
			return err
		}
	}
	if len(result.DataTypes) != 0 {
		if _, err := fmt.Fprintf(stdout, "Data Types: %s\n", strings.Join(result.DataTypes, ",")); err != nil {
			return err
		}
	}
	if result.From != "" || result.To != "" {
		if _, err := fmt.Fprintf(stdout, "Range: %s to %s\n", result.From, result.To); err != nil {
			return err
		}
	}
	if result.ResumedFromCursor {
		if _, err := fmt.Fprintln(stdout, "Resumed from Sync Cursor"); err != nil {
			return err
		}
	}
	if result.SourceFamily != "" {
		if _, err := fmt.Fprintf(stdout, "Source family: %s\n", result.SourceFamily); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "Data Points: seen %d, new %d, updated %d\n", result.DataPointsSeen, result.DataPointsNew, result.DataPointsUpdated); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "Rollups: seen %d, new %d, updated %d\n", result.RollupsSeen, result.RollupsNew, result.RollupsUpdated); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
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
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", status); err != nil {
			return err
		}
		for index, result := range results {
			prefix := fmt.Sprintf("results.%d.", index)
			if _, err := fmt.Fprintf(stdout, "%sstatus: %s\n", prefix, result.Status); err != nil {
				return err
			}
			if len(result.DataTypes) > 0 {
				if _, err := fmt.Fprintf(stdout, "%sdata_type: %s\n", prefix, result.DataTypes[0]); err != nil {
					return err
				}
			}
			if result.SyncRunID != 0 {
				if _, err := fmt.Fprintf(stdout, "%ssync_run_id: %d\n", prefix, result.SyncRunID); err != nil {
					return err
				}
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
				if _, err := fmt.Fprintf(stdout, "%s%s: %d\n", prefix, counter.key, counter.value); err != nil {
					return err
				}
			}
			if result.Message != "" {
				if _, err := fmt.Fprintf(stdout, "%smessage: %s\n", prefix, result.Message); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintf(stdout, "totals.data_points_seen: %d\n", summary.DataPointsSeen); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "totals.data_points_new: %d\n", summary.DataPointsNew); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "totals.data_points_updated: %d\n", summary.DataPointsUpdated); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "totals.rollups_seen: %d\n", summary.RollupsSeen); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "totals.rollups_new: %d\n", summary.RollupsNew); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "totals.rollups_updated: %d\n", summary.RollupsUpdated); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", message)
		return err
	}
	switch status {
	case "sync_completed":
		if _, err := fmt.Fprintf(stdout, "Sync Run fan-out completed across %d Data Types\n", len(results)); err != nil {
			return err
		}
	case "sync_canceled":
		if _, err := fmt.Fprintf(stdout, "Sync Run fan-out canceled across %d Data Types\n", len(results)); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintf(stdout, "Sync Run fan-out failed across %d Data Types\n", len(results)); err != nil {
			return err
		}
	}
	for _, result := range results {
		dataType := "?"
		if len(result.DataTypes) > 0 {
			dataType = result.DataTypes[0]
		}
		if _, err := fmt.Fprintf(stdout, "- %s: %s — Data Points new=%d updated=%d, Rollups new=%d updated=%d\n", dataType, result.Status, result.DataPointsNew, result.DataPointsUpdated, result.RollupsNew, result.RollupsUpdated); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "Totals: Data Points seen=%d new=%d updated=%d, Rollups seen=%d new=%d updated=%d\n", summary.DataPointsSeen, summary.DataPointsNew, summary.DataPointsUpdated, summary.RollupsSeen, summary.RollupsNew, summary.RollupsUpdated); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stdout, message)
	return err
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func defaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gohealthcli", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gohealthcli", "config.toml")
}

func defaultArchivePath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "gohealthcli", "gohealthcli.sqlite")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "gohealthcli", "gohealthcli.sqlite")
}

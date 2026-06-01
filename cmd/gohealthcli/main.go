package main

import (
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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const setupMissingExitCode = 2
const currentSchemaVersion = 2
const version = "dev"
const googleHealthActivityReadonlyScope = "https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly"
const googleHealthHealthMetricsReadonlyScope = "https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly"
const googleHealthSleepReadonlyScope = "https://www.googleapis.com/auth/googlehealth.sleep.readonly"
const googleHealthBaseURL = "https://health.googleapis.com/v4"
const googleHealthIdentityURL = "https://health.googleapis.com/v4/users/me/identity"
const googleHealthRawResponseLimit = 10 << 20

var defaultDataTypes = []string{
	"steps",
	"heart-rate",
	"daily-resting-heart-rate",
	"heart-rate-variability",
	"daily-heart-rate-variability",
	"oxygen-saturation",
	"daily-oxygen-saturation",
	"daily-respiratory-rate",
	"sleep",
	"exercise",
	"distance",
	"total-calories",
	"weight",
}

type doctorResult struct {
	Status            string `json:"status"`
	ConfigPath        string `json:"config_path"`
	ArchivePath       string `json:"archive_path"`
	OAuthClientSource string `json:"oauth_client_source"`
	CredentialStore   string `json:"credential_store"`
	SchemaVersion     *int   `json:"schema_version"`
	ConnectionCount   *int   `json:"connection_count"`
	TokenStatus       string `json:"token_status"`
	Message           string `json:"message"`
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

type archivedConnection struct {
	id                 string
	providerName       string
	googleHealthUserID string
	legacyFitbitUserID string
	tokenMetadataJSON  string
}

type rawProviderRequest struct {
	endpointName   string
	dataType       string
	url            string
	requiredScopes []string
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

var runOAuthFlow = runBrowserOAuthFlow
var fetchIdentity = fetchGoogleIdentity
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
	flags := flag.NewFlagSet("gohealthcli", flag.ContinueOnError)
	flags.SetOutput(stderr)

	configPath := flags.String("config", defaultConfigPath(), "config file path")
	archivePath := flags.String("db", defaultArchivePath(), "SQLite Health Archive path")
	jsonOutput := flags.Bool("json", false, "write stable JSON to stdout")
	plainOutput := flags.Bool("plain", false, "write plain key/value output to stdout")
	noInput := flags.Bool("no-input", false, "never prompt, never wait for browser input")
	versionOutput := flags.Bool("version", false, "print version and exit")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}

	if *versionOutput {
		fmt.Fprintf(stdout, "gohealthcli %s\n", version)
		return 0
	}

	if flags.NArg() == 0 {
		fmt.Fprintln(stderr, "missing command")
		return 1
	}

	switch flags.Arg(0) {
	case "doctor":
		return runDoctor(flags.Args()[1:], *configPath, *archivePath, outputMode{json: *jsonOutput, plain: *plainOutput}, stdout, stderr)
	case "init":
		return runInit(flags.Args()[1:], *configPath, *archivePath, outputMode{json: *jsonOutput, plain: *plainOutput}, stdout, stderr)
	case "connect":
		return runConnect(flags.Args()[1:], *configPath, *archivePath, *noInput, outputMode{json: *jsonOutput, plain: *plainOutput}, stdout, stderr)
	case "identity":
		return runIdentity(flags.Args()[1:], *configPath, *archivePath, outputMode{json: *jsonOutput, plain: *plainOutput}, stdout, stderr)
	case "raw":
		return runRaw(flags.Args()[1:], *configPath, *archivePath, outputMode{json: *jsonOutput, plain: *plainOutput}, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", flags.Arg(0))
		return 1
	}
}

type outputMode struct {
	json  bool
	plain bool
}

func runDoctor(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)

	doctorConfigPath := flags.String("config", configPath, "config file path")
	doctorArchivePath := flags.String("db", archivePath, "SQLite Health Archive path")
	doctorJSONOutput := flags.Bool("json", mode.json, "write stable JSON to stdout")
	doctorPlainOutput := flags.Bool("plain", mode.plain, "write plain key/value output to stdout")
	flags.Bool("no-input", false, "never prompt, never wait for browser input")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected doctor argument: %s\n", flags.Arg(0))
		return 1
	}

	mode = outputMode{json: *doctorJSONOutput, plain: *doctorPlainOutput}
	if fileExists(*doctorConfigPath) && fileExists(*doctorArchivePath) {
		config, err := inspectConfig(*doctorConfigPath, *doctorArchivePath)
		if err != nil {
			return runDoctorInvalid(*doctorConfigPath, *doctorArchivePath, fmt.Sprintf("config check failed: %v", err), mode, stdout, stderr)
		}
		if err := migrateArchiveIfNeeded(*doctorArchivePath); err != nil {
			return runDoctorInvalid(*doctorConfigPath, *doctorArchivePath, fmt.Sprintf("Health Archive migration failed: %v", err), mode, stdout, stderr)
		}
		archive, err := inspectArchive(*doctorArchivePath, true)
		if err != nil {
			return runDoctorInvalid(*doctorConfigPath, *doctorArchivePath, fmt.Sprintf("Health Archive check failed: %v", err), mode, stdout, stderr)
		}
		result := doctorResult{
			Status:            "ok",
			ConfigPath:        *doctorConfigPath,
			ArchivePath:       *doctorArchivePath,
			OAuthClientSource: config.oauthClientSource,
			CredentialStore:   config.credentialStore,
			SchemaVersion:     &archive.schemaVersion,
			ConnectionCount:   &archive.connectionCount,
			TokenStatus:       archive.tokenStatus,
			Message:           "local gohealthcli setup is initialized",
		}
		if err := writeDoctorResult(result, mode, stdout); err != nil {
			fmt.Fprintf(stderr, "write output: %v\n", err)
			return 1
		}
		return 0
	}
	if fileExists(*doctorConfigPath) || fileExists(*doctorArchivePath) {
		return runDoctorInvalid(*doctorConfigPath, *doctorArchivePath, "partial local setup found; run `gohealthcli init` after moving existing config or Health Archive", mode, stdout, stderr)
	}

	result := doctorResult{
		Status:      "setup_missing",
		ConfigPath:  *doctorConfigPath,
		ArchivePath: *doctorArchivePath,
		TokenStatus: "unknown",
		Message:     "local gohealthcli setup not found",
	}

	if err := writeDoctorResult(result, mode, stdout); err != nil {
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return 1
	}

	fmt.Fprintln(stderr, "run `gohealthcli init` to create local config and Health Archive")
	return setupMissingExitCode
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
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return 1
	}
	return 1
}

func runInit(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)

	initConfigPath := flags.String("config", configPath, "config file path")
	initArchivePath := flags.String("db", archivePath, "SQLite Health Archive path")
	initJSONOutput := flags.Bool("json", mode.json, "write stable JSON to stdout")
	initPlainOutput := flags.Bool("plain", mode.plain, "write plain key/value output to stdout")
	flags.Bool("no-input", false, "never prompt, never wait for browser input")
	oauthClientFile := flags.String("oauth-client-file", "", "OAuth client JSON file reference")
	secretProvider := flags.String("secret-provider", "", "Secret Provider name for OAuth client setup")
	oauthClientItem := flags.String("oauth-client-item", "", "Secret Provider item name for OAuth client setup")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected init argument: %s\n", flags.Arg(0))
		return 1
	}

	mode = outputMode{json: *initJSONOutput, plain: *initPlainOutput}
	if fileExists(*initConfigPath) && fileExists(*initArchivePath) {
		if err := validateConfig(*initConfigPath, *initArchivePath); err != nil {
			fmt.Fprintf(stderr, "existing config is not initialized: %v\n", err)
			return 1
		}
		if err := migrateArchiveIfNeeded(*initArchivePath); err != nil {
			fmt.Fprintf(stderr, "existing Health Archive is not initialized: %v\n", err)
			return 1
		}
		if err := validateArchive(*initArchivePath); err != nil {
			fmt.Fprintf(stderr, "existing Health Archive is not initialized: %v\n", err)
			return 1
		}
		result := initResult{
			Status:           "already_initialized",
			ConfigPath:       *initConfigPath,
			ArchivePath:      *initArchivePath,
			DefaultDataTypes: defaultDataTypes,
			SchemaVersion:    currentSchemaVersion,
			Message:          "local gohealthcli setup already exists",
		}
		if err := writeInitResult(result, mode, stdout); err != nil {
			fmt.Fprintf(stderr, "write output: %v\n", err)
			return 1
		}
		return 0
	}
	if fileExists(*initConfigPath) || fileExists(*initArchivePath) {
		fmt.Fprintln(stderr, "refusing to overwrite partial local setup; move existing config or Health Archive first")
		return 1
	}

	source, err := parseOAuthClientSource(*oauthClientFile, *secretProvider, *oauthClientItem)
	if err != nil {
		fmt.Fprintf(stderr, "init: %v\n", err)
		return 1
	}
	if err := validateOAuthClientConfig(source); err != nil {
		fmt.Fprintf(stderr, "init: %v\n", err)
		return 1
	}

	if err := createConfigFile(*initConfigPath, *initArchivePath, source); err != nil {
		fmt.Fprintf(stderr, "create config: %v\n", err)
		return 1
	}
	if err := createArchive(*initArchivePath); err != nil {
		_ = os.Remove(*initConfigPath)
		_ = os.Remove(*initArchivePath)
		fmt.Fprintf(stderr, "create Health Archive: %v\n", err)
		return 1
	}

	result := initResult{
		Status:            "initialized",
		ConfigPath:        *initConfigPath,
		ArchivePath:       *initArchivePath,
		OAuthClientSource: source.kind,
		DefaultDataTypes:  defaultDataTypes,
		SchemaVersion:     currentSchemaVersion,
	}
	if err := writeInitResult(result, mode, stdout); err != nil {
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return 1
	}
	return 0
}

func runConnect(args []string, configPath, archivePath string, globalNoInput bool, mode outputMode, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(stderr)

	connectConfigPath := flags.String("config", configPath, "config file path")
	connectArchivePath := flags.String("db", archivePath, "SQLite Health Archive path")
	connectJSONOutput := flags.Bool("json", mode.json, "write stable JSON to stdout")
	connectPlainOutput := flags.Bool("plain", mode.plain, "write plain key/value output to stdout")
	noInput := flags.Bool("no-input", globalNoInput, "never prompt, never wait for browser input")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected connect argument: %s\n", flags.Arg(0))
		return 1
	}

	mode = outputMode{json: *connectJSONOutput, plain: *connectPlainOutput}
	result, err := connectSetup(*connectConfigPath, *connectArchivePath, *noInput)
	if err != nil {
		result.Status = "connect_failed"
		result.Message = err.Error()
		if writeErr := writeConnectResult(result, mode, stdout); writeErr != nil {
			fmt.Fprintf(stderr, "write output: %v\n", writeErr)
			return 1
		}
		return 1
	}
	if err := writeConnectResult(result, mode, stdout); err != nil {
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return 1
	}
	return 0
}

func runIdentity(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("identity", flag.ContinueOnError)
	flags.SetOutput(stderr)

	identityConfigPath := flags.String("config", configPath, "config file path")
	identityArchivePath := flags.String("db", archivePath, "SQLite Health Archive path")
	identityJSONOutput := flags.Bool("json", mode.json, "write stable JSON to stdout")
	identityPlainOutput := flags.Bool("plain", mode.plain, "write plain key/value output to stdout")
	flags.Bool("no-input", false, "never prompt, never wait for browser input")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected identity argument: %s\n", flags.Arg(0))
		return 1
	}

	mode = outputMode{json: *identityJSONOutput, plain: *identityPlainOutput}
	result, err := identitySetup(*identityConfigPath, *identityArchivePath)
	if err != nil {
		if result.Status == "" {
			result.Status = "identity_failed"
		}
		result.Message = err.Error()
		if writeErr := writeIdentityResult(result, mode, stdout); writeErr != nil {
			fmt.Fprintf(stderr, "write output: %v\n", writeErr)
			return 1
		}
		return 1
	}
	if err := writeIdentityResult(result, mode, stdout); err != nil {
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return 1
	}
	return 0
}

func runRaw(args []string, configPath, archivePath string, _ outputMode, stdout, stderr io.Writer) int {
	options, err := parseRawCommandOptions(args, configPath, archivePath)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, "usage: gohealthcli raw endpoint getIdentity")
			fmt.Fprintln(stdout, "usage: gohealthcli raw endpoint dataTypes.<data-type>.list --from YYYY-MM-DD [--to YYYY-MM-DD]")
			fmt.Fprintln(stdout, "usage: gohealthcli raw data-type <data-type> --from YYYY-MM-DD [--to YYYY-MM-DD]")
			return 0
		}
		fmt.Fprintf(stderr, "raw: %v\n", err)
		return 1
	}
	request, err := buildGoogleHealthRawRequest(options.target, options.from, options.to, options.pageSize, options.pageToken)
	if err != nil {
		fmt.Fprintf(stderr, "raw: %v\n", err)
		return 1
	}
	body, err := rawSetup(options.configPath, options.archivePath, request)
	if err != nil {
		fmt.Fprintf(stderr, "raw: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(body); err != nil {
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return 1
	}
	return 0
}

func connectSetup(configPath, archivePath string, noInput bool) (connectResult, error) {
	config, err := inspectFullConfig(configPath, archivePath)
	if err != nil {
		return connectResult{}, fmt.Errorf("config check failed: %w", err)
	}
	if config.oauthClient.kind != "file" {
		return connectResult{CredentialStore: config.credentialStore.kind}, errors.New("connect requires an OAuth client file source; Secret Provider references are setup-only")
	}
	if err := migrateArchiveIfNeeded(archivePath); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, fmt.Errorf("Health Archive migration failed: %w", err)
	}
	if _, err := inspectArchive(archivePath, false); err != nil {
		return connectResult{}, fmt.Errorf("Health Archive check failed: %w", err)
	}
	store, err := newCredentialStore(config.credentialStore)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := validateCredentialStoreRuntime(config.credentialStore, []string{configPath, archivePath, config.oauthClient.path}); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	client, err := loadOAuthClientConfig(config.oauthClient.path)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	token, err := runOAuthFlow(client, oauthScopesForDataTypes(config.defaultDataTypes), noInput)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	identity, err := fetchIdentity(token.accessToken)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	connectionID := "googlehealth:" + identity.healthUserID

	db, err := openArchive(archivePath)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	defer db.Close()
	if err := ensureSameArchiveIdentity(db, identity.healthUserID); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := store.Store(connectionID, token.rawTokenMaterialObject); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := upsertConnection(db, connectionID, identity, token, currentTime()); err != nil {
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
	config, err := inspectIdentityConfig(configPath, archivePath)
	if err != nil {
		return identityResult{}, fmt.Errorf("config check failed: %w", err)
	}
	if err := migrateArchiveIfNeeded(archivePath); err != nil {
		return identityResult{}, fmt.Errorf("Health Archive migration failed: %w", err)
	}
	if _, err := inspectArchive(archivePath, false); err != nil {
		return identityResult{}, fmt.Errorf("Health Archive check failed: %w", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		return identityResult{}, err
	}
	defer db.Close()
	connection, err := readCurrentConnection(db)
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
	if err := requireUsableConnectionAccessToken(connection.tokenMetadataJSON, currentTime()); err != nil {
		return result, err
	}
	if err := validateCredentialStoreRuntime(config.credentialStore, []string{configPath, archivePath}); err != nil {
		return result, err
	}
	store, err := newCredentialStore(config.credentialStore)
	if err != nil {
		return result, err
	}
	tokenMaterial, err := store.Load(connection.id)
	if err != nil {
		return result, err
	}
	accessToken, ok := tokenMaterial["access_token"].(string)
	if !ok || accessToken == "" {
		return result, errors.New("Credential Store token material is missing access token; run `gohealthcli connect` again")
	}
	identity, err := fetchIdentity(accessToken)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 401") {
			return result, errors.New("Google Health rejected stored Connection token; run `gohealthcli connect` again")
		}
		return result, err
	}
	if identity.healthUserID != connection.googleHealthUserID {
		result.Status = "identity_mismatch"
		return result, errors.New("Provider returned a different Google Identity; use a new archive path")
	}
	if err := refreshConnectionIdentity(db, connection, identity, currentTime()); err != nil {
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

func rawSetup(configPath, archivePath string, request rawProviderRequest) ([]byte, error) {
	config, err := inspectIdentityConfig(configPath, archivePath)
	if err != nil {
		return nil, fmt.Errorf("config check failed: %w", err)
	}
	if err := migrateArchiveIfNeeded(archivePath); err != nil {
		return nil, fmt.Errorf("Health Archive migration failed: %w", err)
	}
	if _, err := inspectArchive(archivePath, false); err != nil {
		return nil, fmt.Errorf("Health Archive check failed: %w", err)
	}
	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	connection, err := readCurrentConnection(db)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("no Connection found; run `gohealthcli connect` first")
		}
		return nil, err
	}
	if err := requireUsableConnectionAccessToken(connection.tokenMetadataJSON, currentTime()); err != nil {
		return nil, err
	}
	if err := requireConnectionScopes(connection.tokenMetadataJSON, request.requiredScopes); err != nil {
		return nil, err
	}
	accessToken, err := loadAccessTokenForConnection(config.credentialStore, connection, []string{configPath, archivePath})
	if err != nil {
		return nil, err
	}
	body, err := fetchRawProvider(request, accessToken)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 401") {
			return nil, errors.New("Google Health rejected stored Connection token; run `gohealthcli connect` again")
		}
		return nil, err
	}
	return body, nil
}

func loadAccessTokenForConnection(config credentialStoreConfig, connection archivedConnection, protectedPaths []string) (string, error) {
	if err := validateCredentialStoreRuntime(config, protectedPaths); err != nil {
		return "", err
	}
	store, err := newCredentialStore(config)
	if err != nil {
		return "", err
	}
	tokenMaterial, err := store.Load(connection.id)
	if err != nil {
		return "", err
	}
	accessToken, ok := tokenMaterial["access_token"].(string)
	if !ok || accessToken == "" {
		return "", errors.New("Credential Store token material is missing access token; run `gohealthcli connect` again")
	}
	return accessToken, nil
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
	for _, requiredScope := range requiredScopes {
		if _, ok := granted[requiredScope]; !ok {
			return fmt.Errorf("Connection token is missing required Google Health scope %s; run `gohealthcli connect` again", requiredScope)
		}
	}
	return nil
}

func parseRawCommandOptions(args []string, configPath, archivePath string) (rawCommandOptions, error) {
	options := rawCommandOptions{configPath: configPath, archivePath: archivePath}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-h" || arg == "--help":
			return rawCommandOptions{}, flag.ErrHelp
		case arg == "--json" || arg == "--plain" || arg == "--no-input":
		case arg == "--config" || arg == "--db" || arg == "--from" || arg == "--to" || arg == "--page-size" || arg == "--page-token":
			index++
			if index >= len(args) {
				return rawCommandOptions{}, fmt.Errorf("%s requires a value", arg)
			}
			if err := setRawCommandOption(&options, arg, args[index]); err != nil {
				return rawCommandOptions{}, err
			}
		case strings.HasPrefix(arg, "--config="):
			if err := setRawCommandOption(&options, "--config", strings.TrimPrefix(arg, "--config=")); err != nil {
				return rawCommandOptions{}, err
			}
		case strings.HasPrefix(arg, "--db="):
			if err := setRawCommandOption(&options, "--db", strings.TrimPrefix(arg, "--db=")); err != nil {
				return rawCommandOptions{}, err
			}
		case strings.HasPrefix(arg, "--from="):
			if err := setRawCommandOption(&options, "--from", strings.TrimPrefix(arg, "--from=")); err != nil {
				return rawCommandOptions{}, err
			}
		case strings.HasPrefix(arg, "--to="):
			if err := setRawCommandOption(&options, "--to", strings.TrimPrefix(arg, "--to=")); err != nil {
				return rawCommandOptions{}, err
			}
		case strings.HasPrefix(arg, "--page-size="):
			if err := setRawCommandOption(&options, "--page-size", strings.TrimPrefix(arg, "--page-size=")); err != nil {
				return rawCommandOptions{}, err
			}
		case strings.HasPrefix(arg, "--page-token="):
			if err := setRawCommandOption(&options, "--page-token", strings.TrimPrefix(arg, "--page-token=")); err != nil {
				return rawCommandOptions{}, err
			}
		case strings.HasPrefix(arg, "-"):
			return rawCommandOptions{}, fmt.Errorf("unknown flag %s", arg)
		default:
			options.target = append(options.target, arg)
		}
	}
	return options, nil
}

func setRawCommandOption(options *rawCommandOptions, name, value string) error {
	switch name {
	case "--config":
		options.configPath = value
	case "--db":
		options.archivePath = value
	case "--from":
		options.from = value
	case "--to":
		options.to = value
	case "--page-size":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return errors.New("--page-size must be a positive integer")
		}
		options.pageSize = parsed
	case "--page-token":
		options.pageToken = value
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
		if secretProvider == "" || oauthClientItem == "" {
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
	if err := ensureOwnerOnlyDir(filepath.Dir(archivePath)); err != nil {
		return err
	}
	file, err := os.OpenFile(archivePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(archivePath)
		}
	}()
	if err := file.Close(); err != nil {
		return err
	}

	db, err := openArchive(archivePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := applyMigrations(db); err != nil {
		return err
	}
	if !usesPOSIXPermissions() {
		return nil
	}
	return os.Chmod(archivePath, 0o600)
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
		credentialStore:  config.credentialStore,
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
		switch currentOS {
		case "darwin":
			if _, err := findExecutable("security"); err != nil {
				return errors.New("OS-native Credential Store requires the security command; configure credential_store type \"file\"")
			}
		case "linux":
			if _, err := findExecutable("secret-tool"); err != nil {
				return errors.New("OS-native Credential Store requires secret-tool; install libsecret tooling or configure credential_store type \"file\"")
			}
		case "windows":
			if _, err := findExecutable("powershell"); err != nil {
				if _, err := findExecutable("powershell.exe"); err != nil {
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
	allowed := make(map[string]struct{}, len(defaultDataTypes))
	for _, dataType := range defaultDataTypes {
		allowed[dataType] = struct{}{}
	}
	seen := make(map[string]struct{}, len(dataTypes))
	for _, dataType := range dataTypes {
		if _, ok := allowed[dataType]; !ok {
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
	if err := json.Unmarshal(content, &raw); err != nil {
		return oauthClientConfig{}, errors.New("OAuth client file must contain a JSON object")
	}
	if len(raw) == 0 {
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
	}
	scopes := make([]string, 0, len(needed))
	for _, scope := range ordered {
		if _, ok := needed[scope]; ok {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

func googleHealthScopesForDataType(dataType string) []string {
	switch dataType {
	case "steps", "distance", "exercise", "total-calories":
		return []string{googleHealthActivityReadonlyScope}
	case "heart-rate", "heart-rate-variability", "daily-heart-rate-variability", "daily-resting-heart-rate", "oxygen-saturation", "daily-oxygen-saturation", "daily-respiratory-rate", "weight":
		return []string{googleHealthHealthMetricsReadonlyScope}
	case "sleep":
		return []string{googleHealthSleepReadonlyScope}
	default:
		return nil
	}
}

func runBrowserOAuthFlow(client oauthClientConfig, scopes []string, noInput bool) (oauthTokenResponse, error) {
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
	if err := openBrowser(authURL); err != nil {
		return oauthTokenResponse{}, fmt.Errorf("open browser: %w", err)
	}
	code, err := waitForOAuthCode(listener, state)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	return exchangeOAuthCode(client, redirectURI, code, verifier)
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
	return parseOAuthTokenResponse(body, currentTime())
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
		if target[1] == "getIdentity" {
			return rawProviderRequest{endpointName: "getIdentity", url: googleHealthIdentityURL}, nil
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

func googleHealthDataTypeListFilterField(dataType string) (string, error) {
	filterDataType := strings.ReplaceAll(dataType, "-", "_")
	switch dataType {
	case "daily-resting-heart-rate", "daily-heart-rate-variability", "daily-oxygen-saturation", "daily-respiratory-rate":
		return filterDataType + ".date", nil
	case "steps", "distance":
		return filterDataType + ".interval.start_time", nil
	case "heart-rate", "heart-rate-variability", "oxygen-saturation", "weight":
		return filterDataType + ".sample_time.physical_time", nil
	case "exercise":
		return filterDataType + ".interval.civil_start_time", nil
	case "sleep":
		return filterDataType + ".interval.end_time", nil
	default:
		return "", fmt.Errorf("raw Data Type %q is not supported by dataPoints.list", dataType)
	}
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
	httpRequest, err := http.NewRequest(http.MethodGet, request.url, nil)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+accessToken)
	httpRequest.Header.Set("Accept", "application/json")
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
		return nil, fmt.Errorf("Google Health raw request failed with HTTP %d", response.StatusCode)
	}
	if tooLarge {
		return nil, fmt.Errorf("Google Health raw response exceeds %d bytes; narrow the raw request", googleHealthRawResponseLimit)
	}
	return body, nil
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
	switch config.kind {
	case "file":
		return fileCredentialStore{path: config.path}, nil
	case "os_native":
		if currentOS != "darwin" && currentOS != "linux" && currentOS != "windows" {
			return nil, errors.New("OS-native Credential Store is not available on this platform; configure credential_store type \"file\"")
		}
		return osNativeCredentialStore{service: config.service}, nil
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
}

func (store osNativeCredentialStore) Store(key string, tokenMaterial map[string]any) error {
	content, err := json.Marshal(tokenMaterial)
	if err != nil {
		return err
	}
	switch currentOS {
	case "darwin":
		return runSecurityAddGenericPassword(store.service, key, content)
	case "linux":
		return runSecretToolStore(store.service, key, content)
	case "windows":
		return runWindowsCredentialWrite(store.service, key, content)
	default:
		return errors.New("OS-native Credential Store is not available on this platform; configure credential_store type \"file\"")
	}
}

func (store osNativeCredentialStore) Load(key string) (map[string]any, error) {
	var content []byte
	var err error
	switch currentOS {
	case "darwin":
		content, err = runSecurityFindGenericPassword(store.service, key)
	case "linux":
		content, err = runSecretToolLookup(store.service, key)
	case "windows":
		content, err = runWindowsCredentialRead(store.service, key)
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

func ensureSameArchiveIdentity(db *sql.DB, healthUserID string) error {
	rows, err := db.Query(`SELECT DISTINCT google_health_user_id FROM connections`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return err
		}
		if existing != healthUserID {
			return errors.New("Health Archive already belongs to a different Google Identity; use a new archive path")
		}
	}
	return rows.Err()
}

func readCurrentConnection(db *sql.DB) (archivedConnection, error) {
	rows, err := db.Query(`SELECT
		id,
		provider_name,
		google_health_user_id,
		legacy_fitbit_user_id,
		token_metadata_json
	FROM connections ORDER BY created_at, id LIMIT 2`)
	if err != nil {
		return archivedConnection{}, err
	}
	defer rows.Close()

	var connections []archivedConnection
	for rows.Next() {
		var connection archivedConnection
		var legacyFitbitUserID sql.NullString
		if err := rows.Scan(
			&connection.id,
			&connection.providerName,
			&connection.googleHealthUserID,
			&legacyFitbitUserID,
			&connection.tokenMetadataJSON,
		); err != nil {
			return archivedConnection{}, err
		}
		if legacyFitbitUserID.Valid {
			connection.legacyFitbitUserID = legacyFitbitUserID.String
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return archivedConnection{}, err
	}
	if len(connections) == 0 {
		return archivedConnection{}, sql.ErrNoRows
	}
	if len(connections) > 1 {
		return archivedConnection{}, errors.New("multiple Connections found; use a separate Health Archive for each Google Identity")
	}
	return connections[0], nil
}

func upsertConnection(db *sql.DB, connectionID string, identity googleIdentity, token oauthTokenResponse, now time.Time) error {
	metadata := map[string]any{
		"credential_store_key": connectionID,
		"expires_at":           token.expiresAt.UTC().Format(time.RFC3339),
		"scopes":               token.scopes,
		"token_type":           token.tokenType,
	}
	if token.refreshTokenExpiresAt != nil {
		metadata["refresh_expires_at"] = token.refreshTokenExpiresAt.UTC().Format(time.RFC3339)
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	nowText := now.UTC().Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO connections (
		id,
		provider_name,
		google_health_user_id,
		legacy_fitbit_user_id,
		token_metadata_json,
		google_identity_json,
		created_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		legacy_fitbit_user_id = excluded.legacy_fitbit_user_id,
		token_metadata_json = excluded.token_metadata_json,
		google_identity_json = excluded.google_identity_json,
		updated_at = excluded.updated_at`,
		connectionID,
		"googlehealth",
		identity.healthUserID,
		identity.legacyFitbitUserID,
		string(metadataJSON),
		identity.rawJSON,
		nowText,
		nowText,
	)
	return err
}

func refreshConnectionIdentity(db *sql.DB, connection archivedConnection, identity googleIdentity, now time.Time) error {
	legacyFitbitUserID := connection.legacyFitbitUserID
	if identity.legacyFitbitUserID != "" {
		legacyFitbitUserID = identity.legacyFitbitUserID
	}
	_, err := db.Exec(`UPDATE connections SET
		google_health_user_id = ?,
		legacy_fitbit_user_id = ?,
		google_identity_json = ?,
		updated_at = ?
	WHERE id = ?`,
		identity.healthUserID,
		legacyFitbitUserID,
		identity.rawJSON,
		now.UTC().Format(time.RFC3339),
		connection.id,
	)
	return err
}

func validateArchive(archivePath string) error {
	_, err := inspectArchive(archivePath, false)
	return err
}

func inspectArchive(archivePath string, validateTokens bool) (archiveCheck, error) {
	if err := validateOwnerOnlyDir(filepath.Dir(archivePath)); err != nil {
		return archiveCheck{}, err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return archiveCheck{}, err
	}
	if info.IsDir() {
		return archiveCheck{}, fmt.Errorf("%s is a directory", archivePath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		mode := info.Mode().Perm()
		return archiveCheck{}, fmt.Errorf("%s is not owner-only: mode %04o, want 0600", archivePath, mode)
	}

	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		return archiveCheck{}, err
	}
	defer db.Close()

	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		return archiveCheck{}, err
	}
	if userVersion != currentSchemaVersion {
		return archiveCheck{}, fmt.Errorf("schema version %d, want %d", userVersion, currentSchemaVersion)
	}

	for version, name := range expectedSchemaMigrations() {
		var migrationCount int
		if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = ? AND name = ?`, version, name).Scan(&migrationCount); err != nil {
			return archiveCheck{}, err
		}
		if migrationCount != 1 {
			return archiveCheck{}, fmt.Errorf("missing schema migration %d", version)
		}
	}
	if !validateTokens {
		return archiveCheck{schemaVersion: userVersion}, nil
	}
	count, tokenStatus, err := inspectConnectionTokenMetadata(db)
	if err != nil {
		return archiveCheck{}, err
	}
	return archiveCheck{
		schemaVersion:   userVersion,
		connectionCount: count,
		tokenStatus:     tokenStatus,
	}, nil
}

func inspectConnectionTokenMetadata(db *sql.DB) (int, string, error) {
	rows, err := db.Query(`SELECT id, token_metadata_json FROM connections ORDER BY id`)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var connectionID string
		var metadata string
		if err := rows.Scan(&connectionID, &metadata); err != nil {
			return 0, "", err
		}
		count++
		if err := validateTokenMetadata(metadata); err != nil {
			return 0, "", fmt.Errorf("Connection %s: %w", connectionID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	if count == 0 {
		return 0, "not_connected", nil
	}
	return count, "metadata_present", nil
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

func requireUsableConnectionAccessToken(metadata string, now time.Time) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		return errors.New("Connection token metadata is not valid JSON; run `gohealthcli connect` again")
	}
	expiresAtText, err := requireJSONString(raw, "expires_at")
	if err != nil {
		return errors.New("Connection token metadata is incomplete; run `gohealthcli connect` again")
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtText)
	if err != nil {
		return errors.New("Connection token expiry is invalid; run `gohealthcli connect` again")
	}
	if !expiresAt.After(now.UTC()) {
		return errors.New("Connection token has expired; run `gohealthcli connect` again")
	}
	return nil
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
	if _, err := tx.Exec(`PRAGMA user_version = 2`); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateArchiveIfNeeded(archivePath string) error {
	if err := validateOwnerOnlyDir(filepath.Dir(archivePath)); err != nil {
		return err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", archivePath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%s is not owner-only: mode %04o, want 0600", archivePath, info.Mode().Perm())
	}

	db, err := openArchive(archivePath)
	if err != nil {
		return err
	}
	defer db.Close()
	return applyPendingMigrations(db)
}

func applyPendingMigrations(db *sql.DB) error {
	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		return err
	}
	switch userVersion {
	case currentSchemaVersion:
		return nil
	case 1:
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		now := time.Now().UTC().Format(time.RFC3339)
		if err := applyGoogleIdentityArchiveMigration(tx, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`PRAGMA user_version = 2`); err != nil {
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

func expectedSchemaMigrations() map[int]string {
	return map[int]string{
		1: "initial_archive_schema",
		2: "add_google_identity_json",
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
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}

	switch result.Status {
	case "ok":
		if _, err := fmt.Fprintln(stdout, "Setup ok"); err != nil {
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

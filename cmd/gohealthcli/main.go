package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const setupMissingExitCode = 2
const currentSchemaVersion = 1
const version = "dev"

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
	SchemaVersion     int    `json:"schema_version"`
	ConnectionCount   int    `json:"connection_count"`
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

type archiveCheck struct {
	schemaVersion   int
	connectionCount int
	tokenStatus     string
}

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
	flags.Bool("no-input", false, "never prompt, never wait for browser input")
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
			SchemaVersion:     archive.schemaVersion,
			ConnectionCount:   archive.connectionCount,
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

func parseOAuthClientSource(oauthClientFile, secretProvider, oauthClientItem string) (oauthClientSource, error) {
	if oauthClientFile != "" {
		if secretProvider != "" || oauthClientItem != "" {
			return oauthClientSource{}, errors.New("use either --oauth-client-file or --secret-provider with --oauth-client-item")
		}
		return oauthClientSource{kind: "file", path: oauthClientFile}, nil
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

	if _, err := fmt.Fprint(file, configContent(archivePath, source)); err != nil {
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

func configContent(archivePath string, source oauthClientSource) string {
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
	builder.WriteString("\nservice = ")
	builder.WriteString(strconv.Quote(store.service))
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
	if err := validateOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return configCheck{}, err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return configCheck{}, err
	}
	if info.IsDir() {
		return configCheck{}, fmt.Errorf("%s is a directory", configPath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		mode := info.Mode().Perm()
		return configCheck{}, fmt.Errorf("%s is not owner-only: mode %04o, want 0600", configPath, mode)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return configCheck{}, err
	}
	config, err := parseConfig(string(configBytes))
	if err != nil {
		return configCheck{}, err
	}
	if config.archivePath == "" {
		return configCheck{}, errors.New("missing archive_path")
	}
	if config.archivePath != archivePath {
		return configCheck{}, fmt.Errorf("archive_path points to %s, want %s", config.archivePath, archivePath)
	}
	if err := validateDefaultDataTypes(config.defaultDataTypes); err != nil {
		return configCheck{}, err
	}
	if err := validateOAuthClientConfig(config.oauthClient); err != nil {
		return configCheck{}, err
	}
	if !config.credentialStoreSeen && config.credentialStore.kind == "" {
		config.credentialStore = defaultCredentialStoreConfig()
	}
	if err := validateCredentialStoreConfig(config.credentialStore); err != nil {
		return configCheck{}, err
	}
	return configCheck{
		oauthClientSource: config.oauthClient.kind,
		credentialStore:   config.credentialStore.kind,
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

func validateDefaultDataTypes(dataTypes []string) error {
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

	var migrationCount int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = 1 AND name = 'initial_archive_schema'`).Scan(&migrationCount); err != nil {
		return archiveCheck{}, err
	}
	if migrationCount != 1 {
		return archiveCheck{}, errors.New("missing schema migration 1")
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
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, 'initial_archive_schema', ?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
		return err
	}
	return tx.Commit()
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
		if result.SchemaVersion != 0 {
			if _, err := fmt.Fprintf(stdout, "schema_version: %d\n", result.SchemaVersion); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "connection_count: %d\n", result.ConnectionCount); err != nil {
			return err
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
	if result.SchemaVersion != 0 {
		if _, err := fmt.Fprintf(stdout, "Schema version: %d\n", result.SchemaVersion); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "Connections: %d\n", result.ConnectionCount); err != nil {
		return err
	}
	if result.TokenStatus != "" {
		if _, err := fmt.Fprintf(stdout, "Token status: %s\n", result.TokenStatus); err != nil {
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

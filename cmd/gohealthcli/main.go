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
	Status      string `json:"status"`
	ConfigPath  string `json:"config_path"`
	ArchivePath string `json:"archive_path"`
	Message     string `json:"message"`
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
		if err := validateConfig(*doctorConfigPath, *doctorArchivePath); err != nil {
			fmt.Fprintf(stderr, "config check failed: %v\n", err)
			return 1
		}
		if err := validateArchive(*doctorArchivePath); err != nil {
			fmt.Fprintf(stderr, "Health Archive check failed: %v\n", err)
			return 1
		}
		result := doctorResult{
			Status:      "ok",
			ConfigPath:  *doctorConfigPath,
			ArchivePath: *doctorArchivePath,
			Message:     "local gohealthcli setup is initialized",
		}
		if err := writeDoctorResult(result, mode, stdout); err != nil {
			fmt.Fprintf(stderr, "write output: %v\n", err)
			return 1
		}
		return 0
	}
	if fileExists(*doctorConfigPath) || fileExists(*doctorArchivePath) {
		fmt.Fprintln(stderr, "partial local setup found; run `gohealthcli init` after moving existing config or Health Archive")
		return 1
	}

	result := doctorResult{
		Status:      "setup_missing",
		ConfigPath:  *doctorConfigPath,
		ArchivePath: *doctorArchivePath,
		Message:     "local gohealthcli setup not found",
	}

	if err := writeDoctorResult(result, mode, stdout); err != nil {
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return 1
	}

	fmt.Fprintln(stderr, "run `gohealthcli init` to create local config and Health Archive")
	return setupMissingExitCode
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
	return builder.String()
}

func createArchive(archivePath string) error {
	if err := ensureOwnerOnlyDir(filepath.Dir(archivePath)); err != nil {
		return err
	}
	file, err := os.OpenFile(archivePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
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
	if err := ensureOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", configPath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		mode := info.Mode().Perm()
		return fmt.Errorf("%s is not owner-only: mode %04o, want 0600", configPath, mode)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	config := string(configBytes)
	for _, want := range []string{
		`archive_path = ` + strconv.Quote(archivePath),
		`default_data_types = [`,
		`"steps"`,
		`"weight"`,
		`[oauth_client]`,
		`source = `,
	} {
		if !strings.Contains(config, want) {
			return fmt.Errorf("missing %s", want)
		}
	}
	switch {
	case strings.Contains(config, `source = "file"`):
		if !strings.Contains(config, `path = `) {
			return errors.New("missing OAuth client file path")
		}
	case strings.Contains(config, `source = "secret_provider"`):
		if !strings.Contains(config, `provider = `) || !strings.Contains(config, `item = `) {
			return errors.New("missing Secret Provider reference")
		}
	default:
		return errors.New("missing OAuth client source")
	}
	return nil
}

func validateArchive(archivePath string) error {
	if err := ensureOwnerOnlyDir(filepath.Dir(archivePath)); err != nil {
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
		mode := info.Mode().Perm()
		return fmt.Errorf("%s is not owner-only: mode %04o, want 0600", archivePath, mode)
	}

	db, err := openArchive(archivePath)
	if err != nil {
		return err
	}
	defer db.Close()

	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		return err
	}
	if userVersion != currentSchemaVersion {
		return fmt.Errorf("schema version %d, want %d", userVersion, currentSchemaVersion)
	}

	var migrationCount int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = 1 AND name = 'initial_archive_schema'`).Scan(&migrationCount); err != nil {
		return err
	}
	if migrationCount != 1 {
		return errors.New("missing schema migration 1")
	}
	return nil
}

func ensureOwnerOnlyDir(dir string) error {
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		if usesPOSIXPermissions() && info.Mode().Perm() != 0o700 {
			mode := info.Mode().Perm()
			return fmt.Errorf("%s is not owner-only: mode %04o, want 0700", dir, mode)
		}
		return nil
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

func usesPOSIXPermissions() bool {
	return runtime.GOOS != "windows"
}

func openArchive(archivePath string) (*sql.DB, error) {
	query := url.Values{}
	query.Add("_pragma", "foreign_keys=on")
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(archivePath), RawQuery: query.Encode()}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func applyMigrations(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, statement := range initialMigrationStatements(time.Now().UTC().Format(time.RFC3339)) {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func initialMigrationStatements(appliedAt string) []string {
	return []string{
		`CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`,
		fmt.Sprintf(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, 'initial_archive_schema', %s)`, sqlString(appliedAt)),
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
		`PRAGMA user_version = 1`,
	}
}

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
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
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}

	switch result.Status {
	case "ok":
		if _, err := fmt.Fprintln(stdout, "Setup ok"); err != nil {
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

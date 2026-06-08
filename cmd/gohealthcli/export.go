package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type dailyStepsExportRow struct {
	ProviderName          string `json:"provider_name"`
	ConnectionID          string `json:"connection_id"`
	CivilDate             string `json:"civil_date"`
	StepCount             int64  `json:"step_count"`
	SourceKind            string `json:"source_kind"`
	SourceFamilyFilter    string `json:"source_family_filter"`
	SourceRecordCount     int64  `json:"source_record_count"`
	LatestSourceTimestamp string `json:"latest_source_timestamp"`
}

type exportDatasetSpec struct {
	name             string
	view             string
	viewSQL          string
	migrationVersion int
	fields           []exportFieldSpec
	orderBy          string
}

type exportFieldSpec struct {
	name string
	kind string
}

type exportRow []string

// exportDatasetDefinitions is the canonical Normalized View registry.
// It lives next to the export writer for historical reasons (this
// package shipped exports before the Registry concept existed); the
// follow-up PR for #109 (describe-schema --json) splits these into
// per-category files (views_steps.go, views_sleep.go, views_identity.go,
// …) and the Registry becomes the only entry point. Until then, treat
// this slice and `normalizedViewsRegistry()` as the same thing — every
// consumer should go through the Registry, never read the slice
// directly.
var exportDatasetDefinitions = []exportDatasetSpec{
	{
		name:             "daily-steps",
		view:             "daily_steps",
		migrationVersion: 4,
		viewSQL: `WITH data_point_days AS (
			SELECT
				provider_name,
				connection_id,
				COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(end_civil_time, 1, 10), substr(start_time_utc, 1, 10), substr(end_time_utc, 1, 10)) AS civil_date,
				IFNULL(source_family_filter, '') AS source_family_filter,
				SUM(CAST(json_extract(raw_json, '$.steps.count') AS INTEGER)) AS step_count,
				COUNT(*) AS source_record_count,
				MAX(COALESCE(end_time_utc, start_time_utc, updated_at, '')) AS latest_source_timestamp
			FROM data_points
			WHERE data_type = 'steps'
				AND json_extract(raw_json, '$.steps.count') IS NOT NULL
				AND COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(end_civil_time, 1, 10), substr(start_time_utc, 1, 10), substr(end_time_utc, 1, 10)) IS NOT NULL
			GROUP BY provider_name, connection_id, civil_date, source_family_filter
		),
		rollup_days AS (
			SELECT
				provider_name,
				connection_id,
				civil_date,
				'' AS source_family_filter,
				CAST(json_extract(raw_json, '$.steps.countSum') AS INTEGER) AS step_count,
				1 AS source_record_count,
				COALESCE(window_end_utc, window_start_utc, civil_date, updated_at, '') AS latest_source_timestamp
			FROM rollups
			WHERE data_type = 'steps'
				AND rollup_kind = 'dailyRollUp'
				AND civil_date IS NOT NULL
				AND json_extract(raw_json, '$.steps.countSum') IS NOT NULL
		)
		SELECT
			provider_name,
			connection_id,
			civil_date,
			source_family_filter,
			step_count,
			'dailyRollUp' AS source_kind,
			source_record_count,
			latest_source_timestamp
		FROM rollup_days
		UNION ALL
		SELECT
			provider_name,
			connection_id,
			civil_date,
			source_family_filter,
			step_count,
			'dataPoints' AS source_kind,
			source_record_count,
			latest_source_timestamp
		FROM data_point_days
		WHERE NOT EXISTS (
			SELECT 1
			FROM rollup_days
				WHERE rollup_days.provider_name = data_point_days.provider_name
					AND rollup_days.connection_id = data_point_days.connection_id
					AND rollup_days.civil_date = data_point_days.civil_date
					AND rollup_days.source_family_filter = data_point_days.source_family_filter
			)`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "civil_date"},
			{name: "step_count", kind: "int"},
			{name: "source_kind"},
			{name: "source_family_filter"},
			{name: "source_record_count", kind: "int"},
			{name: "latest_source_timestamp"},
		},
		orderBy: "civil_date, provider_name, connection_id, source_kind, source_family_filter",
	},
	{
		name:             "heart-rate-samples",
		view:             "heart_rate_samples",
		migrationVersion: 5,
		orderBy:          "sample_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			IFNULL(provider_civil_date, '') AS civil_date,
			CAST(json_extract(raw_json, '$.heartRate.beatsPerMinute') AS TEXT) AS beats_per_minute,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'heart-rate'
			AND record_kind = 'sample'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.heartRate.beatsPerMinute') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "sample_time_utc"},
			{name: "sample_civil_time"},
			{name: "civil_date"},
			{name: "beats_per_minute"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		name:             "resting-heart-rate-by-day",
		view:             "resting_heart_rate_by_day",
		migrationVersion: 5,
		orderBy:          "civil_date, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			provider_civil_date AS civil_date,
			CAST(json_extract(raw_json, '$.dailyRestingHeartRate.beatsPerMinute') AS TEXT) AS beats_per_minute,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'daily-resting-heart-rate'
			AND record_kind = 'daily'
			AND provider_civil_date IS NOT NULL
			AND json_extract(raw_json, '$.dailyRestingHeartRate.beatsPerMinute') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "civil_date"},
			{name: "beats_per_minute"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		name:             "sleep-sessions",
		view:             "sleep_sessions",
		migrationVersion: 5,
		orderBy:          "start_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			IFNULL(start_civil_time, '') AS start_civil_time,
			IFNULL(end_civil_time, '') AS end_civil_time,
			IFNULL(provider_civil_date, '') AS civil_date,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'sleep'
			AND record_kind = 'session'
			AND start_time_utc IS NOT NULL
			AND end_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "start_civil_time"},
			{name: "end_civil_time"},
			{name: "civil_date"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		name:             "exercise-sessions",
		view:             "exercise_sessions",
		migrationVersion: 5,
		orderBy:          "start_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			IFNULL(start_civil_time, '') AS start_civil_time,
			IFNULL(end_civil_time, '') AS end_civil_time,
			IFNULL(provider_civil_date, '') AS civil_date,
			IFNULL(json_extract(raw_json, '$.exercise.exerciseType'), '') AS exercise_type,
			IFNULL(json_extract(raw_json, '$.exercise.activeDuration'), '') AS active_duration,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'exercise'
			AND record_kind = 'session'
			AND start_time_utc IS NOT NULL
			AND end_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "start_civil_time"},
			{name: "end_civil_time"},
			{name: "civil_date"},
			{name: "exercise_type"},
			{name: "active_duration"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		name:             "weight-samples",
		view:             "weight_samples",
		migrationVersion: 5,
		orderBy:          "sample_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			IFNULL(provider_civil_date, '') AS civil_date,
			CAST(json_extract(raw_json, '$.weight.weightGrams') AS TEXT) AS weight_grams,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'weight'
			AND record_kind = 'sample'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.weight.weightGrams') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "sample_time_utc"},
			{name: "sample_civil_time"},
			{name: "civil_date"},
			{name: "weight_grams"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		// paired_devices explodes the device list inside the latest
		// kind='paired-devices' Identity Snapshot via json_each. One
		// row per device with the contracted columns; new fields land
		// as additional json_extract projections, no re-sync needed.
		name:             "paired-devices",
		view:             "paired_devices",
		migrationVersion: 9,
		orderBy:          "connection_id, model",
		viewSQL: `WITH latest AS (
			SELECT
				provider_name,
				connection_id,
				raw_json,
				fetched_at,
				ROW_NUMBER() OVER (PARTITION BY connection_id ORDER BY fetched_at DESC, id DESC) AS rank
			FROM identity_snapshots
			WHERE snapshot_kind = 'paired-devices'
		),
		latest_only AS (
			SELECT * FROM latest WHERE rank = 1
		)
		SELECT
			latest_only.provider_name,
			latest_only.connection_id,
			IFNULL(json_extract(device.value, '$.deviceType'), '') AS device_type,
			IFNULL(json_extract(device.value, '$.model'), '') AS model,
			IFNULL(json_extract(device.value, '$.manufacturer'), '') AS manufacturer,
			json_extract(device.value, '$.batteryPercentage') AS battery_percentage,
			IFNULL(json_extract(device.value, '$.lastSyncTime'), '') AS last_sync_time,
			IFNULL(json_extract(device.value, '$.features'), '[]') AS features,
			latest_only.fetched_at
		FROM latest_only, json_each(latest_only.raw_json, '$.devices') AS device`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "device_type"},
			{name: "model"},
			{name: "manufacturer"},
			{name: "battery_percentage"},
			{name: "last_sync_time"},
			{name: "features"},
			{name: "fetched_at"},
		},
	},
	{
		// current_settings projects the most recent Identity Snapshot of
		// kind='settings' for each Connection into a column-shaped view.
		// New fields land here as additional json_extract projections
		// without a re-sync; raw_json stays the source of truth.
		name:             "current-settings",
		view:             "current_settings",
		migrationVersion: 8,
		orderBy:          "connection_id",
		viewSQL: `WITH latest AS (
			SELECT
				provider_name,
				connection_id,
				snapshot_kind,
				raw_json,
				fetched_at,
				ROW_NUMBER() OVER (PARTITION BY connection_id ORDER BY fetched_at DESC, id DESC) AS rank
			FROM identity_snapshots
			WHERE snapshot_kind = 'settings'
		)
		SELECT
			provider_name,
			connection_id,
			IFNULL(json_extract(raw_json, '$.measurementSystem'), '') AS measurement_system,
			IFNULL(json_extract(raw_json, '$.timezone'), '') AS timezone,
			IFNULL(json_extract(raw_json, '$.strideLengthType'), '') AS stride_length_type,
			fetched_at
		FROM latest
		WHERE rank = 1`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "measurement_system"},
			{name: "timezone"},
			{name: "stride_length_type"},
			{name: "fetched_at"},
		},
	},
}

var exportDatasetSpecs = exportDatasetSpecByName(exportDatasetDefinitions)

func exportDatasetSpecByName(definitions []exportDatasetSpec) map[string]exportDatasetSpec {
	specs := make(map[string]exportDatasetSpec, len(definitions))
	for _, definition := range definitions {
		if definition.name == "" {
			panic("export dataset definition missing name")
		}
		if _, exists := specs[definition.name]; exists {
			panic(fmt.Sprintf("duplicate export dataset definition: %s", definition.name))
		}
		specs[definition.name] = definition
	}
	return specs
}

func exportDatasetViewMigrationStatements(migrationVersion int) []string {
	var statements []string
	for _, definition := range exportDatasetDefinitions {
		if definition.migrationVersion != migrationVersion {
			continue
		}
		statements = append(statements, exportDatasetViewMigrationStatement(definition))
	}
	return statements
}

func exportDatasetViewMigrationStatement(spec exportDatasetSpec) string {
	return fmt.Sprintf("CREATE VIEW %s AS\n%s", spec.view, strings.TrimSpace(spec.viewSQL))
}

func runExport(args []string, configPath, archivePath string, archivePathExplicit bool, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(stderr)

	exportConfigPath := flags.String("config", configPath, "config file path")
	exportArchivePath := flags.String("db", archivePath, "SQLite Health Archive path")
	exportFormat := flags.String("format", "csv", "export format: csv or jsonl")
	exportOutputPath := flags.String("output", "", "write export to path")
	exportStdout := flags.Bool("stdout", false, "write export data to stdout")
	flags.Bool("no-input", false, "never prompt, never wait for browser input")

	dataset, parseArgs, err := splitExportArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}

	if err := flags.Parse(parseArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if dataset == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "export requires exactly one dataset")
		return 1
	}
	spec, ok := exportDatasetSpecs[dataset]
	if !ok {
		fmt.Fprintf(stderr, "export dataset %q is not supported\n", dataset)
		return 1
	}
	if *exportOutputPath == "" && !*exportStdout {
		fmt.Fprintln(stderr, "export requires --output PATH or --stdout")
		return 1
	}
	if *exportOutputPath != "" && *exportStdout {
		fmt.Fprintln(stderr, "export accepts only one destination: --output or --stdout")
		return 1
	}
	if err := validateExportFormat(*exportFormat); err != nil {
		fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}

	resolvedArchivePath, err := resolveConfiguredArchivePath(*exportConfigPath, *exportArchivePath, archivePathExplicit || flagWasProvided(flags, "db"))
	if err != nil {
		fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}
	rows, err := exportRows(resolvedArchivePath, spec)
	if err != nil {
		fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}

	if *exportStdout {
		if err := writeExport(rows, spec, *exportFormat, stdout); err != nil {
			fmt.Fprintf(stderr, "write output: %v\n", err)
			return 1
		}
		return 0
	}
	if err := writeExportFile(rows, spec, *exportFormat, *exportOutputPath); err != nil {
		fmt.Fprintf(stderr, "write export: %v\n", err)
		return 1
	}
	return 0
}

func splitExportArgs(args []string) (string, []string, error) {
	var dataset string
	var flagArgs []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if exportFlagNeedsValue(arg) && !strings.Contains(arg, "=") {
				index++
				if index >= len(args) {
					return "", nil, fmt.Errorf("flag needs an argument: %s", arg)
				}
				flagArgs = append(flagArgs, args[index])
			}
			continue
		}
		if dataset != "" {
			return "", nil, errors.New("export requires exactly one dataset")
		}
		dataset = arg
	}
	return dataset, flagArgs, nil
}

func exportFlagNeedsValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	switch name {
	case "config", "db", "format", "output":
		return true
	default:
		return false
	}
}

func validateExportFormat(format string) error {
	switch format {
	case "csv", "jsonl":
		return nil
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
}

func writeExportFile(rows []exportRow, spec exportDatasetSpec, format, path string) error {
	if usesPOSIXPermissions() {
		if err := restrictExistingExportOutput(path); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	writeErr := writeExport(rows, spec, format, file)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if usesPOSIXPermissions() {
		return os.Chmod(path, 0o600)
	}
	return nil
}

func writeDailyStepsExportFile(rows []dailyStepsExportRow, format, path string) error {
	return writeExportFile(dailyStepsExportRowsToExportRows(rows), exportDatasetSpecs["daily-steps"], format, path)
}

func restrictExistingExportOutput(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if info.Mode().Perm() != 0o600 {
		return os.Chmod(path, 0o600)
	}
	return nil
}

func writeExport(rows []exportRow, spec exportDatasetSpec, format string, writer io.Writer) error {
	switch format {
	case "csv":
		return writeExportCSV(rows, spec, writer)
	case "jsonl":
		return writeExportJSONL(rows, spec, writer)
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
}

func writeDailyStepsExport(rows []dailyStepsExportRow, format string, writer io.Writer) error {
	return writeExport(dailyStepsExportRowsToExportRows(rows), exportDatasetSpecs["daily-steps"], format, writer)
}

func writeExportCSV(rows []exportRow, spec exportDatasetSpec, writer io.Writer) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(exportFieldNames(spec)); err != nil {
		return err
	}
	for _, row := range rows {
		if err := csvWriter.Write([]string(row)); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func writeDailyStepsCSV(rows []dailyStepsExportRow, writer io.Writer) error {
	return writeExportCSV(dailyStepsExportRowsToExportRows(rows), exportDatasetSpecs["daily-steps"], writer)
}

func writeExportJSONL(rows []exportRow, spec exportDatasetSpec, writer io.Writer) error {
	for _, row := range rows {
		if _, err := fmt.Fprint(writer, "{"); err != nil {
			return err
		}
		for index, field := range spec.fields {
			if index > 0 {
				if _, err := fmt.Fprint(writer, ","); err != nil {
					return err
				}
			}
			name, err := json.Marshal(field.name)
			if err != nil {
				return err
			}
			if _, err := writer.Write(name); err != nil {
				return err
			}
			if _, err := fmt.Fprint(writer, ":"); err != nil {
				return err
			}
			if field.kind == "int" && row[index] != "" {
				value, err := strconv.ParseInt(row[index], 10, 64)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprint(writer, strconv.FormatInt(value, 10)); err != nil {
					return err
				}
				continue
			}
			value, err := json.Marshal(row[index])
			if err != nil {
				return err
			}
			if _, err := writer.Write(value); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(writer, "}"); err != nil {
			return err
		}
	}
	return nil
}

func writeDailyStepsJSONL(rows []dailyStepsExportRow, writer io.Writer) error {
	return writeExportJSONL(dailyStepsExportRowsToExportRows(rows), exportDatasetSpecs["daily-steps"], writer)
}

func dailyStepsExportFields() []string {
	return exportFieldNames(exportDatasetSpecs["daily-steps"])
}

func exportFieldNames(spec exportDatasetSpec) []string {
	fields := make([]string, 0, len(spec.fields))
	for _, field := range spec.fields {
		fields = append(fields, field.name)
	}
	return fields
}

func dailyStepsExportRowsToExportRows(rows []dailyStepsExportRow) []exportRow {
	out := make([]exportRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, exportRow{
			row.ProviderName,
			row.ConnectionID,
			row.CivilDate,
			strconv.FormatInt(row.StepCount, 10),
			row.SourceKind,
			row.SourceFamilyFilter,
			strconv.FormatInt(row.SourceRecordCount, 10),
			row.LatestSourceTimestamp,
		})
	}
	return out
}

func exportRows(archivePath string, spec exportDatasetSpec) ([]exportRow, error) {
	reader, err := openHealthArchiveReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader.ExportRows(spec)
}

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
	view    string
	fields  []exportFieldSpec
	orderBy string
}

type exportFieldSpec struct {
	name string
	kind string
}

type exportRow []string

var exportDatasetSpecs = map[string]exportDatasetSpec{
	"daily-steps": {
		view: "daily_steps",
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
	"heart-rate-samples": {
		view:    "heart_rate_samples",
		orderBy: "sample_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
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
	"resting-heart-rate-by-day": {
		view:    "resting_heart_rate_by_day",
		orderBy: "civil_date, provider_name, connection_id, source_family_filter, upstream_resource_name",
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "civil_date"},
			{name: "beats_per_minute"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	"sleep-sessions": {
		view:    "sleep_sessions",
		orderBy: "start_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
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
	"exercise-sessions": {
		view:    "exercise_sessions",
		orderBy: "start_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
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
	"weight-samples": {
		view:    "weight_samples",
		orderBy: "sample_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
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

package main

import (
	"database/sql"
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
	if dataset != "daily-steps" {
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
	rows, err := dailyStepsExportRows(resolvedArchivePath)
	if err != nil {
		fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}

	if *exportStdout {
		if err := writeDailyStepsExport(rows, *exportFormat, stdout); err != nil {
			fmt.Fprintf(stderr, "write output: %v\n", err)
			return 1
		}
		return 0
	}
	if err := writeDailyStepsExportFile(rows, *exportFormat, *exportOutputPath); err != nil {
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

func dailyStepsExportRows(archivePath string) ([]dailyStepsExportRow, error) {
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

	rows, err := db.Query(`SELECT
		provider_name,
		connection_id,
		civil_date,
		step_count,
		source_kind,
		source_family_filter,
		source_record_count,
		latest_source_timestamp
	FROM daily_steps
	ORDER BY civil_date, provider_name, connection_id, source_kind, source_family_filter`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []dailyStepsExportRow
	for rows.Next() {
		var item dailyStepsExportRow
		var latest sql.NullString
		if err := rows.Scan(
			&item.ProviderName,
			&item.ConnectionID,
			&item.CivilDate,
			&item.StepCount,
			&item.SourceKind,
			&item.SourceFamilyFilter,
			&item.SourceRecordCount,
			&latest,
		); err != nil {
			return nil, err
		}
		item.LatestSourceTimestamp = latest.String
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func writeDailyStepsExportFile(rows []dailyStepsExportRow, format, path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	writeErr := writeDailyStepsExport(rows, format, file)
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

func writeDailyStepsExport(rows []dailyStepsExportRow, format string, writer io.Writer) error {
	switch format {
	case "csv":
		return writeDailyStepsCSV(rows, writer)
	case "jsonl":
		return writeDailyStepsJSONL(rows, writer)
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
}

func writeDailyStepsCSV(rows []dailyStepsExportRow, writer io.Writer) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(dailyStepsExportFields()); err != nil {
		return err
	}
	for _, row := range rows {
		if err := csvWriter.Write([]string{
			row.ProviderName,
			row.ConnectionID,
			row.CivilDate,
			strconv.FormatInt(row.StepCount, 10),
			row.SourceKind,
			row.SourceFamilyFilter,
			strconv.FormatInt(row.SourceRecordCount, 10),
			row.LatestSourceTimestamp,
		}); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func writeDailyStepsJSONL(rows []dailyStepsExportRow, writer io.Writer) error {
	for _, row := range rows {
		content, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := writer.Write(content); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
	}
	return nil
}

func dailyStepsExportFields() []string {
	return []string{
		"provider_name",
		"connection_id",
		"civil_date",
		"step_count",
		"source_kind",
		"source_family_filter",
		"source_record_count",
		"latest_source_timestamp",
	}
}

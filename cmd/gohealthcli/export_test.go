package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDailyStepsNormalizedViewPrefersRollupsAndAggregatesDataPoints(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportStepDataPoint(t, archivePath, "users/me/dataTypes/steps/dataPoints/c", "2026-01-01T12:00:00Z", "2026-01-01T12:10:00Z", `{"steps":{"count":"128"}}`)
	insertExportStepDataPoint(t, archivePath, "users/me/dataTypes/steps/dataPoints/d", "2026-01-04T12:00:00Z", "2026-01-04T12:10:00Z", `{"steps":{"count":"1"}}`)
	insertExportStepDataPointWithSourceFamily(t, archivePath, "users/me/dataTypes/steps/dataPoints/wearable", "2026-01-01T08:00:00Z", "2026-01-01T08:15:00Z", `{"steps":{"count":"256"}}`, "wearable")
	insertExportStepDataPointWithSourceFamily(t, archivePath, "users/me/dataTypes/steps/dataPoints/wearable-rollup-day", "2026-01-04T08:00:00Z", "2026-01-04T08:15:00Z", `{"steps":{"count":"384"}}`, "wearable")

	rows, err := dailyStepsExportRows(archivePath)
	if err != nil {
		t.Fatalf("daily steps rows: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("row count = %d, want 5: %+v", len(rows), rows)
	}
	assertDailyStepsRow(t, rows[0], "2026-01-01", 640, "dataPoints", "", 2)
	assertDailyStepsRow(t, rows[1], "2026-01-01", 256, "dataPoints", "wearable", 1)
	assertDailyStepsRow(t, rows[2], "2026-01-02", 1024, "dataPoints", "", 1)
	assertDailyStepsRow(t, rows[3], "2026-01-04", 2048, "dailyRollUp", "", 1)
	assertDailyStepsRow(t, rows[4], "2026-01-04", 384, "dataPoints", "wearable", 1)
}

func TestExportDailyStepsCSVToFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	outputPath := filepath.Join(tempDir, "daily-steps.csv")

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"daily-steps",
		"--format", "csv",
		"--output", outputPath,
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty for file export", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	want := "provider_name,connection_id,civil_date,step_count,source_kind,source_family_filter,source_record_count,latest_source_timestamp\n" +
		"googlehealth,googlehealth:111111256096816351,2026-01-01,512,dataPoints,,1,2026-01-01T08:15:00Z\n" +
		"googlehealth,googlehealth:111111256096816351,2026-01-02,1024,dataPoints,,1,2026-01-02T08:15:00Z\n" +
		"googlehealth,googlehealth:111111256096816351,2026-01-04,2048,dailyRollUp,,1,2026-01-04\n"
	if string(content) != want {
		t.Fatalf("export content =\n%s\nwant:\n%s", string(content), want)
	}
	if usesPOSIXPermissions() {
		info, err := os.Stat(outputPath)
		if err != nil {
			t.Fatalf("stat export: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("export mode = %04o, want 0600", info.Mode().Perm())
		}
	}
	assertNoSecretWords(t, stdout.String()+stderr.String()+string(content))
}

func TestExportDailyStepsRestrictsExistingOutputBeforeOverwrite(t *testing.T) {
	if !usesPOSIXPermissions() {
		t.Skip("POSIX mode assertions are not meaningful on this platform")
	}
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	outputPath := filepath.Join(tempDir, "daily-steps.csv")
	if err := os.WriteFile(outputPath, []byte("old export"), 0o644); err != nil {
		t.Fatalf("seed output: %v", err)
	}
	if err := os.Chmod(outputPath, 0o644); err != nil {
		t.Fatalf("chmod seed output: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"daily-steps",
		"--format", "csv",
		"--output", outputPath,
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("stat export: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("export mode = %04o, want 0600", info.Mode().Perm())
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if !strings.HasPrefix(string(content), "provider_name,connection_id,civil_date") {
		t.Fatalf("export content = %q, want CSV header", string(content))
	}
	assertNoSecretWords(t, stdout.String()+stderr.String()+string(content))
}

func TestExportDailyStepsJSONLToStdout(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"daily-steps",
		"--format", "jsonl",
		"--stdout",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("JSONL line count = %d, want 3\nstdout:\n%s", len(lines), stdout.String())
	}
	var first dailyStepsExportRow
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first JSONL line is invalid: %v\nline: %s", err, lines[0])
	}
	assertDailyStepsRow(t, first, "2026-01-01", 512, "dataPoints", "", 1)
	if !strings.Contains(lines[0], `"civil_date":"2026-01-01"`) || !strings.Contains(lines[0], `"step_count":512`) {
		t.Fatalf("first JSONL line missing stable fields: %s", lines[0])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestExportDailyStepsRequiresExplicitDestination(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	for _, tc := range []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{
			name:       "missing destination",
			args:       []string{"export", "--config", configPath, "daily-steps", "--format", "csv"},
			wantStderr: "requires --output PATH or --stdout",
		},
		{
			name:       "ambiguous destination",
			args:       []string{"export", "--config", configPath, "daily-steps", "--format", "csv", "--output", filepath.Join(tempDir, "out.csv"), "--stdout"},
			wantStderr: "accepts only one destination",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run(tc.args, stdout, stderr)
			if code != 1 {
				t.Fatalf("export exit code = %d, want 1", code)
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.wantStderr)
			}
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
	}
}

func TestExportRejectsUnsupportedFormatBeforeWritingFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	outputPath := filepath.Join(tempDir, "daily-steps.txt")
	if err := os.WriteFile(outputPath, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seed output: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"daily-steps",
		"--format", "txt",
		"--output", outputPath,
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("export exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unsupported export format "txt"`) {
		t.Fatalf("stderr = %q, want unsupported format", stderr.String())
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(content) != "keep me" {
		t.Fatalf("output file = %q, want unchanged", string(content))
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func insertExportStepDataPoint(t *testing.T, archivePath, resourceName, startUTC, endUTC, rawJSON string) {
	t.Helper()
	insertExportStepDataPointWithSourceFamily(t, archivePath, resourceName, startUTC, endUTC, rawJSON, "")
}

func insertExportStepDataPointWithSourceFamily(t *testing.T, archivePath, resourceName, startUTC, endUTC, rawJSON, sourceFamily string) {
	t.Helper()
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO data_points (
		provider_name,
		connection_id,
		data_type,
		upstream_resource_name,
		record_kind,
		start_time_utc,
		end_time_utc,
		data_source_json,
		source_family_filter,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		"steps",
		resourceName,
		"interval",
		startUTC,
		endUTC,
		"{}",
		nullString(sourceFamily),
		rawJSON,
		"2026-01-04T00:00:00Z",
		"2026-01-04T00:00:00Z",
	); err != nil {
		t.Fatalf("insert export Data Point: %v", err)
	}
}

func assertDailyStepsRow(t *testing.T, row dailyStepsExportRow, civilDate string, stepCount int64, sourceKind, sourceFamily string, sourceRecordCount int64) {
	t.Helper()
	if row.ProviderName != "googlehealth" ||
		row.ConnectionID != "googlehealth:111111256096816351" ||
		row.CivilDate != civilDate ||
		row.StepCount != stepCount ||
		row.SourceKind != sourceKind ||
		row.SourceFamilyFilter != sourceFamily ||
		row.SourceRecordCount != sourceRecordCount {
		t.Fatalf("row = %+v, want date=%s steps=%d source=%s source_family=%s records=%d", row, civilDate, stepCount, sourceKind, sourceFamily, sourceRecordCount)
	}
}

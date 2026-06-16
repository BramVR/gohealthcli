package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestStatusReportsHealthArchiveCountsAndSyncRunsReadOnly(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	testRuntime := runtimeAdapters{}
	testRuntime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		t.Fatal("status should not call Provider identity")
		return googleIdentity{}, nil
	}
	testRuntime.fetchProfile = func(accessToken string) (googleProfile, error) {
		t.Fatal("status should not call Provider profile")
		return googleProfile{}, nil
	}
	testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		t.Fatal("status should not call Provider raw endpoints")
		return nil, nil
	}
	testRuntime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		t.Fatal("status should not refresh tokens")
		return oauthTokenResponse{}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"status",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONString(t, got, "archive_path", archivePath)
	assertJSONNumber(t, got, "schema_version", currentSchemaVersion)
	assertJSONNumber(t, got, "data_point_count", 3)
	assertJSONNumber(t, got, "rollup_count", 1)
	assertJSONNumber(t, got, "profile_snapshot_count", 1)
	assertJSONNumber(t, got, "sync_run_count", 3)

	heartRateStatus := statusDataTypeFromJSON(t, got, "heart-rate")
	assertJSONNumber(t, heartRateStatus, "data_point_count", 1)
	assertJSONNumber(t, heartRateStatus, "rollup_count", 0)
	assertJSONString(t, heartRateStatus, "newest_data_point_timestamp", "2026-01-03T09:00:00Z")
	if _, ok := heartRateStatus["newest_rollup_timestamp"]; ok {
		t.Fatalf("heart-rate newest_rollup_timestamp = %v, want omitted", heartRateStatus["newest_rollup_timestamp"])
	}

	stepsStatus := statusDataTypeFromJSON(t, got, "steps")
	assertJSONNumber(t, stepsStatus, "data_point_count", 2)
	assertJSONNumber(t, stepsStatus, "rollup_count", 1)
	assertJSONString(t, stepsStatus, "newest_data_point_timestamp", "2026-01-02T08:15:00Z")
	assertJSONString(t, stepsStatus, "newest_rollup_timestamp", "2026-01-04")

	success, ok := got["latest_successful_sync_run"].(map[string]any)
	if !ok {
		t.Fatalf("latest_successful_sync_run = %T(%v), want object", got["latest_successful_sync_run"], got["latest_successful_sync_run"])
	}
	assertJSONNumber(t, success, "id", 2)
	assertJSONString(t, success, "status", "sync_completed")
	assertJSONString(t, success, "from", "2026-01-02")
	assertJSONString(t, success, "to", "2026-01-03T00:00:00Z")
	assertJSONString(t, success, "endpoint_family", "reconcile")
	assertJSONString(t, success, "source_family_filter", "wearable")
	assertJSONNumber(t, success, "seen_count", 2)

	failed, ok := got["latest_failed_sync_run"].(map[string]any)
	if !ok {
		t.Fatalf("latest_failed_sync_run = %T(%v), want object", got["latest_failed_sync_run"], got["latest_failed_sync_run"])
	}
	assertJSONNumber(t, failed, "id", 3)
	assertJSONString(t, failed, "status", "sync_failed")
	assertJSONString(t, failed, "error_summary", "Provider timeout after 30s")
	if strings.Contains(stdout.String(), "gap") || strings.Contains(stdout.String(), "completeness") {
		t.Fatalf("status inferred completeness or gaps:\n%s", stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusDoesNotRecreateMissingAttachmentRoot(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	rootDir := attachmentRootDirForArchive(archivePath)
	movedRootDir := rootDir + ".moved"
	if err := os.Rename(rootDir, movedRootDir); err != nil {
		t.Fatalf("rename attachment root: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if _, err := os.Stat(rootDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attachment root stat err = %v, want missing root", err)
	}
}

// TestWriteStatusSyncRunPlainEscapesControlBytes pins issue #244 for the
// status plain writers: provider-influenced fields (error_summary from a Sync
// Run's failure, source_family_filter) must have their C0/C1 control bytes
// rendered in a visible, reversible escape form so terminal escape-sequence
// injection (CWE-150) can never reach the terminal raw. ESC (0x1b) and BEL
// (0x07) stand in for the decoded provider-derived control bytes.
func TestWriteStatusSyncRunPlainEscapesControlBytes(t *testing.T) {
	t.Parallel()
	run := &statusSyncRun{
		ID:                 3,
		Status:             "sync_failed",
		SourceFamilyFilter: "wear\x1bable",
		ErrorSummary:       "Provider \x1btimeout\x07 after 30s",
	}
	stdout := new(bytes.Buffer)
	writer := newStickyWriter(stdout)
	writeStatusSyncRunPlain(writer, "latest_failed_sync_run", run)
	if err := writer.Err(); err != nil {
		t.Fatalf("writeStatusSyncRunPlain: %v", err)
	}
	out := stdout.String()
	wantLines := []string{
		`latest_failed_sync_run_source_family_filter: wear\x1bable` + "\n",
		`latest_failed_sync_run_error_summary: Provider \x1btimeout\x07 after 30s` + "\n",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Fatalf("status plain output missing %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("status plain output contains a raw control byte:\n%q", out)
	}
}

func TestStatusPlainReportsEmptyHealthArchive(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--plain",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	wantLines := []string{
		"status: ok\n",
		"archive_path: " + archivePath + "\n",
		fmt.Sprintf("schema_version: %d\n", currentSchemaVersion),
		"data_point_count: 0\n",
		"rollup_count: 0\n",
		"profile_snapshot_count: 0\n",
		"sync_run_count: 0\n",
		"message: Health Archive status summarized\n",
	}
	for _, want := range wantLines {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "latest_successful_sync_run") || strings.Contains(stdout.String(), "known_data_types") {
		t.Fatalf("stdout reported absent archive details:\n%s", stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusMigratesLegacyV3ArchiveBeforeValidation(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV3Archive(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONNumber(t, got, "schema_version", currentSchemaVersion)
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
}

func TestStatusRejectsConfigArchiveMismatch(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, _, _ := initializeFileCredentialSetup(t, tempDir)
	otherArchivePath := filepath.Join(tempDir, "other-data", "other.sqlite")
	if err := createArchive(otherArchivePath); err != nil {
		t.Fatalf("create other archive: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--db", otherArchivePath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "status_failed")
	// PRD #144 slice 1 (issue #155): a read-command mismatch error names
	// the user-facing flags directly, never the internal `archive_path`
	// config field. The resolver test matrix pins the exact wording; here
	// we pin only the externally observable surface: both flag names
	// appear, the internal name does not.
	message, _ := got["message"].(string)
	for _, want := range []string{"--db", "--config", otherArchivePath, configPath} {
		if !strings.Contains(message, want) {
			t.Errorf("message = %q, missing substring %q", message, want)
		}
	}
	if strings.Contains(message, "archive_path") {
		t.Errorf("message = %q, must not mention internal archive_path field", message)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusRejectsExplicitDefaultArchiveMismatch(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "xdg-data"))
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	defaultArchivePath := defaultArchivePath()
	if defaultArchivePath == archivePath {
		t.Fatalf("default archive path unexpectedly matches config archive path: %s", archivePath)
	}
	if err := createArchive(defaultArchivePath); err != nil {
		t.Fatalf("create default archive: %v", err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "status flag",
			args: []string{"status", "--config", configPath, "--db", defaultArchivePath, "--json"},
		},
		{
			name: "global flag",
			args: []string{"--config", configPath, "--db", defaultArchivePath, "status", "--json"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run(tc.args, stdout, stderr)
			if code != 1 {
				t.Fatalf("status exit code = %d, want 1", code)
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "status_failed")
			// PRD #144 slice 1 (issue #155): when --config and --db are
			// both explicit and disagree, the read-command error must
			// name the user-facing flags directly and must NOT mention
			// the internal `archive_path` config field. Both flag values
			// must show up so the user can see what they pointed each
			// flag at.
			message, _ := got["message"].(string)
			for _, want := range []string{"--db", "--config", defaultArchivePath, configPath, archivePath} {
				if !strings.Contains(message, want) {
					t.Errorf("message = %q, missing substring %q", message, want)
				}
			}
			if strings.Contains(message, "archive_path") {
				t.Errorf("message = %q, must not mention internal archive_path field", message)
			}
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
	}
}

// TestStatusReportsNewestElectrocardiogramEventTimestamp pins the
// AC for #104: once any electrocardiogram session is archived, the
// per-Data-Type status entry projects the latest event timestamp
// through the existing readStatusDataTypes loop. No new code path
// is added — the test catches a regression that strips ECG (or any
// future opt-in Data Type) from the per-Data-Type rollup.
func TestStatusReportsNewestElectrocardiogramEventTimestamp(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO data_points (
		provider_name,
		connection_id,
		data_type,
		upstream_resource_name,
		record_kind,
		start_time_utc,
		end_time_utc,
		data_source_json,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		"electrocardiogram",
		"users/me/dataTypes/electrocardiogram/dataPoints/ecg-2026-05-20",
		"session",
		"2026-05-20T10:30:00Z",
		"2026-05-20T10:30:30Z",
		"{}",
		`{"electrocardiogram":{"classification":"SINUS_RHYTHM"}}`,
		"2026-05-21T00:00:00Z",
		"2026-05-21T00:00:00Z",
	); err != nil {
		db.Close()
		t.Fatalf("insert ECG fixture: %v", err)
	}
	db.Close()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	ecgStatus := statusDataTypeFromJSON(t, got, "electrocardiogram")
	assertJSONNumber(t, ecgStatus, "data_point_count", 1)
	assertJSONString(t, ecgStatus, "newest_data_point_timestamp", "2026-05-20T10:30:30Z")
}

func TestStatusReportsMigrationFailureForUnsupportedSchema(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	setArchiveUserVersion(t, archivePath, currentSchemaVersion+1)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "status_failed")
	if _, ok := got["schema_version"]; ok {
		t.Fatalf("schema_version = %v, want omitted before migration succeeds", got["schema_version"])
	}
	if !strings.Contains(got["message"].(string), fmt.Sprintf("schema version %d", currentSchemaVersion+1)) {
		t.Fatalf("message = %q, want schema version", got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusReportsSchemaVersionForArchiveInspectionFailure(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM schema_migrations WHERE version = ?`, currentSchemaVersion); err != nil {
		_ = db.Close()
		t.Fatalf("delete schema migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "status_failed")
	assertJSONNumber(t, got, "schema_version", currentSchemaVersion)
	if !strings.Contains(got["message"].(string), "missing schema migration") {
		t.Fatalf("message = %q, want missing migration", got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

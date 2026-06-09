package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestQueryReadsArchivedStepsDataPointsReadOnly(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"--json",
		"SELECT data_type, end_time_utc FROM data_points WHERE data_type = 'steps' ORDER BY end_time_utc",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "query_completed")
	assertJSONString(t, got, "archive_path", archivePath)
	assertJSONNumber(t, got, "row_count", 2)
	columns, ok := got["columns"].([]any)
	if !ok {
		t.Fatalf("columns = %T(%v), want array", got["columns"], got["columns"])
	}
	if fmt.Sprint(columns) != "[data_type end_time_utc]" {
		t.Fatalf("columns = %v, want data_type/end_time_utc", columns)
	}
	rows, ok := got["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("rows = %T(%v), want 2 rows", got["rows"], got["rows"])
	}
	firstRow, ok := rows[0].([]any)
	if !ok {
		t.Fatalf("first row = %T(%v), want array", rows[0], rows[0])
	}
	if fmt.Sprint(firstRow) != "[steps 2026-01-01T08:15:00Z]" {
		t.Fatalf("first row = %v, want first steps Data Point", firstRow)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestQueryPlainOutputIsStable(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"--plain",
		"SELECT count(*) AS data_point_count FROM data_points",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	wantLines := []string{
		"status: query_completed\n",
		"archive_path: " + archivePath + "\n",
		"columns: data_point_count\n",
		"row_count: 1\n",
		"row.1.1: 3\n",
		"message: Query completed\n",
	}
	for _, want := range wantLines {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestQueryPlainOutputEscapesControlCharacters(t *testing.T) {
	tempDir := t.TempDir()
	configPath, _, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"--plain",
		`SELECT 'a' || char(10) || 'b' || char(9) || '\c' AS note`,
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := `row.1.1: a\nb\t\\c` + "\n"
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
	}
	if strings.Contains(stdout.String(), "row.1.1: a\nb") {
		t.Fatalf("stdout contains an unescaped row newline:\n%s", stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestQueryDefaultOutputEmitsPlainShape pins the PRD #144 slice 7 contract:
// when neither --json nor --plain is set, `query` emits the --plain key/value
// shape (parseable, byte-for-byte the same as --plain) instead of the legacy
// `Row N: column=value column=value` format. The legacy format is gone from
// every code path — see TestQueryDefaultOutputDoesNotEmitLegacyRowFormat and
// TestQueryDefaultOutputMatchesPlainModeByteForByte for the negative and
// equivalence assertions.
func TestQueryDefaultOutputEmitsPlainShape(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"SELECT data_type, end_time_utc FROM data_points WHERE data_type = 'steps' ORDER BY end_time_utc LIMIT 1",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	wantLines := []string{
		"status: query_completed\n",
		"archive_path: " + archivePath + "\n",
		"columns: data_type,end_time_utc\n",
		"row_count: 1\n",
		"row.1.1: steps\n",
		"row.1.2: 2026-01-01T08:15:00Z\n",
		"message: Query completed\n",
	}
	for _, want := range wantLines {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestQueryDefaultOutputDoesNotEmitLegacyRowFormat guards the PRD #144 slice 7
// removal of the unparseable `Row N: column=value column=value` format. A
// value containing a space (the classic silent-footgun shape — `SELECT
// 'a b' AS x` would have produced `Row 1: x=a b`) is the test fixture so a
// future regression that reintroduces the format is caught even if the column
// names happen to be space-free.
func TestQueryDefaultOutputDoesNotEmitLegacyRowFormat(t *testing.T) {
	tempDir := t.TempDir()
	configPath, _, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"SELECT 'hello world' AS greeting",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	// Silent default: PRD #144 slice 7 chose no stderr warning, so any
	// stray bytes on stderr are a regression in the documented contract.
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty (silent default)", stderr.String())
	}
	out := stdout.String()
	// The legacy format wrapped every row in `Row N:` and joined columns
	// with ` k=v` pairs separated by spaces. None of those markers may
	// appear in the default output anywhere.
	for _, banned := range []string{"Row 1:", "Row 1: ", "Query completed: ", "Columns: ", "Message: "} {
		if strings.Contains(out, banned) {
			t.Fatalf("stdout contains banned legacy default marker %q:\n%s", banned, out)
		}
	}
	// The value should round-trip verbatim in the --plain shape so a
	// downstream parser can split on the first `: ` and recover it.
	want := "row.1.1: hello world\n"
	if !strings.Contains(out, want) {
		t.Fatalf("stdout missing parseable value line %q:\n%s", want, out)
	}
	assertNoSecretWords(t, out+stderr.String())
}

// TestQueryDefaultOutputMatchesPlainModeByteForByte asserts that the no-flag
// default and `--plain` produce identical bytes on the same query. The two
// modes are the same code path post-slice-7; this test catches a future
// divergence (an accidental stderr warning, a header line added to one mode
// only) before it ships.
func TestQueryDefaultOutputMatchesPlainModeByteForByte(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	statement := "SELECT data_type, end_time_utc FROM data_points WHERE data_type = 'steps' ORDER BY end_time_utc"

	defaultStdout := new(bytes.Buffer)
	defaultStderr := new(bytes.Buffer)
	if code := run([]string{"query", "--config", configPath, statement}, defaultStdout, defaultStderr); code != 0 {
		t.Fatalf("default exit code = %d, want 0\nstderr: %s\nstdout: %s", code, defaultStderr.String(), defaultStdout.String())
	}

	plainStdout := new(bytes.Buffer)
	plainStderr := new(bytes.Buffer)
	if code := run([]string{"query", "--config", configPath, "--plain", statement}, plainStdout, plainStderr); code != 0 {
		t.Fatalf("plain exit code = %d, want 0\nstderr: %s\nstdout: %s", code, plainStderr.String(), plainStdout.String())
	}

	if defaultStdout.String() != plainStdout.String() {
		t.Fatalf("default stdout differs from --plain stdout\ndefault:\n%s\n---\nplain:\n%s", defaultStdout.String(), plainStdout.String())
	}
	if defaultStderr.String() != plainStderr.String() {
		t.Fatalf("default stderr differs from --plain stderr\ndefault: %q\nplain:   %q", defaultStderr.String(), plainStderr.String())
	}
	if defaultStderr.Len() != 0 {
		t.Fatalf("default stderr is non-empty (silent default expected): %q", defaultStderr.String())
	}
	assertNoSecretWords(t, defaultStdout.String()+defaultStderr.String()+plainStdout.String()+plainStderr.String())
}

func TestQueryMigratesLegacyV3ArchiveBeforeValidation(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV3Archive(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"SELECT name FROM schema_migrations WHERE version = 4",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "query_completed")
	assertJSONNumber(t, got, "row_count", 1)
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
}

func TestQueryRejectsWriteAttemptsBeforeMigratingLegacyArchive(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV3Archive(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"DELETE FROM data_points",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("query exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "query_failed")
	if !strings.Contains(got["message"].(string), "SELECT statements only") {
		t.Fatalf("message = %q, want SELECT-only rejection", got["message"])
	}
	assertArchiveUserVersion(t, archivePath, 3)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestQueryAcceptsSelectCTE(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"--json",
		"WITH recent AS (SELECT data_type FROM data_points WHERE data_type = 'steps') SELECT count(*) AS count FROM recent",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "query_completed")
	assertJSONNumber(t, got, "row_count", 1)
	rows, ok := got["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("rows = %T(%v), want one row", got["rows"], got["rows"])
	}
	firstRow, ok := rows[0].([]any)
	if !ok || len(firstRow) != 1 || firstRow[0] != float64(2) {
		t.Fatalf("first row = %T(%v), want count 2", rows[0], rows[0])
	}
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestQueryAcceptsCTEIdentifierDigits(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"--json",
		"WITH last_30_days AS (SELECT data_type FROM data_points WHERE data_type = 'steps') SELECT count(*) AS count FROM last_30_days",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "query_completed")
	assertJSONNumber(t, got, "row_count", 1)
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestQueryAcceptsTrailingCommentsAfterTerminator(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"query",
		"--config", configPath,
		"--json",
		"SELECT count(*) AS data_point_count FROM data_points; -- read-only count",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "query_completed")
	assertJSONNumber(t, got, "row_count", 1)
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestQueryRejectsWriteAttemptsWithoutChangingArchive(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	for _, tc := range []struct {
		name       string
		statement  string
		wantError  string
		wantUserV  int
		wantPoints int
	}{
		{
			name:       "non select",
			statement:  "DELETE FROM data_points",
			wantError:  "SELECT statements only",
			wantUserV:  currentSchemaVersion,
			wantPoints: 3,
		},
		{
			name:       "mutating pragma",
			statement:  "PRAGMA user_version = 99",
			wantError:  "SELECT statements only",
			wantUserV:  currentSchemaVersion,
			wantPoints: 3,
		},
		{
			name:       "multi statement mutation",
			statement:  "SELECT count(*) FROM data_points; DELETE FROM data_points",
			wantError:  "one SELECT statement only",
			wantUserV:  currentSchemaVersion,
			wantPoints: 3,
		},
		{
			name:       "cte mutation",
			statement:  "WITH target AS (SELECT id FROM data_points) DELETE FROM data_points WHERE id IN (SELECT id FROM target)",
			wantError:  "SELECT statements only",
			wantUserV:  currentSchemaVersion,
			wantPoints: 3,
		},
		{
			name:       "mutation after trailing comment",
			statement:  "SELECT count(*) FROM data_points; -- comment\nDELETE FROM data_points",
			wantError:  "one SELECT statement only",
			wantUserV:  currentSchemaVersion,
			wantPoints: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run([]string{
				"query",
				"--config", configPath,
				"--json",
				tc.statement,
			}, stdout, stderr)
			if code != 1 {
				t.Fatalf("query exit code = %d, want 1", code)
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "query_failed")
			if !strings.Contains(got["message"].(string), tc.wantError) {
				t.Fatalf("message = %q, want %q", got["message"], tc.wantError)
			}
			assertArchiveUserVersion(t, archivePath, tc.wantUserV)
			assertArchiveTableCount(t, archivePath, "data_points", tc.wantPoints)
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestExportDatasetDefinitionsIncludeViewSQL(t *testing.T) {
	if len(exportDatasetDefinitions) != len(exportDatasetSpecs) {
		t.Fatalf("definition count = %d, lookup count = %d", len(exportDatasetDefinitions), len(exportDatasetSpecs))
	}
	seen := map[string]bool{}
	for _, spec := range exportDatasetDefinitions {
		if strings.TrimSpace(spec.name) == "" {
			t.Fatal("dataset name = empty")
		}
		if seen[spec.name] {
			t.Fatalf("duplicate dataset definition: %s", spec.name)
		}
		seen[spec.name] = true
		if strings.TrimSpace(spec.view) == "" {
			t.Fatalf("%s view = empty", spec.name)
		}
		if len(spec.fields) == 0 {
			t.Fatalf("%s fields = empty", spec.name)
		}
		if strings.TrimSpace(spec.orderBy) == "" {
			t.Fatalf("%s orderBy = empty", spec.name)
		}
		if strings.TrimSpace(spec.viewSQL) == "" {
			t.Fatalf("%s viewSQL = empty", spec.name)
		}
		if strings.Contains(spec.viewSQL, "CREATE VIEW") {
			t.Fatalf("%s viewSQL duplicates migration DDL", spec.name)
		}
		if spec.migrationVersion == 0 {
			t.Fatalf("%s migrationVersion = 0", spec.name)
		}
		if _, ok := exportDatasetSpecs[spec.name]; !ok {
			t.Fatalf("%s missing from lookup", spec.name)
		}
	}
}

func TestExportDatasetDefinitionsDriveViewMigrations(t *testing.T) {
	tests := []struct {
		name       string
		statements []string
		wantViews  []string
	}{
		{
			name:       "daily steps migration",
			statements: dailyStepsViewMigrationStatements(),
			wantViews:  []string{"daily_steps"},
		},
		{
			name:       "first release normalized views migration",
			statements: firstReleaseNormalizedViewMigrationStatements(),
			wantViews: []string{
				"heart_rate_samples",
				"resting_heart_rate_by_day",
				"sleep_sessions",
				"exercise_sessions",
				"weight_samples",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if len(test.statements) != len(test.wantViews) {
				t.Fatalf("statement count = %d, want %d: %v", len(test.statements), len(test.wantViews), test.statements)
			}
			for index, wantView := range test.wantViews {
				wantPrefix := "CREATE VIEW " + wantView + " AS\n"
				if !strings.HasPrefix(test.statements[index], wantPrefix) {
					t.Fatalf("statement %d = %q, want prefix %q", index, test.statements[index], wantPrefix)
				}
			}
		})
	}
	if got := normalizedViewsRegistry().MigrationStatements(999); len(got) != 0 {
		t.Fatalf("unknown migration statements = %v, want empty", got)
	}
}

func TestExportDatasetLookupRejectsDuplicateNames(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate dataset panic = nil")
		}
	}()
	exportDatasetSpecByName([]exportDatasetSpec{
		{name: "daily-steps"},
		{name: "daily-steps"},
	})
}

func TestExportDatasetLookupRejectsMissingNames(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("missing dataset name panic = nil")
		}
	}()
	exportDatasetSpecByName([]exportDatasetSpec{
		{view: "daily_steps"},
	})
}

// TestExportDatasetCatalogNamesSortedAndDeduped pins the discovery
// adapter's Names() contract: the slice is sorted alphabetically and
// contains no duplicates, even if the underlying registry would ever
// pick up two entries with the same name (defensive — exportDatasetSpecByName
// already panics on duplicates, but Names() owns the public surface, so
// it should not assume that).
func TestExportDatasetCatalogNamesSortedAndDeduped(t *testing.T) {
	catalog := newExportDatasetCatalog([]exportDatasetSpec{
		{name: "weight-samples"},
		{name: "daily-steps"},
		{name: "daily-steps"},
		{name: "heart-rate-samples"},
	})
	names := catalog.Names()
	want := []string{"daily-steps", "heart-rate-samples", "weight-samples"}
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("Names()[%d] = %q, want %q (full: %v)", i, names[i], n, names)
		}
	}
}

// TestExportDatasetCatalogFindHitAndMiss pins Find() returns (spec, true)
// for a known dataset and (zero, false) for an unknown one.
func TestExportDatasetCatalogFindHitAndMiss(t *testing.T) {
	catalog := newExportDatasetCatalog(exportDatasetDefinitions)
	if spec, ok := catalog.Find("daily-steps"); !ok || spec.name != "daily-steps" {
		t.Fatalf("Find(\"daily-steps\") = (%+v, %v), want spec with name daily-steps and ok=true", spec, ok)
	}
	if spec, ok := catalog.Find("totally-not-a-dataset"); ok || spec.name != "" {
		t.Fatalf("Find(\"totally-not-a-dataset\") = (%+v, %v), want (zero spec, false)", spec, ok)
	}
}

// TestExportDatasetCatalogSuggestRanking covers the Suggest() contract
// per the PRD slice 3 acceptance criteria: a one-edit typo returns the
// closest match first; a three-edit typo still surfaces a list; a
// gibberish input (distance > 3 from every name) returns an empty
// slice (not nil — the same shape contract commandRegistry.Suggest uses).
func TestExportDatasetCatalogSuggestRanking(t *testing.T) {
	catalog := newExportDatasetCatalog(exportDatasetDefinitions)

	// One-edit typo: "daily-step" → "daily-steps" (insert one char).
	suggestions := catalog.Suggest("daily-step")
	if len(suggestions) == 0 || suggestions[0] != "daily-steps" {
		t.Fatalf("Suggest(\"daily-step\") = %v, want first entry daily-steps", suggestions)
	}
	if len(suggestions) > 3 {
		t.Fatalf("Suggest(\"daily-step\") = %v, want at most 3 entries", suggestions)
	}

	// Three-edit typo: should still surface at least one suggestion.
	// "heart-rate-sample" is 1 edit from "heart-rate-samples" (insert 's').
	// Use a deliberately further typo to exercise the ≤3 cutoff.
	suggestions = catalog.Suggest("hart-rate-sample")
	if len(suggestions) == 0 {
		t.Fatalf("Suggest(\"hart-rate-sample\") = empty, want at least one heart-rate-samples-ish entry")
	}
	if suggestions[0] != "heart-rate-samples" {
		t.Fatalf("Suggest(\"hart-rate-sample\")[0] = %q, want heart-rate-samples", suggestions[0])
	}

	// Gibberish: no name within Levenshtein 3 → empty slice (not nil).
	suggestions = catalog.Suggest("xxxxxxxxxxxxxxxxxxxx")
	if suggestions == nil {
		t.Fatalf("Suggest gibberish = nil, want empty slice")
	}
	if len(suggestions) != 0 {
		t.Fatalf("Suggest gibberish = %v, want empty", suggestions)
	}
}

// TestExportDatasetCatalogSuggestCapAndTieBreak pins that Suggest returns
// at most 3 entries and that ties on distance break alphabetically.
func TestExportDatasetCatalogSuggestCapAndTieBreak(t *testing.T) {
	catalog := newExportDatasetCatalog([]exportDatasetSpec{
		{name: "bcde"},  // distance 1 from "abcde"
		{name: "abcdf"}, // distance 1
		{name: "abcdz"}, // distance 1
		{name: "abxxx"}, // distance 3
		{name: "yyyyy"}, // distance 5 → excluded
	})
	got := catalog.Suggest("abcde")
	if len(got) > 3 {
		t.Fatalf("Suggest cap exceeded: %v", got)
	}
	// Three names tie at distance 1: abcdf, abcdz, bcde. Alphabetical
	// order: abcdf, abcdz, bcde.
	want := []string{"abcdf", "abcdz", "bcde"}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Fatalf("Suggest(\"abcde\") = %v, want %v", got, want)
		}
	}
}

// TestExportHelpListsEveryDataset is the end-to-end drift guard for
// PRD #144 slice 3 AC: `gohealthcli export --help` stdout (or stderr —
// flag.PrintDefaults writes to FlagSet.Output()) must enumerate every
// name returned by the catalog so the binary's help text stays the
// authoritative surface for LLM and script consumers.
func TestExportHelpListsEveryDataset(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"export", "--help"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("export --help exit code = %d, want 0\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	// stdlib flag package writes --help output to FlagSet.Output(); the
	// export command sets that to stderr. Concatenate so we don't pin a
	// brittle stream choice and only assert content.
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "Supported datasets:") {
		t.Fatalf("export --help missing \"Supported datasets:\" section header\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
	for _, spec := range exportDatasetDefinitions {
		if !strings.Contains(combined, spec.name) {
			t.Errorf("export --help missing dataset name %q\nstdout=%s\nstderr=%s", spec.name, stdout.String(), stderr.String())
		}
	}
}

// TestExportTypoMentionsDidYouMeanAndHelp covers the AC pair: a typo
// like `daily-step` must produce stderr containing both "did you mean"
// and the closest match (daily-steps); a gibberish dataset name must
// produce stderr that points to `export --help`.
func TestExportTypoMentionsDidYouMeanAndHelp(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"daily-step",
		"--stdout",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("export daily-step exit code = %d, want 1\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	lower := strings.ToLower(stderr.String())
	if !strings.Contains(lower, "did you mean") {
		t.Fatalf("stderr missing \"did you mean\" line: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "daily-steps") {
		t.Fatalf("stderr missing suggestion daily-steps: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "export --help") {
		t.Fatalf("stderr missing \"export --help\" pointer: %q", stderr.String())
	}
}

// TestExportGibberishDatasetPointsAtHelp covers the AC variant where the
// typo is far enough that no suggestion fires; stderr must still point
// at `export --help` so the user knows where to discover the full list.
func TestExportGibberishDatasetPointsAtHelp(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"totally-not-a-dataset",
		"--stdout",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("export gibberish exit code = %d, want 1\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "export --help") {
		t.Fatalf("stderr missing \"export --help\" pointer: %q", stderr.String())
	}
}

// TestREADMEListsEveryExportDataset is the drift guard for issue #146:
// the README's "Normalized export datasets" section must enumerate every
// entry in exportDatasetDefinitions. If a new dataset is added to the
// registry without a README update, this test fails. Each name must
// appear as an inline code span (`name`) so the match is unambiguous
// and not satisfied by prose that happens to contain the substring.
//
// The search is scoped to the "Normalized export datasets" section only
// (between the section marker and the next markdown header). A name
// that appears in an unrelated code example elsewhere in the README
// must not silently satisfy the guard while the section itself stays
// stale.
func TestREADMEListsEveryExportDataset(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	const sectionMarker = "Normalized export datasets"
	readme := string(content)
	start := strings.Index(readme, sectionMarker)
	if start < 0 {
		t.Fatalf("README section marker %q not found; the drift guard cannot enforce sync. Restore the section or update sectionMarker.", sectionMarker)
	}
	rest := readme[start:]
	// The section ends at the next top-level or sub-section markdown
	// header. Skip the marker itself (which may be on a header line) by
	// searching from the next newline onward.
	bodyStart := strings.Index(rest, "\n")
	if bodyStart < 0 {
		bodyStart = 0
	}
	body := rest[bodyStart:]
	end := strings.Index(body, "\n## ")
	if end < 0 {
		end = len(body)
	}
	section := body[:end]
	for _, spec := range exportDatasetDefinitions {
		token := "`" + spec.name + "`"
		if !strings.Contains(section, token) {
			t.Errorf("README \"Normalized export datasets\" section missing %s (looked for %q); update README.md to match exportDatasetDefinitions", spec.name, token)
		}
	}
}

func TestDailyStepsNormalizedViewPrefersRollupsAndAggregatesDataPoints(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportStepDataPoint(t, archivePath, "users/me/dataTypes/steps/dataPoints/c", "2026-01-01T12:00:00Z", "2026-01-01T12:10:00Z", `{"steps":{"count":"128"}}`)
	insertExportStepDataPoint(t, archivePath, "users/me/dataTypes/steps/dataPoints/d", "2026-01-04T12:00:00Z", "2026-01-04T12:10:00Z", `{"steps":{"count":"1"}}`)
	insertExportStepDataPointWithSourceFamily(t, archivePath, "users/me/dataTypes/steps/dataPoints/wearable", "2026-01-01T08:00:00Z", "2026-01-01T08:15:00Z", `{"steps":{"count":"256"}}`, "wearable")
	insertExportStepDataPointWithSourceFamily(t, archivePath, "users/me/dataTypes/steps/dataPoints/wearable-rollup-day", "2026-01-04T08:00:00Z", "2026-01-04T08:15:00Z", `{"steps":{"count":"384"}}`, "wearable")

	rows, err := exportRows(archivePath, exportDatasetSpecs["daily-steps"])
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

func TestExportDailyStepsRefusesSymlinkedOutput(t *testing.T) {
	if !usesPOSIXPermissions() {
		t.Skip("symlink permission assertions are not meaningful on this platform")
	}
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	targetPath := filepath.Join(tempDir, "target.csv")
	const targetContent = "do not overwrite me\n"
	if err := os.WriteFile(targetPath, []byte(targetContent), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.Chmod(targetPath, 0o644); err != nil {
		t.Fatalf("chmod seed target: %v", err)
	}
	outputPath := filepath.Join(tempDir, "daily-steps.csv")
	if err := os.Symlink(targetPath, outputPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
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
	if code == 0 {
		t.Fatalf("export exit code = 0, want non-zero for symlinked --output\nstderr: %s\nstdout: %s", stderr.String(), stdout.String())
	}
	if !strings.Contains(stderr.String(), "symbolic link") {
		t.Fatalf("stderr = %q, want a symbolic link refusal", stderr.String())
	}
	if !strings.Contains(stderr.String(), "resolved target path") {
		t.Fatalf("stderr = %q, want guidance to pass the resolved target path", stderr.String())
	}

	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(content) != targetContent {
		t.Fatalf("target content = %q, want %q (must be untouched)", string(content), targetContent)
	}
	info, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("lstat target: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("target mode = %04o, want 0644 (must be unchanged)", info.Mode().Perm())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
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
	var first struct {
		ProviderName       string `json:"provider_name"`
		ConnectionID       string `json:"connection_id"`
		CivilDate          string `json:"civil_date"`
		StepCount          int64  `json:"step_count"`
		SourceKind         string `json:"source_kind"`
		SourceFamilyFilter string `json:"source_family_filter"`
		SourceRecordCount  int64  `json:"source_record_count"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first JSONL line is invalid: %v\nline: %s", err, lines[0])
	}
	if first.ProviderName != "googlehealth" ||
		first.ConnectionID != "googlehealth:111111256096816351" ||
		first.CivilDate != "2026-01-01" ||
		first.StepCount != 512 ||
		first.SourceKind != "dataPoints" ||
		first.SourceFamilyFilter != "" ||
		first.SourceRecordCount != 1 {
		t.Fatalf("first JSONL row = %+v, want date=2026-01-01 steps=512 source=dataPoints source_family=\"\" records=1", first)
	}
	if !strings.Contains(lines[0], `"civil_date":"2026-01-01"`) || !strings.Contains(lines[0], `"step_count":512`) {
		t.Fatalf("first JSONL line missing stable fields: %s", lines[0])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestWriteExportJSONLCanonicalizesIntegerFields(t *testing.T) {
	output := new(bytes.Buffer)
	err := writeExportJSONL([]exportRow{{
		"googlehealth",
		"googlehealth:111111256096816351",
		"2026-01-01",
		"+512",
		"dataPoints",
		"",
		"01",
		"2026-01-01T08:15:00Z",
	}}, exportDatasetSpecs["daily-steps"], output)
	if err != nil {
		t.Fatalf("write JSONL: %v", err)
	}
	want := `{"provider_name":"googlehealth","connection_id":"googlehealth:111111256096816351","civil_date":"2026-01-01","step_count":512,"source_kind":"dataPoints","source_family_filter":"","source_record_count":1,"latest_source_timestamp":"2026-01-01T08:15:00Z"}` + "\n"
	if output.String() != want {
		t.Fatalf("JSONL =\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestExportHeartRateSamplesJSONLFromNormalizedView(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "heart-rate",
		resourceName: "users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a",
		recordKind:   "sample",
		startUTC:     "2026-01-01T07:30:00Z",
		startCivil:   "2026-01-01T08:30:00",
		civilDate:    "2026-01-01",
		dataSource:   `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`,
		rawJSON:      `{"heartRate":{"sampleTime":{"physicalTime":"2026-01-01T08:30:00+01:00"},"beatsPerMinute":"72"}}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"heart-rate-samples",
		"--format", "jsonl",
		"--stdout",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := `{"provider_name":"googlehealth","connection_id":"googlehealth:111111256096816351","sample_time_utc":"2026-01-01T07:30:00Z","sample_civil_time":"2026-01-01T08:30:00","civil_date":"2026-01-01","beats_per_minute":"72","source_family_filter":"","upstream_resource_name":"users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a"}` + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %s\nwant = %s", stdout.String(), want)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestExportRemainingFirstReleaseDatasetsCSVAndJSONL(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "heart-rate",
		resourceName: "users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a",
		recordKind:   "sample",
		startUTC:     "2026-01-01T07:30:00Z",
		startCivil:   "2026-01-01T08:30:00",
		civilDate:    "2026-01-01",
		dataSource:   `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`,
		rawJSON:      `{"heartRate":{"sampleTime":{"physicalTime":"2026-01-01T08:30:00+01:00"},"beatsPerMinute":"72"}}`,
	})
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "daily-resting-heart-rate",
		resourceName: "users/me/dataTypes/daily-resting-heart-rate/dataPoints/rhr-2026-01-01",
		recordKind:   "daily",
		civilDate:    "2026-01-01",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"dailyRestingHeartRate":{"date":{"year":2026,"month":1,"day":1},"beatsPerMinute":"61"}}`,
	})
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "sleep",
		resourceName: "users/me/dataTypes/sleep/dataPoints/sleep-2026-01-01",
		recordKind:   "session",
		startUTC:     "2026-01-01T21:30:00Z",
		endUTC:       "2026-01-02T05:45:00Z",
		startCivil:   "2026-01-01T22:30:00",
		endCivil:     "2026-01-02T06:45:00",
		civilDate:    "2026-01-01",
		dataSource:   `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`,
		rawJSON:      `{"sleep":{"interval":{"startTime":"2026-01-01T22:30:00+01:00","endTime":"2026-01-02T06:45:00+01:00"},"stages":[{"type":"LIGHT"}]}}`,
	})
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "exercise",
		resourceName: "users/me/dataTypes/exercise/dataPoints/exercise-2026-01-01",
		recordKind:   "session",
		startUTC:     "2026-01-01T16:15:00Z",
		endUTC:       "2026-01-01T16:45:00Z",
		startCivil:   "2026-01-01T17:15:00",
		endCivil:     "2026-01-01T17:45:00",
		civilDate:    "2026-01-01",
		dataSource:   `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`,
		rawJSON:      `{"exercise":{"interval":{"startTime":"2026-01-01T17:15:00+01:00","endTime":"2026-01-01T17:45:00+01:00"},"exerciseType":"RUNNING","activeDuration":"1800s"}}`,
	})
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "weight",
		resourceName: "users/me/dataTypes/weight/dataPoints/weight-2026-01-01",
		recordKind:   "sample",
		startUTC:     "2026-01-01T05:45:00Z",
		startCivil:   "2026-01-01T06:45:00",
		civilDate:    "2026-01-01",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"weight":{"sampleTime":{"physicalTime":"2026-01-01T06:45:00+01:00"},"weightGrams":71234.5}}`,
	})

	tests := []struct {
		dataset  string
		wantCSV  string
		wantJSON string
	}{
		{
			dataset: "heart-rate-samples",
			wantCSV: "provider_name,connection_id,sample_time_utc,sample_civil_time,civil_date,beats_per_minute,source_family_filter,upstream_resource_name\n" +
				"googlehealth,googlehealth:111111256096816351,2026-01-01T07:30:00Z,2026-01-01T08:30:00,2026-01-01,72,,users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a\n",
			wantJSON: `{"provider_name":"googlehealth","connection_id":"googlehealth:111111256096816351","sample_time_utc":"2026-01-01T07:30:00Z","sample_civil_time":"2026-01-01T08:30:00","civil_date":"2026-01-01","beats_per_minute":"72","source_family_filter":"","upstream_resource_name":"users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a"}` + "\n",
		},
		{
			dataset: "resting-heart-rate-by-day",
			wantCSV: "provider_name,connection_id,civil_date,beats_per_minute,source_family_filter,upstream_resource_name\n" +
				"googlehealth,googlehealth:111111256096816351,2026-01-01,61,,users/me/dataTypes/daily-resting-heart-rate/dataPoints/rhr-2026-01-01\n",
			wantJSON: `{"provider_name":"googlehealth","connection_id":"googlehealth:111111256096816351","civil_date":"2026-01-01","beats_per_minute":"61","source_family_filter":"","upstream_resource_name":"users/me/dataTypes/daily-resting-heart-rate/dataPoints/rhr-2026-01-01"}` + "\n",
		},
		{
			dataset: "sleep-sessions",
			wantCSV: "provider_name,connection_id,start_time_utc,end_time_utc,start_civil_time,end_civil_time,civil_date,source_family_filter,upstream_resource_name\n" +
				"googlehealth,googlehealth:111111256096816351,2026-01-01T21:30:00Z,2026-01-02T05:45:00Z,2026-01-01T22:30:00,2026-01-02T06:45:00,2026-01-01,,users/me/dataTypes/sleep/dataPoints/sleep-2026-01-01\n",
			wantJSON: `{"provider_name":"googlehealth","connection_id":"googlehealth:111111256096816351","start_time_utc":"2026-01-01T21:30:00Z","end_time_utc":"2026-01-02T05:45:00Z","start_civil_time":"2026-01-01T22:30:00","end_civil_time":"2026-01-02T06:45:00","civil_date":"2026-01-01","source_family_filter":"","upstream_resource_name":"users/me/dataTypes/sleep/dataPoints/sleep-2026-01-01"}` + "\n",
		},
		{
			dataset: "exercise-sessions",
			wantCSV: "provider_name,connection_id,start_time_utc,end_time_utc,start_civil_time,end_civil_time,civil_date,exercise_type,active_duration,source_family_filter,upstream_resource_name\n" +
				"googlehealth,googlehealth:111111256096816351,2026-01-01T16:15:00Z,2026-01-01T16:45:00Z,2026-01-01T17:15:00,2026-01-01T17:45:00,2026-01-01,RUNNING,1800s,,users/me/dataTypes/exercise/dataPoints/exercise-2026-01-01\n",
			wantJSON: `{"provider_name":"googlehealth","connection_id":"googlehealth:111111256096816351","start_time_utc":"2026-01-01T16:15:00Z","end_time_utc":"2026-01-01T16:45:00Z","start_civil_time":"2026-01-01T17:15:00","end_civil_time":"2026-01-01T17:45:00","civil_date":"2026-01-01","exercise_type":"RUNNING","active_duration":"1800s","source_family_filter":"","upstream_resource_name":"users/me/dataTypes/exercise/dataPoints/exercise-2026-01-01"}` + "\n",
		},
		{
			dataset: "weight-samples",
			wantCSV: "provider_name,connection_id,sample_time_utc,sample_civil_time,civil_date,weight_grams,source_family_filter,upstream_resource_name\n" +
				"googlehealth,googlehealth:111111256096816351,2026-01-01T05:45:00Z,2026-01-01T06:45:00,2026-01-01,71234.5,,users/me/dataTypes/weight/dataPoints/weight-2026-01-01\n",
			wantJSON: `{"provider_name":"googlehealth","connection_id":"googlehealth:111111256096816351","sample_time_utc":"2026-01-01T05:45:00Z","sample_civil_time":"2026-01-01T06:45:00","civil_date":"2026-01-01","weight_grams":"71234.5","source_family_filter":"","upstream_resource_name":"users/me/dataTypes/weight/dataPoints/weight-2026-01-01"}` + "\n",
		},
	}
	for _, test := range tests {
		t.Run(test.dataset+"/csv", func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run([]string{"export", "--config", configPath, test.dataset, "--format", "csv", "--stdout"}, stdout, stderr)
			if code != 0 {
				t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
			}
			if stdout.String() != test.wantCSV {
				t.Fatalf("CSV =\n%s\nwant:\n%s", stdout.String(), test.wantCSV)
			}
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
		t.Run(test.dataset+"/jsonl", func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run([]string{"export", "--config", configPath, test.dataset, "--format", "jsonl", "--stdout"}, stdout, stderr)
			if code != 0 {
				t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
			}
			if stdout.String() != test.wantJSON {
				t.Fatalf("JSONL =\n%s\nwant:\n%s", stdout.String(), test.wantJSON)
			}
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
	}
}

func TestExportDailyStepsRequiresExplicitDestination(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	datasets := []string{
		"daily-steps",
		"heart-rate-samples",
		"resting-heart-rate-by-day",
		"sleep-sessions",
		"exercise-sessions",
		"weight-samples",
	}
	for _, dataset := range datasets {
		t.Run(dataset+"/missing destination", func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run([]string{"export", "--config", configPath, dataset, "--format", "csv"}, stdout, stderr)
			if code != 1 {
				t.Fatalf("export exit code = %d, want 1", code)
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), "requires --output PATH or --stdout") {
				t.Fatalf("stderr = %q, want destination error", stderr.String())
			}
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
	}

	for _, tc := range []struct {
		name       string
		args       []string
		wantStderr string
	}{
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

func TestExportMigratesLegacyV4ArchiveBeforeReadingNormalizedView(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV4Archive(t, archivePath)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "heart-rate",
		resourceName: "users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a",
		recordKind:   "sample",
		startUTC:     "2026-01-01T07:30:00Z",
		startCivil:   "2026-01-01T08:30:00",
		civilDate:    "2026-01-01",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"heartRate":{"beatsPerMinute":"72"}}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"export", "--config", configPath, "heart-rate-samples", "--format", "csv", "--stdout"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("export exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := "provider_name,connection_id,sample_time_utc,sample_civil_time,civil_date,beats_per_minute,source_family_filter,upstream_resource_name\n" +
		"googlehealth,googlehealth:111111256096816351,2026-01-01T07:30:00Z,2026-01-01T08:30:00,2026-01-01,72,,users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a\n"
	if stdout.String() != want {
		t.Fatalf("stdout =\n%s\nwant:\n%s", stdout.String(), want)
	}
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
	assertNoSecretWords(t, stdout.String()+stderr.String())
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
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "steps",
		resourceName: resourceName,
		recordKind:   "interval",
		startUTC:     startUTC,
		endUTC:       endUTC,
		dataSource:   "{}",
		sourceFamily: sourceFamily,
		rawJSON:      rawJSON,
	})
}

type exportDataPointFixture struct {
	dataType     string
	resourceName string
	recordKind   string
	startUTC     any
	endUTC       any
	startCivil   any
	endCivil     any
	civilDate    any
	dataSource   string
	sourceFamily string
	rawJSON      string
}

func insertExportDataPoint(t *testing.T, archivePath string, point exportDataPointFixture) {
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
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		data_source_json,
		source_family_filter,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		point.dataType,
		point.resourceName,
		point.recordKind,
		point.startUTC,
		point.endUTC,
		point.startCivil,
		point.endCivil,
		point.civilDate,
		point.dataSource,
		nullString(point.sourceFamily),
		point.rawJSON,
		"2026-01-04T00:00:00Z",
		"2026-01-04T00:00:00Z",
	); err != nil {
		t.Fatalf("insert export Data Point: %v", err)
	}
}

// TestResolveExportFormat pins the small export-specific validator that
// maps the Common Flag Set synonyms onto the export-specific --format
// value. The mutual exclusion of --plain and --json itself is enforced
// at the Common Flag Set seam (covered by
// TestExportPlainAndJSONMutuallyExclusive); this table covers the
// synonym ↔ --format interactions.
func TestResolveExportFormat(t *testing.T) {
	cases := []struct {
		name           string
		format         string
		formatExplicit bool
		jsonSynonym    bool
		plainSynonym   bool
		want           string
		wantErr        string
	}{
		{name: "default", format: "csv", want: "csv"},
		{name: "explicit-format-csv", format: "csv", formatExplicit: true, want: "csv"},
		{name: "explicit-format-jsonl", format: "jsonl", formatExplicit: true, want: "jsonl"},
		{name: "json-synonym-no-format", format: "csv", jsonSynonym: true, want: "jsonl"},
		{name: "plain-synonym-no-format", format: "csv", plainSynonym: true, want: "csv"},
		{name: "json-synonym-with-jsonl-format-is-redundant-not-conflict", format: "jsonl", formatExplicit: true, jsonSynonym: true, want: "jsonl"},
		{name: "plain-synonym-with-csv-format-is-redundant-not-conflict", format: "csv", formatExplicit: true, plainSynonym: true, want: "csv"},
		{name: "json-vs-explicit-csv-conflicts", format: "csv", formatExplicit: true, jsonSynonym: true, wantErr: "--json conflicts with --format csv"},
		{name: "plain-vs-explicit-jsonl-conflicts", format: "jsonl", formatExplicit: true, plainSynonym: true, wantErr: "--plain conflicts with --format jsonl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveExportFormat(tc.format, tc.formatExplicit, tc.jsonSynonym, tc.plainSynonym)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExportJSONFlagSynonymousWithFormatJSONL is the tracer for PRD #143
// slice 9 (#184): `export --json` is the documented Common Flag Set
// synonym for `--format jsonl`. Both invocations must emit the exact
// same payload on stdout.
func TestExportJSONFlagSynonymousWithFormatJSONL(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	jsonStdout := new(bytes.Buffer)
	jsonStderr := new(bytes.Buffer)
	jsonCode := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"daily-steps",
		"--stdout",
	}, jsonStdout, jsonStderr)
	if jsonCode != 0 {
		t.Fatalf("export --json exit code = %d, want 0\nstderr=%s", jsonCode, jsonStderr.String())
	}

	formatStdout := new(bytes.Buffer)
	formatStderr := new(bytes.Buffer)
	formatCode := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--format", "jsonl",
		"daily-steps",
		"--stdout",
	}, formatStdout, formatStderr)
	if formatCode != 0 {
		t.Fatalf("export --format jsonl exit code = %d, want 0\nstderr=%s", formatCode, formatStderr.String())
	}
	if jsonStdout.String() != formatStdout.String() {
		t.Fatalf("--json stdout differs from --format jsonl stdout\n--json:\n%s\n--format jsonl:\n%s", jsonStdout.String(), formatStdout.String())
	}
}

// TestExportPlainFlagSynonymousWithFormatCSV is the tracer for PRD #143
// slice 9 (#184): `export --plain` is the documented Common Flag Set
// synonym for `--format csv`. Both invocations must emit the exact same
// payload on stdout.
func TestExportPlainFlagSynonymousWithFormatCSV(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	plainStdout := new(bytes.Buffer)
	plainStderr := new(bytes.Buffer)
	plainCode := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
		"daily-steps",
		"--stdout",
	}, plainStdout, plainStderr)
	if plainCode != 0 {
		t.Fatalf("export --plain exit code = %d, want 0\nstderr=%s", plainCode, plainStderr.String())
	}

	formatStdout := new(bytes.Buffer)
	formatStderr := new(bytes.Buffer)
	formatCode := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--format", "csv",
		"daily-steps",
		"--stdout",
	}, formatStdout, formatStderr)
	if formatCode != 0 {
		t.Fatalf("export --format csv exit code = %d, want 0\nstderr=%s", formatCode, formatStderr.String())
	}
	if plainStdout.String() != formatStdout.String() {
		t.Fatalf("--plain stdout differs from --format csv stdout\n--plain:\n%s\n--format csv:\n%s", plainStdout.String(), formatStdout.String())
	}
}

// TestExportRejectsJSONWithConflictingFormatCSV pins the small validator
// that lives in export.go (NOT in the Common Flag Set module): when
// --json and --format csv are both passed, the command fails with a
// targeted "--json conflicts with --format csv" error via the unified
// Failure Reporter. The same applies to --plain --format jsonl.
func TestExportRejectsJSONWithConflictingFormatCSV(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"--format", "csv",
		"daily-steps",
		"--stdout",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("export --json --format csv exit code = %d, want 1\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "--json conflicts with --format csv") {
		t.Fatalf("output missing conflict error\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
}

// TestExportRejectsPlainWithConflictingFormatJSONL mirrors the above for
// the --plain side.
func TestExportRejectsPlainWithConflictingFormatJSONL(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
		"--format", "jsonl",
		"daily-steps",
		"--stdout",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("export --plain --format jsonl exit code = %d, want 1\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "--plain conflicts with --format jsonl") {
		t.Fatalf("output missing conflict error\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
}

// TestExportFailureHonorsJSONMode pins that runExport's ReportFailure
// call sites pass Mode so a `--json` invocation emits the unified
// `{"status":"...","message":"..."}` envelope on stdout (matching the
// failure_reporter contract every other migrated subcommand follows),
// instead of falling back to the default `<cmd>: <msg>` line on stderr.
// Regression for PRD #143 follow-up: the slice-9 export migration
// dropped Mode on every ReportFailure inside runExport.
func TestExportFailureHonorsJSONMode(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"bogus-dataset",
		"--stdout",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("export --json bogus exit code = %d, want 1\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr should be empty in --json mode, got %q", stderr.String())
	}
	var envelope struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a single-line JSON envelope: %v\nstdout=%q", err, stdout.String())
	}
	if envelope.Status != "flag_invalid" {
		t.Fatalf("envelope.status = %q, want flag_invalid", envelope.Status)
	}
	if !strings.Contains(envelope.Message, "bogus-dataset") {
		t.Fatalf("envelope.message = %q, want contains \"bogus-dataset\"", envelope.Message)
	}
}

// TestExportMultiPositionalFailureHonorsJSONMode pins that the multi-
// dataset rejection (e.g. `export --json a b`) emits the unified JSON
// envelope on stdout. The check used to fire inside splitExportArgs
// BEFORE ParseCommon ran, so common.JSONOutput had not yet been
// populated from the inner --json flag and the failure fell back to
// default mode. The fix defers the multi-positional rejection to AFTER
// ParseCommon so Mode is known when ReportFailure runs.
func TestExportMultiPositionalFailureHonorsJSONMode(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"daily-steps",
		"extra-arg",
		"--stdout",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("export --json daily-steps extra-arg exit code = %d, want 1\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr should be empty in --json mode, got %q", stderr.String())
	}
	var envelope struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a single-line JSON envelope: %v\nstdout=%q", err, stdout.String())
	}
	if envelope.Status != "flag_invalid" {
		t.Fatalf("envelope.status = %q, want flag_invalid", envelope.Status)
	}
	if !strings.Contains(envelope.Message, "exactly one dataset") {
		t.Fatalf("envelope.message = %q, want contains \"exactly one dataset\"", envelope.Message)
	}
}

// TestExportFailureHonorsPlainMode mirrors the JSON-mode test for
// --plain: the failure should print the `<cmd>: <msg>` line on stderr
// AND the `status: <s>\nmessage: <m>\n` block on stdout. Without Mode
// threading, the stdout block is missing.
func TestExportFailureHonorsPlainMode(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
		"bogus-dataset",
		"--stdout",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("export --plain bogus exit code = %d, want 1\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "export: ") {
		t.Fatalf("stderr missing `export: ` prefix in --plain mode: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "status: flag_invalid") {
		t.Fatalf("stdout missing `status: flag_invalid` block in --plain mode: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "message: ") {
		t.Fatalf("stdout missing `message: ` line in --plain mode: %q", stdout.String())
	}
}

// TestExportPlainAndJSONMutuallyExclusive pins the CommonFlagSet
// mutual-exclusion contract for the export verb.
func TestExportPlainAndJSONMutuallyExclusive(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"export",
		"--config", configPath,
		"--db", archivePath,
		"--plain",
		"--json",
		"daily-steps",
		"--stdout",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("export --plain --json exit code = %d, want 1\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "--plain and --json are mutually exclusive") {
		t.Fatalf("output missing mutual-exclusion error\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
}

// assertDailyStepsRow asserts one daily-steps exportRow in the live
// dataset field order (provider_name, connection_id, civil_date,
// step_count, source_kind, source_family_filter, source_record_count,
// latest_source_timestamp). The latest_source_timestamp column is
// fixture-derived and not pinned here, matching the historical helper.
func assertDailyStepsRow(t *testing.T, row exportRow, civilDate string, stepCount int64, sourceKind, sourceFamily string, sourceRecordCount int64) {
	t.Helper()
	if len(row) != 8 ||
		row[0] != "googlehealth" ||
		row[1] != "googlehealth:111111256096816351" ||
		row[2] != civilDate ||
		row[3] != strconv.FormatInt(stepCount, 10) ||
		row[4] != sourceKind ||
		row[5] != sourceFamily ||
		row[6] != strconv.FormatInt(sourceRecordCount, 10) {
		t.Fatalf("row = %v, want date=%s steps=%d source=%s source_family=%s records=%d", row, civilDate, stepCount, sourceKind, sourceFamily, sourceRecordCount)
	}
}

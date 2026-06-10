package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestDescribeSchemaSQLDumpsTablesAndViews is the tracer for #109's
// --sql mode: every table and view from sqlite_master appears in the
// DDL dump, internal SQLite tables (sqlite_*) are excluded.
func TestDescribeSchemaSQLDumpsTablesAndViews(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"describe-schema", "--config", configPath, "--db", archivePath, "--sql"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("describe-schema --sql exit code = %d, stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	// Core tables and a few representative views must appear.
	// SQLite quotes renamed tables in sqlite_master.sql (post-ALTER), so
	// match either form: 'CREATE TABLE x' or 'CREATE TABLE "x"'.
	wantTables := []string{"connections", "data_points", "identity_snapshots", "sync_runs", "sync_cursors"}
	for _, table := range wantTables {
		unquoted := "CREATE TABLE " + table
		quoted := `CREATE TABLE "` + table + `"`
		if !strings.Contains(output, unquoted) && !strings.Contains(output, quoted) {
			t.Errorf("--sql output missing CREATE TABLE %s (or quoted variant)", table)
		}
	}
	wantViews := []string{"daily_steps", "current_settings", "paired_devices", "searchable_text"}
	for _, view := range wantViews {
		if !strings.Contains(output, view) {
			t.Errorf("--sql output missing view %q", view)
		}
	}
	if strings.Contains(output, "CREATE TABLE sqlite_") {
		t.Error("--sql output included internal sqlite_* table; want filtered")
	}
}

// TestDescribeSchemaJSONIncludesEveryRegisteredView is the tracer for
// the --json mode: every view registered in normalizedViewsRegistry()
// surfaces in the JSON catalog with at least its name + columns. The
// curated narrative file lives separately and merges in.
func TestDescribeSchemaJSONIncludesEveryRegisteredView(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"describe-schema", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("describe-schema --json exit code = %d, stderr=%s", code, stderr.String())
	}
	var catalog struct {
		Views []struct {
			Name    string   `json:"name"`
			Columns []string `json:"columns"`
		} `json:"views"`
		Tables []struct {
			Name string `json:"name"`
		} `json:"tables"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, stdout.String())
	}
	gotViews := make(map[string]bool)
	for _, view := range catalog.Views {
		gotViews[view.Name] = true
		if len(view.Columns) == 0 {
			t.Errorf("view %q has no columns in catalog", view.Name)
		}
	}
	for _, name := range normalizedViewsRegistry().Catalog() {
		spec, _ := normalizedViewsRegistry().View(name)
		if !gotViews[spec.view] {
			t.Errorf("registered view %q (registry name %q) missing from --json catalog", spec.view, name)
		}
	}
}

// TestDescribeSchemaJSONIncludesCuratedNarrative pins that the
// hand-curated narrative file (docs/llm-schema.json) is merged into
// the --json output. Downstream tools rely on the narrative for
// guidance ("for unit-aware queries JOIN current_settings", etc).
func TestDescribeSchemaJSONIncludesCuratedNarrative(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"describe-schema", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("describe-schema --json exit code = %d, stderr=%s", code, stderr.String())
	}
	var catalog struct {
		Narrative struct {
			Guidance                   []string          `json:"guidance"`
			PreferredViewsByQueryClass map[string]string `json:"preferred_views_by_query_class"`
		} `json:"narrative"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	if len(catalog.Narrative.Guidance) == 0 {
		t.Fatal("narrative.guidance empty; want hand-curated entries merged in")
	}
	if catalog.Narrative.PreferredViewsByQueryClass["daily step totals"] != "daily_steps" {
		t.Fatalf("narrative.preferred_views_by_query_class['daily step totals'] = %q, want daily_steps",
			catalog.Narrative.PreferredViewsByQueryClass["daily step totals"])
	}
}

// TestDescribeSchemaPlainEmitsJSONCatalogWithStderrNote is the tracer for
// PRD #143 slice 9 (#184): `describe-schema --plain` exits 0, emits the
// JSON catalog to stdout (the schema has no key/value plain shape so
// --plain is a no-op), and surfaces a `// note: --plain is a no-op …`
// comment line on stderr so the user knows their flag was honoured as a
// best-effort fallback. Stdout users redirecting to a file still get the
// catalog uncluttered by the note.
func TestDescribeSchemaPlainEmitsJSONCatalogWithStderrNote(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"describe-schema", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("describe-schema --plain exit code = %d, want 0\nstderr=%s", code, stderr.String())
	}
	var catalog struct {
		Version int `json:"version"`
		Views   []struct {
			Name string `json:"name"`
		} `json:"views"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("--plain output is not valid JSON catalog: %v\nstdout=%s", err, stdout.String())
	}
	if catalog.Version != schemaCatalogVersion {
		t.Fatalf("catalog.version = %d, want %d", catalog.Version, schemaCatalogVersion)
	}
	if len(catalog.Views) == 0 {
		t.Fatal("catalog.views is empty; want the JSON catalog on stdout")
	}
	if !strings.Contains(stderr.String(), "// note: --plain is a no-op on describe-schema; emitting JSON catalog") {
		t.Fatalf("stderr missing --plain no-op note\nstderr=%s", stderr.String())
	}
}

// TestDescribeSchemaPlainAndJSONMutuallyExclusive pins the CommonFlagSet
// mutual-exclusion contract: passing --plain and --json together exits 1
// with the documented error, even though --plain is otherwise a no-op
// (the user's intent is contradictory and we surface that explicitly).
func TestDescribeSchemaPlainAndJSONMutuallyExclusive(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"describe-schema", "--config", configPath, "--db", archivePath, "--plain", "--json"}, stdout, stderr)
	if code != 1 {
		t.Fatalf("describe-schema --plain --json exit code = %d, want 1\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "--plain and --json are mutually exclusive") {
		t.Fatalf("output missing mutual-exclusion error\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
}

// TestDescribeSchemaSQLStillOverridesJSONDefault pins that --sql remains
// the explicit override of the --json default after the CommonFlagSet
// migration. The existing `TestDescribeSchemaSQLDumpsTablesAndViews`
// covers the happy path, but does not include the global --json flag.
// This test guards the documented interaction: `--sql` wins over the
// implicit `--json=true` default that RegisterCommon seeds.
func TestDescribeSchemaSQLStillOverridesJSONDefault(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"describe-schema", "--config", configPath, "--db", archivePath, "--sql"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("describe-schema --sql exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "CREATE TABLE") && !strings.Contains(stdout.String(), `CREATE TABLE "`) {
		t.Fatalf("--sql output missing DDL; got %q", stdout.String())
	}
	// --sql output is DDL, NOT a JSON catalog — the first non-space
	// character must not be `{`.
	trimmed := strings.TrimLeft(stdout.String(), " \t\r\n")
	if strings.HasPrefix(trimmed, "{") {
		t.Fatalf("--sql output looks like JSON catalog (starts with `{`); expected DDL\nstdout=%s", stdout.String())
	}
}

// TestDescribeSchemaJSONHonorsExplicitDBAgainstTwoDistinctArchives is
// the end-to-end tracer for PRD #144 slice 1 (issue #155) acceptance
// criterion "describe-schema --db against two temp archives with
// different schema state produces different JSON outputs". This proves
// `--db` is honoured (not merely a regression-only assertion): pointing
// the same binary at two distinct archive paths must yield two distinct
// catalogs because each catalog reflects the live `sqlite_master` of
// the archive at the resolved `--db` path.
//
// The two archives diverge by an extra table in archive B (the
// resolver-honours-it test does not care which side of the schema is
// different, only that one archive's catalog is observably distinct
// from the other under the same binary invocation).
func TestDescribeSchemaJSONHonorsExplicitDBAgainstTwoDistinctArchives(t *testing.T) {
	tempDir := t.TempDir()
	archiveA := filepath.Join(tempDir, "a", "a.sqlite")
	archiveB := filepath.Join(tempDir, "b", "b.sqlite")
	if err := createArchive(archiveA); err != nil {
		t.Fatalf("create archive A: %v", err)
	}
	if err := createArchive(archiveB); err != nil {
		t.Fatalf("create archive B: %v", err)
	}
	// Introduce a divergence the catalog will surface: an extra plain
	// table in archive B. The catalog's `Tables` block is built from
	// sqlite_master so the stray table only shows up against archive B.
	db, err := openArchive(archiveB)
	if err != nil {
		t.Fatalf("open archive B: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE slice1_marker (id INTEGER PRIMARY KEY)`); err != nil {
		db.Close()
		t.Fatalf("create marker table on B: %v", err)
	}
	db.Close()

	runDescribeSchema := func(t *testing.T, archivePath string) string {
		t.Helper()
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		code := run([]string{"describe-schema", "--db", archivePath, "--json"}, stdout, stderr)
		if code != 0 {
			t.Fatalf("describe-schema --db %s exit code = %d, stderr=%s", archivePath, code, stderr.String())
		}
		return stdout.String()
	}

	outA := runDescribeSchema(t, archiveA)
	outB := runDescribeSchema(t, archiveB)

	if outA == outB {
		t.Fatalf("describe-schema --json produced identical output for two distinct archives — --db was not honoured\nA=%s\nB=%s", archiveA, archiveB)
	}
	if !strings.Contains(outB, "slice1_marker") {
		t.Fatalf("archive B catalog missing slice1_marker table; output may be from default archive, not --db\nB=%s\n%s", archiveB, outB)
	}
	if strings.Contains(outA, "slice1_marker") {
		t.Fatalf("archive A catalog unexpectedly includes slice1_marker; output may be from archive B\nA=%s\n%s", archiveA, outA)
	}
}

// TestDescribeSchemaJSONDriftDetectionFailsWhenViewMissingFromCatalog
// is the CI guard: a view present in sqlite_master without a
// corresponding catalog entry causes the test to fail. This pins the
// PRD's contract that downstream tools (a Claude skill, MCP server)
// can trust the JSON catalog as the source of truth.
func TestDescribeSchemaJSONDriftDetectionFailsWhenViewMissingFromCatalog(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	// Add a stray view not registered with the Registry.
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.Exec(`CREATE VIEW orphan_view AS SELECT 1 AS x`); err != nil {
		db.Close()
		t.Fatalf("create orphan view: %v", err)
	}
	db.Close()

	if err := assertNoSchemaDrift(archivePath); err == nil {
		t.Fatal("assertNoSchemaDrift returned nil; want drift detected (orphan_view has no catalog entry)")
	}
}

// TestNormalizeViewColumnTypeFallback is the tracer for PRD #144 slice 8
// (issue #159): SQLite views do not carry declared column types, so
// pragma_table_info on a view reports the type as either empty or the
// literal "BLOB" — both of which would mislead an LLM reading the JSON
// catalog as a contract. The PRD's Architecture Notes deliberately reject
// parsing the view's SELECT projection (one bespoke SQL parser sitting
// next to a working pragma call); we stamp "unknown" instead. Real
// declared SQL types pass through unchanged.
func TestNormalizeViewColumnTypeFallback(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "unknown"},
		{"BLOB", "unknown"},
		{"INTEGER", "INTEGER"},
		{"TEXT", "TEXT"},
		{"REAL", "REAL"},
		{"NUMERIC", "NUMERIC"},
	}
	for _, tc := range cases {
		got := normalizeViewColumnType(tc.in)
		if got != tc.want {
			t.Errorf("normalizeViewColumnType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDescribeSchemaJSONViewColumnsNeverBLOB is the end-to-end pin for
// PRD #144 slice 8 (issue #159): `describe-schema --json` against a real
// archive must never report `"type": "BLOB"` inside any
// `views[*].columns_detailed`. The fallback is the literal "unknown".
// Real declared types (TEXT, INTEGER, REAL, NUMERIC) pass through.
func TestDescribeSchemaJSONViewColumnsNeverBLOB(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"describe-schema", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("describe-schema --json exit code = %d, stderr=%s", code, stderr.String())
	}
	var catalog struct {
		Views []struct {
			Name            string                `json:"name"`
			ColumnsDetailed []schemaCatalogColumn `json:"columns_detailed"`
		} `json:"views"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	if len(catalog.Views) == 0 {
		t.Fatal("catalog.views is empty; want registered views")
	}
	allowed := map[string]bool{
		"":        false, // empty must have been rewritten to "unknown"
		"unknown": true,
		"TEXT":    true,
		"INTEGER": true,
		"REAL":    true,
		"NUMERIC": true,
	}
	for _, view := range catalog.Views {
		for _, col := range view.ColumnsDetailed {
			if col.Type == "BLOB" {
				t.Errorf("view %q column %q has type=BLOB; want a real SQL type or \"unknown\"",
					view.Name, col.Name)
			}
			if col.Type == "" {
				t.Errorf("view %q column %q has empty type; want a real SQL type or \"unknown\"",
					view.Name, col.Name)
			}
			if _, ok := allowed[col.Type]; !ok {
				// Any other non-empty value is a legitimate declared SQL
				// type (rare on a view) — pass through, but flag exotic
				// names so they surface in review.
				t.Logf("view %q column %q has uncommon type %q (passed through)",
					view.Name, col.Name, col.Type)
			}
		}
	}
}

// TestDescribeSchemaJSONTableColumnsUnchanged pins that the view-only
// fallback does NOT touch tables. Real BLOB columns on real tables still
// report BLOB; declared types on tables pass through. The fixture archive
// has at least one TEXT column (`connections.id`) we can pin; any future
// BLOB column on a real table must still report BLOB.
func TestDescribeSchemaJSONTableColumnsUnchanged(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	// Inject a real BLOB column into a real table so the test exercises
	// the "tables keep BLOB" guarantee even if no production table has
	// one today. We use a fresh ad-hoc table so we never depend on a
	// migration-managed schema feature.
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE _slice8_blob_probe (id INTEGER PRIMARY KEY, payload BLOB)`); err != nil {
		db.Close()
		t.Fatalf("create probe table: %v", err)
	}
	db.Close()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"describe-schema", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("describe-schema --json exit code = %d, stderr=%s", code, stderr.String())
	}
	var catalog struct {
		Tables []struct {
			Name    string                `json:"name"`
			Columns []schemaCatalogColumn `json:"columns"`
		} `json:"tables"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	var probe struct {
		found      bool
		payloadCol schemaCatalogColumn
	}
	for _, table := range catalog.Tables {
		if table.Name != "_slice8_blob_probe" {
			continue
		}
		probe.found = true
		for _, col := range table.Columns {
			if col.Name == "payload" {
				probe.payloadCol = col
			}
		}
	}
	if !probe.found {
		t.Fatal("probe table _slice8_blob_probe missing from catalog.tables")
	}
	if probe.payloadCol.Type != "BLOB" {
		t.Errorf("real table BLOB column rewritten: got type=%q, want BLOB",
			probe.payloadCol.Type)
	}
}

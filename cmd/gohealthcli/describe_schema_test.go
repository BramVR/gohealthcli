package main

import (
	"bytes"
	"encoding/json"
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
			Guidance                 []string                  `json:"guidance"`
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

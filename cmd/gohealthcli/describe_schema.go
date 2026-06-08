package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
)

// curatedSchemaCatalogJSON is the hand-curated narrative catalog
// shipped alongside the binary. Downstream tools may also fetch the
// file directly from cmd/gohealthcli/llm-schema.json for offline use;
// the catalog emitted by `describe-schema --json` merges this on top
// of the Registry- and pragma-sourced data.
//
//go:embed llm-schema.json
var curatedSchemaCatalogJSON []byte

func runDescribeSchemaWithRuntime(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, _ runtimeAdapters) int {
	flags := flag.NewFlagSet("describe-schema", flag.ContinueOnError)
	flags.SetOutput(stderr)

	describeConfigPath := flags.String("config", configPath, "config file path")
	describeArchivePath := flags.String("db", archivePath, "SQLite Health Archive path")
	// --json defaults to true here because the JSON catalog is the
	// downstream contract; an explicit --sql overrides. We do NOT
	// reject the combination — that would mis-fire when the user
	// passes a global --json flag plus a subcommand-level --sql.
	flags.Bool("json", true, "emit the curated JSON catalog (default)")
	describeSQL := flags.Bool("sql", false, "emit live DDL from sqlite_master instead of the JSON catalog")
	// --no-input matches the common-flag set so the global --no-input
	// flag doesn't trigger flag.ErrHelp; we don't use it here.
	flags.Bool("no-input", false, "ignored by describe-schema (kept for common-flag compatibility)")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected describe-schema argument: %s\n", flags.Arg(0))
		return 1
	}
	_ = mode // describe-schema picks JSON or SQL via its own flags, not the global outputMode.

	resolvedPath, err := resolveConfiguredArchivePath(*describeConfigPath, *describeArchivePath, false)
	if err != nil {
		fmt.Fprintf(stderr, "describe-schema: %v\n", err)
		return 1
	}
	if err := migrateArchiveIfNeeded(resolvedPath); err != nil {
		fmt.Fprintf(stderr, "describe-schema: %v\n", err)
		return 1
	}
	db, err := openArchiveReadOnly(resolvedPath)
	if err != nil {
		fmt.Fprintf(stderr, "describe-schema: %v\n", err)
		return 1
	}
	defer db.Close()

	if *describeSQL {
		if err := writeSchemaSQL(db, stdout); err != nil {
			fmt.Fprintf(stderr, "describe-schema --sql: %v\n", err)
			return 1
		}
		return 0
	}
	// Default is --json.
	if err := writeSchemaJSON(db, stdout); err != nil {
		fmt.Fprintf(stderr, "describe-schema --json: %v\n", err)
		return 1
	}
	return 0
}

// writeSchemaSQL dumps DDL from sqlite_master in name order. Internal
// sqlite_* objects (the autoincrement sequence table, etc.) are
// excluded so the output is what gohealthcli authored.
func writeSchemaSQL(db *sql.DB, stdout io.Writer) error {
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%'
			AND sql IS NOT NULL
		ORDER BY type DESC, name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name, sqlText string
		if err := rows.Scan(&typ, &name, &sqlText); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "-- %s %s\n%s;\n\n", typ, name, sqlText); err != nil {
			return err
		}
	}
	return rows.Err()
}

type schemaCatalogColumn struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type schemaCatalogView struct {
	Name             string                `json:"name"`
	DatasetName      string                `json:"dataset_name,omitempty"`
	MigrationVersion int                   `json:"migration_version"`
	Columns          []string              `json:"columns"`
	ColumnsDetailed  []schemaCatalogColumn `json:"columns_detailed,omitempty"`
}

type schemaCatalogTable struct {
	Name    string                `json:"name"`
	Columns []schemaCatalogColumn `json:"columns"`
}

type schemaCatalog struct {
	Version   int                  `json:"version"`
	Views     []schemaCatalogView  `json:"views"`
	Tables    []schemaCatalogTable `json:"tables"`
	Narrative json.RawMessage      `json:"narrative,omitempty"`
}

const schemaCatalogVersion = 1

// writeSchemaJSON builds the curated JSON catalog. Views come from the
// Normalized Views Registry (their migration version, dataset name,
// declared field list). Tables come from sqlite_master, with column
// names and types pulled from pragma_table_info. Future #109 follow-ups
// will merge in a hand-curated narrative file at docs/llm-schema.json.
func writeSchemaJSON(db *sql.DB, stdout io.Writer) error {
	catalog := schemaCatalog{Version: schemaCatalogVersion}

	registry := normalizedViewsRegistry()
	for _, name := range registry.Catalog() {
		spec, ok := registry.View(name)
		if !ok {
			continue
		}
		view := schemaCatalogView{
			Name:             spec.view,
			DatasetName:      spec.name,
			MigrationVersion: spec.migrationVersion,
		}
		for _, field := range spec.fields {
			view.Columns = append(view.Columns, field.name)
		}
		// Augment with column types from pragma_table_info if the view
		// has been materialised in the live archive. Skip silently when
		// the archive doesn't have the view yet (older schema versions).
		columns, err := readPragmaTableInfo(db, spec.view)
		if err == nil && len(columns) > 0 {
			view.ColumnsDetailed = columns
		}
		catalog.Views = append(catalog.Views, view)
	}

	tableNames, err := listSchemaTables(db)
	if err != nil {
		return err
	}
	for _, name := range tableNames {
		columns, err := readPragmaTableInfo(db, name)
		if err != nil {
			return err
		}
		catalog.Tables = append(catalog.Tables, schemaCatalogTable{Name: name, Columns: columns})
	}

	// Merge the hand-curated narrative file into the catalog as a
	// `narrative` sub-object. Downstream tools may also fetch
	// docs/llm-schema.json directly without running the binary.
	if json.Valid(curatedSchemaCatalogJSON) {
		catalog.Narrative = json.RawMessage(curatedSchemaCatalogJSON)
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(catalog)
}

func listSchemaTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master
		WHERE type = 'table'
			AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func listSchemaViews(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master
		WHERE type = 'view'
			AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func readPragmaTableInfo(db *sql.DB, name string) ([]schemaCatalogColumn, error) {
	rows, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []schemaCatalogColumn
	for rows.Next() {
		var col schemaCatalogColumn
		if err := rows.Scan(&col.Name, &col.Type); err != nil {
			return nil, err
		}
		columns = append(columns, col)
	}
	return columns, rows.Err()
}

// assertNoSchemaDrift returns a non-nil error if any view in sqlite_master
// lacks a catalog entry. The drift test calls this; a follow-up CI hook
// can call it from the binary directly.
func assertNoSchemaDrift(archivePath string) error {
	db, err := openArchiveReadOnly(archivePath)
	if err != nil {
		return err
	}
	defer db.Close()
	views, err := listSchemaViews(db)
	if err != nil {
		return err
	}
	registry := normalizedViewsRegistry()
	registered := map[string]bool{}
	for _, name := range registry.Catalog() {
		if spec, ok := registry.View(name); ok {
			registered[spec.view] = true
		}
	}
	var orphans []string
	for _, view := range views {
		if !registered[view] {
			orphans = append(orphans, view)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		return fmt.Errorf("describe-schema drift: %d view(s) in sqlite_master have no catalog entry: %v", len(orphans), orphans)
	}
	return nil
}

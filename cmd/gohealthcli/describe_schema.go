package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
)

// curatedSchemaCatalogJSON is the hand-curated narrative catalog
// shipped alongside the binary. Downstream tools may also fetch the
// file directly from cmd/gohealthcli/llm-schema.json for offline use;
// the catalog emitted by `describe-schema --json` merges this on top
// of the Registry- and pragma-sourced data.
//
//go:embed llm-schema.json
var curatedSchemaCatalogJSON []byte

// describeSchemaCommonFlagUsageOverrides is describe-schema's divergence
// from the canonical commonFlagsSpec wording, declared once: the
// registry entry (commands.go, via withCommonOverrides) and the runtime
// CommonFlagSpec below both consume this map, so `describe-schema
// --help`, the `schema --json` contract, and the generated
// docs/commands/describe-schema.md page render identical strings by
// construction (issue #76).
var describeSchemaCommonFlagUsageOverrides = map[string]string{
	"json":     "accepted for uniformity; the JSON catalog is the success-mode default",
	"plain":    "no-op (schema catalog has no plain shape); emits JSON catalog + stderr note",
	"no-input": "accepted for uniformity; describe-schema does no prompting",
}

func runDescribeSchemaWithRuntime(args []string, configPath, archivePath string, configPathExplicit, archivePathExplicit bool, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("describe-schema", flag.ContinueOnError)
	flags.SetOutput(stderr)

	// describe-schema accepts the full Common Flag Set ({config, db, json,
	// plain, no-input}) so the "every subcommand accepts the same global
	// flags" invariant (PRD #143) holds. The schema catalog is always the
	// success-mode output (the only override is the explicit `--sql`
	// flag); the Common Flag Set `--json` flag is therefore accepted for
	// uniformity but does not change behaviour. `--plain` is a documented
	// no-op (the schema catalog has no key/value plain shape): when set
	// without --json, we still emit the JSON catalog and surface a
	// `// note: …` comment line on stderr so the catalog itself stays
	// uncluttered for users redirecting stdout to a file. `--no-input` is
	// likewise accepted but ignored — describe-schema does no prompting.
	// The usage strings for the describe-schema-specific shared-flag
	// semantics come from describeSchemaCommonFlagUsageOverrides — the
	// same map the registry entry renders into the published schema — so
	// `describe-schema --help` reflects the documented no-op /
	// accepted-but-ignored semantics instead of the generic "write stable
	// JSON to stdout" wording, without a second hand-typed copy.
	spec := AllCommonFlagsSpec()
	spec.UsageOverrides = describeSchemaCommonFlagUsageOverrides
	common := RegisterCommon(flags, spec, CommonFlagValues{
		ConfigPath:          configPath,
		ArchivePath:         archivePath,
		JSONOutput:          mode.json,
		PlainOutput:         mode.plain,
		ArchivePathExplicit: archivePathExplicit,
		ConfigPathExplicit:  configPathExplicit,
	})
	// --sql is a describe-schema-specific override that wins over the JSON
	// catalog default. We register it AFTER RegisterCommon so the Common
	// Flag Set seam keeps owning the shared invariants; the override is
	// applied locally below.
	describeSQL := flags.Bool("sql", false, "dump live DDL from sqlite_master (excludes internal sqlite_* objects)")

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	// describe-schema's success output is always the JSON catalog unless
	// `--sql` overrides — the Common Flag Set `--json` flag is accepted
	// only for the uniform-flag contract. Its FAILURE output still
	// honours the global outputMode so the unified failure contract
	// (slice 7, #178) applies uniformly: `--json describe-schema bogus`
	// gets the JSON envelope on stdout like every other subcommand.
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "describe-schema",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected describe-schema argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	resolvedPath, err := resolveReadArchivePath(*common)
	if err != nil {
		return ReportFailure(FailureReport{Command: "describe-schema", Status: StatusOperationFailed, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	if err := migrateArchiveIfNeeded(context.Background(), resolvedPath); err != nil {
		return ReportFailure(FailureReport{Command: "describe-schema", Status: StatusOperationFailed, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	db, err := openArchiveReadOnly(resolvedPath)
	if err != nil {
		return ReportFailure(FailureReport{Command: "describe-schema", Status: StatusOperationFailed, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	defer db.Close()

	// --plain is a no-op on describe-schema (the schema catalog has no
	// key/value plain shape). Emit a comment line on stderr so the user
	// knows their flag was honoured as a best-effort fallback; stdout
	// stays the catalog so redirecting it to a file still produces valid
	// JSON. The ParseCommon mutual-exclusion check already rejects
	// `--plain --json`, and an explicit `--sql` is the user's chosen
	// override, so we skip the note in that case (the note's wording
	// would be misleading because we're about to emit DDL, not the JSON
	// catalog).
	if common.PlainOutput && !*describeSQL {
		fmt.Fprintln(stderr, "// note: --plain is a no-op on describe-schema; emitting JSON catalog")
	}

	if *describeSQL {
		if err := writeSchemaSQL(db, stdout); err != nil {
			return ReportFailure(FailureReport{Command: "describe-schema --sql", Status: StatusArchiveUnwritable, Message: err.Error(), Mode: mode}, stdout, stderr)
		}
		return 0
	}
	// JSON catalog is the success-mode default; --sql above is the only
	// override. The Common Flag Set `--json` flag does not change
	// behaviour here — it's accepted for the uniform-flag contract.
	if err := writeSchemaJSON(db, stdout); err != nil {
		return ReportFailure(FailureReport{Command: "describe-schema --json", Status: StatusArchiveUnwritable, Message: err.Error(), Mode: mode}, stdout, stderr)
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
		//
		// SQLite views do not carry declared column types — pragma_table_info
		// reports the underlying expression's affinity, which for any
		// non-trivial JSON projection comes back as empty or `BLOB`. The
		// JSON catalog is positioned as a contract for LLM consumers, so
		// `BLOB` on a column whose runtime values are INTEGER/TEXT actively
		// poisons agent reasoning. PRD #144 slice 8 (#159) deliberately
		// rejects parsing the view's SELECT projection (a bespoke SQL
		// parser sitting next to a working pragma call) and instead stamps
		// the literal "unknown" — downstream consumers treat the runtime
		// type as opaque rather than mis-declared. Tables stay untouched
		// so real BLOB columns on real tables still report BLOB.
		columns, err := readPragmaTableInfo(db, spec.view)
		if err == nil && len(columns) > 0 {
			for i := range columns {
				columns[i].Type = normalizeViewColumnType(columns[i].Type)
			}
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

// normalizeViewColumnType rewrites the type pragma_table_info reports for
// a Normalized View column to the literal "unknown" when the underlying
// expression has no declared SQLite affinity (empty string) or has been
// reported as `BLOB` (the affinity SQLite assigns to any non-trivial JSON
// projection). Real declared types (TEXT, INTEGER, REAL, NUMERIC, …) pass
// through unchanged. The fallback is view-only — see writeSchemaJSON for
// why tables keep their `BLOB` columns intact.
func normalizeViewColumnType(declared string) string {
	switch declared {
	case "", "BLOB":
		return "unknown"
	default:
		return declared
	}
}

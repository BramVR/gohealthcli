# Inspect a Health Archive

The read surface lets users summarize an archive, run guarded SELECT statements, and inspect the live schema without requiring a Connection or Provider access.

## Sub-features

- `inspect-status-json` reports schema and record counts from an empty archive.
- `inspect-query-json` accepts one SELECT and returns stable typed rows.
- `inspect-query-guard` rejects a write statement and leaves the archive unchanged.
- `inspect-schema-json` returns the curated catalog merged with live archive schema.
- `inspect-schema-sql` emits live SQLite DDL.
- `inspect-export` writes empty CSV and JSONL datasets to explicit destinations and retains their bytes as evidence.

## How to get to it (user POV)

- Run `gohealthcli --db <archive> --json status`.
- Run `gohealthcli --db <archive> --json query 'SELECT version, name FROM schema_migrations ORDER BY version DESC LIMIT 1'`.
- Run `gohealthcli --db <archive> --json describe-schema`.
- Use `--sql` instead to emit live DDL.
- Run `gohealthcli --db <archive> export <dataset> --format csv --output <path>` or use `--stdout`.

## Driving it with verify-gohealthcli

Preconditions:

- `launch`, `drive initialize`, and `doctor` pass for the same run.
- The archive and sidecar are regular task-owned paths.

- **Status entries.** Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" inspect`. JSON and plain transcripts report the current schema and zero Data Points.
- **Query entries.** The exact schema-migration SELECT succeeds. A separate DELETE attempt exits `1`, names the read-only guard, and leaves the complete SQLite file set unchanged, including any journal, WAL, or shared-memory sibling.
- **Schema entries.** JSON output contains the schema contract and live objects. The SQL form contains table DDL.
- **Export entries.** The drive retains the empty CSV header and zero-byte JSONL file under evidence before cleanup.

## Gotchas

- `query` accepts one SELECT only. Keep both the successful read and rejected write proof.
- `status` may fence abandoned Sync Runs. The isolated empty archive has none; do not aim it at shared state.
- These read surfaces can run archive lifecycle work before reading: pending migrations may be applied, and `status` can fence stale Sync Runs. This recipe uses a current isolated archive where neither changes state.
- `describe-schema --plain` still emits JSON and a note on stderr. Use `--json` for this recipe.
- An empty archive proves command contracts and schema, not normalized values from health records.
- Do not claim human, config-resolved, real-data, or Windows paths from these JSON/plain `--db` routes.

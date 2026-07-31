---
name: gohealthcli
description: "Use for safe local Google Health Archive inspection, sync, query, and export with gohealthcli."
---

# gohealthcli

Work archive-first: inspect the local Health Archive before making Provider
requests. Treat every command result as sensitive personal health data.

## Safety boundary

- Preserve the read-only Provider boundary. gohealthcli does not write or
  delete Provider health data.
- Distinguish Provider reads from local mutations. `gohealthcli sync` archives
  records locally; setup and Identity Snapshot commands also change local
  state. Some archive health checks can fence abandoned Sync Runs.
- Keep one Google Identity per Health Archive. Never reconnect an existing
  archive to a different identity. If the identity must change, create or
  select a separate archive only with explicit user direction.
- Use the installed-app browser flow for OAuth.
  `gohealthcli connect --no-input` detects when interaction is required; it
  cannot complete OAuth headlessly. Run connect only with explicit user intent
  and a user available for consent.
- Never print tokens, OAuth client JSON, raw Provider payloads, private paths,
  or exported health records in chat or public logs. Summarize structure and
  counts; redact identifying values.
- Do not upload or share an archive or export. Do not schedule background
  collection. Do not provide medical interpretation, diagnosis, or treatment
  advice.

## Archive-first workflow

### 1. Confirm scope

Establish the intended Health Archive, requested time range, Data Type, and
output destination. Prefer the configured archive. Use an explicit `--db` only
when the user identifies the archive.

### 2. Inspect local state

Start without Provider I/O:

```bash
gohealthcli doctor --plain --no-input
gohealthcli status --json --no-input
gohealthcli sync --status --window 2h --json --no-input
```

Treat the status commands as local archive operations. They may mark an
abandoned `sync_running` row failed when its heartbeat is stale; they never
advance a Sync Cursor.

### 3. Discover the live schema

Use the installed binary as the catalog instead of memorizing names:

```bash
gohealthcli describe-schema --json --no-input
gohealthcli describe-schema --sql --no-input
```

Read `views[].dataset_name`, `views[].name`, and declared columns from the JSON
catalog. Treat an `"unknown"` view-column type as opaque; inspect a sample row
when the type matters. Do not copy a complete Data Type, export dataset, or
Normalized View list into this skill. Use archive status, schema output, and
the user's requested Data Type as the current discovery surfaces.

### 4. Query the stable read surface

Prefer a registered Normalized View over raw tables. Place flags before the SQL
argument, use a bounded result, and request JSON for machine processing:

```bash
gohealthcli query --json --no-input 'SELECT civil_date, step_count FROM daily_steps ORDER BY civil_date DESC LIMIT 30'
```

The example reads Normalized View `daily_steps`. Query accepts one guarded
`SELECT` statement only. Inspect `raw_json` only when normalized columns cannot
answer the question, and do not reproduce the payload in the response.

### 5. Sync only when requested

Begin with a small explicit window:

```bash
gohealthcli sync --types steps --from 2026-01-01 --to 2026-01-02 --plain --no-input
```

The example requests Data Type `steps`. A multi-type invocation creates one
Sync Run per Data Type. A successful Sync Run advances only its matching Sync
Cursor: Connection, Data Type, source-family filter, and Rollup kind. Never
infer a cursor from the newest archived timestamp. A failed or canceled run
leaves the cursor unchanged, so retrying may re-fetch idempotently.

Use Rollups only when summary history is acceptable. Rollups are separate from
raw Data Points, do not replace them, and have separate Sync Cursors. Validate
the requested combination through command output before starting a large
backfill.

### 6. Export deliberately

Export only on an explicit request and use a deliberate destination:

```bash
gohealthcli export daily-steps --format jsonl --output ./daily-steps.jsonl --no-input
```

The example uses export dataset `daily-steps`. The output contains private
health data even though it comes from a read-only Normalized View. Keep the
file local and report its path and row count without quoting records.

### 7. Use raw Provider reads last

Use raw reads only when local status, schema, and archived data cannot answer
the question. Choose a fresh, user-approved private destination. Set
owner-only file creation and no-clobber in the same shell before redirecting
stdout. On a POSIX shell:

```bash
umask 077
set -o noclobber
gohealthcli raw data-type steps --from 2026-01-01 --to 2026-01-02 > ./raw-health-response.json
```

On PowerShell, create a unique file inside the current user's ACL-protected
local application-data directory:

```powershell
$rawPath = Join-Path $env:LOCALAPPDATA ("gohealthcli-raw-{0}.json" -f [guid]::NewGuid())
New-Item -ItemType File -Path $rawPath -ErrorAction Stop | Out-Null
gohealthcli raw data-type steps --from 2026-01-01 --to 2026-01-02 | Set-Content -LiteralPath $rawPath -Encoding utf8
```

This example reads Data Type `steps` from the Provider and redirects its raw
JSON without archiving the response. It may refresh Connection token metadata
locally. If the destination already exists, choose another path instead of
overwriting it. Keep the file private, minimize the time range, and never paste
the payload into chat or logs.

## Report

Report the command purpose, exit status, archive/schema health, counts, time
window, Sync Cursor behavior, and output location. State whether Provider I/O
or a local mutation occurred. Redact identities, tokens, payload values, and
private paths.

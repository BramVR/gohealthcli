---
title: "gohealthcli describe-schema"
description: "Self-describe the Health Archive for LLM consumption."
---

Emit the archive's schema in one of two modes.

`--sql` dumps live DDL straight from `sqlite_master`, excluding internal `sqlite_*` objects. Use this when you want the actual truth of what tables and views exist right now.

The JSON catalog is the success-mode default: it emits a curated document combining the Normalized Views Registry's per-view metadata (name, migration version, declared columns), the live table+column shape from `pragma_table_info`, the merged hand-curated narrative file, and a stable wire-shape version field. Downstream tools (a Claude skill, an MCP server, a dashboard) read the JSON catalog as the contract. The Common Flag Set `--json` flag is accepted for the uniform-flag contract but does not change behaviour — the catalog is emitted unless `--sql` overrides.

`--plain` is accepted as a no-op — the schema catalog has no key/value plain shape, so `describe-schema --plain` emits the JSON catalog and surfaces a `// note: --plain is a no-op …` comment line on stderr; stdout stays valid JSON so users redirecting it to a file are unaffected. `--plain --json` together fails with the documented mutual-exclusion error.

A drift test in CI fails when a public view exists in `sqlite_master` without a matching catalog entry — the JSON shape and the live schema cannot diverge silently.

### Normalized View column types

SQLite views don't carry declared column types. `pragma_table_info` reports the type of each view column from the underlying expression's affinity, which for any non-trivial JSON projection comes back as either an empty string or the literal `BLOB`. The JSON catalog is read as a contract by LLM consumers, so a column like `daily_steps.step_count` (an INTEGER projection over `data_points`) being reported as `BLOB` actively poisons agent reasoning.

The catalog rewrites those misleading values to the literal `"unknown"`. Every entry in `views[*].columns_detailed[*].type` is therefore either a real SQL type (`TEXT`, `INTEGER`, `REAL`, `NUMERIC`, …) or the literal `"unknown"` — never `BLOB`, never empty. Treat `"unknown"` as "the runtime type is opaque from the catalog alone — inspect a row or consult the view DDL".

Table columns (in `tables[*].columns`) are unaffected: real `BLOB` columns on real tables still report `BLOB`. The fallback is view-only.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | accepted for uniformity; the JSON catalog is the success-mode default |
| `--plain` | bool | `false` | no-op (schema catalog has no plain shape); emits JSON catalog + stderr note |
| `--no-input` | bool | `false` | accepted for uniformity; describe-schema does no prompting |
| `--sql` | bool | `false` | dump live DDL from sqlite_master (excludes internal sqlite_* objects) |

---
title: "gohealthcli query"
description: "Run guarded read-only SQL over the Health Archive."
---

Execute a single SQL statement against the Health Archive. The binary refuses anything that would write or alter the archive — `query` is for inspection, not maintenance.

Flags must appear **before** the SQL argument because Go's `flag` parser stops at the first positional argument. To explore the schema, query the `sqlite_master` table or run `gohealthcli export` for the canonical normalised datasets.

In `--json` mode, JSON-typed columns pass through as nested JSON objects so downstream consumers can read them with one parse instead of two. The recognised columns are `raw_json`, `data_source_json`, `timezone_metadata`, `token_metadata_json`, `google_identity_json`, and any column whose name ends in `_json`. Pass `--raw-text` to opt out and receive the literal stored string instead. NULL JSON-typed cells stay `null`; invalid JSON payloads fall back to the stored string so no row ever fails the query.

## Usage

```
gohealthcli query <sql>
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--raw-text` | bool | `false` | in JSON mode, return JSON-typed columns as strings instead of nested objects |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

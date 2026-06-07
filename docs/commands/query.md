---
title: "gohealthcli query"
description: "Run guarded read-only SQL over the Health Archive."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Execute a single SQL statement against the Health Archive. The binary refuses anything that would write or alter the archive — `query` is for inspection, not maintenance.

Flags must appear **before** the SQL argument because Go's `flag` parser stops at the first positional argument. To explore the schema, query the `sqlite_master` table or run `gohealthcli export` for the canonical normalised datasets.

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
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

---
title: "gohealthcli describe-schema"
description: "Self-describe the Health Archive for LLM consumption."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Emit the archive's schema in one of two modes.

`--sql` dumps live DDL straight from `sqlite_master`, excluding internal `sqlite_*` objects. Use this when you want the actual truth of what tables and views exist right now.

`--json` (default) emits a curated catalog combining the Normalized Views Registry's per-view metadata (name, migration version, declared columns), the live table+column shape from `pragma_table_info`, and a stable wire-shape version field. Downstream tools (a Claude skill, an MCP server, a dashboard) read the JSON catalog as the contract.

A drift test in CI fails when a public view exists in `sqlite_master` without a matching catalog entry — the JSON shape and the live schema cannot diverge silently.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |
| `--sql` | bool | `false` | dump live DDL from sqlite_master (excludes internal sqlite_* objects) |

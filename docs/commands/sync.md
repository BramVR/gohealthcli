---
title: "gohealthcli sync"
description: "Archive Google Health Data Points and supported Rollups."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Pull raw Data Points for the requested Data Types within an inclusive `--from` / exclusive `--to` window and append them to the Health Archive. Sync is the primary write path; everything else in the binary either reads from the archive or refreshes metadata.

`--types` accepts a comma-separated list (for example `steps,heart-rate,sleep`). `--rollup daily` switches the sync from raw Data Points to daily Rollup records for the same Data Types (where the provider supports it). `--source-family wearable` restricts the result set to Data Points whose Data Source family is a watch or tracker.

A Sync Run is recorded for every invocation — succeeded or failed — so the archive carries an audit trail of attempts as well as records.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |
| `--types` | string | `steps` | comma-separated Data Types |
| `--from` | string | — | inclusive sync range start |
| `--to` | string | — | exclusive sync range end |
| `--rollup` | string | — | rollup kind to sync; supported: daily |
| `--source-family` | string | — | source family filter; supported: wearable |

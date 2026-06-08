---
title: "gohealthcli sync"
description: "Archive Google Health Data Points and supported Rollups."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Pull raw Data Points for the requested Data Types within an inclusive `--from` / exclusive `--to` window and append them to the Health Archive. Sync is the primary write path; everything else in the binary either reads from the archive or refreshes metadata.

`--types` accepts a comma-separated list (for example `steps,heart-rate,sleep`); multi-type invocations fan out into one Sync Run per Data Type, each with its own outcome and Sync Cursor. `--all` is shorthand for every default Data Type in the catalog. Per-type failures stay isolated: one Data Type erroring does not stop the others. `--rollup daily` switches the sync from raw Data Points to daily Rollup records for the same Data Types (where the provider supports it). `--source-family wearable` restricts the result set to Data Points whose Data Source family is a watch or tracker.

`--from` is optional once an initial backfill has succeeded — subsequent runs read the durable Sync Cursor for the same `(data_type, source_family, rollup)` key and resume from it. The cursor advances only when a Sync Run finishes with `sync_completed`, so failed or cancelled runs re-read the same window on the next attempt (ADR-0008).

A Sync Run is recorded for every invocation — succeeded, failed, or cancelled — so the archive carries an audit trail of attempts as well as records. SIGINT (Ctrl-C) during a fan-out marks the in-flight Sync Run `sync_canceled`, leaves its Sync Cursor un-advanced, and stops cleanly; prior Data Types remain `sync_completed`.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |
| `--types` | string | — | comma-separated Data Types |
| `--all` | bool | `false` | sync every default Data Type |
| `--from` | string | — | inclusive sync range start; optional once a Sync Cursor exists |
| `--to` | string | — | exclusive sync range end |
| `--rollup` | string | — | rollup kind to sync; supported: daily |
| `--source-family` | string | — | source family filter; supported: wearable |

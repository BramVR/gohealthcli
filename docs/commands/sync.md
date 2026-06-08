---
title: "gohealthcli sync"
description: "Archive Google Health Data Points and supported Rollups."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Pull raw Data Points for the requested Data Types within an inclusive `--from` / exclusive `--to` window and append them to the Health Archive. Sync is the primary write path; everything else in the binary either reads from the archive or refreshes metadata.

`--types` accepts a comma-separated list (for example `steps,heart-rate,sleep`); multi-type invocations fan out into one Sync Run per Data Type, each with its own outcome and Sync Cursor. When neither `--types` nor `--all` is set, `sync` falls back to a single-type run against `steps`. `--all` is shorthand for every default Data Type in the catalog. Per-type failures stay isolated: one Data Type erroring does not stop the others. `--rollup` switches the sync from raw Data Points to upstream Rollup records: `daily` calls the `dailyRollUp` endpoint (civil-time windows), `hourly` / `weekly` / `window=<duration>` call the windowed `rollUp` endpoint (RFC3339 windows) with a 1h / 7d / parsed-duration window size respectively. Unsupported combinations error with the Data Type's actual `SupportedEndpoints` quoted in the message. `--source-family wearable` restricts the result set to Data Points whose Data Source family is a watch or tracker.

`--from` is optional once an initial backfill has succeeded — subsequent runs read the durable Sync Cursor for the same `(connection_id, data_type, source_family_filter, rollup_kind)` key and resume from it. Each rollup kind (`daily` / `hourly` / `weekly` / `window=<duration>`) carries its own cursor, so syncing weekly aggregates does not disturb the hourly cursor for the same Data Type. The cursor advances only when a Sync Run finishes with `sync_completed`, so failed or cancelled runs re-read the same window on the next attempt (ADR-0008). The terminal Sync Run status and the cursor advance are written in one SQLite transaction, so a crash between them cannot leave the audit trail and the cursor disagreeing.

A Sync Run row is recorded for every invocation that reaches upstream — succeeded, failed, or cancelled — so the archive carries an audit trail of attempts as well as records. Preflight failures that exit before contacting the provider (for example, omitting `--from` when no Sync Cursor exists yet) surface the error without writing an audit row. SIGINT (Ctrl-C) during a fan-out marks the in-flight Sync Run `sync_canceled`, leaves its Sync Cursor un-advanced, and stops cleanly; prior Data Types remain `sync_completed`. SIGINT received before the first Data Type starts exits as `sync_canceled` without writing an audit row.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |
| `--types` | string | — | comma-separated Data Types; defaults to "steps" when neither --types nor --all is set |
| `--all` | bool | `false` | sync every default Data Type |
| `--from` | string | — | inclusive sync range start; optional once a Sync Cursor exists |
| `--to` | string | — | exclusive sync range end |
| `--rollup` | string | — | rollup kind to sync; supported: daily \| hourly \| weekly \| window=<duration> |
| `--source-family` | string | — | source family filter; supported: wearable |

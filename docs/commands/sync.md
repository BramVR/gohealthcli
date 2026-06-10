---
title: "gohealthcli sync"
description: "Archive Google Health Data Points and supported Rollups."
---

Pull raw Data Points for the requested Data Types within an inclusive `--from` / exclusive `--to` window and append them to the Health Archive. Sync is the primary write path; everything else in the binary either reads from the archive or refreshes metadata.

`--types` accepts a comma-separated list (for example `steps,heart-rate,sleep`); multi-type invocations fan out into one Sync Run per Data Type, each with its own outcome and Sync Cursor. When neither `--types` nor `--all` is set, `sync` falls back to a single-type run against `steps`. `--all` is shorthand for every default Data Type in the catalog. Per-type failures stay isolated: one Data Type erroring does not stop the others. `--rollup` switches the sync from raw Data Points to upstream Rollup records: `daily` calls the `dailyRollUp` endpoint (civil-time windows), `hourly` / `weekly` / `window=<duration>` call the windowed `rollUp` endpoint (RFC3339 windows) with a 1h / 7d / parsed-duration window size respectively. Unsupported combinations error with the Data Type's actual `SupportedEndpoints` quoted in the message. `--source-family wearable` restricts the result set to Data Points whose Data Source family is a watch or tracker.

`--from` and `--to` accept both civil dates (`YYYY-MM-DD`, interpreted as start-of-UTC-day) and RFC3339 timestamps. The emitted shape is per rollup kind:

- `daily`: emits civil dates (`YYYY-MM-DD`). RFC3339 inputs are projected to their UTC calendar day so the upstream `dailyRollUp` body carries the catalog-required civil interval.
- `hourly` / `weekly` / `window=<duration>`: emits RFC3339 so the windowed `rollUp` body carries the upstream-required RFC3339 range.

Shape-rejection messages name both supported forms per rollup kind so operators no longer see an opaque upstream HTTP 400 for civil-on-hourly or similar.

`--from` is optional once an initial backfill has succeeded — subsequent runs read the durable Sync Cursor for the same `(connection_id, data_type, source_family_filter, rollup_kind)` key and resume from it. Each rollup kind (`daily` / `hourly` / `weekly` / `window=<duration>`) carries its own cursor, so syncing weekly aggregates does not disturb the hourly cursor for the same Data Type. The cursor advances only when a Sync Run finishes with `sync_completed`, so failed or cancelled runs re-read the same window on the next attempt (ADR-0008). The terminal Sync Run status and the cursor advance are written in one SQLite transaction, so a crash between them cannot leave the audit trail and the cursor disagreeing.

A Sync Run row is recorded for every invocation that reaches upstream — succeeded, failed, or cancelled — so the archive carries an audit trail of attempts as well as records. Every `--json` envelope carries a non-empty `status` from the enum `sync_completed | sync_failed | sync_canceled`; the empty string is structurally impossible because every code path emits a non-empty status.

Preflight failures exit before contacting the provider and do NOT write a `sync_runs` audit row. The full list of no-audit-row rejections is:

- Unparseable `--from` or `--to` (range parse).
- Inverted range (`--from > --to`).
- Zero-width range (`--from == --to`).
- Unsupported `--rollup` kind (parse failure).
- `--rollup <kind>` requested for a Data Type whose catalog entry does not support that kind (e.g. `--rollup hourly --types daily-resting-heart-rate`).
- Unsupported Data Type (not syncable yet).
- Source-family vs Data Type mismatch.
- `--rollup` combined with `--source-family` (mutually exclusive).
- No Connection on file (connection lookup failure).
- `--all` combined with `--types` (mutually exclusive).
- Duplicate entries in `--types`.
- `--all` expanding to zero supported Data Types.
- SIGINT received before any Data Type has started its audit row (no run is in flight to mark).

SIGINT (Ctrl-C) during a fan-out marks the in-flight Sync Run `sync_canceled`, leaves its Sync Cursor un-advanced, and stops cleanly; prior Data Types remain `sync_completed`.

Terminal writes are resilient to SQLite contention: on `SQLITE_BUSY`, the terminal write retries with bounded exponential backoff plus full jitter. If the retry budget is exhausted, the run surfaces as `sync_failed` with a contention-aware message and a separate short-transaction recovery write drives the row to a terminal state under the same retry budget so a `sync_running` row never lingers. `sync_canceled` outcomes are preserved through the recovery path — they are never reclassified as `sync_failed`.

Live progress (#236): after every archived page the Sync Run heartbeats — the running counts plus a `last_progress_at` timestamp land on the `sync_runs` row as a best-effort autocommit write — so a concurrent reader can watch progress from another terminal while the run is in flight. Heartbeats are advisory; the finalize transaction's terminal counts stay authoritative, and a heartbeat write failure never fails the sync.

`sync --status` is that concurrent reader, packaged: it lists recent Sync Runs from the local archive — one row per run with id, Data Types, status, counts, duration, heartbeat age, and a truncated error summary — and performs no provider I/O. Finished runs are listed when they finished inside `--window` (Go duration, default `15m`, max `24h`); `sync_running` rows are window-exempt, so a long in-flight run never ages out of the default view. `--status` cannot be combined with `--types`, `--all`, `--from`, `--to`, `--rollup`, or `--source-family`, and `--window` requires `--status`. The shared `--json` / `--plain` flags shape the output like every other read command.

Abandoned-run fencing: on entry to `sync`, `sync --status`, and `status`, any `sync_running` row whose heartbeat (or `started_at`, for rows that died before their first page) is older than 5 minutes is flipped to `sync_failed` with `error_summary` `abandoned (no heartbeat for 5m)` and `finished_at` set — so orphans from killed processes stop reading as alive without manual SQL. The fence is idempotent and never touches the Sync Cursor (ADR-0008: only a completed finalize advances it). Because it keys on heartbeat staleness rather than wall-clock age, a multi-hour backfill with a fresh heartbeat is never mis-flagged; and if a fenced process turns out to be alive after all, its eventual finalize overwrites the fence so the row converges to its true terminal status.

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
| `--status` | bool | `false` | list recent Sync Runs from the local archive instead of syncing |
| `--window` | string | — | with --status: how far back to list finished Sync Runs (Go duration, default 15m, max 24h) |

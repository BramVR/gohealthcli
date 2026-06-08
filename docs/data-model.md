---
summary: "SQLite archive sketch, raw JSON preservation, normalized views, and dedupe questions."
read_when:
  - "Designing the SQLite schema."
  - "Deciding how raw API records map to normalized exports."
  - "Reviewing dedupe and identity semantics."
---
# Data Model

## Principles

- Preserve raw provider JSON for every archived record.
- Treat raw provider JSON as immutable audit material; migrations may add
  derived fields/views but should not rewrite raw records except to repair
  corruption.
- Manage archive schema changes through monotonic migrations from the first
  implementation.
- Keep normalized columns only for stable query/export paths.
- Treat normalized views as the stable query/export surface. Raw tables are
  queryable but may change with provider shape.
- Store Data Points separately from Rollups.
- Treat daily-named Data Types as Data Points unless they came from a
  `rollUp` or `dailyRollUp` endpoint.
- Store physical time as UTC instants when available, and preserve provider
  civil dates/timezone metadata for day-oriented queries.
- Treat provider record names/IDs as upstream identity when present.
- Update the canonical Data Point row when an upstream correction is detected,
  and preserve the previous raw JSON in revisions.
- Include Data Type, Data Source, and time fields in dedupe keys.
- Never use floating point for values that need exact text preservation.

## Tables Sketch

`schema_migrations`

- migration version
- applied timestamp
- checksum or identifier

`connections`

- local connection ID: `provider:google_health_user_id`
- provider name
- Google Health user ID
- legacy Fitbit user ID
- token metadata, not token value if stored elsewhere
- created/updated timestamps

`data_points`

- local ID
- provider name
- connection ID
- Data Type
- upstream resource name
- record kind: interval, sample, daily, session
- start/end physical time as UTC instants when available
- start/end civil time or provider civil date when supplied
- timezone offset or provider timezone metadata when supplied
- Data Source JSON
- source-family filter when fetched through `reconcile`
- raw Data Point JSON
- inserted/updated timestamps

`data_point_revisions`

- local revision ID
- Data Point local ID
- previous raw Data Point JSON
- replaced timestamp
- replacement reason when known

`rollups`

- local ID
- provider name
- connection ID
- Data Type
- rollup kind: rollUp or dailyRollUp
- window start/end as UTC instants when available
- civil date when daily
- timezone offset or provider timezone metadata when supplied
- raw Rollup JSON
- inserted/updated timestamps

Rollups are upserted by Data Type, window, and rollup kind. The First Release
keeps the current raw Rollup only; add Rollup revisions later if provider
correction behavior warrants it.

`identity_snapshots` (renamed from `profile_snapshots` in migration 7)

- local ID
- provider name
- connection ID
- snapshot kind (`profile` | `settings` | `paired-devices` | `irn-profile`)
- raw provider JSON
- fetched timestamp

Identity Snapshots are append-only and kind-tagged. The `current_settings`
Normalized View projects the latest snapshot of kind=`settings` per Connection
into columns (measurement system, timezone, stride length type); follow-up
slices add `current_profile`, `paired_devices`, and `current_irn_profile`.

Rows pre-dating migration 7 keep `snapshot_kind='profile'` via the column
default; no parallel-table-with-view shim was used (PRD #93
§"identity_snapshots migration: explicit strategy").

`sync_runs`

- local ID
- provider name
- connection ID
- Data Types requested
- range requested
- endpoint family used
- source-family filter when requested
- status (`sync_running`, `sync_completed`, `sync_failed`, `sync_canceled`)
- seen/new/updated counts
- started/finished timestamps
- error summary

The First Release records Sync Runs and newest archived timestamps, but does not
infer completeness gaps. Gap tracking can wait until pagination, sparse Data
Types, source filtering, and correction behavior are better understood.

`sync_cursors`

- connection ID
- Data Type
- source-family filter (empty string when none)
- rollup kind (`none`, `daily`; future: `hourly`, `weekly`, `window:<duration>`)
- cursor time — the durable highwater mark, used as the next `--from`
- advanced-at timestamp

A Sync Cursor is the resume point for `gohealthcli sync` when `--from` is
omitted. It advances **only** when a Sync Run for the same key finishes with
status `sync_completed` (ADR-0008). A failed or cancelled Sync Run leaves the
cursor at its prior value — the next sync re-reads the same window and relies
on idempotent upsert dedupe to absorb the overlap.

The Sync Cursor is *not* `max(timestamp)` over `data_points`. A partial run
may leave archived rows past the cursor without advancing it; that is by
design (see ADR-0008).

## Normalized Views

First Release views:

- daily steps
- heart rate samples
- resting heart rate by day
- sleep sessions
- exercise sessions
- weight samples

Later views can be added after raw archive fixtures prove Data Type shape and
history quality.

## Dedupe Questions

- Are upstream Data Point names stable across repeated list/reconcile calls?
- Are Rollups deterministic enough to upsert by Data Type and window?
- Do corrected historical records appear with the same name or as replacements?
- Are upstream Data Point names stable across source-family-filtered `reconcile`
  and default `list` records?

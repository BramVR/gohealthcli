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

`profile_snapshots`

- local ID
- provider name
- connection ID
- raw profile/settings JSON
- fetched timestamp

`sync_runs`

- local ID
- provider name
- connection ID
- Data Types requested
- range requested
- endpoint family used
- source-family filter when requested
- status
- seen/new/updated counts
- started/finished timestamps
- error summary

The First Release records Sync Runs and newest archived timestamps, but does not
infer completeness gaps. Gap tracking can wait until pagination, sparse Data
Types, source filtering, and correction behavior are better understood.

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

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

Identity Snapshots are append-only and kind-tagged. Normalized Views project
the latest snapshot of each kind per Connection into queryable columns:

- `current_settings` — `kind='settings'` projected as measurement system,
  timezone, stride length type.
- `paired_devices` — `kind='paired-devices'` exploded via `json_each` into
  one row per device with `device_type`, `model`, `manufacturer`,
  `battery_percentage`, `last_sync_time`, and `features`.
- `current_irn_profile` — `kind='irn-profile'` projected as
  `onboarding_state`, `enrollment_state`, `last_update_time`. Requires
  the `irn.readonly` OAuth scope granted via `connect --add-scopes irn`.

Per-stage / per-split Normalized Views read structure already present in
the raw Data Point JSON — no re-sync needed:

- `sleep_stages` — explodes each sleep session's `sleep.stages[]` array
  into one row per stage (LIGHT / DEEP / REM / AWAKE) with start/end
  timestamps, civil date, and `duration_seconds`.
- `exercise_splits` — explodes each exercise session's
  `exercise.splits[]` array into one row per split with `split_type`
  and `distance_meters`.

`searchable_text` is the one-target free-text needle path for LLM and
human queries that need to find a string (device model, source app
name, exercise label) without knowing which underlying column to look
in. Schema: `(kind, text, ref_table, ref_id)`. Use `WHERE text LIKE
'%needle%'`. `kind ∈ {device, data_source, profile, exercise_type}`
tags where the row came from; `ref_table` + `ref_id` let downstream
code jump back to the source row.

Note on the `profile` kind: the view extracts `firstName`/`lastName`
from profile snapshots, but Google Health's current `users.getProfile`
response does not emit those fields (only `name` as the resource path,
plus `age`, membership date, and stride lengths). Profile rows will
appear here only when Google starts emitting name fields. The kind is
reserved so prompts can stay stable across the API change.

The view name is the stable contract — the backing can swap to FTS5
later without changing prompts.

## Data Point Attachments

`data_point_attachments` indexes binary payloads (today: TCX route
bytes) that live as content-addressed sidecar files next to the
SQLite archive, NOT inside it. ADR-0009 explains the trade-off. The
sidecar tree lives at `<archive>.attachments/<kind>/<sha256[0:2]>/<sha256>.<ext>`
with owner-only POSIX permissions (`0700` dirs, `0600` files). Schema:
`(id, data_point_id, kind, sha256, path_relative, byte_size,
fetched_at)`.

The Attachment Store module exposes `Store(dataPointID, kind, bytes)
→ {sha256, path}` (content-addressed; same bytes → same path,
idempotent insert), `Resolve(sha256) → path`, and `Walk(fn)` for
orphan detection (row with no sidecar, sidecar with no row). `doctor`
surfaces the orphan counts in its default report (`attachments_orphan_files`
and `attachments_orphan_rows` in `--plain`, an `attachments` block in
`--json`), emitted only when a count is positive.

Exercise sync (Data Type `exercise`) drives the only Attachment
producer today (#107): after each upserted exercise Data Point, the
ingestion calls `users.dataTypes.dataPoints.exportExerciseTcx` and, on
HTTP 200 with a non-empty body, Stores the bytes as a `tcx`-kind
Attachment linked by `data_point_id`. HTTP 404 (no TCX route for that
exercise — manually entered, no GPS) and HTTP 200 with empty body are
silent skips; the Sync Run stays `sync_completed`. 5xx, 401, and
transport errors are surfaced so the Sync Cursor stays put and the
user can retry.

The TCX hook is scope-gated (#140). Google requires
`googlehealth.location.readonly` on top of
`activity_and_fitness.readonly` for `exportExerciseTcx`; without the
second scope every call returns 403. Users opt in via
`gohealthcli connect --add-scopes tcx`. When the stored Connection
token does not include `location.readonly`, the hook short-circuits
before the HTTP call — exercise Data Points still archive, but no TCX
sidecar is fetched and no round-trip is wasted on a guaranteed-403
endpoint. The 403 graceful-skip remains as belt-and-suspenders.

## LLM-facing schema discovery

`gohealthcli describe-schema --json` emits the curated JSON catalog
(view metadata, table/column shape, hand-curated narrative, version
field) that downstream tools (a Claude skill, an MCP server, a
dashboard) read as the contract. The narrative companion file lives
at `cmd/gohealthcli/llm-schema.json` — downstream tools can fetch it
directly without running the binary. `--sql` dumps live DDL straight
from `sqlite_master`. A drift test in CI fails when a public view in
`sqlite_master` has no matching catalog entry, so the contract and
the live schema cannot diverge silently.

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

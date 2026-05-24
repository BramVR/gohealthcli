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
- Keep normalized columns only for stable query/export paths.
- Store Data Points separately from Rollups.
- Treat provider record names/IDs as upstream identity when present.
- Include Data Type, Data Source, and time fields in dedupe keys.
- Never use floating point for values that need exact text preservation.

## Tables Sketch

`connections`

- local connection ID
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
- start/end physical time
- start/end civil time
- Data Source JSON
- raw Data Point JSON
- inserted/updated timestamps

`rollups`

- local ID
- provider name
- connection ID
- Data Type
- rollup kind: rollUp or dailyRollUp
- window start/end
- civil date when daily
- raw Rollup JSON
- inserted/updated timestamps

`sync_runs`

- local ID
- provider name
- connection ID
- Data Types requested
- range requested
- endpoint family used
- status
- seen/new counts
- started/finished timestamps
- error summary

## Normalized Views

Likely first views:

- daily steps
- resting heart rate by day
- heart rate samples
- sleep sessions
- exercise sessions
- daily oxygen saturation
- daily respiratory rate
- weight samples

## Dedupe Questions

- Are upstream Data Point names stable across repeated list/reconcile calls?
- Are Rollups deterministic enough to upsert by Data Type and window?
- Do corrected historical records appear with the same name or as replacements?
- How should source-family-filtered `reconcile` records coexist with unfiltered `list` records?

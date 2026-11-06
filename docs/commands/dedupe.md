---
title: "gohealthcli dedupe"
description: "Report or remove exact twin Data Points in the Health Archive."
---

Find twin Data Points: two or more archived rows that share one logical identity under the same rule `backup push` uses to validate a Health Archive Snapshot. Rows with an `upstream_resource_name` match on Provider, Connection, Data Type, resource name, and source-family filter; rows without one match on Provider, Connection, Data Type, record kind, start and end times, civil times and date, timezone metadata, Data Source, and source-family filter. Twins block `backup push` with a duplicate Data Point identity error.

Without `--confirm`, `dedupe` is a dry run. It opens the Health Archive inspection-only, like `doctor`, and never migrates the schema, creates sidecars, or changes a row. With `--confirm`, it opens the Health Archive for writing, recomputes the plan inside one write transaction, deletes the removable duplicates, and commits. A rerun after a successful `--confirm` reports zero twin groups.

Each twin group keeps its lowest id. A group is removable only when every duplicate matches the kept row in every column except `id`, `inserted_at`, and `updated_at`, with `raw_json` compared byte for byte, and no duplicate has a Data Point Revision or Data Point Attachment. Otherwise the whole group is blocked and left unchanged, and `blocked` lists one entry per offending duplicate with `keep_id`, `duplicate_id`, `data_type`, and a `reason` of `content_differs`, `duplicate_has_revisions`, or `duplicate_has_attachments`. `--confirm` still removes the removable groups when others are blocked. Rollups are not checked.

The report carries `status` (`dedupe_planned` or `dedupe_applied`), `archive_path`, `twin_groups`, `removable_rows`, `removed_rows`, `blocked_groups`, `groups` (removable groups with `keep_id`, `duplicate_ids`, and `data_type`), `blocked`, and `message`. `--plain` prints key/value lines with zero-based `groups.N.<field>` and `blocked.N.<field>` entries; `duplicate_ids` is comma-separated. Human output lists each group and, on a dry run, tells you to rerun with `--confirm`.

Exit status is 0 when no group is blocked and 1 when any group is blocked, after the full report is written. Open and archive errors fail with `operation_failed`. `dedupe` never contacts the Provider, reads the Credential Store, refreshes a token, records a Sync Run, or changes a Sync Cursor. Take a copy of the Health Archive before running `--confirm`.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--confirm` | bool | `false` | delete removable twin Data Points instead of only reporting them |

---
title: "gohealthcli status"
description: "Summarise archive counts and newest synced timestamps."
---

Print a per-Data-Type summary of the Health Archive: how many Data Points are stored, the newest synced timestamp, and the most recent Sync Run status. Useful as a quick health check before or after a long sync.

Also reports identity-metadata freshness: a `paired_device_count` line (when a `paired-devices` snapshot is archived) and an `identity_snapshot.<kind>.fetched_at` line per Identity Snapshot kind that has at least one row (`profile`, `settings`, `paired-devices`, `irn-profile`). In `--json` these surface under an `identity_snapshots_freshness` block — omitted entirely when no snapshots exist. `paired_device_count` is also emitted as a top-level JSON key so `--plain` and `--json` carry the same field; the nested `identity_snapshots_freshness.paired_device_count` is preserved for back-compat.

`--plain` and `--json` carry the same information. The plain `known_data_types: a,b,c` line maps to a top-level `known_data_types` JSON array. Plain `data_type.<name>.*` and `identity_snapshot.<kind>.*` lines flatten the JSON `data_types[]` and `identity_snapshots_freshness` blocks, and the `latest_successful_sync_run_*` / `latest_failed_sync_run_*` lines flatten the matching JSON objects.

Also reports Tier 2 coverage: `electrocardiogram_event_count` and `irregular_rhythm_notification_count` (plain) appear only when the corresponding scope has been granted via `connect --add-scopes ecg,irn`. In `--json` these surface under a `tier_2` block alongside `electrocardiogram_scope_granted` / `irregular_rhythm_notification_scope_granted` flags, both counts defaulting to 0 when the scope is not granted.

`status` does no provider I/O — it reads only the local Health Archive. On entry it fences abandoned Sync Runs: any `sync_running` row whose heartbeat is older than 5 minutes is flipped to `sync_failed` with `error_summary` `abandoned (no heartbeat for 5m)` (see `sync --status` for the full fencing rule), so the summary never reports a killed process as still running.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

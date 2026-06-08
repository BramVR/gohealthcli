---
title: "gohealthcli status"
description: "Summarise archive counts and newest synced timestamps."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Print a per-Data-Type summary of the Health Archive: how many Data Points are stored, the newest synced timestamp, and the most recent Sync Run status. Useful as a quick health check before or after a long sync.

Also reports identity-metadata freshness: a `paired_device_count` line (when a `paired-devices` snapshot is archived) and an `identity_snapshot.<kind>.fetched_at` line per Identity Snapshot kind that has at least one row (`profile`, `settings`, `paired-devices`, `irn-profile`). In `--json` these surface under an `identity_snapshots_freshness` block — omitted entirely when no snapshots exist.

`status` does no provider I/O — it reads only the local Health Archive.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

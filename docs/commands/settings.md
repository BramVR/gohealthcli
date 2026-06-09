---
title: "gohealthcli settings"
description: "Archive a Settings Snapshot from the provider."
---

Fetch the upstream `users.getSettings` payload and append it to the Health Archive as a new Identity Snapshot of kind `settings`. The `current_settings` Normalized View projects the latest snapshot's measurement system, timezone, and stride-length type into columns for `query` and `export`.

`settings` is read-only against the provider and writes the raw response to the archive; the JSON shape stays the source of truth, so new fields can be projected into the view without a re-sync.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |

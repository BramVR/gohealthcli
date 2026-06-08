---
title: "gohealthcli devices"
description: "Archive a Paired Devices Snapshot from the provider."
---

Fetch the upstream `users.pairedDevices.list` payload and append it to the Health Archive as a new Identity Snapshot of kind `paired-devices`. The `paired_devices` Normalized View explodes the latest snapshot via `json_each`, returning one row per device with `device_type`, `model`, `manufacturer`, `battery_percentage`, `last_sync_time`, and `features`.

This is the LLM's path to questions like "which Pixel Watch synced last?" or "what's my Fitbit battery?" — every projection is read-only against the raw snapshot, so new fields can be added without re-syncing.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

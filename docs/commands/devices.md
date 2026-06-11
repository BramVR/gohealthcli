---
title: "gohealthcli devices"
description: "Archive a Paired Devices Snapshot from the provider."
---

Fetch the upstream `users.pairedDevices.list` payload and append it to the Health Archive as a new Identity Snapshot of kind `paired-devices`. The `paired_devices` Normalized View explodes the latest snapshot via `json_each`, returning one row per device with `name`, `device_type`, `device_version`, `battery_status`, and `battery_level` — the real payload shape verified against a live archive (#298).

This is the LLM's path to questions like "which devices are paired?" or "what's my watch battery?" — every projection is read-only against the raw snapshot, so new fields can be added without re-syncing.

Requires the `settings.readonly` OAuth scope (PRD #142 #176 confirmed empirically — `profile.readonly` alone returns HTTP 403). If the scope is missing, `devices` exits with status `devices_scope_missing` and a remediation hint; run `gohealthcli connect --add-scopes settings` once to grant it. No second base-set browser sign-in is needed.

If the Provider cannot be reached — a network failure or a non-auth upstream HTTP error — the command exits non-zero with JSON status `provider_unreachable`, so automation can distinguish a Provider outage from local misconfiguration. An upstream HTTP 401 instead reports the `Google Health rejected stored Connection token` message: re-run `gohealthcli connect`.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |

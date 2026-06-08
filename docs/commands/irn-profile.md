---
title: "gohealthcli irn-profile"
description: "Archive an IRN Profile Snapshot from the provider."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Fetch the upstream `users.getIrnProfile` payload (onboarding state, enrollment state for Google's irregular-rhythm-notification feature) and append it to the Health Archive as a new Identity Snapshot of kind `irn-profile`. The `current_irn_profile` Normalized View projects the latest snapshot as columns.

Requires the `irn.readonly` OAuth scope — run `gohealthcli connect --add-scopes irn` once to grant it. If the scope is not granted, `irn-profile` exits with a clear reconnect instruction and does **not** trigger the browser flow.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

---
title: "gohealthcli irn-profile"
description: "Archive an IRN Profile Snapshot from the provider."
---

Fetch the upstream `users.getIrnProfile` payload (onboarding state, enrollment state for Google's irregular-rhythm-notification feature) and append it to the Health Archive as a new Identity Snapshot of kind `irn-profile`. The `current_irn_profile` Normalized View projects the latest snapshot as columns.

Requires the `irn.readonly` OAuth scope — run `gohealthcli connect --add-scopes irn` once to grant it. If the scope is not granted, `irn-profile` exits with a clear reconnect instruction and does **not** trigger the browser flow.

If the Provider cannot be reached — a network failure or a non-auth upstream HTTP error — the command exits non-zero with JSON status `provider_unreachable`, so automation can distinguish a Provider outage from local misconfiguration. An upstream HTTP 401 instead reports the `Google Health rejected stored Connection token` message: re-run `gohealthcli connect`.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |

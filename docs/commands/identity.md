---
title: "gohealthcli identity"
description: "Refresh the archived Google Identity metadata."
---

Re-fetch the upstream Google Identity payload (Google Health user ID and legacy Fitbit user ID when present) and update the metadata stored alongside the Connection.

`identity` does not change the OAuth tokens or move the Connection between archives — use `connect` for those. It is a low-cost, read-only operation against the provider.

If the Provider cannot be reached — a network failure or a non-auth upstream HTTP error — the command exits non-zero with JSON status `provider_unreachable`, so automation can distinguish a Provider outage from local misconfiguration. An upstream HTTP 401 instead reports the `Google Health rejected stored Connection token` message: re-run `gohealthcli connect`.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

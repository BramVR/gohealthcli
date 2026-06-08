---
title: "gohealthcli identity"
description: "Refresh the archived Google Identity metadata."
---

Re-fetch the upstream Google Identity payload (Google Health user ID and legacy Fitbit user ID when present) and update the metadata stored alongside the Connection.

`identity` does not change the OAuth tokens or move the Connection between archives — use `connect` for those. It is a low-cost, read-only operation against the provider.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

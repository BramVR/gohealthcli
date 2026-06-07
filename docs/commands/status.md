---
title: "gohealthcli status"
description: "Summarise archive counts and newest synced timestamps."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Print a per-Data-Type summary of the Health Archive: how many Data Points are stored, the newest synced timestamp, and the most recent Sync Run status. Useful as a quick health check before or after a long sync.

`status` does no provider I/O — it reads only the local Health Archive.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

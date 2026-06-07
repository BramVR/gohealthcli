---
title: `gohealthcli doctor`
description: Validate local setup and provider reachability.
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Run a diagnostic check against the local gohealthcli installation: config presence, Health Archive path, Credential Store status, schema version, and connection count.

With `--online`, also refresh stored tokens and verify Google Health API reachability. The command never writes health data; it only inspects local state and (with `--online`) performs a single read-only round trip to the provider.

The output is a structured report on stdout. Use `--json` for stable machine-readable output, `--plain` for terminal-friendly key/value lines.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--online` | bool | `false` | refresh tokens and check provider reachability |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

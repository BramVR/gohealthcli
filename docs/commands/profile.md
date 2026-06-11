---
title: "gohealthcli profile"
description: "Archive a Profile Snapshot from the provider."
---

Fetch the upstream profile blob (units, time zone, demographic settings as exposed by the Google Health API) and append it to the Health Archive as a new Profile Snapshot. Each invocation creates a new dated snapshot rather than overwriting the prior one, so historical settings drift is preserved.

A Profile Snapshot is not a Data Point. It is metadata about the consenting user's account and the unit conventions in force at the time of fetch.

If the Provider cannot be reached — a network failure or a non-auth upstream HTTP error — the command exits non-zero with JSON status `provider_unreachable`, so automation can distinguish a Provider outage from local misconfiguration. An upstream HTTP 401 instead reports the `Google Health rejected stored Connection token` message: re-run `gohealthcli connect`.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

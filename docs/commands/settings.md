---
title: "gohealthcli settings"
description: "Archive a Settings Snapshot from the provider."
---

Fetch the upstream `users.getSettings` payload and append it to the Health Archive as a new Identity Snapshot of kind `settings`. The `current_settings` Normalized View projects the latest snapshot's measurement system, timezone, and stride-length type into columns for `query` and `export`.

`settings` is read-only against the provider and writes the raw response to the archive; the JSON shape stays the source of truth, so new fields can be projected into the view without a re-sync.

Requires the `settings.readonly` OAuth scope (PRD #142 #176 confirmed empirically — `profile.readonly` alone returns HTTP 403). If the scope is missing, `settings` exits with status `settings_scope_missing` and a remediation hint; run `gohealthcli connect --add-scopes settings` once to grant it. No second base-set browser sign-in is needed.

If the Provider cannot be reached — a network failure or a non-auth upstream HTTP error — the command exits non-zero with JSON status `provider_unreachable`, so automation can distinguish a Provider outage from local misconfiguration. An upstream HTTP 401 instead reports the `Google Health rejected stored Connection token` message: re-run `gohealthcli connect`.

Setup and Connection failures may add `remediation` to JSON results and zero-based `remediation.N` fields to plain results; human output stays unchanged. Steps are Reporter-owned and diagnosis-first: `doctor` before `init` or `connect`, `doctor --online` before reconnecting or choosing a new archive, and missing-scope consent uses only sorted `--add-scopes` keywords from the public catalog. Building or rendering these steps never performs Provider I/O, starts OAuth, or interpolates error text.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |

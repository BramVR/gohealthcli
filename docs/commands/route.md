---
title: "gohealthcli route"
description: "Write one archived exercise route as its exact TCX file."
---

Read the archived TCX route of one exercise Data Point from the local Health Archive and write its exact bytes. Name the exercise by the `upstream_resource_name` shown in the `exercise-sessions` export (`users/<user>/dataTypes/exercise/dataPoints/<id>`) or by its trailing Provider ID. The identifier is matched exactly; an unknown identifier, one that matches more than one Data Point, or one that names a non-exercise Data Point fails with `flag_invalid` before any sidecar read.

When `sync --types exercise` archived several TCX Attachments for one session, `route` returns the newest. Before emitting anything it checks the sidecar's canonical path, byte size, and SHA-256 against the Attachment row and refuses missing, symbolic-link, or out-of-root sidecars with `operation_failed` and `gohealthcli doctor` as remediation. It never falls back to an older Attachment.

Exactly one of `--output PATH` or `--stdout` must be supplied. `--stdout` writes the exact bytes. `--output` follows the same new-file contract as `raw --output`: it refuses every existing destination and never overwrites, creates the file as owner-only mode `0600` on Linux and macOS, writes nothing to stdout, and reports only the quoted path and byte count on stderr. On Windows the file inherits its parent directory ACL, so choose a private directory. Route data is location data.

An exercise with no archived route fails with `operation_failed` and remediation `gohealthcli connect --add-scopes tcx`, then `gohealthcli sync --types exercise --from YYYY-MM-DD` over that session's window.

`route` is offline. It never contacts the Provider, reads the Credential Store, refreshes a token, writes or migrates the Health Archive, records a Sync Run, changes a Sync Cursor, or creates a sidecar. `--json` and `--plain` select only the failure envelope; success output is the route bytes in every mode.

## Usage

```
gohealthcli route <exercise>
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | report failures as one JSON envelope on stdout; route bytes follow --format |
| `--plain` | bool | `false` | report failures as status/message lines on stdout; route bytes follow --format |
| `--no-input` | bool | `false` | accepted for uniformity; route does no prompting |
| `--format` | string | `tcx` | route format: tcx (exact archived bytes) |
| `--output` | string | — | write the route to a new owner-only file (never overwrites) |
| `--stdout` | bool | `false` | write the route bytes to stdout |

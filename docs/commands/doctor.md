---
title: "gohealthcli doctor"
description: "Validate local setup and provider reachability."
---

Run a diagnostic check against the local gohealthcli installation: config presence, resolved timezone, Health Archive path, Credential Store status, schema version, and connection count. Legacy configs without `timezone` report UTC.

The report also includes the Data Point Attachment Store: `attachment_root_path`, `attachment_root_mode`, and an optional `attachments` block. Every Attachment row is checked with the same canonical-path, no-follow regular-file read, byte-size, and SHA-256 verification used by `route`. Symbolic links and Windows reparse points in the root or sidecar path are rejected. An unsafe or missing root is never walked or recreated.

The `attachments` block retains `orphan_rows` and `orphan_files` arrays. Missing canonical sidecars and escaping paths remain orphan rows; unclaimed sidecars remain orphan files. Orphan-only reports retain status `ok` and exit 0. `verification_failures` is present only when nonempty, ordered by Attachment ID, with `attachment_id`, `data_point_id`, `sha256`, `path_relative`, and `reason` for each failure. Clean archives omit the entire block. Verification failures return `setup_invalid` and exit 1 while retaining every finding. Restore the matching Health Archive and sidecars from a known-good copy to recover corrupt content.

Human output lists each verification failure. `--plain` retains positive `attachments_orphan_files` and `attachments_orphan_rows` counts and adds `attachments_verification_failures: N` plus zero-based `attachments_verification_failures.N.<field>` lines for every failure field. `--json` returns those records under `attachments.verification_failures`.

`doctor` opens the Health Archive inspection-only. It never migrates the schema, creates sidecars, or repairs content. Stale schemas and SQLite WAL mode fail without changes. Local integrity failures stop `--online` before Credential Store access or Provider work.

With `--online`, also refresh stored tokens and verify Google Health API reachability. The online path may update token material and token metadata after successful refresh, but never writes health data. Local archive and Attachment checks must pass first.

The output is a structured report on stdout. Use `--json` for stable machine-readable output, `--plain` for terminal-friendly key/value lines.

Setup and Connection failures may add `remediation` to JSON results and zero-based `remediation.N` fields to plain results; human output stays unchanged. Steps are Reporter-owned and diagnosis-first: `doctor` before `init` or `connect`, `doctor --online` before reconnecting or choosing a new archive, and missing-scope consent uses only sorted `--add-scopes` keywords from the public catalog. Building or rendering these steps never performs Provider I/O, starts OAuth, or interpolates error text.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |
| `--online` | bool | `false` | refresh tokens and check provider reachability |

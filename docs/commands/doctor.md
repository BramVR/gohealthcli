---
title: "gohealthcli doctor"
description: "Validate local setup and provider reachability."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Run a diagnostic check against the local gohealthcli installation: config presence, Health Archive path, Credential Store status, schema version, and connection count.

The report also includes the Data Point Attachment Store: the `attachment_root_path` and `attachment_root_mode` it owns, plus an `attachments` block listing orphan sidecar files (file on disk with no matching row) and orphan rows (row in the archive whose sidecar file is gone). In `--plain` mode the orphan counts surface as `attachments_orphan_files: N` and `attachments_orphan_rows: N` lines, emitted only when the count is positive. `doctor` never modifies the archive or the sidecar tree — it reports only.

With `--online`, also refresh stored tokens and verify Google Health API reachability. The command never writes health data; it only inspects local state and (with `--online`) performs a single read-only round trip to the provider.

The output is a structured report on stdout. Use `--json` for stable machine-readable output, `--plain` for terminal-friendly key/value lines.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |
| `--online` | bool | `false` | refresh tokens and check provider reachability |

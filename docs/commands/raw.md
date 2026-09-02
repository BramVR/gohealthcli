---
title: "gohealthcli raw"
description: "Print one raw Provider page or plan its request."
---

Fetch one upstream Google Health API response page and print the raw body to stdout. Useful for endpoint exploration without committing the response to the Health Archive.

First positional argument is `endpoint <name>` (for example `endpoint getIdentity`) or `data-type <data-type>` (for example `data-type steps --from yesterday --to today`). Data Type list and reconcile targets accept the same exact range grammar as `sync`: `now`, `today`, `yesterday`, `YYYY-MM-DD`, or RFC3339. Named boundaries use `--timezone`, then the configured timezone, then UTC; one captured clock and the Provider Data Type's physical, civil, or daily filter shape determine the exact range. Identity endpoints reject `--from`, `--to`, and `--timezone` because those flags have no meaning there. Google's ECG list endpoint accepts only a physical `electrocardiogram.interval.start_time >= ...` lower bound, so `raw data-type electrocardiogram` rejects an explicit `--to` rather than claiming to narrow the Provider-shaped response. `--page-size` and `--page-token` select one page. Identity endpoints reject those paging flags.

`raw data-type <data-type> get --id <provider-id>` fetches one Data Point only when the compiled Provider catalog includes the `get` endpoint family. The Provider ID remains opaque and is escaped as one URL path component. This operation rejects range, timezone, paging, and source-filter inputs before setup, performs one request without pagination, and shares raw's exact-byte stdout, safe `--output`, Provider error, scope, and zero-effect `--plan` paths.

`raw data-type <data-type> reconcile --source-family <family> --from <boundary> [--to <boundary>]` fetches one reconciled response page only when the compiled Provider catalog includes the `reconcile` endpoint family and accepts that source family. It uses the same Provider request builder, range shape, required scopes, source-family mapping, default page size, and page-token handling as sync. It never follows a returned page token automatically, parses or archives Data Points, records a Sync Run, advances a Sync Cursor, or creates an Attachment sidecar. Missing or incompatible inputs fail before credential or Provider access.

`--plan` prints the exact secret-free request description without contacting the Provider, reading the Credential Store, loading or refreshing a token, opening or writing the Health Archive, migrating, changing a Sync Cursor, or creating an Attachment sidecar. The plan contains the method, sanitized production URL, non-secret headers, required scopes, resolved range and timezone for Data Type targets, source family where present, paging inputs, and an all-false effect report. Page-token and Provider-ID material is redacted. A lower-bound-only target omits `range.to` and the human mode labels the missing Provider upper bound. Use `--json` or `--plain` with `--plan` for structured output.

Without `--plan`, `raw` is Provider-shaped on purpose. The JSON you see is what the Provider returns, not the normalized shape the archive stores. Normal reads preserve the Provider's exact response bytes on stdout and reject command-local `--json` or `--plain`; their only possible write remains persisting an existing OAuth token refresh.

Pass `--output PATH` to write those same exact bytes to a new file instead of stdout. The command refuses every existing destination, including symbolic links and directories, and never overwrites. On Linux and macOS the parent directory must be owned by the effective user and not writable by group or other users; macOS also rejects parents with an extended ACL. The command creates the file as owner-only mode `0600`, writes no Provider content to stdout, and reports only the quoted path and byte count on stderr. A failed, short, permission, close, or publish write never creates the final destination. Windows uses atomic no-replace publication on local volumes and rejects a pre-existing reparse destination, but the new file inherits its parent directory ACL; choose a directory whose ACL is already private. Windows also rejects filenames with special Win32 normalization, reserved device names, or alternate-stream syntax. UNC paths, mapped network drives, and local-looking parent links that resolve to a network share are rejected before the Provider read because Windows network rename requests cannot preserve the pinned-parent publication contract. Other build targets reject file output because the required atomic no-replace rename is unavailable. `--output` cannot be combined with `--plan`.

Failures route through the unified Failure Reporter: a Provider outage (network failure or non-auth upstream HTTP error) reports status `provider_unreachable`, while other operation errors, including an upstream HTTP 401 auth rejection with the `Google Health rejected stored Connection token` message, report `operation_failed`.

Setup and Connection failures may add `remediation` to JSON results and zero-based `remediation.N` fields to plain results; human output stays unchanged. Steps are Reporter-owned and diagnosis-first: `doctor` before `init` or `connect`, `doctor --online` before reconnecting or choosing a new archive, and missing-scope consent uses only sorted `--add-scopes` keywords from the public catalog. Building or rendering these steps never performs Provider I/O, starts OAuth, or interpolates error text.

## Usage

```
gohealthcli raw <target> [<args>...]
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--id` | string | — | opaque Provider Data Point ID (data-type get only) |
| `--from` | string | — | inclusive time-range start (where supported by the endpoint) |
| `--to` | string | — | exclusive time-range end (where supported by the endpoint) |
| `--timezone` | string | — | IANA timezone for now, today, and yesterday (Data Type range reads only; default config, then UTC) |
| `--page-size` | int | — | pagination page size (positive integer; where supported by the endpoint) |
| `--page-token` | string | — | pagination page token from a prior response |
| `--source-family` | string | — | source family filter; supported: wearable |
| `--plan` | bool | `false` | print the exact secret-free Provider request plan without external access |
| `--output` | string | — | write exact Provider response bytes to a new private file |

---
title: "gohealthcli raw"
description: "Print raw provider JSON or plan a Provider request."
---

Fetch a single upstream Google Health API response and print the raw body to stdout. Useful for endpoint exploration without committing the response to the Health Archive.

First positional argument is `endpoint <name>` (for example `endpoint getIdentity`) or `data-type <data-type>` (for example `data-type steps --from yesterday --to today`). Data Type list targets accept the same exact range grammar as `sync`: `now`, `today`, `yesterday`, `YYYY-MM-DD`, or RFC3339. Named boundaries use `--timezone`, then the configured timezone, then UTC; one captured clock and the provider Data Type's physical, civil, or daily filter shape determine the exact range. Identity endpoints reject `--from`, `--to`, and `--timezone` because those flags have no meaning there. Google's ECG list endpoint accepts only a physical `electrocardiogram.interval.start_time >= ...` lower bound, so `raw data-type electrocardiogram` rejects an explicit `--to` rather than claiming to narrow the provider-shaped response. `--page-size` and `--page-token` drive pagination. Identity endpoints reject those paging flags.

`--plan` prints the exact secret-free request description without contacting the Provider, reading the Credential Store, loading or refreshing a token, opening or writing the Health Archive, migrating, changing a Sync Cursor, or creating an Attachment sidecar. The plan contains the method, sanitized production URL, non-secret headers, required scopes, resolved range and timezone for Data Type targets, paging inputs, and an all-false effect report. Page-token material is redacted. A lower-bound-only target omits `range.to` and the human mode labels the missing Provider upper bound. Use `--json` or `--plain` with `--plan` for structured output.

Without `--plan`, `raw` is provider-shaped on purpose. The JSON you see is what the Provider returns, not the normalized shape the archive stores. Normal reads preserve the Provider's exact response bytes on stdout and reject command-local `--json` or `--plain`; their only possible write remains persisting an existing OAuth token refresh.

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
| `--from` | string | — | inclusive time-range start (where supported by the endpoint) |
| `--to` | string | — | exclusive time-range end (where supported by the endpoint) |
| `--timezone` | string | — | IANA timezone for now, today, and yesterday (Data Type lists only; default config, then UTC) |
| `--page-size` | int | — | pagination page size (positive integer; where supported by the endpoint) |
| `--page-token` | string | — | pagination page token from a prior response |
| `--plan` | bool | `false` | print the exact secret-free Provider request plan without external access |

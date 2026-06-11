---
title: "gohealthcli raw"
description: "Print raw provider JSON for endpoint exploration."
---

Fetch a single upstream Google Health API response and print the raw body to stdout. Useful for endpoint exploration without committing the response to the Health Archive.

First positional argument is `endpoint <name>` (for example `endpoint getIdentity`) or `data-type <data-type>` (for example `data-type steps --from 2026-01-01 --to 2026-01-02`). `--from` and `--to` constrain time ranges where the endpoint supports them; `--page-size` and `--page-token` drive pagination.

`raw` is provider-shaped on purpose — the JSON you see is what the provider returns, not the normalised shape the archive stores.

Failures route through the unified Failure Reporter: a Provider outage (network failure or non-auth upstream HTTP error) reports status `provider_unreachable`, while other operation errors — including an upstream HTTP 401 auth rejection, which carries the `Google Health rejected stored Connection token` message — report `operation_failed`.

## Usage

```
gohealthcli raw <target> [<args>...]
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--from` | string | — | inclusive time-range start (where supported by the endpoint) |
| `--to` | string | — | exclusive time-range end (where supported by the endpoint) |
| `--page-size` | int | — | pagination page size (positive integer; where supported by the endpoint) |
| `--page-token` | string | — | pagination page token from a prior response |

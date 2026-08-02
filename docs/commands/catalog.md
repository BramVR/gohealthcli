---
title: "gohealthcli catalog"
description: "Verify the Provider catalog against Google discovery."
---

Compare gohealthcli's canonical Google Health Data Type catalog with the live public v4 discovery document, or with an offline document supplied through `--discovery PATH`. The command requires no config, Health Archive, Connection, credentials, or Provider account.

The stable result status is `verified`, `verified_with_known_gaps`, or `drift_detected`; unexpected drift exits nonzero. Known local Rollup-only and upstream-only raw Data Types are reported as explicit exceptions. The public discovery document does not establish exact per-Data-Type operation or filter support, so the result reports those facts as unverifiable instead of guessing.

## Usage

```
gohealthcli catalog verify
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--discovery` | string | — | read a Google Health discovery document from PATH instead of the public endpoint |

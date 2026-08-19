---
title: "gohealthcli catalog"
description: "Browse and verify the compiled Provider catalog."
---

Use `catalog list` to list every compiled Google Health Data Type in canonical catalog order. Each machine-readable row separates default or opt-in selection from supported or unsupported raw Data Point access. Use `catalog scopes` to group each exact required OAuth scope with its catalog-ordered Data Type membership. Both actions work without config, a Health Archive, Connection, credentials, or network access; they report catalog requirements, not the current token's grants.

Use `catalog verify` to compare the same canonical catalog with the live public v4 discovery document, or with an offline document supplied through `--discovery PATH`. Its stable result status remains `verified`, `verified_with_known_gaps`, or `drift_detected`; unexpected drift exits nonzero. Known local Rollup-only and upstream-only raw Data Types are reported as explicit exceptions. The public discovery document does not establish exact per-Data-Type operation or filter support, so the result reports those facts as unverifiable instead of guessing.

## Usage

```
gohealthcli catalog <list|scopes|verify>
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--discovery` | string | — | with verify: read a Google Health discovery document from PATH instead of the public endpoint |

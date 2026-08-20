---
title: "gohealthcli catalog"
description: "Browse and verify the compiled Provider catalog."
---

Use `catalog list` to list every compiled Google Health Data Type in canonical catalog order. Each machine-readable row separates default or opt-in selection from supported or unsupported raw Data Point access. Use `catalog scopes` to group each exact required OAuth scope with its catalog-ordered Data Type membership.

Use `catalog describe <data-type>` for the detailed contract of one compiled Data Type. Endpoint families, filters, range shapes, page policies, required scopes, record kind, and rollup modes come only from the compiled catalog. Field names and JSON types come from the committed discovery snapshot by default. Use `--discovery PATH` for a bounded local document or `--live` for the fixed public v4 discovery endpoint; the two flags are mutually exclusive. Every fact group reports its source and discovery revision. Discovery enrichment is validated against the compiled identity and record kind and never overrides compiled facts.

The list, scopes, and default describe actions work without config, a Health Archive, Connection, credentials, or network access; they report catalog requirements, not the current token's grants.

Use `catalog verify` to compare the same canonical catalog with the live public v4 discovery document, or with an offline document supplied through `--discovery PATH`. Its stable result status remains `verified`, `verified_with_known_gaps`, or `drift_detected`; unexpected drift exits nonzero. Known local Rollup-only, upstream-only raw, and upstream write-only Data Types are reported as explicit exceptions. The public discovery document does not establish exact per-Data-Type operation or filter support, so the result reports those facts as unverifiable instead of guessing.

## Usage

```
gohealthcli catalog <list|scopes|verify|describe> [data-type]
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--discovery` | string | — | with describe or verify: read a Google Health discovery document from PATH |
| `--live` | bool | `false` | with describe: read the fixed public Google Health discovery endpoint |

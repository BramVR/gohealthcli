---
title: "gohealthcli export"
description: "Write a normalised dataset to CSV or JSONL."
---

Render one of the curated normalised datasets (daily-steps, heart-rate-samples, resting-heart-rate-by-day, sleep-sessions, exercise-sessions, weight-samples, and many more) from the Health Archive. Exports are read-only; nothing in the archive is mutated.

Every registered dataset, including daily and Rollup datasets, preserves gaps in the Health Archive. An absent day or window produces no row; a stored zero produces a zero row. An absent day means no qualifying archived record, not measured zero. Non-wear, delayed device sync, or a day outside the synced range are possible causes; absence alone does not identify the cause or prove completeness. Do not fill absent days with zeros when calculating weekly or monthly averages. Include stored zeros and report the observed-day count and requested range. When averaging `daily_steps`, filter or group by `source_family_filter`. For `total_calories_rollups`, filter or group by `rollup_kind`. Count observed days with `COUNT(DISTINCT civil_date)`.

Run `gohealthcli export --help` to see the full list of supported datasets, sorted alphabetically. If you pass a name that does not exist, the error message includes the closest matches (Levenshtein ≤ 3, top 3) and a pointer back to `export --help`.

Exactly one of `--output PATH` or `--stdout` must be supplied — the explicit destination prevents an accidental terminal dump of a long export.

`--json` is a Common Flag Set synonym for `--format jsonl`; `--plain` is a synonym for `--format csv`. Passing a synonym alongside a contradictory `--format` value (`--json --format csv`, `--plain --format jsonl`) fails with a `--<synonym> conflicts with --format <value>` error. `--plain --json` together fails with the documented mutual-exclusion error from the Common Flag Set seam.

## Usage

```
gohealthcli export <dataset>
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | synonym for --format jsonl |
| `--plain` | bool | `false` | synonym for --format csv |
| `--no-input` | bool | `false` | accepted for uniformity; export does no prompting |
| `--format` | string | `csv` | export format: csv or jsonl (synonyms: --json → jsonl, --plain → csv) |
| `--output` | string | — | write export to path |
| `--stdout` | bool | `false` | write export data to stdout |

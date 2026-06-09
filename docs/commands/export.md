---
title: "gohealthcli export"
description: "Write a normalised dataset to CSV or JSONL."
---

Render one of the curated normalised datasets (daily-steps, heart-rate-samples, resting-heart-rate-by-day, sleep-sessions, exercise-sessions, weight-samples) from the Health Archive. Exports are read-only; nothing in the archive is mutated.

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

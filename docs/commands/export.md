---
title: "gohealthcli export"
description: "Write a normalised dataset to CSV or JSONL."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Render one of the curated normalised datasets (daily-steps, heart-rate-samples, resting-heart-rate-by-day, sleep-sessions, exercise-sessions, weight-samples) from the Health Archive. Exports are read-only; nothing in the archive is mutated.

Exactly one of `--output PATH` or `--stdout` must be supplied — the explicit destination prevents an accidental terminal dump of a long export.

## Usage

```
gohealthcli export <dataset>
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--format` | string | `csv` | export format: csv or jsonl |
| `--output` | string | — | write export to path |
| `--stdout` | bool | `false` | write export data to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |

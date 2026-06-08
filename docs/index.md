---
title: gohealthcli
permalink: /
description: A local-first, read-only CLI that archives personal health and fitness data from the Google Health API into a queryable SQLite Health Archive on your own machine.
---

## What it is

`gohealthcli` connects to the Google Health API, stores raw provider JSON in a local SQLite **Health Archive**, and offers scriptable commands for sync, status, query, raw API exploration, and CSV or JSONL exports.

It is for **local inspection and personal data archiving**. It does not write health data, delete health data, run a server, upload archives, or share exports.

## Try it

```bash
gohealthcli init --oauth-client-file ~/Downloads/client_secret_*.json
gohealthcli doctor --plain
gohealthcli connect --plain
gohealthcli sync --types steps --from 2026-01-01 --to 2026-01-02 --plain
gohealthcli status --plain
```

## What you can archive

Raw Data Points across the Tier 1 Activity & Fitness, Health Metrics, and Daily catalogs — steps, heart rate, HRV, oxygen saturation, distance, sleep, exercise, weight, body fat, height, VO2 max, blood glucose, core body temperature, floors, and their daily Rollups. Tier 2 adds ECG sessions and irregular-rhythm notifications behind opt-in scopes. Exercise sessions can archive the upstream TCX route as a Data Point Attachment when the TCX scope is granted. Wearable-only filtering is available for Data Types backed by the Google Health reconcile path; `sync` orchestrates `--all` or `--types csv` into per-Data-Type runs with backoff/retry on 429/5xx and an outcome-aware Sync Cursor. Identity snapshots, paired devices, settings, and the IRN profile each have their own verb.

Normalised exports cover 33 datasets across activity, heart rate, heart rhythm, sleep, exercise, body measurements, VO2 max, biomarkers, and device/account metadata — written as CSV or JSONL, on demand, to a path you choose. Rollups widen to `hourly`, `weekly`, or `window=<dur>`. The full list lives in the [README](https://github.com/BramVR/gohealthcli#readme) and is drift-guarded against the binary.

## Where to start

- **Install** — pick a path that works today: `go install`, source build, or the upcoming Homebrew tap.
- **Quickstart** — walk through OAuth setup and your first sync.
- **Reference** — every subcommand and flag at a stable URL, including `devices`, `settings`, `irn-profile`, and `describe-schema`.

## What it isn't

`gohealthcli` is not a cloud service. It does not run in the background, does not phone home, does not upload your data, and does not write back to the provider. The archive sits on disk in a file you can move, back up, or delete.

## Project

`gohealthcli` is open source and in active development under [BramVR/gohealthcli](https://github.com/BramVR/gohealthcli). First Release is in progress: the command surface and storage shape are designed as durable foundations rather than a disposable MVP, because health data is sensitive and local archives are hard to rebuild casually.

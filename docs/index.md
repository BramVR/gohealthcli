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

Raw Data Points across a focused set of Data Types: steps, heart rate, heart rate variability, oxygen saturation, sleep, exercise, distance, weight, and daily Rollups for the same. Wearable-only filtering is available for Data Types backed by the Google Health reconcile path.

Normalised exports cover daily steps, heart rate samples, resting heart rate by day, sleep sessions, exercise sessions, and weight samples — written as CSV or JSONL, on demand, to a path you choose.

## Where to start

- **Install** — pick a path that works today: `go install`, source build, or the upcoming Homebrew tap.
- **Quickstart** — walk through OAuth setup and your first sync.
- **Reference** — every subcommand and flag at a stable URL.

## What it isn't

`gohealthcli` is not a cloud service. It does not run in the background, does not phone home, does not upload your data, and does not write back to the provider. The archive sits on disk in a file you can move, back up, or delete.

## Project

`gohealthcli` is open source and in active development under [BramVR/gohealthcli](https://github.com/BramVR/gohealthcli). First Release is in progress: the command surface and storage shape are designed as durable foundations rather than a disposable MVP, because health data is sensitive and local archives are hard to rebuild casually.

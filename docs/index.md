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

Raw Data Points across the Google Health Tier 1 and Tier 2 catalogs and their Rollups, plus identity, device, settings, and profile snapshots. Wearable-only filtering is available for Data Types backed by the Google Health reconcile path. `sync` orchestrates `--all` or `--types csv` into per-Data-Type runs with backoff/retry on 429/5xx and an outcome-aware Sync Cursor; expired access tokens auto-refresh from the stored refresh token — even mid-run, so long backfills survive token expiry. A long sync can be watched from another terminal with `sync --status` — every run heartbeats per archived page, and abandoned runs are fenced automatically. Exercise sessions can archive the upstream TCX route as a Data Point Attachment when the TCX scope is granted.

Normalised CSV or JSONL exports cover every Data Type the catalog supports. Rollups widen to `hourly`, `weekly`, or `window=<dur>`. The [README](https://github.com/BramVR/gohealthcli#readme) is the canonical catalog and export list — drift-guarded against the binary — and the [commands reference](commands.html) covers every verb and flag.

## Where to start

- **Install** — pick a path that works today: Homebrew, `go install`, or source build.
- **Quickstart** — walk through OAuth setup and your first sync.
- **Reference** — every subcommand and flag at a stable URL.

## What it isn't

`gohealthcli` is not a cloud service. It does not run in the background, does not phone home, does not upload your data, and does not write back to the provider. The archive sits on disk in a file you can move, back up, or delete.

## Project

`gohealthcli` is open source and in active development under [BramVR/gohealthcli](https://github.com/BramVR/gohealthcli). First Release is in progress: the command surface and storage shape are designed as durable foundations rather than a disposable MVP, because health data is sensitive and local archives are hard to rebuild casually.

**Live today**: Homebrew/Go install paths, the full command surface from `init` to `describe-schema`, the Tier 1 daily + hydration catalog slice, identity / device / settings / profile snapshots, sync run observability — a per-page heartbeat, a read-only `sync --status` view, and automatic fencing of abandoned runs — stable `--plain` and `--json` read contracts for scripted and LLM consumers, and CI running gofmt, build, tests, and docs drift checks on every pull request and push to `main`.

**Release path:** tagged releases publish GitHub Release archives and update `BramVR/homebrew-tap`, so `brew install BramVR/tap/gohealthcli` tracks the latest release.

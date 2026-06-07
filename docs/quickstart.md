---
title: Quickstart
description: Walk from a fresh install through OAuth setup, your first connect, and your first sync of Google Health Data Points into a local Health Archive.
---

This page walks from a fresh install to your first sync. Plan for fifteen minutes — most of that is the one-time Google Cloud OAuth setup, which you only do once per Google account.

If you have not installed yet, start with the [Install](install.html) page.

## What you will set up

- A Google Cloud project with the Google Health API enabled
- An OAuth client (Desktop app) bound to your Google account
- A local Health Archive that will store the raw provider JSON
- A Connection that anchors one Google Identity to the archive

## Google Cloud and OAuth

`gohealthcli` uses the Google Health API, which requires a Google Cloud project under your control. Anyone with a Google account can create one for free.

In the Google Cloud console:

1. Create or pick a project.
2. Enable the **Google Health API**.
3. Configure the **Google Auth Platform** branding, audience, and data access.
4. While the app is unverified, keep it in **Testing** and add your own Google account as a test user.
5. Add these Data Access scopes (all read-only):
   - `https://www.googleapis.com/auth/googlehealth.profile.readonly`
   - `https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly`
   - `https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly`
   - `https://www.googleapis.com/auth/googlehealth.sleep.readonly`
6. Create an OAuth client with application type **Desktop app**.
7. Download the client JSON.

`gohealthcli` rejects Web application client JSON — it uses an installed-app localhost callback flow.

Keep the downloaded file owner-only:

```bash
chmod 600 ~/Downloads/client_secret_*.json
```

## Initialise the archive

Point `init` at the client JSON. This writes the config, creates an empty Health Archive, and runs the initial schema migration.

```bash
gohealthcli init --oauth-client-file ~/Downloads/client_secret_*.json
gohealthcli doctor --plain
```

`doctor --plain` reports the local state. Expect a green status for config, archive, and credential store at this point — there is no Connection yet, which is the next step.

## Connect your Google identity

`connect` opens your browser, runs the OAuth flow against the client JSON you provided, and stores the resulting tokens in the OS-native Credential Store. Verify the Connection is healthy and that token refresh works against the live API.

```bash
gohealthcli connect --plain
gohealthcli doctor --online --plain
gohealthcli identity --plain
```

`doctor --online` checks token refresh and Google Health reachability. `identity` prints the Google Identity attached to the Connection. If `doctor --online` reports a token refresh failure, run `connect` again — the most common cause is that the OAuth consent screen needed your Google account added as a test user.

## Optional: archive a profile snapshot

A Profile Snapshot is a single, point-in-time copy of the upstream profile blob. Useful as a baseline before you sync Data Points.

```bash
gohealthcli profile --plain
```

## Your first sync

Always start with a small range — one or two days. This confirms the sync path end-to-end without committing to a long run.

```bash
gohealthcli sync --types steps --from 2026-01-01 --to 2026-01-02 --plain
gohealthcli status --plain
```

`status` prints archive counts and the newest synced timestamp per Data Type. Once you have a successful single-day sync, broaden the range or add more Data Types.

```bash
gohealthcli sync --types heart-rate,sleep --from 2026-01-01 --to 2026-01-07 --plain
```

## Daily Rollups and wearable-only filtering

Daily Rollups summarise raw Data Points over a day — useful for time-series charting without re-aggregating in your own code. Wearable-only filtering returns Data Points whose Data Source is a watch or tracker.

```bash
gohealthcli sync --types steps --rollup daily --from 2026-01-01 --to 2026-01-31 --plain
gohealthcli sync --types heart-rate --source-family wearable --from 2026-01-01 --to 2026-01-02 --plain
```

## Read your archive

The archive is a SQLite file. `query` runs guarded read-only SQL.

```bash
gohealthcli query --plain 'SELECT data_type, COUNT(*) FROM data_points GROUP BY data_type'
```

Command flags must appear **before** the SQL argument — Go's `flag` parser stops at the first positional argument.

`export` writes normalised datasets to CSV or JSONL.

```bash
gohealthcli export daily-steps --format jsonl --stdout
gohealthcli export daily-steps --format csv --output steps.csv
```

`raw` prints upstream provider JSON for endpoint exploration without writing to the archive.

```bash
gohealthcli raw endpoint getIdentity
gohealthcli raw data-type steps --from 2026-01-01 --to 2026-01-02
```

## Where next

- `gohealthcli <command> --help` prints flags and a usage summary for any subcommand.
- `gohealthcli doctor --plain` is the fastest sanity check whenever something feels off.
- A per-subcommand reference section will appear in the sidebar once the auto-generated command reference ships.

The archive is yours. Move it, back it up, query it — `gohealthcli` will only write when you ask.

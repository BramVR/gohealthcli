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

   Optionally add these too if you plan to use `connect --add-scopes`:
   - `https://www.googleapis.com/auth/googlehealth.irn.readonly` — needed by `gohealthcli irn-profile`.
   - `https://www.googleapis.com/auth/googlehealth.electrocardiogram.readonly` — needed by Tier 2 ECG Data Types.
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

## How long will a sync take?

Cursor-resumed incremental syncs (no `--from`) finish in seconds. An explicit backfill window costs time in proportion to how many Data Points it covers, and that depends on the Data Type's density. Sustained throughput measures roughly 2,000–5,000 Data Points per minute on real runs; the table plans with the conservative ~2,000/min, using densities measured 2026-06-10 from a real watch-backed archive (continuous heart-rate sampling):

| Data Type | Density (points/day) | Two weeks ≈ | Sync time ≈ |
| --- | --- | --- | --- |
| `heart-rate` | ~27,500 | ~385,000 pts | 1.5–3 h, in 2–3-day runs |
| `time-in-heart-rate-zone` | ~960 | ~13,400 pts | ~5 min |
| `active-energy-burned` | ~630 | ~8,800 pts | ~4 min |
| `oxygen-saturation` | ~480 | ~6,700 pts | ~3 min |
| `steps` | ~260 | ~3,600 pts | ~2 min |
| `sleep`, `daily-*` types | ~1 | ~14 pts | seconds |

Two caveats. Density is account-specific — a phone-only account without a continuously-sampling wearable runs far lower across the board. And a single run's OAuth token lives about an hour and is only fetched at run start, so chunk dense backfills to 2–3 days of heart-rate per `--from`/`--to` run; `--all` is safe in aggregate because every per-Data-Type run gets a fresh token.

The full per-type table — every measured Data Type, not just these anchors — is on the [Data Types page](data-types.html#how-long-does-each-type-take-to-sync).

Watch a long run from a second terminal while it is in flight:

```bash
gohealthcli sync --status
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

- `gohealthcli <command> --help` (or the equivalent verb `gohealthcli help
  <command>`) prints flags and a usage summary for any subcommand. A bare
  `gohealthcli` invocation prints the same top-level help to stdout and
  exits 0, so typing the binary name without arguments is always safe.
- `gohealthcli --version` prints a build-stamped identifier line, or pass
  `--version --json` for a single-line `{"version":..., "commit":..., "built":...}`
  envelope. See [commands/version.html](commands/version.html).
- A mistyped subcommand prints `unknown command: <typo>`, an optional
  Levenshtein-2 "Did you mean" hint, and a link to `--help`. The full
  contract is on the [help reference page](commands/help.html).
- `gohealthcli doctor --plain` is the fastest sanity check whenever
  something feels off.
- The per-subcommand reference is at [Command reference](commands.html).

The archive is yours. Move it, back it up, query it — `gohealthcli` will only write when you ask.

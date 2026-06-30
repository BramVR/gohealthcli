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

   Optionally add these too if you plan to use `connect --add-scopes`. Each scope below maps to one of the five `--add-scopes` keywords (`ecg`, `irn`, `nutrition`, `settings`, `tcx`); declaring it here now saves a return trip to Google Cloud later:
   - `https://www.googleapis.com/auth/googlehealth.electrocardiogram.readonly` (`ecg`) — needed by the Tier 2 `electrocardiogram` Data Type.
   - `https://www.googleapis.com/auth/googlehealth.irn.readonly` (`irn`) — needed by `gohealthcli irn-profile` and the Tier 2 `irregular-rhythm-notification` Data Type.
   - `https://www.googleapis.com/auth/googlehealth.nutrition.readonly` (`nutrition`) — needed by the `hydration-log` Data Type.
   - `https://www.googleapis.com/auth/googlehealth.settings.readonly` (`settings`) — needed by `gohealthcli settings` and `gohealthcli devices`.
   - `https://www.googleapis.com/auth/googlehealth.location.readonly` (`tcx`) — needed to archive TCX route files during exercise sync.
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

## Choose an Initial Backfill path

An Initial Backfill is the first historical range you archive for a Data Type or Rollup kind. For low-density Data Types, a raw Data Point Initial Backfill is usually fine. For continuous wearable heart-rate history, choose deliberately:

- Exact raw samples: use raw Data Points when you need every point-in-time BPM reading in `data_points` and the `heart_rate_samples` Normalized View.
- Fast summary history: use heart-rate Rollups when daily or hourly trends are enough. Rollups store separate records in `rollups`; they do not replace raw heart-rate Data Points or prove raw sample history is complete.

Raw heart-rate samples:

```bash
gohealthcli sync --types heart-rate --from 2026-01-01 --to 2026-01-15 --plain
```

Fast daily heart-rate summary history:

```bash
gohealthcli sync --types heart-rate --rollup daily --from 2026-01-01 --to 2026-03-01 --plain
```

Hourly summary history:

```bash
gohealthcli sync --types heart-rate --rollup hourly --from 2026-01-01 --to 2026-01-15 --plain
```

Each raw or Rollup path has its own Sync Cursor key. A successful daily Rollup Sync Run advances the `heart-rate` daily Rollup cursor, not the raw `heart-rate` cursor; a later raw Data Point sync still needs its own successful Initial Backfill before cursor-resumed raw syncs can omit `--from`.

## How long will a sync take?

Cursor-resumed incremental syncs (no `--from`) finish in seconds. An explicit Initial Backfill window costs time in proportion to how many Data Points it covers, and that depends on the Data Type's density. `sync` uses the largest safe raw Data Point page size automatically — `pageSize=10000` for Data Types such as `heart-rate` and `steps`, and the provider's smaller `pageSize=25` cap for `sleep` and `exercise` — but page size only reduces provider round-trips. It does not reduce the number of raw Data Points that must be parsed and archived. Sustained throughput measures roughly 2,000–5,000 Data Points per minute on real runs; the table plans with the conservative ~2,000/min, using densities measured 2026-06-10 from a real archive backed by a Pixel Watch 4 (continuous heart-rate sampling). A Data Point is the upstream record unit, which is why the counts differ so wildly per type: a heart-rate point is a single reading (every ~3 seconds on the watch), a steps point is a one-minute bucket, and a sleep point is an entire night with its stage breakdown.

| Data Type | Density (points/day) | Two weeks ≈ | Sync time ≈ |
| --- | --- | --- | --- |
| `heart-rate` | ~27,500 | ~385,000 pts | 1.5–3 h |
| `time-in-heart-rate-zone` | ~960 | ~13,400 pts | ~5 min |
| `active-energy-burned` | ~630 | ~8,800 pts | ~4 min |
| `oxygen-saturation` | ~480 | ~6,700 pts | ~3 min |
| `steps` | ~260 | ~3,600 pts | ~2 min |
| `sleep`, `daily-*` types | ~1 | ~14 pts | seconds |

One caveat: density is account-specific — a phone-only account without a continuously-sampling wearable runs far lower across the board. Long backfills are safe to run in one go: when a run outlives its OAuth access token (about an hour), the token is refreshed mid-run and the failed page retried automatically in the standard `init --oauth-client-file` setup.

The full per-type table — every measured Data Type, not just these anchors — is on the [Data Types page](data-types.html#how-long-does-each-type-take-to-sync).

Watch a long run from a second terminal while it is in flight:

```bash
gohealthcli sync --status
```

## Daily Rollups and wearable-only filtering

Daily Rollups summarise raw Data Points over a day — useful for time-series charting without re-aggregating in your own code. They write Rollups and carry Rollup Sync Cursors, separate from raw Data Points and raw Sync Cursors. Wearable-only filtering returns Data Points whose Data Source is a watch or tracker.

```bash
gohealthcli sync --types steps --rollup daily --from 2026-01-01 --to 2026-01-31 --plain
gohealthcli sync --types heart-rate --rollup daily --from 2026-01-01 --to 2026-01-31 --plain
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

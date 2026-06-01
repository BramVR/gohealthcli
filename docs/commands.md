---
summary: "Planned CLI commands, global flags, and scriptable output behavior."
read_when:
  - "Adding or changing CLI commands or flags."
  - "Checking scriptable output expectations."
  - "Comparing behavior with gobankcli."
---
# Commands

## Global Flags

- `--config PATH`: config file path.
- `--db PATH`: SQLite archive path.
- `--json`: stable JSON on stdout.
- `--plain`: simple key/value output on stdout.
- `--no-input`: never prompt, never wait for browser input.
- `--version`: print version and exit.

Human hints and warnings go to stderr. Machine-readable data goes to stdout.

## Planned Commands

`init`: create owner-only config and local data directories, write the default
Data Types, record either an explicit OAuth client file reference or exact
Secret Provider reference, and create an empty Health Archive with the first
schema migration. Re-running `init` against an existing complete setup reports
`already_initialized` without rewriting local files.

`doctor`: check config, archive, OAuth client, Credential Store, token presence,
and token expiry shape. `doctor --online` may refresh tokens and check Google
Health reachability. Default `doctor` is offline-only: it validates local file
permissions, config references, the read-only Health Archive schema, and stored
Connection token metadata without opening a browser or calling a Provider.

Before `init`, `doctor` reports `setup_missing` and exits 2. Invalid or partial
setup reports `setup_invalid` and exits 1. Machine-readable `--json` and
`--plain` output goes to stdout; the setup hint goes to stderr. Valid setup
reports OAuth client source kind, Credential Store kind, schema version,
Connection count, and token metadata status without printing token values or
OAuth client secrets.

`connect`: run OAuth browser flow and create a Connection. It consumes resolved
OAuth client config, does not search Secret Providers, stores runtime token
material in the configured Credential Store, and immediately archives Google
Identity metadata. Request recognized readonly Google Health scopes; later
`sync` surfaces may require reconnect as Google exposes narrower scopes.
Re-authorizing the same Google Identity updates token metadata in place; a
different Google Identity requires a separate Health Archive.

`identity`: call Google Health API identity endpoint, print current identity
metadata, and refresh the archived Google Identity.

`profile`: show profile/settings information exposed by the provider and
archive raw profile/settings JSON separately from Data Points and Rollups.

`sync`: fetch Data Points or Rollups for selected or configured Data Types and
date ranges. By default, sync raw Data Points from all Data Sources exposed by
the Provider. Fetch Rollups only when `--rollup` is provided. Sync is idempotent
and reports seen, new, and updated counts. If required scopes are missing, fail
with a clear re-connect instruction instead of starting browser consent. Require
`--from`; `--to` defaults to now when omitted. Date-only inputs are civil dates
in the user's current local timezone unless a timezone is supplied.

`status`: show archive counts, known Data Types, newest archived timestamps,
latest successful Sync Run, and latest failed Sync Run with a short error
summary. Do not infer completeness gaps unless gap tracking exists.

`query`: run read-only SQL against the local Health Archive. Open the archive
read-only, reject non-SELECT statements and mutating pragmas, and return
machine-readable stdout.

`export`: export named normalized records as CSV or JSONL. Arbitrary SQL stays
in `query`. Require either `--output PATH` or explicit `--stdout`.

`raw`: fetch one provider endpoint or Data Type convenience path and print raw
provider JSON for API exploration. Endpoint mode may use provider names
directly. `raw endpoint getIdentity` calls the identity endpoint. `raw endpoint
dataTypes.steps.list --from 2026-01-01` and `raw data-type steps --from
2026-01-01` call the Data Type list path. `raw` does not archive responses by
default.

## Init Sketch

```bash
gohealthcli init --oauth-client-file client_secret.json
gohealthcli init --secret-provider 1password --oauth-client-item "Google Health OAuth"
```

## Sync Sketch

```bash
gohealthcli sync --types steps,heart-rate,sleep,exercise --from 2026-01-01
gohealthcli sync --types daily-resting-heart-rate,daily-oxygen-saturation --from 2026-01-01
gohealthcli sync --types steps --rollup daily --from 2026-01-01 --to 2026-05-24
gohealthcli sync --types steps --source-family wearable --from 2026-01-01
```

## Export Sketch

```bash
gohealthcli export daily-steps --format csv --output steps.csv
gohealthcli export sleep-sessions --format jsonl --output sleep.jsonl
gohealthcli export daily-steps --format jsonl --stdout
```

## Raw Sketch

```bash
gohealthcli raw endpoint getIdentity
gohealthcli raw endpoint dataTypes.steps.list --from 2026-01-01
gohealthcli raw data-type steps --from 2026-01-01
```

## Output Sketch

Plain mode:

```text
connection_id: googlehealth:111111256096816351
data_types: 4
data_points_seen: 12043
data_points_new: 11880
data_points_updated: 12
rollups_seen: 144
```

JSON mode:

```json
{
  "connection_id": "googlehealth:111111256096816351",
  "data_types": 4,
  "data_points_seen": 12043,
  "data_points_new": 11880,
  "data_points_updated": 12,
  "rollups_seen": 144
}
```

Connect plain mode:

```text
status: connected
connection_id: googlehealth:111111256096816351
provider_name: googlehealth
google_health_user_id: 111111256096816351
legacy_fitbit_user_id: A1B2C3
credential_store: file
token_status: metadata_present
message: Google Identity connected
```

Identity plain mode:

```text
status: identity_refreshed
connection_id: googlehealth:111111256096816351
provider_name: googlehealth
google_health_user_id: 111111256096816351
legacy_fitbit_user_id: A1B2C3
message: Google Identity refreshed
```

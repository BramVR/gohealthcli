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

With `--online`, `doctor` requires a current Connection, validates token refresh,
calls Google Health `getIdentity` with the refreshed access token, and reports
Connection health failures as `connection_unhealthy` instead of archive setup
failures.

`connect`: run OAuth browser flow and create a Connection. It consumes resolved
OAuth client config, does not search Secret Providers, stores runtime token
material in the configured Credential Store, and immediately archives Google
Identity metadata. Request recognized readonly Google Health scopes; later
`sync` surfaces may require reconnect as Google exposes narrower scopes.
Re-authorizing the same Google Identity updates token metadata in place; a
different Google Identity requires a separate Health Archive.

`identity`: call Google Health API identity endpoint, print current identity
metadata, and refresh the archived Google Identity.

`profile`: call the provider profile endpoint for the current Connection,
archive the raw Profile Snapshot with `fetched_at`, and print stable summary
fields including `snapshot_id`. Profile Snapshots stay separate from Data Points
and Rollups.

`sync`: currently archives raw `steps` Data Points from the provider list path,
steps Data Points from the reconcile path when `--source-family wearable` is
explicit, or steps daily Rollups when `--rollup daily` is explicit. Default sync
fetches Data Points from all Data Sources and never calls Rollup endpoints.
Sync is idempotent and reports Data Point and Rollup seen, new, and updated
counts separately. If required scopes are missing, fail with a clear re-connect
instruction instead of starting browser consent. Require `--from`; Data Point
sync `--to` defaults to current UTC time when omitted, while daily Rollup sync
`--to` defaults to the current civil date and accepts date-only `YYYY-MM-DD`
ranges.
Preserve physical UTC interval times when available, provider civil time
metadata, Data Source JSON, source-family filter, and raw provider JSON.
Corrected upstream raw Data Point JSON updates the canonical Data Point for the
same source-family filter and stores the previous raw JSON as a Data Point
Revision; corrected Rollup JSON updates the Rollup in place.

`status`: show archive counts, known Data Types, newest archived timestamps,
latest successful Sync Run, and latest failed Sync Run with a short error
summary. Do not infer completeness gaps unless gap tracking exists.

`query`: run read-only SQL against the local Health Archive. Open the archive
read-only, reject non-SELECT statements and mutating pragmas, and return
machine-readable stdout.

`export`: export named normalized records as CSV or JSONL. Arbitrary SQL stays
in `query`. Require either `--output PATH` or explicit `--stdout`.

`raw`: fetch one provider endpoint or Data Type convenience path and print raw
provider JSON for API exploration. Endpoint mode accepts supported provider
endpoint names. `raw endpoint getIdentity` calls the identity endpoint. `raw
endpoint dataTypes.steps.list --from 2026-01-01` and `raw data-type steps
--from 2026-01-01` call the Data Type list path. `raw` does not archive
responses by default.

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
gohealthcli query 'SELECT data_type, end_time_utc FROM data_points ORDER BY end_time_utc DESC LIMIT 10'
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
status: sync_completed
sync_run_id: 1
connection_id: googlehealth:111111256096816351
provider_name: googlehealth
data_types: steps
from: 2026-01-01
to: 2026-01-02T00:00:00Z
endpoint_family: list
data_points_seen: 12043
data_points_new: 11880
data_points_updated: 12
rollups_seen: 0
rollups_new: 0
rollups_updated: 0
message: Sync Run archived steps Data Points
```

JSON mode:

```json
{
  "status": "sync_completed",
  "sync_run_id": 1,
  "connection_id": "googlehealth:111111256096816351",
  "provider_name": "googlehealth",
  "data_types": ["steps"],
  "from": "2026-01-01",
  "to": "2026-01-02T00:00:00Z",
  "endpoint_family": "list",
  "data_points_seen": 12043,
  "data_points_new": 11880,
  "data_points_updated": 12,
  "rollups_seen": 0,
  "rollups_new": 0,
  "rollups_updated": 0,
  "message": "Sync Run archived steps Data Points"
}
```

Status plain mode:

```text
status: ok
archive_path: /path/to/gohealthcli.sqlite
schema_version: 3
data_point_count: 12043
rollup_count: 2
profile_snapshot_count: 1
sync_run_count: 4
known_data_types: steps
data_type.steps.data_point_count: 12043
data_type.steps.rollup_count: 2
data_type.steps.newest_data_point_timestamp: 2026-01-02T00:00:00Z
latest_successful_sync_run_id: 4
latest_successful_sync_run_status: sync_completed
message: Health Archive status summarized
```

Query plain mode:

```text
status: query_completed
archive_path: /path/to/gohealthcli.sqlite
columns: data_type,end_time_utc
row_count: 1
row.1.1: steps
row.1.2: 2026-01-02T00:00:00Z
message: Query completed
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

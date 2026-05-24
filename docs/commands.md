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

`init`: create config and local directories.

`doctor`: check config, archive, OAuth client, token presence, token expiry, and
provider reachability.

`connect`: run OAuth browser flow and create a Connection.

`identity`: call Google Health API identity endpoint and archive Google Identity.

`profile`: show profile/settings information exposed by the provider.

`sync`: fetch Data Points or Rollups for selected Data Types and date ranges.

`status`: show archive counts, latest Sync Runs, known Data Types, and newest
archived timestamps.

`query`: run read-only SQL against the local Health Archive.

`export`: export normalized records as CSV or JSONL.

`raw`: fetch one provider endpoint and print raw JSON for API exploration.

## Sync Sketch

```bash
gohealthcli sync --types steps,heart-rate,sleep,exercise --from 2026-01-01
gohealthcli sync --types daily-resting-heart-rate,daily-oxygen-saturation --from 2026-01-01
gohealthcli sync --types steps --rollup daily --from 2026-01-01 --to 2026-05-24
```

## Output Sketch

Plain mode:

```text
connection_id: googlehealth:111111256096816351
data_types: 4
data_points_seen: 12043
data_points_new: 11880
rollups_seen: 144
```

JSON mode:

```json
{
  "connection_id": "googlehealth:111111256096816351",
  "data_types": 4,
  "data_points_seen": 12043,
  "data_points_new": 11880,
  "rollups_seen": 144
}
```

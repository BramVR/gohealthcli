<p align="center">
  <img src="./assets/readme-header.jpg" alt="gohealthcli local-first Google Health archive CLI">
</p>

# gohealthcli

[![Go Reference](https://pkg.go.dev/badge/github.com/BramVR/gohealthcli.svg)](https://pkg.go.dev/github.com/BramVR/gohealthcli)
[![Go Report Card](https://goreportcard.com/badge/github.com/BramVR/gohealthcli)](https://goreportcard.com/report/github.com/BramVR/gohealthcli)
![Go version](https://img.shields.io/github/go-mod/go-version/BramVR/gohealthcli)
[![GitHub repository](https://img.shields.io/badge/GitHub-BramVR%2Fgohealthcli-24292f?logo=github)](https://github.com/BramVR/gohealthcli)

Local-first, read-only Google Health archive CLI.

`gohealthcli` connects to the Google Health API, stores raw provider JSON in a
local SQLite archive, and provides scriptable commands for sync, status, query,
raw API exploration, and CSV/JSONL exports.

It is for local inspection and personal data archiving. It does not write health
data, delete health data, run a server, upload archives, or share exports.

## Status

First CLI tracer in progress. Implemented commands:

- `init`: create config and an empty Health Archive.
- `doctor`: validate local setup; `doctor --online` verifies token refresh and
  Google Health reachability.
- `connect`: run browser OAuth and anchor one Google Health identity.
- `identity`: refresh archived Google Health identity metadata.
- `profile`: archive a Google Health profile snapshot.
- `sync`: archive Google Health Data Points and supported Rollups.
- `status`: summarize archive counts and newest synced timestamps.
- `query`: run guarded read-only SQL over the archive.
- `export`: write normalized CSV or JSONL datasets.
- `raw`: print provider JSON for endpoint exploration.

Supported Data Point sync types:

- `steps`
- `heart-rate`
- `heart-rate-variability`
- `oxygen-saturation`
- `daily-resting-heart-rate`
- `daily-heart-rate-variability`
- `daily-oxygen-saturation`
- `daily-respiratory-rate`
- `sleep`
- `exercise`
- `distance`
- `weight`

`sync --source-family wearable` is available for Data Types backed by the
Google Health reconcile path. `sync --types steps --rollup daily` archives
steps daily Rollups. `total-calories` is known to the catalog but is not
supported by raw Data Point sync because Google exposes it as Rollup data.

Normalized export datasets (grouped by domain; `gohealthcli export` accepts
any of these names as its positional argument):

- Activity and steps: `daily-steps`, `active-minutes-intervals`,
  `active-zone-minutes-intervals`, `activity-level-intervals`,
  `sedentary-period-intervals`, `floors-intervals`, `altitude-intervals`.
- Heart rate: `heart-rate-samples`, `resting-heart-rate-by-day`,
  `daily-heart-rate-zones`, `time-in-heart-rate-zone-intervals`.
- Heart rhythm (Tier 2): `electrocardiogram-sessions`,
  `irregular-rhythm-notifications`, `current-irn-profile`.
- Sleep: `sleep-sessions`, `sleep-stages`, `respiratory-rate-sleep-summary`,
  `daily-sleep-temperature-derivations`.
- Exercise: `exercise-sessions`, `exercise-splits`,
  `swim-lengths-data-intervals`.
- Body measurements: `weight-samples`, `body-fat-samples`, `height-samples`,
  `current-height`.
- VO2 max: `vo2-max-samples`, `run-vo2-max-samples`, `daily-vo2-max`.
- Other biomarkers: `blood-glucose-samples`, `core-body-temperature-samples`.
- Device and account metadata: `paired-devices`, `current-settings`,
  `searchable-text`.

The drift guard in `cmd/gohealthcli/export_test.go`
(`TestREADMEListsEveryExportDataset`) fails if a dataset is added to the
`exportDatasetDefinitions` registry without a matching entry here.

## Install

From source:

```bash
go install github.com/BramVR/gohealthcli/cmd/gohealthcli@latest
gohealthcli --version
```

For local development:

```bash
git clone https://github.com/BramVR/gohealthcli.git
cd gohealthcli
go test ./...
go run ./cmd/gohealthcli --help
```

## Google Auth Setup

Google Health API access requires a Google Cloud project and OAuth setup.

In Google Cloud:

- Enable the Google Health API.
- Configure Google Auth Platform branding, audience, and data access.
- While unverified, keep the app in Testing and add your Google account as a
  test user.
- Add these Data Access scopes:
  - `https://www.googleapis.com/auth/googlehealth.profile.readonly`
  - `https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly`
  - `https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly`
  - `https://www.googleapis.com/auth/googlehealth.sleep.readonly`
- Create an OAuth client with application type `Desktop app`.
- Download the client JSON.

Do not use a Web application client. `gohealthcli` uses an installed-app
localhost callback flow and rejects web-client JSON.

Keep the downloaded OAuth client JSON owner-only:

```bash
chmod 600 ~/Downloads/client_secret_*.json
```

## Quick Start

Initialize local config and archive:

```bash
gohealthcli init --oauth-client-file ~/Downloads/client_secret_*.json
gohealthcli doctor --plain
```

Connect in the browser and verify the connection:

```bash
gohealthcli connect --plain
gohealthcli doctor --online --plain
gohealthcli identity --plain
gohealthcli profile --plain
```

Sync a small window first:

```bash
gohealthcli sync --types steps --from 2026-01-01 --to 2026-01-02 --plain
gohealthcli status --plain
```

Archive daily step Rollups or wearable-filtered Data Points when needed:

```bash
gohealthcli sync --types steps --rollup daily --from 2026-01-01 --to 2026-01-31 --plain
gohealthcli sync --types heart-rate --source-family wearable --from 2026-01-01 --to 2026-01-02 --plain
```

Export normalized daily steps:

```bash
gohealthcli export daily-steps --format jsonl --stdout
gohealthcli export daily-steps --format csv --output steps.csv
```

Explore raw provider JSON:

```bash
gohealthcli raw endpoint getIdentity
gohealthcli raw data-type steps --from 2026-01-01 --to 2026-01-02
```

Query the local archive:

```bash
gohealthcli query --plain 'SELECT data_type, COUNT(*) FROM data_points GROUP BY data_type'
```

Command flags must appear before the SQL argument because Go flag parsing stops
at the first positional argument.

Use `gohealthcli <command> --help` for command-specific flags.

## Configuration

Default local paths:

- config: `~/.config/gohealthcli/config.toml`
- archive: `~/.local/share/gohealthcli/gohealthcli.sqlite`
- file Credential Store fallback: `~/.config/gohealthcli/tokens.json`

Default runtime token storage is OS-native:

- macOS: Keychain
- Windows: Windows Credential Manager
- Linux: Secret Service/libsecret

For local testing, the explicit file Credential Store is acceptable if it stays
owner-only:

```toml
[credential_store]
type = "file"
path = "/absolute/path/to/gohealthcli/tokens.json"
```

Use `doctor --plain` to check local setup without provider calls. Use
`doctor --online --plain` only when you want token refresh and Google Health
reachability checks.

## Safety

- Read-only provider behavior: no health writes or deletes.
- Local-first archive: no cloud service and no background upload.
- OAuth token values are not printed in normal command output.
- Exports can reveal health history; commands require explicit `--stdout` or
  `--output`.
- Keep the SQLite archive, token files, and exported CSV/JSONL files private.

## Docs

- [CONTEXT.md](./CONTEXT.md): project glossary only, used by grill-style review.
- [docs/google-auth-setup.md](./docs/google-auth-setup.md): local Google
  Health OAuth setup checklist.
- [docs/commands.md](./docs/commands.md): CLI surface and output behavior.
- [docs/data-model.md](./docs/data-model.md): archive model sketch.
- [docs/security.md](./docs/security.md): local credentials and health data safety.
- [docs/research.md](./docs/research.md): source-backed Google Health API notes.
- [docs/plan.md](./docs/plan.md): product and implementation plan.
- [docs/adr/](./docs/adr): short architectural decision records.

<p align="center">
  <img src="./assets/readme-header.jpg" alt="gohealthcli local-first Google Health archive CLI">
</p>

# gohealthcli

[![CI](https://github.com/BramVR/gohealthcli/actions/workflows/ci.yml/badge.svg)](https://github.com/BramVR/gohealthcli/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/BramVR/gohealthcli.svg)](https://pkg.go.dev/github.com/BramVR/gohealthcli)
[![Go Report Card](https://goreportcard.com/badge/github.com/BramVR/gohealthcli)](https://goreportcard.com/report/github.com/BramVR/gohealthcli)
![Go version](https://img.shields.io/github/go-mod/go-version/BramVR/gohealthcli)
[![GitHub repository](https://img.shields.io/badge/GitHub-BramVR%2Fgohealthcli-24292f?logo=github)](https://github.com/BramVR/gohealthcli)
[![Project Site](https://img.shields.io/badge/Project%20Site-gohealthcli.bramvanrompuy.be-0b7285)](https://gohealthcli.bramvanrompuy.be)

Local-first, read-only Google Health CLI — archive your Google Health data locally.

`gohealthcli` connects to the Google Health API, stores raw provider JSON in a
local SQLite archive, and provides scriptable commands for sync, status, query,
raw API exploration, and CSV/JSONL exports.

It is for local inspection and personal data archiving. It does not write health
data, delete health data, run a server, upload in the background, or share
plaintext archives or exports. Explicit `backup push` encrypts Health Archive
payloads with age before Git sees them.

## Status

The full command surface is live: setup (`init`, `doctor`), Provider catalog
browsing and verification (`catalog list`, `catalog scopes`, `catalog verify`), OAuth and
identity snapshots (`connect` through `irn-profile`), archiving (`sync`,
with heartbeat-backed `sync --status` observability and auto-fencing of
abandoned runs), encrypted backup setup/push/pull/status (`backup init`, `backup push`,
`backup pull`, `backup status`),
raw provider exploration (`raw`), and a stable read
surface (`status`,
`query`, `export`, `describe-schema`) with predictable `--plain` /
`--json` contracts for scripted and LLM consumers (PRD #144). The Tier 1 daily + hydration catalog slice is
sealed, and CI runs gofmt, golangci-lint (`make lint`), build, tests, and the
command-reference drift
guard (`make docs-check`) on every pull request and push to `main`. The Command Registry in
`cmd/gohealthcli/commands.go` is the single source of truth for the user-facing
surface; the list below mirrors each entry's `Short` description and stays in
sync with `gohealthcli --help`.

- `init`: Create local config and an empty Health Archive.
- `doctor`: Validate local setup and provider reachability.
- `catalog`: Browse and verify the compiled Provider catalog.
- `connect`: Run the browser OAuth flow and anchor one Google Identity.
- `identity`: Refresh the archived Google Identity metadata.
- `profile`: Archive a Profile Snapshot from the provider.
- `settings`: Archive a Settings Snapshot from the provider.
- `devices`: Archive a Paired Devices Snapshot from the provider.
- `irn-profile`: Archive an IRN Profile Snapshot from the provider.
- `sync`: Archive Google Health Data Points and supported Rollups.
- `backup`: Initialize, push, pull, and inspect encrypted Health Archive backups.
- `status`: Summarise archive counts and newest synced timestamps.
- `query`: Run guarded read-only SQL over the Health Archive.
- `export`: Write a normalised dataset to CSV or JSONL.
- `raw`: Print raw provider JSON for endpoint exploration.
- `describe-schema`: Self-describe the Health Archive for LLM consumption.
- `completion`: Generate a shell completion script.

The discoverability verbs added by PRD #143 cover the rest of the surface:

- `gohealthcli` with no arguments prints the same Subcommands block as
  `gohealthcli --help` to stdout and exits 0 — the binary never errors on a
  bare invocation.
- `gohealthcli help` and `gohealthcli help <command>` are alias verbs for
  `--help` / `<command> --help`, prepending the registry's long-form prose to
  the flag block on stderr.
- `gohealthcli --version` and `gohealthcli --version --json` print the
  build-stamped `version`, `commit`, and `built` identifiers; see
  [docs/commands/version.md](./docs/commands/version.md) for the shape.
- An unknown command prints `unknown command: <typo>` on stderr, a
  Levenshtein-2 "Did you mean" hint (at most two suggestions), and the
  canonical `Run 'gohealthcli --help' for a list of commands.` discovery
  line — see [docs/commands/help.md](./docs/commands/help.md).

Supported Data Point sync types (grouped by domain):

- Activity and fitness: `steps`, `distance`, `floors`, `altitude`,
  `active-energy-burned`, `basal-energy-burned`, `active-minutes`,
  `active-zone-minutes`,
  `activity-level`, `sedentary-period`, `time-in-heart-rate-zone`,
  `vo2-max`, `run-vo2-max`, `daily-vo2-max`, `swim-lengths-data`.
- Heart rate: `heart-rate`, `heart-rate-variability`,
  `daily-resting-heart-rate`, `daily-heart-rate-variability`,
  `daily-heart-rate-zones`.
- Heart rhythm (Tier 2 opt-in scopes): `electrocardiogram`,
  `irregular-rhythm-notification`.
- Sleep and respiration: `sleep`, `oxygen-saturation`,
  `daily-oxygen-saturation`, `daily-respiratory-rate`,
  `respiratory-rate-sleep-summary`, `daily-sleep-temperature-derivations`.
- Exercise: `exercise`.
- Body measurements: `weight`, `body-fat`, `height`.
- Other biomarkers: `blood-glucose`, `core-body-temperature`.
- Nutrition (opt-in `nutrition.readonly` scope): `hydration-log`,
  `nutrition-log`.

`sync --source-family wearable` is available for Data Types backed by the
Google Health reconcile path. `sync --types steps --rollup daily` archives
steps daily Rollups, and `sync --types heart-rate --rollup daily` archives
daily heart-rate summary Rollups without replacing raw heart-rate samples.
`sync --types total-calories --rollup daily` and physical modes such as
`--rollup hourly` archive provider-computed calorie totals; export them through
`total-calories-rollups`.
Use `sync --plan` to inspect one operation per requested Data Type. In planning
mode, `--types` may be repeated and preserves requested order; `--all` uses
catalog order.
Every operation resolves its own range and Sync Cursor, and a blocker stays
isolated to that Data Type. The plan reports endpoint, page policy, required
scopes, a sanitized request preview, conditional exercise TCX work, and
predicted sync effects. Planning uses only strict read-only local inspection:
it performs no Provider request, Credential Store read, token refresh, archive
write or migration, Sync Cursor advance, or Attachment sidecar creation. A
ready plan explicitly leaves credential availability, Google Identity
matching, and Provider reachability unverified.
`total-calories` is known to the catalog and supports daily and physical-window
Rollup sync, but raw Data Point sync remains rejected because Google exposes it
only as Rollup data;
`calories-in-heart-rate-zone` is also catalog-known but not yet implemented
because Google exposes it only through Rollup operations whose payload shape
is not pinned in gohealthcli yet.

With the Tier 2 `tcx` scope granted (`gohealthcli connect --add-scopes
tcx`), `exercise` sync also archives each session's TCX route file as a
`tcx`-kind Attachment under `<archive>.attachments/` (ADR-0009). Without
the scope, exercise Data Points still sync and the TCX step is skipped —
no failed provider call, no partial archive.

The drift guard in `internal/googlehealth/readme_sync_types_test.go`
(`TestREADMEListsEverySyncableDataType` and
`TestREADMECaveatListsCatalogTypesSyncRejects`) fails if a Data Type is
added to the Google Health catalog without a matching entry in the list
above or the caveat sentence.

For a plain-language description of each Data Type — what it captures,
the upstream record shape, required scope, and the normalized view it
projects into — see [docs/data-types.md](./docs/data-types.md).

Run `gohealthcli catalog list --json` for every compiled Data Type in catalog
order. Each row reports `selection` (`default` or `opt_in`),
`raw_data_points` (`supported` or `unsupported`), and the exact
`required_scopes`. Run `gohealthcli catalog scopes --json` to group those exact
scope URLs by Data Type membership. Both actions are offline and do not read
config, a Connection, the Credential Store, or a Health Archive.

Run `gohealthcli catalog verify` to compare this catalog with Google's live
public v4 discovery document without reading config, credentials, a Connection,
or a Health Archive. Use `--discovery PATH` for the committed offline fixture
or another saved discovery document. The stable status is `verified`,
`verified_with_known_gaps`, or `drift_detected`; unexpected drift exits
nonzero. See [the generated command reference](./docs/commands/catalog.md) for
the machine-readable gap and unverifiable-fact fields.

Normalized export datasets. `gohealthcli export` accepts any of the
names below as its positional argument. The list is auto-generated from
`exportDatasetCatalogSingleton.Names()` by `make docs-export-datasets`;
the markers around the block are stable so the regenerator can rewrite
just the bullets without touching the surrounding prose.

<!-- export-datasets:start -->
- `active-minutes-intervals`
- `active-zone-minutes-intervals`
- `activity-level-intervals`
- `altitude-intervals`
- `basal-energy-burned-intervals`
- `blood-glucose-samples`
- `body-fat-samples`
- `core-body-temperature-samples`
- `current-height`
- `current-irn-profile`
- `current-settings`
- `daily-heart-rate-zones`
- `daily-sleep-temperature-derivations`
- `daily-steps`
- `daily-vo2-max`
- `electrocardiogram-sessions`
- `exercise-sessions`
- `exercise-splits`
- `floors-intervals`
- `heart-rate-samples`
- `height-samples`
- `hydration-log-sessions`
- `irregular-rhythm-notifications`
- `nutrition-log-nutrients`
- `nutrition-log-sessions`
- `paired-devices`
- `respiratory-rate-sleep-summary`
- `resting-heart-rate-by-day`
- `run-vo2-max-samples`
- `searchable-text`
- `sedentary-period-intervals`
- `sleep-sessions`
- `sleep-stages`
- `swim-lengths-data-intervals`
- `time-in-heart-rate-zone-intervals`
- `total-calories-rollups`
- `vo2-max-samples`
- `weight-samples`
<!-- export-datasets:end -->

Nullable Nutrition Log summary columns render as empty CSV cells (including
`export --plain`) and as JSON `null` in JSONL. `nutrients_json` stays one JSON
array on the parent `nutrition-log-sessions` row. Use
`nutrition-log-nutrients` for one deterministic row per nutrient, linked to
the source session and food context; unknown nutrient enum values pass through.

The drift guard in `cmd/gohealthcli/docs_export_datasets_test.go`
(`TestREADMEExportDatasetsBlockMatchesCatalog`) fails if the committed
block does not match a fresh regeneration; the companion
`TestREADMEListsEveryExportDataset` keeps the wider section honest.

## Install

With Homebrew:

```bash
brew install BramVR/tap/gohealthcli
gohealthcli --version
```

With Go:

```bash
go install github.com/BramVR/gohealthcli/cmd/gohealthcli@latest
gohealthcli --version
```

Install the Agent Skill:

```bash
npx skills add BramVR/gohealthcli --skill gohealthcli
```

On Windows amd64, install the checksummed release ZIP with the
[PowerShell instructions](./docs/install.md#windows).

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

The four scopes above cover the default Tier 1 surface. The Tier 2
features — the `settings`, `devices`, and `irn-profile` commands, the
`electrocardiogram`, `irregular-rhythm-notification`, and
`hydration-log` and `nutrition-log` sync types, and TCX route archiving — each need an
extra opt-in scope granted with `gohealthcli connect --add-scopes`
(keywords: `ecg`, `irn`, `nutrition`, `settings`, `tcx`); without it
the provider returns HTTP 403. Add the matching optional scopes in
Google Cloud as well — the full scope-to-keyword table is in
[docs/google-auth-setup.md](./docs/google-auth-setup.md).

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

On a host without a local browser, start an expiring authorization and complete
it by supplying the browser's full redirected loopback URL over stdin:

```bash
gohealthcli connect --headless-start --plain
gohealthcli connect --complete --plain
```

The pending PKCE verifier and binding metadata live only in the configured
Credential Store and expire after ten minutes. Copy the entire redirected URL,
never only its authorization code; do not put either URL in shell history,
logs, screenshots, or shared artifacts. If `--add-scopes` is used, pass the
same value to both commands. Invalid, expired, or concurrent-loser attempts do
not replace the Connection or stored token material.

Sync a small window first:

```bash
gohealthcli sync --types steps --from 2026-01-01 --to 2026-01-02 --plain
gohealthcli status --plain
```

Watch a long sync from another terminal (or an agent) while it runs:

```bash
gohealthcli sync --status
gohealthcli sync --status --window 2h --json
```

`sync --status` reads the local `sync_runs` audit table — no provider calls.
It reports the stored resolved `from` / `to` boundaries for each run, plus the
resolution timezone and timestamp and any original `now` / `today` /
`yesterday` inputs recorded by newer builds. Historical rows expose their
existing resolved range without inventing missing metadata or recomputing it.
Every Sync Run heartbeats before each page fetch (counts so far plus
`last_progress_at`), so in-flight rows show live progress; finished runs are
listed inside the `--window` (default 15m, max 24h) while running rows never
age out of view. On entry, `sync`, `sync --status`, and `status` fence
abandoned runs: a `sync_running` row with no heartbeat for 5 minutes flips to
`sync_failed` with `error_summary='abandoned (no heartbeat for 5m)'`, and the
Sync Cursor stays put so the next run re-reads the same window.

How long does a sync take? Cursor-resumed incremental syncs finish in
seconds. Explicit backfills cost time in proportion to Data Point count —
sustained throughput measures roughly 2,000–5,000 Data Points/minute on
real runs (plan with ~2,000/min) — so the Data Type's density decides the
wall-clock. Densities measured 2026-06-10 from a real archive backed by a
Pixel Watch 4 (continuous heart-rate sampling), and what two weeks of
data costs. A Data Point is the upstream record unit, which is why the
counts differ so wildly per type: a heart-rate point is a single reading
(every ~3 seconds on the watch), a steps point is a one-minute bucket,
and a sleep point is an entire night with its stage breakdown.

| Data Type                 | Density (points/day) | Two weeks ≈  | Sync time ≈              |
| ------------------------- | -------------------- | ------------ | ------------------------ |
| `heart-rate`              | ~27,500              | ~385,000 pts | 1.5–3 h                  |
| `time-in-heart-rate-zone` | ~960                 | ~13,400 pts  | ~5 min                   |
| `active-energy-burned`    | ~630                 | ~8,800 pts   | ~4 min                   |
| `oxygen-saturation`       | ~480                 | ~6,700 pts   | ~3 min                   |
| `steps`                   | ~260                 | ~3,600 pts   | ~2 min                   |
| `sleep`, `daily-*` types  | ~1                   | ~14 pts      | seconds                  |

Density is account-specific — a phone-only account with no
continuously-sampling wearable runs far lower. Long runs survive OAuth
token expiry: a mid-run 401 triggers a single token refresh and a retry
of the failed page, so a multi-hour heart-rate backfill can run as one
`--from`/`--to` window in the standard `init --oauth-client-file` setup.
Watch long runs from another terminal with `sync --status`. The full
per-type table covering every measured Data Type is in
[docs/data-types.md](./docs/data-types.md).

Archive daily Rollups or wearable-filtered Data Points when needed:

```bash
gohealthcli sync --types steps --rollup daily --from 2026-01-01 --to 2026-01-31 --plain
gohealthcli sync --types heart-rate --rollup daily --from 2026-01-01 --to 2026-01-31 --plain
gohealthcli sync --types total-calories --rollup hourly --from 2026-01-01 --to 2026-01-15 --plain
gohealthcli sync --types heart-rate --source-family wearable --from 2026-01-01 --to 2026-01-02 --plain
```

Export normalized daily steps:

```bash
gohealthcli export daily-steps --format jsonl --stdout
gohealthcli export daily-steps --format csv --output steps.csv
gohealthcli export total-calories-rollups --format csv --output calories.csv
```

Explore raw provider JSON:

```bash
gohealthcli raw endpoint getIdentity
gohealthcli raw data-type steps --from yesterday --to today --timezone Europe/Brussels
```

Raw Data Type lists share `sync`'s exact range grammar and timezone precedence.
Identity endpoints reject range and timezone flags instead of ignoring them.

Query the local archive:

```bash
gohealthcli query --plain 'SELECT data_type, COUNT(*) FROM data_points GROUP BY data_type'
```

Command flags must appear before the SQL argument because Go flag parsing stops
at the first positional argument.

Use `gohealthcli <command> --help` or `gohealthcli help <command>` for
command-specific flags.

## Global flags

These flags apply to the top-level invocation and (where the subcommand
accepts them) to the per-subcommand parse. The shared set is the contract
captured by the Common Flag Set module in
[`cmd/gohealthcli/common_flags.go`](./cmd/gohealthcli/common_flags.go):

- `--config <path>`: config file path.
- `--db <path>`: SQLite Health Archive path.
- `--json`: write stable JSON to stdout.
- `--plain`: write plain key/value output to stdout.
- `--no-input`: never prompt, never wait for browser input.
- `--version`: print the build-stamped version line and exit (top level only).

`--plain` and `--json` are mutually exclusive — passing both exits non-zero
with a `flag_invalid` failure envelope ("`--plain and --json are mutually
exclusive`"). The check fires for `--version` too, so
`gohealthcli --plain --json --version` is rejected before any output is
written.

Failure Reporter JSON keeps the stable `status` and `message` fields and may
add `remediation`, an ordered array of at most three safe recovery commands.
Plain output emits the same steps as zero-based `remediation.N` fields. The
field or lines are absent when no structured remediation is available, and
default human failure output is unchanged. Remediation comes only from typed,
Reporter-owned recovery metadata; messages, Provider text, paths, identifiers,
health data, SQL, and user input are never parsed into steps.

Authentication commands use that same field on their result-specific JSON and
plain failure shapes. Actions are diagnosis-first: `doctor` precedes `init`,
`connect`, or a sorted allowlisted `connect --add-scopes ...` consent step;
rejected or unhealthy tokens use `doctor --online` before reconnecting; and an
identity mismatch points to `init --help` without inventing or exposing an
archive path. Constructing or rendering remediation never performs Provider I/O
or starts OAuth.

Sync and archive result envelopes use the same optional contract. A missing
Initial Backfill cursor suggests the fixed
`gohealthcli sync --from YYYY-MM-DD` template without copying any invocation
arguments. Multi-Data-Type sync keeps remediation only on the affected result
child. Missing setup may diagnose then initialise; Connection, scope, token,
and identity failures reuse the reviewed authentication actions. Canceled,
corrupt, invalid-query, and unknown failures add no fabricated step. JSON and
explicit `--plain` may expose the metadata; default human output remains
unchanged.

A few subcommands deviate from the standard `--plain` / `--json`
contract:

- `describe-schema` always emits the curated JSON catalog (or live DDL when
  `--sql` is passed). Its own `--json` flag is on by default; the global
  `--json` / `--plain` are accepted and parsed but have no effect on the
  schema bytes. Its *failure* envelopes do route through the Failure
  Reporter, so `gohealthcli --json describe-schema bogus` lands a JSON
  failure on stdout like every other subcommand.
- `export` always writes CSV (default) or JSONL according to its
  `--format` flag, and treats `--plain` / `--json` as format synonyms
  rather than output-mode switches: `--json` means `--format jsonl` and
  `--plain` means `--format csv`. Passing a synonym alongside a
  contradictory `--format` value (`--json --format csv`) fails with a
  "`--json conflicts with --format csv`" error. Failure envelopes do
  honour the requested mode — `export <dataset> --json` reports failures
  as a JSON envelope, `--plain` as plain key/value lines. See
  [docs/commands/export.md](./docs/commands/export.md).
- `raw` writes the provider's raw bytes to stdout and ignores `--plain`,
  `--json`, and `--no-input`; passing any of them directly on `raw` is
  rejected at parse time with a targeted "not supported by raw" message.

## Read surface

The four read commands — `status`, `query`, `export`, `describe-schema` —
are the primary interface for LLM consumers and scripted users. PRD #144
made the contract predictable across them; the notes below summarise the
behaviour that is now stable. See each command's reference page under
[docs/commands/](./docs/commands/) for the full prose.

- `--db <path>` works on its own for every read command. Passing
  `gohealthcli --db /tmp/scratch.sqlite status` opens that archive
  directly without requiring a matching `--config` file. When only
  `--db` is explicit it wins without an agreement check; when both
  `--config` and `--db` are explicit and disagree, the error names
  `--db` and `--config` rather than the internal `archive_path` field.
  `describe-schema --db` is honoured the same way (PRD #144 slice 1).
- `status --plain` and `status --json` carry the same information. The
  plain `known_data_types: a,b,c` line maps to a top-level
  `known_data_types` JSON array; `paired_device_count` is a top-level
  JSON key as well as the back-compat nested
  `identity_snapshots_freshness.paired_device_count`. A consumer who
  picks one mode never loses fields the other mode carries (PRD #144
  slice 9).
- `query` with no flags emits the same `row.<row>.<column>: <value>`
  shape as `--plain` — the legacy `Row N: column=value column=value`
  output (which silently broke on values containing spaces or `=`) was
  removed in PRD #144 slice 7. Scripted and LLM consumers get a
  parseable shape by default.
- `query --json` returns JSON-typed columns (`raw_json`,
  `data_source_json`, `timezone_metadata`, `token_metadata_json`,
  `google_identity_json`, and any column whose name ends in `_json`) as
  nested JSON objects so downstream consumers parse once instead of
  twice. Pass `--raw-text` to opt out. BLOB columns are wrapped in a
  `{"__blob_base64__": "<base64>"}` marker object so raw bytes survive
  the JSON path without UTF-8 corruption (PRD #144 slices 5–6).
- `export --help` lists every supported dataset alphabetically (PRD #144
  slice 3). The full list is auto-generated above between the
  `<!-- export-datasets:start -->` / `<!-- export-datasets:end -->`
  markers from the same registry (PRD #144 slice 4). `export <typo>`
  surfaces the closest matches (Levenshtein ≤ 3, top 3) and a pointer
  back to `export --help`.
- `describe-schema --json` reports view column types as the literal
  `"unknown"` rather than a misleading `BLOB` or empty string when the
  underlying expression's affinity does not carry a declared type.
  Table columns still report their declared types — the fallback is
  view-only (PRD #144 slice 8).

## Configuration

Default local paths:

- config: `~/.config/gohealthcli/config.toml`
- archive: `~/.local/share/gohealthcli/gohealthcli.sqlite`
- backup config: `~/.config/gohealthcli/backup.json`
- backup age identity: `~/.config/gohealthcli/backup-age-identity.txt`

Default runtime token storage is OS-native:

- macOS: Keychain
- Windows: Windows Credential Manager
- Linux: Secret Service/libsecret

For local testing, an explicit file Credential Store is acceptable if it stays
owner-only. There is no default file path — a `type = "file"` store must set
`path` explicitly (a conventional location is
`~/.config/gohealthcli/tokens.json`):

```toml
[credential_store]
type = "file"
path = "/absolute/path/to/gohealthcli/tokens.json"
```

Use `doctor --plain` to check local setup without provider calls. Use
`doctor --online --plain` only when you want token refresh and Google Health
reachability checks.

## Encrypted backups

`backup init` creates or reuses an X25519 age identity, records the backup
checkout/remote/recipients in an owner-only config, initializes or clones the
Git checkout, and commits a recovery README. It pushes that setup commit unless
`--no-push` is set. Use an explicit local or remote target while evaluating:

```bash
gohealthcli backup init --remote /path/to/backup-gohealthcli.git --no-push --plain
gohealthcli backup push --db /path/to/gohealthcli.sqlite --no-push --plain
gohealthcli backup pull --db /path/to/fresh-restored.sqlite --plain
gohealthcli backup status --plain
```

`backup push` exports the current Health Archive only. It does not run `sync`,
refresh Identity Snapshots, read Provider credentials, or contact Google Health.
Each logical collection becomes deterministic JSONL with fixed gzip metadata,
then age encryption happens before the shard is written into the Git checkout.
The command commits the encrypted shards and cleartext manifest locally, and
pushes unless `--no-push` is set. An unchanged rerun leaves the checkout clean.

To add another machine, obtain its public age recipient, then rerun `backup
init` on the current backup machine with every desired additional recipient
and run `backup push`:

```bash
gohealthcli backup init --recipient <second-machine-age-recipient> --no-push --plain
gohealthcli backup push --db /path/to/gohealthcli.sqlite --plain
```

Recipient values are trimmed, deduplicated, and sorted before they are saved or
written to the manifest. A recipient change re-encrypts every unchanged shard
and creates a backup commit so the new identity can restore existing data. A
following push with unchanged recipients and Health Archive data reuses the
encrypted shards and leaves Git clean. Shards absent from the new Snapshot are
removed from the current backup tree; earlier Git history remains unchanged.

`backup pull` pulls/rebases the configured Git checkout before reading its
manifest. It confines every shard to the backup data tree, decrypts with the
configured age identity, verifies the declared plaintext hashes, and validates
the complete Health Archive Snapshot before creating the target selected by
`--db`. The target must be a fresh path. Data Point Attachment rows and payloads
restore together, with owner-only sidecars that pass the same orphan checks as
`doctor`. Use a throwaway target periodically to prove recovery without touching
the current Health Archive.

Private Credential Store token material, OAuth client secrets, and Secret
Provider contents are not backed up. A restored Health Archive may therefore
need `connect` before Provider commands work.

`backup status` is a read-only manifest inspection path. It works without the
private age identity and reports a clear `backup_uninitialized` or
`backup_empty` state before the first encrypted archive manifest exists. Once a
manifest exists it reports encryption, export time, shard count, and Health
Archive counts without decrypting health data or opening the live archive.

The config and private identity never belong in the Git checkout. Keep a
private recovery copy of the identity: losing every configured identity makes
the encrypted backup unrecoverable. The repository still exposes metadata such
as public recipients, table names, counts, encrypted sizes, plaintext hashes,
backup cadence, and changed shards.

## Safety

- Read-only provider behavior: no health writes or deletes.
- Local-first archive: no cloud service and no background upload; backup Git
  operations happen only through an explicit `backup` command.
- OAuth token values are not printed in normal command output.
- OAuth endpoints from the client JSON are pinned to https Google hosts,
  and the client file must stay owner-only — enforced both at `connect`
  and on the token auto-refresh path.
- Exports can reveal health history; commands require explicit `--stdout` or
  `--output`, and `export` refuses to write through a symlinked `--output`.
- `query` and `status` plain output escapes control characters, so archived
  provider data cannot inject terminal escape sequences.
- Data Point Attachment paths are validated against path traversal before
  they are joined to the attachments root.
- Keep the SQLite archive, token files, backup config/identity, and exported
  CSV/JSONL files private.

## Release

Tagged releases publish GitHub Release archives and update the Homebrew tap.
Install with:

```bash
brew install BramVR/tap/gohealthcli
```

Release operators: see [docs/release.md](./docs/release.md).

## Docs

- [Project Site](https://gohealthcli.bramvanrompuy.be): rendered install,
  quickstart, Data Types, and command reference pages.
- [CONTEXT.md](./CONTEXT.md): project glossary only, used by grill-style review.
- [docs/google-auth-setup.md](./docs/google-auth-setup.md): local Google
  Health OAuth setup checklist.
- [docs/commands.md](./docs/commands.md): CLI surface and output behavior.
- [docs/data-model.md](./docs/data-model.md): archive model sketch.
- [docs/security.md](./docs/security.md): local credentials and health data safety.
- [docs/backups.md](./docs/backups.md): encrypted backup and recovery drills.
- [docs/research.md](./docs/research.md): source-backed Google Health API notes.
- [docs/plan.md](./docs/plan.md): product and implementation plan.
- [docs/adr/](./docs/adr): short architectural decision records.

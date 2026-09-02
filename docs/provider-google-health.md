---
summary: "Google Health API provider notes, endpoint families, naming traps, and launch risk."
read_when:
  - "Implementing Google Health API access."
  - "Choosing scopes or endpoint families."
  - "Debugging provider normalization."
---
# Google Health Provider

## Provider Name

Canonical provider name: `googlehealth`.

## API

Base API: Google Health API v4.

The provider client is hand-rolled REST over `net/http` against
`https://health.googleapis.com/v4` (`internal/googlehealth/fetch.go`).
There is no `google.golang.org/api` Go module dependency.

## Offline catalog browsing

`gohealthcli catalog list` serializes every compiled Data Type in canonical
catalog order. The `selection` field separates default from opt-in Data Types;
`raw_data_points` reports `supported` or `unsupported` independently because a
default Data Type can be Rollup-only. `required_scopes` contains exact OAuth
scope URLs.

`gohealthcli catalog scopes` groups those exact scope URLs with catalog-ordered
Data Type membership. It reports requirements only. It does not inspect a token
or claim that a scope is granted.

`gohealthcli catalog describe <data-type>` joins two explicitly sourced groups:

- `compiled` projects endpoint families, filter fields, lower-bound-only
  behavior, range shapes, page policies, exact scopes, record kind, and Rollup
  modes from the canonical compiled catalog.
- `discovery` projects the JSON field, schema reference, sorted field names,
  and JSON types from discovery, together with its source and revision.

Describe uses the committed discovery snapshot by default, so it runs offline
without config, a Connection, credentials, or a Health Archive. `--discovery
PATH` reads a bounded local document; `--live` reads only the fixed public v4
discovery endpoint without authorization. The flags are mutually exclusive.
Discovery identity and record shape must remain compatible with the compiled
entry, and enrichment never replaces compiled facts.

List, scopes, and default describe all run without config, a Connection,
credentials, a Health Archive, or network access.

## Discovery-backed catalog audit

`gohealthcli catalog verify` fetches Google's unauthenticated public v4
discovery document and compares its raw Data Point union with the canonical
local catalog. `--discovery PATH` performs the same audit against an offline
document. Neither path reads config, a Connection, credentials, or a Health
Archive, and neither invokes a Provider data operation.

The committed reduced snapshot at
`internal/googlehealth/testdata/google-health-discovery-v4.json` records only
the discovery facts these catalog surfaces use: Data Type union membership,
JSON field-to-schema references, temporal shape, and direct field names and
JSON types. It intentionally
does not claim that discovery verifies exact per-Data-Type operation support or
filter fields; those facts are reported as unverifiable.

ECG is a separately evidenced exception to that discovery limitation. The live
v4 list-method description names only the physical
`electrocardiogram.interval.start_time >= ...` filter; it supports no ECG upper
bound. The Provider request therefore sends the lower bound and ingestion
enforces sync's exclusive `to` boundary before any archive write or count. The
provider-shaped `raw` command rejects an explicit ECG `--to` instead of
silently returning a wider response.

Current explicit exceptions are:

- Local Rollup-only: `calories-in-heart-rate-zone`, `total-calories`.
- Upstream raw only: `food`, `food-measurement-unit`.
  Google classifies the two Food records as Data Types in its
  [public Data Types catalog](https://developers.google.com/health/data-types)
  even though their discovery descriptions call them details rather than
  collections.
- Upstream write-only: `menstrual-period`, `moods`, `ovulation-test`,
  `symptoms`. Revision `20260817` adds them to the Data Point union, but Google
  documents only `create`, `update`, and `batchDelete` operations under
  write-only scopes. They stay outside gohealthcli's read-only compiled
  catalog.

An otherwise matching document therefore reports
`verified_with_known_gaps`. Unexpected additions, removals, reference or shape
changes, malformed documents, and unavailable discovery transport report
`drift_detected` and exit nonzero. Results and their nested lists are sorted for
stable JSON, plain, and human output.

## Endpoint Families

`getIdentity`

- Fetch Google Health user ID.
- Fetch legacy Fitbit user ID when available.
- Should run immediately after OAuth consent.

`list`

- Detailed Data Points for one Data Type.
- Default fetch path for raw Data Point sync.

`get`

- Fetch one identifiable Data Point by its opaque Provider ID.
- Supported by the compiled catalog for `blood-glucose`, `body-fat`,
  `core-body-temperature`, `exercise`, `height`, `hydration-log`,
  `nutrition-log`, `sleep`, and `weight`.
- Uses one request with no range, source filter, or pagination.
- Google also documents `get` for Food and Food Measurement Unit, but those
  non-temporal catalog entities remain outside the operational Data Point
  catalog and this command.

`reconcile`

- Reconciled stream across sources.
- Supports data source family filtering such as wearable-only data.
- Wearable filter maps to
  `users/me/dataSourceFamilies/google-wearables`.
- Use when source-family filtering is requested, or if provider behavior proves
  it has better correction semantics than `list`.
- Important for "watch data" questions.
- `basal-energy-burned` uses the same
  `basal_energy_burned.interval.start_time` filter as `list`; Google exposes
  no Rollup operation for this Data Type.
- `nutrition-log` uses
  `nutrition_log.interval.civil_start_time` for both list and reconcile,
  under the existing opt-in `nutrition.readonly` scope. gohealthcli does not
  implement its upstream Rollup shapes yet.

`rollUp`

- Aggregate over arbitrary time windows.
- Used for hourly, weekly, and custom summaries, including total calories.
- Total-calories requests have a 14-day maximum physical span.

`dailyRollUp`

- Civil-day aggregate.
- Used for steps, floors, heart-rate, and total-calories daily summaries.
- Total-calories requests have a 14-day maximum civil span.

## Retry behavior

`sync` wraps the upstream fetch in a bounded retry middleware so transient
provider failures do not require restarting a multi-year backfill:

- `429 Too Many Requests` and `5xx` responses retry with exponential backoff
  (`250ms` base, doubling each attempt) plus jitter. The exponential
  component is capped at `30s`; the final sleep can exceed that cap when
  the server-supplied `Retry-After` value is larger (see next bullet).
- `Retry-After` (when present on a retryable response, `429` or `5xx`) is
  honored as the minimum next-attempt delay and overrides the exponential
  cap when larger, so a `Retry-After: 120` response waits ~120 s before
  the next attempt.
- `401 Unauthorized` surfaces immediately with the existing
  "run `gohealthcli connect` again" message.
- Other `4xx` (`400`, `403`, `404`, ...) surface immediately without retry.
- Bounded at `5` total attempts per request; after the budget is exhausted
  the last error surfaces and the Sync Run is recorded as `sync_failed`.

The Identity Snapshot commands (`devices`, `settings`, `irn-profile`,
`identity`, `profile`) ride the same retry middleware through the shared
Provider GET module (#280), with the same backoff schedule, `Retry-After`
floor, and 5-attempt budget per fetch. `raw` is an exploration tool and
surfaces upstream errors immediately so failure modes stay visible.

## Naming Trap

Endpoint path Data Type identifiers use kebab case, for example
`heart-rate`. Filter expressions use snake case, for example `heart_rate`.

## Launch Risk

As of 2026-05-24, Google recommends waiting until the end of May 2026 before
official integration launch because breaking changes may still occur.

For this project, that means:

- Build a `raw` command early.
- Keep provider parsing isolated.
- Keep raw JSON in the archive.
- Add fixtures for every Data Type before normalizing it.
- Complete provider pagination within a Sync Run; pagination page tokens are
  not persisted. Durable time cursors are a separate concept governed by the
  sync-cursors ADR (ADR-0008, "Sync Cursors Advance Only on
  `sync_completed`"): the Sync Cursor is a durable highwater mark stored in
  the `sync_cursors` table, and reruns stay safe because archiving is
  idempotent.

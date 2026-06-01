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

Likely Go module: `google.golang.org/api/health/v4`.

## Endpoint Families

`getIdentity`

- Fetch Google Health user ID.
- Fetch legacy Fitbit user ID when available.
- Should run immediately after OAuth consent.

`list`

- Detailed Data Points for one Data Type.
- Default fetch path for raw Data Point sync.

`reconcile`

- Reconciled stream across sources.
- Supports data source family filtering such as wearable-only data.
- Wearable filter maps to
  `users/me/dataSourceFamilies/google-wearables`.
- Use when source-family filtering is requested, or if provider behavior proves
  it has better correction semantics than `list`.
- Important for "watch data" questions.

`rollUp`

- Aggregate over arbitrary time windows.
- Useful later for hourly or custom summaries.

`dailyRollUp`

- Civil-day aggregate.
- Useful first normalized export path for steps, distance, calories, and heart-rate summaries.

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
- Complete provider pagination within a Sync Run. Do not persist durable resume
  cursors unless the API requires them for correctness; reruns should rely on
  idempotent archiving.

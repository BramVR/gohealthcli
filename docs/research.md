---
summary: "Source-backed Google Health API research and provider feasibility notes."
read_when:
  - "Checking current Google Health API facts."
  - "Revisiting provider choice."
  - "Updating scope after Google API changes."
---
# Research

Last checked: 2026-08-03.

## Findings

Google Health API is the primary candidate. Google describes it as the next
generation of the Fitbit Web API and says it exposes health and fitness data
from Fitbit, Pixel Watch, and other third-party devices and apps through Google
OAuth 2.0.

Google recommends waiting until the end of May 2026 before official integration
launches because breaking changes may occur during the transition period.

Google Health API access requires a Google Cloud project, API enablement, OAuth
client, test users while unverified, and configured scopes.

In OAuth testing mode, refresh tokens expire after 7 days. In production mode,
refresh tokens generally do not expire unless revoked or unused for a prolonged
period. Supporting more than 100 users requires third-party security review.

The API has endpoint families useful for a CLI archive:

- `getIdentity`: fetch Google Health user ID and legacy Fitbit user ID.
- `list`: fetch detailed Data Points for a Data Type.
- `reconcile`: fetch reconciled streams, including source-family filtering.
- `rollUp`: aggregate over arbitrary windows.
- `dailyRollUp`: aggregate across civil days.

The data type catalog includes the first target set: `steps`, `heart-rate`,
`heart-rate-variability`, `daily-heart-rate-variability`,
`daily-resting-heart-rate`, `oxygen-saturation`, `daily-oxygen-saturation`,
`daily-respiratory-rate`, `sleep`, `exercise`, `distance`, `total-calories`,
`weight`, and related activity/fitness types.

Health Connect is not the primary desktop CLI path. It is Android/on-device
infrastructure and may be useful later as an import/export fallback.

Google Fit API should be avoided for new work. It is legacy/deprecated relative
to Google Health API and does not fit a new CLI started in 2026.

Existing `fitbit-cli` tools may be useful references, but they are not the clean
long-term target if Google Health API access works.

## ECG contract evidence (C1)

Non-sensitive live public evidence checked on 2026-08-02 records the current
ECG contract:

- Google's scope catalog, migration mapping, release notes, and REST list
  reference name
  `https://www.googleapis.com/auth/googlehealth.ecg.readonly`.
- The live v4 discovery document at revision `20260730` names the physical
  `electrocardiogram.interval.start_time` filter and says ECG supports only the
  `>=` start-time restriction, not an exclusive upper-bound clause.
- That discovery document's list-method scope array still omits the newer ECG
  and IRN scopes. This upstream discrepancy remains recorded here rather than
  being hidden in gohealthcli's known-gap allowlist; the current REST reference
  and dedicated scope catalog are authoritative for consent.

No credential, Connection identifier, Provider payload, or Health Archive data
was read or published for this evidence.

## Total calories Rollup contract evidence (C3)

Non-sensitive public evidence checked on 2026-08-03 records the current
`total-calories` contract:

- Google's calorie catalog names `total-calories` as read-only, derived calorie
  expenditure with `rollUp` and `dailyRollUp` operations under the
  `activity_and_fitness` scope. Both modes have a 14-day maximum request span.
- The live v4 discovery document at revision `20260730` exposes the
  `totalCalories` Rollup union member. `TotalCaloriesRollupValue` contains a
  numeric `kcalSum`.
- The REST reference distinguishes an absent union member (no manual or
  on-wrist data) from an explicit zero (the device was worn and measured zero).

No credential, Connection identifier, Provider payload, or Health Archive data
was read or published for this evidence.

## Basal energy burned contract evidence (C2)

Non-sensitive public evidence checked on 2026-08-03 records the current
`basal-energy-burned` contract:

- The live v4 discovery document at revision `20260730` contains
  `DataPoint.basalEnergyBurned` and `ReconciledDataPoint.basalEnergyBurned`.
  Its `BasalEnergyBurned` schema is interval-shaped with a required numeric
  `kcal` value.
- Google's localized calorie documentation names the endpoint identifier
  `basal-energy-burned`, filter prefix `basal_energy_burned`, `list` and
  `reconcile` operations, the `activity_and_fitness` scope, and no Rollup
  operations. The English catalog still omits the Data Type, so gohealthcli
  keeps it out of `sync --all` while allowing explicit `--types` sync.

No credential, Connection identifier, Provider payload, or Health Archive data
was read or published for this evidence.

## Sources

- Google Health API home: https://developers.google.com/health
- Google Health API setup/OAuth: https://developers.google.com/health/setup
- Google Health API endpoints: https://developers.google.com/health/endpoints
- Google Health API data types: https://developers.google.com/health/data-types
- Google Health API calories and energy data types (localized current catalog): https://developers.google.com/health/data-types/calories?hl=pt-br
- Google Health API scopes: https://developers.google.com/health/scopes
- Google Health API list method: https://developers.google.com/health/reference/rest/v4/users.dataTypes.dataPoints/list
- Google Health API Rollup method: https://developers.google.com/health/reference/rest/v4/users.dataTypes.dataPoints/rollUp
- Google Health API daily Rollup method: https://developers.google.com/health/reference/rest/v4/users.dataTypes.dataPoints/dailyRollUp
- Google Health API total-calories Rollup value: https://developers.google.com/health/reference/rest/v4/TotalCaloriesRollupValue
- Google Health API release notes: https://developers.google.com/health/release-notes
- Google Health API Go client: https://pkg.go.dev/google.golang.org/api/health/v4
- Grill with docs skill: https://github.com/mattpocock/skills/blob/main/skills/engineering/grill-with-docs/SKILL.md

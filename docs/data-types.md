---
title: "Data Types"
description: "What each Google Health Data Type captures and how gohealthcli stores it."
---

A plain-language guide to every Data Type the Google Health catalog exposes through `gohealthcli`. Each entry names the sync key you pass to `sync --types`, the upstream record shape (sample / interval / session / daily), the OAuth scope required, and what the catalog row supports. For the canonical machine-readable list, see the [README](https://github.com/BramVR/gohealthcli#readme); for storage shape, see [`docs/data-model.md`](https://github.com/BramVR/gohealthcli/blob/main/docs/data-model.md).

## How to read this page

- **Sync key** — the literal string accepted by `sync --types`.
- **Shape** — how the upstream returns each Data Point: `sample` (point-in-time reading), `interval` (a value over a span), `session` (a user activity with start/end), `daily` (one row per civil date).
- **Scope** — the OAuth scope required. Tier 2 keys (`ecg`, `irn`, `tcx`) are opt-in via `connect --add-scopes …`.
- **Rollups** — which `--rollup` kinds the catalog row supports beyond raw Data Points.
- **Stored as** — the table the row lands in (`data_points` for raw, `rollups` for aggregates) plus the normalized view exposed for queries and `export`.

The catalog is authoritative in `cmd/gohealthcli/google_health_data_types.go`; this page is its narrative companion.

## Activity and fitness

### Steps

- **Sync key:** `steps`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Rollups:** `daily`, `hourly`, `weekly`, `window=<duration>` (1h / 1d / 7d granularities)
- **Stored as:** `data_points` (raw) or `rollups` (aggregated); normalized view `daily-steps`

Step counts over a time interval (typically one minute or one stride bucket from the device). The default Data Type when `sync` is run with no `--types` flag.

### Distance

- **Sync key:** `distance`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`

Distance travelled over a time interval, in metres. Reconcile-capable, so `--source-family wearable` works.

### Floors

- **Sync key:** `floors`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Rollups:** `daily`, `hourly`, `weekly`, `window=<duration>`
- **Stored as:** `data_points` or `rollups`

Floors climbed over an interval. Not yet a default sync type — opt in via `--types floors` until the upstream filter shape is confirmed across multiple weeks of real data.

### Altitude

- **Sync key:** `altitude`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `altitude-intervals`

Altitude readings over an interval.

### Active energy burned

- **Sync key:** `active-energy-burned`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`

Active-only energy expenditure (kilocalories) over an interval, excluding basal metabolism.

### Active minutes

- **Sync key:** `active-minutes`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `active-minutes-intervals`

Minutes the user spent above the device's active threshold over each interval.

### Active zone minutes

- **Sync key:** `active-zone-minutes`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `active-zone-minutes-intervals`

Minutes spent in any active heart-rate zone over an interval. Counts moderate-zone minutes once and vigorous-zone minutes twice, per the Fitbit convention.

### Activity level

- **Sync key:** `activity-level`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `activity-level-intervals`

Per-interval activity classification (sedentary / lightly active / fairly active / very active).

### Sedentary period

- **Sync key:** `sedentary-period`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `sedentary-period-intervals`

Continuous spans of low movement the provider flagged as sedentary.

### Calories in heart-rate zone

- **Sync key:** `calories-in-heart-rate-zone`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`

Calories burned while in each heart-rate zone over an interval. The live API currently rejects the assumed filter; the catalog row is kept for future debugging.

### Time in heart-rate zone

- **Sync key:** `time-in-heart-rate-zone`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `time-in-heart-rate-zone-intervals`

Minutes spent in each heart-rate zone (out-of-zone / fat-burn / cardio / peak) over an interval.

### VO2 max

- **Sync key:** `vo2-max`
- **Shape:** sample
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `vo2-max-samples`

Estimated maximum oxygen uptake (mL/kg/min) at a point in time, derived from heart-rate response to exertion.

### Run VO2 max

- **Sync key:** `run-vo2-max`
- **Shape:** sample
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `run-vo2-max-samples`

Run-specific VO2 max estimate, fitted from outdoor run heart-rate and pace data.

### Daily VO2 max

- **Sync key:** `daily-vo2-max`
- **Shape:** daily
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `daily-vo2-max`

One VO2 max value per civil date, the provider's daily summary derivation.

### Swim lengths data

- **Sync key:** `swim-lengths-data`
- **Shape:** interval
- **Scope:** `activity_and_fitness.readonly`
- **Stored as:** `data_points`; normalized export `swim-lengths-data-intervals`

Per-length swim metrics (stroke type, length, duration) captured by waterproof wearables.

### Total calories

- **Sync key:** `total-calories`
- **Status:** catalog-reserved; raw sync is not supported because Google exposes total-calories only as Rollup data.

## Heart rate Data Types

### Heart rate

- **Sync key:** `heart-rate`
- **Shape:** sample
- **Scope:** `health_metrics_and_measurements.readonly`
- **Rollups:** `hourly`, `weekly`, `window=<duration>` (1h / 1d / 7d granularities)
- **Stored as:** `data_points` or `rollups`; normalized view `heart-rate-samples`

Beats-per-minute readings at a point in time. The high-volume Data Type for any wearable, typically arriving at one sample per few seconds to minutes.

### Heart-rate variability

- **Sync key:** `heart-rate-variability`
- **Shape:** sample
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`

Beat-to-beat variability measurements (HRV — typically RMSSD in milliseconds), a recovery and autonomic-balance proxy.

### Daily resting heart rate

- **Sync key:** `daily-resting-heart-rate`
- **Shape:** daily
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`; normalized view `resting-heart-rate-by-day`

One resting-heart-rate estimate per civil date.

### Daily heart-rate variability

- **Sync key:** `daily-heart-rate-variability`
- **Shape:** daily
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`

One HRV summary per civil date.

### Daily heart-rate zones

- **Sync key:** `daily-heart-rate-zones`
- **Shape:** daily
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`; normalized export `daily-heart-rate-zones`

Per-day minutes spent in each heart-rate zone, the canonical daily rollup of zone-resident time.

## Heart rhythm (Tier 2)

These are gated behind opt-in scopes the user grants via `gohealthcli connect --add-scopes ecg,irn`. They are list-only session shapes.

### Electrocardiogram

- **Sync key:** `electrocardiogram`
- **Shape:** session
- **Scope:** `ecg.readonly` (Tier 2 opt-in)
- **Stored as:** `data_points`; normalized export `electrocardiogram-sessions`

A user-triggered ECG measurement: start/end, classification (e.g. sinus rhythm, AFib), and the underlying samples preserved in raw JSON.

### Irregular rhythm notification

- **Sync key:** `irregular-rhythm-notification`
- **Shape:** session
- **Scope:** `irn.readonly` (Tier 2 opt-in)
- **Stored as:** `data_points`; normalized export `irregular-rhythm-notifications`

A provider-issued alert that the wearable detected an irregular rhythm over a span. The companion `current-irn-profile` view tracks per-Connection IRN onboarding state.

## Sleep and respiration

### Sleep

- **Sync key:** `sleep`
- **Shape:** session
- **Scope:** `sleep.readonly`
- **Stored as:** `data_points`; normalized views `sleep-sessions` and `sleep-stages`

One row per sleep session, with the per-stage (LIGHT / DEEP / REM / AWAKE) breakdown preserved inside the raw JSON. The `sleep-stages` view explodes that array into one row per stage so downstream queries can read stage duration without parsing JSON.

### Oxygen saturation

- **Sync key:** `oxygen-saturation`
- **Shape:** sample
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`

Blood-oxygen (SpO2) saturation readings as a percentage, at a point in time. Wearables typically measure these only during sleep.

### Daily oxygen saturation

- **Sync key:** `daily-oxygen-saturation`
- **Shape:** daily
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`

One SpO2 summary per civil date (typically min / mean / max across the sleep window).

### Daily respiratory rate

- **Sync key:** `daily-respiratory-rate`
- **Shape:** daily
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`

One respiratory-rate summary per civil date.

### Respiratory rate sleep summary

- **Sync key:** `respiratory-rate-sleep-summary`
- **Shape:** sample
- **Scope:** `sleep.readonly`
- **Stored as:** `data_points`; normalized export `respiratory-rate-sleep-summary`

Breaths-per-minute summary derived from the sleep window — emitted per sleep session, not per civil date.

### Daily sleep temperature derivations

- **Sync key:** `daily-sleep-temperature-derivations`
- **Shape:** daily
- **Scope:** `sleep.readonly`
- **Stored as:** `data_points`; normalized export `daily-sleep-temperature-derivations`

One row per civil date carrying the provider's nightly skin-temperature derivations.

## Exercise Data Types

### Exercise

- **Sync key:** `exercise`
- **Shape:** session
- **Scope:** `activity_and_fitness.readonly` (plus `googlehealth.location.readonly` for TCX routes, opt-in via `connect --add-scopes tcx`)
- **Stored as:** `data_points`; normalized views `exercise-sessions` and `exercise-splits`; optional TCX sidecar Attachment

One row per logged workout: exercise type, start/end, distance, calories, splits, and per-stage metadata preserved in raw JSON. When the TCX scope is granted, each session also archives the upstream TCX route XML as a content-addressed sidecar (`<archive>.attachments/tcx/…`); see [`docs/data-model.md`](https://github.com/BramVR/gohealthcli/blob/main/docs/data-model.md) for the attachment store.

## Body measurements

### Weight

- **Sync key:** `weight`
- **Shape:** sample
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`; normalized view `weight-samples`

One body-weight reading at a point in time, typically from a smart scale or manual entry.

### Body fat

- **Sync key:** `body-fat`
- **Shape:** sample
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`; normalized export `body-fat-samples`

Body-fat percentage at a point in time.

### Height

- **Sync key:** `height`
- **Shape:** sample
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`; normalized exports `height-samples` and `current-height`

Height readings; one current-height row tracks the latest value per Connection.

## Other biomarkers

### Blood glucose

- **Sync key:** `blood-glucose`
- **Shape:** sample
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`; normalized export `blood-glucose-samples`

Glucose readings at a point in time, from a CGM or manual entry.

### Core body temperature

- **Sync key:** `core-body-temperature`
- **Shape:** sample
- **Scope:** `health_metrics_and_measurements.readonly`
- **Stored as:** `data_points`; normalized export `core-body-temperature-samples`

Core-temperature readings at a point in time.

## Hydration

### Hydration log

- **Sync key:** `hydration-log`
- **Shape:** session
- **Scope:** `nutrition.readonly`
- **Stored as:** `data_points`

A user-logged hydration entry: volume over a civil window. The only Data Type under the nutrition scope today.

## Identity, device, and settings snapshots

These ride alongside the Data Point catalog and capture per-Connection metadata. They land in the `identity_snapshots` table and are exposed through normalized views.

- **Profile** — `gohealthcli profile`. Provider account profile (membership date, age, stride lengths). Stored as `kind='profile'`; queryable via the `searchable-text` view.
- **Settings** — `gohealthcli settings`. Measurement system, timezone, stride length type. Exposed via the `current-settings` view.
- **Paired devices** — `gohealthcli devices`. One row per paired wearable with model, manufacturer, battery, last-sync, and feature set. Exposed via the `paired-devices` view.
- **IRN profile** — `gohealthcli irn-profile` (Tier 2 `irn.readonly` opt-in). Tracks onboarding and enrollment state for irregular-rhythm notifications. Exposed via the `current-irn-profile` view.

## Rollups

Most Data Types support `--rollup daily`, which calls the upstream `dailyRollUp` endpoint and writes to the `rollups` table instead of `data_points`. The catalog rows for `steps`, `heart-rate`, and `floors` additionally support the windowed `rollUp` endpoint (`--rollup hourly`, `--rollup weekly`, or `--rollup window=<duration>`) at 1h / 1d / 7d granularities. Each rollup kind carries its own Sync Cursor — syncing weekly aggregates does not disturb the hourly cursor for the same Data Type. See [`sync`](commands/sync.html) for the full flag matrix.

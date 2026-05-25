---
summary: "Product scope, First Release commands, non-goals, and open planning questions."
read_when:
  - "Scoping First Release behavior."
  - "Deciding whether a feature belongs before first implementation."
  - "Running a grill-style planning session."
---
# Plan

Purpose: local-first read-only CLI for personal health data available through
Google Health API.

## Goal

Build a `gobankcli`-style tool for health data:

- Terminal-first.
- Scriptable output.
- Local SQLite archive.
- Raw upstream JSON preserved.
- Normalized views for common personal analytics.
- Read-only until there is a strong reason to write health data.

## First Release

The First Release should be intentionally narrow but foundation-grade: secure
token handling, stable command contracts, durable archive semantics, and clear
provider boundaries matter more than broad Data Type coverage.

- `init`: create config paths.
- `doctor`: verify config, OAuth client, token status, local archive.
- `connect`: browser OAuth flow and token storage.
- `identity`: fetch Google Health user ID and legacy Fitbit user ID.
- `profile`: fetch available user profile/settings data.
- `sync`: fetch selected Data Types over a date range.
- `status`: summarize local archive counts and latest Sync Runs.
- `query`: read-only SQL over the archive.
- `export`: export selected normalized data to CSV or JSONL.
- `raw`: make one direct provider call for debugging early API changes.

## First Raw Data Types

`sync` should be able to archive raw JSON for these Data Types before every one
has a polished normalized export.

- `steps`
- `heart-rate`
- `daily-resting-heart-rate`
- `heart-rate-variability`
- `daily-heart-rate-variability`
- `oxygen-saturation`
- `daily-oxygen-saturation`
- `daily-respiratory-rate`
- `sleep`
- `exercise`
- `distance`
- `total-calories`
- `weight`

## First Normalized Views

- daily steps
- heart-rate samples
- resting heart rate by day
- sleep sessions
- exercise sessions
- weight samples

## Non-Goals

- No writes to Google Health API in the First Release.
- No Health Connect Export or Android companion app in the First Release.
- No legacy Google Fit API implementation.
- No legacy Fitbit Web API implementation or fallback code paths unless Google
  Health API blocks personal use.
- No scheduled or background sync in the First Release.
- No medical advice or diagnosis features.
- No cloud-hosted service.

## Open Questions

These require live Google Cloud or Google Health API testing for the connected
Google Identity:

- Which exact Google Cloud OAuth app should be used for personal access?
- Does the API expose the user's existing Fitbit-connected watch data immediately?
- Which Data Types support high-resolution historical access for Bram's account?

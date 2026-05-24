---
summary: "Product scope, MVP commands, non-goals, and open planning questions."
read_when:
  - "Scoping MVP behavior."
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

## MVP

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

## First Data Types

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

## Non-Goals

- No writes to Google Health API in the first version.
- No Health Connect Android companion app in the first version.
- No legacy Google Fit API implementation.
- No legacy Fitbit Web API implementation unless Google Health API blocks personal use.
- No medical advice or diagnosis features.
- No cloud-hosted service.

## Open Questions

- Which exact Google Cloud OAuth app should be used for personal access?
- Which scopes are needed for the first Data Types?
- Does the API expose the user's existing Fitbit-connected watch data immediately?
- Which Data Types support high-resolution historical access for Bram's account?
- Should OAuth token material live in a local file, macOS keychain, or both?
- Should the archive support more than one Google Identity from day one?

---
title: "Command reference"
description: "Every gohealthcli subcommand at a stable URL."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Every user-facing subcommand exposed by `gohealthcli`. Pages are regenerated from the binary by `make docs-commands`; the committed copies must match a fresh regeneration.

## Subcommands

- [`gohealthcli init`](commands/init.html) — Create local config and an empty Health Archive.
- [`gohealthcli doctor`](commands/doctor.html) — Validate local setup and provider reachability.
- [`gohealthcli connect`](commands/connect.html) — Run the browser OAuth flow and anchor one Google Identity.
- [`gohealthcli identity`](commands/identity.html) — Refresh the archived Google Identity metadata.
- [`gohealthcli profile`](commands/profile.html) — Archive a Profile Snapshot from the provider.
- [`gohealthcli settings`](commands/settings.html) — Archive a Settings Snapshot from the provider.
- [`gohealthcli devices`](commands/devices.html) — Archive a Paired Devices Snapshot from the provider.
- [`gohealthcli irn-profile`](commands/irn-profile.html) — Archive an IRN Profile Snapshot from the provider.
- [`gohealthcli sync`](commands/sync.html) — Archive Google Health Data Points and supported Rollups.
- [`gohealthcli status`](commands/status.html) — Summarise archive counts and newest synced timestamps.
- [`gohealthcli query`](commands/query.html) — Run guarded read-only SQL over the Health Archive.
- [`gohealthcli export`](commands/export.html) — Write a normalised dataset to CSV or JSONL.
- [`gohealthcli raw`](commands/raw.html) — Print raw provider JSON for endpoint exploration.

# gohealthcli verification map

This directory is the maintained contract for user-facing `gohealthcli` proof. Read this index, then the matching feature file. A run through tests or an in-process Go function does not satisfy these recipes.

## Baseline preconditions

- Work from the repository root on macOS or Linux with Bash, Git, Go, and `script` available.
- Choose a unique run ID accepted by `[a-z0-9][a-z0-9._-]*`.
- Launch with `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli launch "$RUN_ID"`.
- Require `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli doctor "$RUN_ID"` to pass.
- Never drive a run root without the exact `.gohealthcli-verify-owner` marker.
- Never substitute a real OAuth client, Credential Store, Connection, archive, or Provider account.
- Require the helper's task-owned HOME/XDG environment so default-path output cannot expose a developer home path.

## Driving conventions

- Run every user-behavior action through the helper so it gets a fresh PTY and complete evidence triplet. Direct stdout/stderr replays are allowed only to supplement an already captured PTY action with stream-placement evidence.
- A fresh route means config, archive, and Attachment sidecar are absent.
- Initialization routes may create only `/tmp/gohealthcli-verify-$RUN_ID/{config,data}`.
- Backup routes may also create `/tmp/gohealthcli-verify-$RUN_ID/backup`; their Git remote is a local bare repository under that directory.
- Flags and SQL below are literal. Keep ordering and quoting unchanged.
- One entry point proves only itself. Record unreachable alternatives instead of borrowing proof from another route.

## Proof and skip reporting

- Evidence directory: `/tmp/gohealthcli-verify-evidence/$RUN_ID/`.
- Require command, transcript, exit code, and route proof files.
- Require `manifest.json`, append-only `events.jsonl`, phase-specific doctor snapshots, before/after repository-content fingerprints, and before/after Git status.
- A durable local write requires a separate read through the compiled CLI.
- A no-effect mode requires filesystem observation in addition to its output claim.
- Report the feature ID, entry point, exact evidence path, and unmet prerequisite for every skipped route.
- Preserve evidence during cleanup; remove only the owned scratch run.

## Feature entry contract

Each feature uses exactly four H2 sections: `Sub-features`, `How to get to it (user POV)`, `Driving it with verify-gohealthcli`, and `Gotchas`.

## Features

- [Initialize a Health Archive](initialize.md): isolated config/archive creation, permissions, idempotence boundary, and second-read migration proof.
- [Diagnose local setup](local-diagnostics.md): offline setup health, archive integrity, output modes, and the online authorization boundary.
- [Inspect a Health Archive](inspect-archive.md): status, guarded SQL, and schema description against local state.
- [Plan a Provider request](plan-provider-request.md): secret-free offline request planning with observed zero local effects.
- [Plan and inspect sync](sync-planning-and-history.md): empty Sync Run history, blocked local planning, and preflight behavior without a Connection.
- [Back up and restore an archive](encrypted-backup.md): task-local Git, age-encrypted shards, idempotence, restore, and second-read proof.
- [Reach connected Provider commands](connected-provider-commands.md): local failure gates and the explicit live-authorization boundary.
- [Discover commands](discover-commands.md): top-level help, command help, registry schema, and suggestions.
- [Browse the Provider catalog](provider-catalog.md): offline Data Type, scope, description, and discovery-drift surfaces.

## Command Registry coverage

- `init`: Initialize a Health Archive.
- `doctor`: Diagnose local setup.
- `catalog`: Browse the Provider catalog.
- `connect`, `identity`, `profile`, `settings`, `devices`, `irn-profile`: Reach connected Provider commands.
- `sync`: Plan and inspect sync.
- `backup`: Back up and restore an archive.
- `status`, `query`, `export`, `describe-schema`: Inspect a Health Archive.
- `raw`: Plan a Provider request.
- `completion`, `schema`, `docs-export-datasets`: Discover commands.

Global version output and the `help` verb also live under Discover commands. `self-check` compares this inventory with the binary's current registry.

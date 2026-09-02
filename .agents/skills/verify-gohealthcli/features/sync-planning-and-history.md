# Plan and inspect sync

Sync planning reports local readiness and predicted effects without Provider, Credential Store, token-refresh, archive-write, cursor, migration, or sidecar effects. Sync status reads recent Sync Runs from the archive.

## Sub-features

- `sync-status-empty` reports no recent Sync Runs in a fresh archive.
- `sync-plan-blocked` reports the missing Connection while every planning effect stays false.
- `sync-preflight` rejects a live sync before a Sync Run is created when no Connection exists.
- `sync-live` reads Provider data and writes Sync Runs, archived records, and a cursor; unreachable without explicit live authorization.

## How to get to it (user POV)

- Run `gohealthcli --db <archive> --json sync --status`.
- Run `gohealthcli --config <path> --db <archive> --json sync --types steps --from <start> --to <end> --plan`.
- Omit `--plan` only with an authorized Connection and intended Provider access.

## Driving it with verify-gohealthcli

Preconditions:

- `launch`, `drive initialize`, and helper doctor pass for the same run.
- The archive has no Connection or Sync Runs.

- Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" sync`.
- `sync-status-before.typescript` reports no recent Sync Runs.
- `sync-plan-blocked.typescript` reports `plan_blocked` and every planning effect false for a fixed `steps` window.
- `sync-live-unreachable.typescript` exercises normal sync and fails at the missing Connection preflight.
- `sync-sidecar-before.txt`, `sync-sidecar-after-plan.txt`, and `sync-sidecar-after-live.txt` must contain the same complete Attachment-sidecar tree fingerprint.
- `sync-status-after.typescript` and the complete SQLite file-set fingerprint prove that neither blocked path created a Sync Run or changed the archive, journal, WAL, or shared-memory sibling.

## Gotchas

- A blocked plan proves safe local preflight. It does not prove a ready plan with Connection metadata.
- `sync --status` may fence abandoned runs. The isolated archive has none, so its bytes must stay unchanged.
- A ready plan and a successful sync need a real Connection today. Do not seed one by writing SQLite directly.
- Live sync remains unreachable until the product has an approved compiled-CLI synthetic Provider boundary or Bram authorizes a real account run.

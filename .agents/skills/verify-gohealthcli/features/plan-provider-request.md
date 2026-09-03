# Explore Provider requests safely

`raw` plans or performs one Provider-shaped request without Sync Run ingestion. Planning exposes the sanitized request with observed zero effects; live reads and exact-byte file output remain behind explicit Connection and network prerequisites.

## Sub-features

- `raw-plan-identity-json` describes `getIdentity` without setup or network access.
- `raw-plan-data-type` resolves fixed list, Data Point get, reconcile, daily Rollup, and physical Rollup requests without setup.
- `raw-plan-no-effects` reports all planning effects false and leaves local setup absent.
- `raw-output-safety` rejects invalid destinations before credentials or the Provider, preserves existing files and symlink targets, and removes staging files after local setup failure.
- `raw-live-read` performs a credentialed Provider read; unreachable without explicit live-account authorization.

## How to get to it (user POV)

- Run `gohealthcli raw endpoint getIdentity --plan --json`.
- Run `gohealthcli raw data-type steps --from <boundary> --to <boundary> --timezone <zone> --plan --json`.
- Use `raw data-type <type> get --id <provider-id>`, `reconcile --source-family <family>`, `daily-rollup`, or `rollup --window <duration>` before the shared `--plan --json` flags for their request shapes.
- Remove `--plan` and plan-only `--json`/`--plain` for a live Provider-shaped response after real setup and Connection. Add `--output <new-path>` to write exact response bytes to a new private file.

## Driving it with verify-gohealthcli

Preconditions:

- `launch` and `doctor` pass for a fresh run.
- Explicit and isolated XDG-default config, archive, and Attachment sidecar paths are absent.

- **Identity plan.** Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" plan`. `plan-identity.command.txt` records the compiled-CLI action. The transcript must contain status `plan_ready`, target `getIdentity`, method `GET`, the exact sanitized production URL, no sensitive request fields, and exit `0`.
- **Observe no effects.** The helper requires every boolean in `planning_effects` to be false, then independently checks that both the explicit task paths and the actual isolated XDG-default config, archive, and sidecar paths remain absent. `plan-proof.txt` records the observation.
- **Data Type plans.** Separately labeled drives cover a `steps` list over a fixed RFC3339 range, a redacted `sleep` Data Point ID, a fixed civil `daily-resting-heart-rate` reconcile request, a `steps` daily Rollup, and a `steps` one-hour physical Rollup. Each transcript pins its endpoint family and all-false effects.
- **Safe output gates.** The drive checks the plan/output conflict, empty path, existing file, directory, symlink, file parent, missing parent, and group/other-writable parent. It proves existing bytes and mode plus the symlink target stay unchanged. A valid private destination followed by missing setup must leave neither a final nor staging file. Stream replays prove failures write nothing to stdout and remediation to stderr. A successful exact-byte output remains unreachable without a real Connection and Provider response.
- **Live read.** The drive attempts a normal identity read with explicit missing task-owned config/archive paths. It must fail locally before external access; `plan-live-unreachable.typescript` records the missing setup prerequisite. A successful live read remains unreachable without explicit authorization for a real Connection.

## Gotchas

- Trusting `--plan` or the output message alone is circular. Require both the effect object and filesystem absence.
- Identity planning proves no range behavior; it rejects range and timezone flags by design.
- A Data Type named boundary depends on a captured clock and timezone. Prefer fixed dates/RFC3339 for deterministic evidence.
- `--output` cannot be combined with `--plan`, and normal raw reads reject plan-only `--json` and `--plain` flags.
- Never capture page tokens, OAuth tokens, personal IDs, or real Provider payloads.

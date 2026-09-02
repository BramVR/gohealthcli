# Plan a Provider request

Raw planning shows the exact sanitized Google Health request shape while promising no Provider, credential, token, archive, migration, cursor, or sidecar effects.

## Sub-features

- `raw-plan-identity-json` describes `getIdentity` without setup or network access.
- `raw-plan-data-type` resolves a fixed Data Type range and timezone without setup.
- `raw-plan-no-effects` reports all planning effects false and leaves local setup absent.
- `raw-live-read` performs a credentialed Provider read; unreachable without explicit live-account authorization.

## How to get to it (user POV)

- Run `gohealthcli raw endpoint getIdentity --plan --json`.
- Run `gohealthcli raw data-type steps --from <boundary> --to <boundary> --timezone <zone> --plan --json`.
- Omit `--plan` for a live Provider-shaped response after real setup and Connection.

## Driving it with verify-gohealthcli

Preconditions:

- `launch` and `doctor` pass for a fresh run.
- Explicit and isolated XDG-default config, archive, and Attachment sidecar paths are absent.

- **Identity plan.** Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" plan`. `plan-identity.command.txt` records the compiled-CLI action. The transcript must contain status `plan_ready`, target `getIdentity`, method `GET`, the exact sanitized production URL, no sensitive request fields, and exit `0`.
- **Observe no effects.** The helper requires every boolean in `planning_effects` to be false, then independently checks that both the explicit task paths and the actual isolated XDG-default config, archive, and sidecar paths remain absent. `plan-proof.txt` records the observation.
- **Data Type plan.** The drive runs a separately labeled `steps` plan from `2026-01-01T00:00:00Z` to `2026-01-02T00:00:00Z` in `Europe/Brussels`. Its transcript contains the exact resolved bounds, requested timezone, and all-false effects.
- **Live read.** The drive attempts a normal identity read with explicit missing task-owned config/archive paths. It must fail locally before external access; `plan-live-unreachable.typescript` records the missing setup prerequisite. A successful live read remains unreachable without explicit authorization for a real Connection.

## Gotchas

- Trusting `--plan` or the output message alone is circular. Require both the effect object and filesystem absence.
- Identity planning proves no range behavior; it rejects range and timezone flags by design.
- A Data Type named boundary depends on a captured clock and timezone. Prefer fixed dates/RFC3339 for deterministic evidence.
- Never capture page tokens, OAuth tokens, personal IDs, or real Provider payloads.

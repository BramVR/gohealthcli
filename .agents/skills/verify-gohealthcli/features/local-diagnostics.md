# Diagnose local setup

`doctor` inspects local config, archive schema, Connection count, token status, and Attachment sidecar integrity. It contacts Google only with `--online`.

## Sub-features

- `doctor-json` returns the stable local diagnostic envelope.
- `doctor-plain` returns script-friendly key/value lines.
- `doctor-attachments` reports the owner-controlled sidecar path and omits the orphan block when no orphans exist.
- `doctor-online` refreshes tokens and reads Provider reachability; unreachable without an authorized Connection.

## How to get to it (user POV)

- Run `gohealthcli --config <path> --db <path> --json doctor`.
- Replace `--json` with `--plain` for key/value output.
- Add `--online` only when a real Connection and network access are intentional.

## Driving it with verify-gohealthcli

Preconditions:

- `launch`, `drive initialize`, and helper doctor pass for the same run.

- Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" local`.
- `local-doctor-json.typescript` reports `ok`, schema state, zero Connections, the owner-only Attachment root, and no orphan block.
- `local-doctor-plain.typescript` reports zero Connections through the plain output contract.
- `local-doctor-online-unreachable.typescript` stops at the missing Connection. It exits `1` before Credential Store or Provider access.

## Gotchas

- Helper doctor checks the verification run. Product `gohealthcli doctor` checks user-facing local setup. Keep both.
- Default doctor may inspect and migrate the task-owned archive. It does not read credentials or contact Google.
- `doctor --online` is a different external-access contract. Do not claim it from the offline result.
- A zero-Connection archive proves the local diagnostic boundary, not token health.

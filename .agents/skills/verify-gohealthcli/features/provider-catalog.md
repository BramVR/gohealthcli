# Browse the Provider catalog

The catalog lets users inspect compiled Google Health Data Types and OAuth scopes, describe one Data Type with the committed discovery snapshot, and compare the catalog with a bounded discovery document without config, credentials, an archive, or Provider access.

## Sub-features

- `catalog-list-json` lists compiled Data Types in canonical order.
- `catalog-scopes-json` groups exact OAuth scopes with their Data Types.
- `catalog-describe-json` describes `steps` from compiled facts plus the committed discovery snapshot.
- `catalog-verify-offline` reports the expected known-gap status against the committed public discovery fixture.
- `catalog-live` uses the public discovery endpoint; unreachable without explicit network intent during isolated verification.

## How to get to it (user POV)

- Run `gohealthcli catalog list --json`.
- Run `gohealthcli catalog scopes --json`.
- Run `gohealthcli catalog describe steps --json`.
- Run `gohealthcli catalog verify --discovery <path> --json` for a bounded offline document.
- Use `catalog describe steps --live` or omit `--discovery` from `catalog verify` for the live public discovery endpoint.

## Driving it with verify-gohealthcli

Preconditions:

- `launch` and `doctor` pass. Config and archive state are irrelevant because offline catalog actions do not read them.

- **List.** Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" catalog`. `catalog-list.typescript` contains the `data_types` array and `steps`; exit is `0`.
- **Scopes.** The drive runs `catalog scopes --json`. `catalog-scopes.typescript` contains the scopes array and the activity-and-fitness scope used by `steps`; exit is `0`.
- **Describe.** The drive runs `catalog describe steps --json`. `catalog-describe.typescript` identifies `steps`, `compiled_catalog`, and `committed_snapshot`; exit is `0`.
- **Offline verify.** Launch stages `internal/googlehealth/testdata/google-health-discovery-v4.json` into the owned run; the drive passes that staged path explicitly. `catalog-verify.typescript` reports `verified_with_known_gaps`; exit is `0`.
- **Live discovery.** Do not substitute live discovery for offline proof. Record `catalog-live` as unreachable unless the user explicitly authorizes network access for that run.

## Gotchas

- Bare `catalog verify` is live by default. Always pass the committed fixture in this verification route.
- `catalog describe` uses the committed snapshot by default; `--live` changes the external-access contract.
- `verified_with_known_gaps` is expected for the committed discovery revision and is not product drift.
- Catalog facts do not prove the current Connection has matching granted scopes.

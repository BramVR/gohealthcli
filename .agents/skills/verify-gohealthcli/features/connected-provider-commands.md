# Reach connected Provider commands

Connection setup and Provider reads require a real OAuth client, Credential Store, consenting Google Identity, granted scopes, and network access. Isolated verification proves their local gates and records the live paths as unreachable.

## Sub-features

- `connect` starts or completes OAuth and writes runtime tokens to the configured Credential Store.
- `identity`, `profile`, `settings`, `devices`, and `irn-profile` read Provider identity metadata and may append snapshots.
- `sync-live` reads Provider records and writes archive state.
- `provider-gates` reject every command before credentials or network when isolated setup is absent.

## How to get to it (user POV)

- Run `gohealthcli connect` after `init` for an interactive Connection.
- Use `connect --headless-start` and pipe the complete redirected loopback URL to `connect --complete` on a headless host.
- Run `identity`, `profile`, `settings`, `devices`, `irn-profile`, or `sync` after Connection setup and any required scope grants.

## Driving it with verify-gohealthcli

Preconditions:

- `launch` and helper doctor pass for a fresh run.
- Config, archive, sidecar, and Connection are absent.

- Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" connected`.
- `connected-doctor-missing.typescript` proves the missing-setup boundary.
- Separate command triplets exercise `connect`, `identity`, `profile`, `settings`, `devices`, `irn-profile`, and `sync`. Each exits locally with its stable failed status.
- Every transcript must report the stable missing-config message. The task environment denies external proxy traffic and shadows OS Credential Store commands with an access tripwire.
- `connected-proof.txt` confirms config, archive, sidecar, and Credential Store tripwire remain absent.

## Gotchas

- Local gate failures do not prove OAuth, token refresh, granted-scope behavior, Provider payloads, snapshot persistence, or successful sync.
- Authorization URLs, redirected loopback URLs, tokens, Google identities, and payloads must never enter evidence.
- Do not add a hidden production endpoint override merely to make this route green.
- A compiled-CLI synthetic Provider path needs an accepted design that preserves production request construction and blocks accidental use outside task-owned verification.

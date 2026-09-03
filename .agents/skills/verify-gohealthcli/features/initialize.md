# Initialize a Health Archive

Initialization creates an owner-controlled config, empty SQLite Health Archive, and Attachment sidecar root without starting OAuth or contacting Google.

## Sub-features

- `init-json` creates fresh local setup and returns the stable JSON result.
- `init-permissions` creates owner-only config, archive, and sidecar paths after validating the owner-only OAuth reference.
- `init-persistence` exposes the applied schema through a separate guarded query.
- `init-existing` reports `already_initialized` when the same setup is initialized again.

## How to get to it (user POV)

- Run `gohealthcli --config <path> --db <path> --json --no-input init --oauth-client-file <path>`.
- Run the same command again against the initialized paths to reach the existing-setup result.
- Confirm the archive through `gohealthcli --db <path> --json query <sql>`.

## Driving it with verify-gohealthcli

Preconditions:

- `launch` and `doctor` pass for a fresh run.
- Config, archive, and sidecar paths do not exist.

- **Create setup.** Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" initialize`. The `initialize.command.txt` action invokes the compiled binary with only task-owned paths. `initialize.typescript` contains JSON status `initialized`; exit is `0`.
- **Confirm durable state.** The same drive runs a second compiled-CLI action named `initialize-confirm`. Its guarded query must return the version and name of the latest migration declared by the current source; exit is `0`.
- **Inspect path safety.** `initialize-proof.txt` records owner-only checks for config, archive, OAuth client file, and sidecar directory.
- **Existing setup entry.** The drive reruns the same command as `initialize-existing`. Its transcript contains JSON status `already_initialized`; exit is `0`.
- **Sequence evidence.** `doctor-before-initialize.txt` proves fresh state; `doctor-after-initialize.txt` proves created state. `events.jsonl` retains both phases.

## Gotchas

- A normal Google OAuth client file is sensitive. Use only the helper's synthetic `client_id` and `client_secret` fixture.
- `init` writes local state even though the Provider remains untouched.
- Never reroute `--config` or `--db` outside `/tmp/gohealthcli-verify-$RUN_ID`.
- File existence is insufficient persistence proof; require the separate `query` result.
- Initialization proof does not prove OAuth, Connection, sync, Provider reads, real-data normalization, or Windows permissions.
- Reinitializing existing setup may apply pending migrations and recreate a missing Attachment sidecar before reporting `already_initialized`; it is not an unconditional no-op.

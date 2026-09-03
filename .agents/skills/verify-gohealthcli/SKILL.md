---
name: verify-gohealthcli
description: "Verify gohealthcli through its real CLI in isolated local state with terminal evidence."
---

# Verify gohealthcli

Drive the compiled `gohealthcli` binary, never an in-process test seam. Use this skill for user-facing CLI proof, regression verification, or release smoke checks. Read [features/README.md](features/README.md) and the matching feature file before driving.

Never use a real OAuth client, Connection, Credential Store, or Health Archive. The helper creates synthetic OAuth client metadata and disposable state under a run-specific `/tmp` directory. Live Provider reads, OAuth, external Git remotes, and any path outside the owned run directory are out of scope unless Bram explicitly authorizes them. Offline sync preflight and encrypted backup against a task-owned local Git remote are supported.

## Launch

From the repository root, choose a unique lowercase run ID and launch:

```bash
RUN_ID="verify-$(date -u +%Y%m%d-%H%M%S)-$$"
VERIFY=".agents/skills/verify-gohealthcli/helpers/verify-gohealthcli"
"$VERIFY" launch "$RUN_ID"
```

`launch` refuses an existing run directory. It creates `/tmp/gohealthcli-verify-$RUN_ID`, records ownership in `.gohealthcli-verify-owner`, creates an owner-only synthetic Desktop OAuth client file and task HOME, stages the committed public discovery fixture, and compiles a task-owned binary with the repository's Makefile stamp values. The build source is a task-owned snapshot containing exactly tracked and non-ignored untracked files covered by the repository fingerprint; ignored files cannot affect the binary. It does not write `dist/` or reuse a shared build. Readiness requires a successful `--json --version` invocation plus stable binary and repository-content fingerprints.

There is no long-running process or port. User-behavior actions run as short-lived CLI processes in fresh PTYs through `/usr/bin/script` on macOS or `script` on Linux, with `HOME`, `XDG_CONFIG_HOME`, and `XDG_DATA_HOME` rooted inside the owned run. The discover route additionally replays bare and explicit help without a PTY to prove their stdout/stderr placement.

## Doctor

Run the read-only ownership and readiness check before every drive:

```bash
"$VERIFY" doctor "$RUN_ID"
```

Doctor requires the exact owner marker, a non-symlink run root and task HOME, the launch-time binary hash and repository-content fingerprint, an executable task-owned binary whose stamped commit matches the checkout, exact owner-only synthetic OAuth and public-discovery fixtures, and either absent or regular task-owned config/archive paths. It never opens the Health Archive, reads a Credential Store, or contacts Google. Repeated standalone checks allocate `doctor-manual-<n>.txt`; launch and every drive retain separate phase-specific doctor files.

Refuse to drive if doctor fails. Never repair, delete, or adopt an unverified run directory.

## Drive

Use one mapped route. Each route records the literal command, PTY transcript, and exit code.

```bash
"$VERIFY" drive "$RUN_ID" initialize
"$VERIFY" drive "$RUN_ID" local
"$VERIFY" drive "$RUN_ID" inspect
"$VERIFY" drive "$RUN_ID" plan
"$VERIFY" drive "$RUN_ID" sync
"$VERIFY" drive "$RUN_ID" backup
"$VERIFY" drive "$RUN_ID" connected
"$VERIFY" drive "$RUN_ID" discover
"$VERIFY" drive "$RUN_ID" catalog
```

- `initialize`: requires a fresh run. Executes `init --json`, confirms the newest migration through `query --json`, then proves the same setup returns `already_initialized`.
- `local`: requires an initialized run. Executes product `doctor` in JSON and plain modes, then proves `doctor --online` stops at the missing Connection.
- `inspect`: requires an initialized run. Exercises status modes, accepted and rejected query paths, schema JSON and SQL, and empty CSV/JSONL exports.
- `plan`: requires a fresh run. Executes identity and fixed-range Data Type plans, checks every reported planning effect is false, exercises raw output validation and cleanup without Provider access, confirms local paths remain absent, then records the live-read path as unreachable at missing isolated setup.
- `sync`: requires an initialized run without a Connection. Reads empty Sync Run history, records a blocked plan with all execution effects false, and proves normal sync fails before creating a Sync Run.
- `backup`: requires an initialized run. Creates a task-owned bare Git remote, encrypts and pushes the empty archive locally, proves unchanged-push idempotence, restores to a new archive, and reads it through the compiled CLI.
- `connected`: requires a fresh run. Exercises the local setup gate for connect, identity snapshots, and sync without touching a Credential Store or Provider.
- `discover`: executes bare help, top-level `help`, both command-help forms, `schema --json`, and the expected-failure typo-suggestion path.
- `catalog`: executes offline `list`, `scopes`, `describe steps`, and `verify` against the committed public discovery snapshot.

Do not infer that one route proves another. Exact entry points and invalidating traps live in the feature map.

## Evidence

Evidence persists below the owner-only, non-symlink `/tmp/gohealthcli-verify-evidence/` parent at `$RUN_ID/`. Every PTY CLI invocation produces:

- `<label>.command.txt`: literal shell-escaped user action.
- `<label>.typescript`: real PTY transcript containing resulting output.
- `<label>.exit.txt`: process exit code.

Direct stream-placement replays produce `<label>.command.txt`, `<label>.stdout.txt`, `<label>.stderr.txt`, and `<label>.exit.txt` instead of a PTY transcript. They use the same clean task environment and only supplement an already captured PTY action.

`manifest.json` records evidence schema version, run identity, source commit, binary hash, platform, lifecycle, and driven routes. `events.jsonl` is append-only. Launch and cleanup compare repository-content fingerprints as well as Git status. Doctor writes non-overwritable `doctor-launch.txt`, `doctor-before-<route>.txt`, and `doctor-after-<route>.txt` snapshots. Every drive claims a non-overwritable `<route>-attempted.txt` marker before execution and writes `<route>-proof.txt` only after assertions and any second read pass. Command evidence uses a fixed system PATH and contains no ambient developer PATH entries.

Initialization proof requires both the `initialized` result and a separate `query_completed` result naming the latest migration. Planning proof requires the real JSON effect report plus filesystem absence checks; the word `plan` alone is not proof. Never copy personal paths, identifiers, tokens, or Provider payloads into evidence.

## Cleanup

Clean up only the verified run root:

```bash
"$VERIFY" cleanup "$RUN_ID"
```

Cleanup first validates the exact owner marker and rejects symlinks. No process teardown is needed because every CLI invocation is short-lived. It removes `/tmp/gohealthcli-verify-$RUN_ID` and leaves `/tmp/gohealthcli-verify-evidence/$RUN_ID/` intact. It then confirms the scratch root is absent and evidence remains.

After cleanup:

```bash
test ! -e "/tmp/gohealthcli-verify-$RUN_ID"
test -d "/tmp/gohealthcli-verify-evidence/$RUN_ID"
git status --short
```

Only the intended verification-skill source files may appear in Git status.

## Self-check and smoke suite

Run structural drift checks after editing the skill:

```bash
"$VERIFY" self-check
```

`self-check` validates helper syntax, the four-section feature contract, feature-index links, and exact Command Registry coverage.

Run every mapped feature with one command:

```bash
SUITE_ID="smoke-$(date -u +%Y%m%d-%H%M%S)-$$"
"$VERIFY" suite "$SUITE_ID" smoke
```

The suite uses one initialized state run plus separate fresh runs for planning, discovery, catalog, and connected-command gates. It cleans every owned run root and writes `/tmp/gohealthcli-verify-evidence/$SUITE_ID/summary.json`. Child evidence remains in sibling directories.

## Helpers

One owned helper exists: [helpers/verify-gohealthcli](helpers/verify-gohealthcli).

```bash
.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli self-check
.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli suite <run-prefix> [smoke]
.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli <launch|doctor|cleanup> <run-id>
.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive <run-id> <route>
```

Requirements: Bash, the local Go toolchain declared by `go.mod`, a populated local Go module download cache, Git, and `/usr/bin/script`; `shasum` or `sha256sum` for identities. Builds use task-owned caches, `GOWORK=off`, `GOTOOLCHAIN=local`, and the existing module cache as a read-only file proxy. Supported harness hosts are macOS and Linux. Windows CLI behavior remains a separate entry point until an equivalent isolated PowerShell/ConPTY helper exists; do not claim it from a macOS or Linux run.

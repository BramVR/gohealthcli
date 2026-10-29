# Read an archived exercise route

`route` writes the archived TCX route of one exercise Data Point as its exact bytes, offline, after verifying the sidecar against its Attachment row.

## Sub-features

- `route-help` documents the exercise positional and the `--format`, `--output`, and `--stdout` flags.
- `route-missing-destination` rejects an invocation without `--output` or `--stdout` as `flag_invalid`.
- `route-unknown-exercise` rejects an identifier that matches no archived Data Point as `flag_invalid` before any sidecar read, leaves the requested output path absent, and leaves the SQLite file set and sidecar tree unchanged.

## How to get to it (user POV)

- Run `gohealthcli route --help`.
- Run `gohealthcli --db <archive> --json route <exercise>` without a destination.
- Run `gohealthcli --db <archive> --json route <exercise> --output <path>` for an exercise the archive does not hold.
- With an archived route, `route <exercise> --output <path>` or `--stdout` writes its exact bytes.

## Driving it with verify-gohealthcli

Preconditions:

- `launch`, `drive initialize`, and `doctor` pass for the same run.
- The archive is a regular task-owned file.

- **Help entry.** Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" route`. The help transcript lists `--output` and `--stdout`.
- **Destination entry.** The JSON envelope reports `flag_invalid` and `route requires --output PATH or --stdout`.
- **Unknown exercise entry.** The JSON envelope reports `flag_invalid` with `no archived Data Point matches`. The output path stays absent. The complete SQLite file set and the sidecar tree fingerprint are unchanged.

## Gotchas

- The success path needs an archived TCX Attachment, which only `sync --types exercise` with the `tcx` scope can create. That needs Provider access, so this offline route cannot reach it. Go tests and the maintainer's live gate prove exact bytes, newest selection, integrity failures, and the owner-only file.
- An empty archive proves identifier and destination contracts only, not route bytes.
- Do not claim human-mode, config-resolved, or Windows paths from these `--db --json` entries.

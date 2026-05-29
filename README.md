# gohealthcli

`gohealthcli` is a planned local-first, read-only health data archive for data
available through the Google Health API.

The project is intentionally docs-first. The current goal is to make the domain
model, scope, risks, and early architecture explicit before writing the CLI.

## Current Direction

- Go CLI modeled after `gobankcli` and `gogcli` ergonomics.
- Google Health API as the primary provider.
- Fitbit / Pixel Watch / Google Watch data accessed through Google Health API.
- Local SQLite archive with raw API JSON preserved.
- Stable scriptable output: human, `--json`, and `--plain`.
- Read-only first: no writes, deletes, or user health mutations.

## Docs

- [CONTEXT.md](./CONTEXT.md): project glossary only, used by grill-style review.
- [docs/plan.md](./docs/plan.md): product and implementation plan.
- [docs/commands.md](./docs/commands.md): planned CLI surface.
- [docs/data-model.md](./docs/data-model.md): archive model sketch.
- [docs/security.md](./docs/security.md): local credentials and health data safety.
- [docs/research.md](./docs/research.md): source-backed research notes.
- [docs/adr/](./docs/adr): short architectural decision records.

## Status

Early implementation. The CLI is runnable with command dispatch, `--version`,
and a local-only `doctor` diagnostic that reports whether a gohealthcli setup
exists. Output is available in human, `--json`, and `--plain` modes, with
machine-readable data on stdout and human hints on stderr.

```bash
gohealthcli --version
gohealthcli doctor            # human status on stdout, hints on stderr
gohealthcli doctor --json     # stable JSON on stdout
gohealthcli doctor --plain    # stable key/value on stdout
```

`doctor` exits non-zero when the local setup is missing, so scripts can gate on
a healthy setup. Run the local test gate with `make check`.


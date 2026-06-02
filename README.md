# gohealthcli

`gohealthcli` is a local-first, read-only health data archive for data available
through the Google Health API.

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

First CLI tracer in progress. `gohealthcli init` creates local config and an
empty Health Archive, `gohealthcli doctor` validates local setup offline,
`gohealthcli connect` anchors a Google Identity, `gohealthcli identity`
refreshes it, and `gohealthcli profile` archives Profile Snapshots.
`gohealthcli sync` archives steps Data Points, wearable-filtered steps, and
explicit steps daily Rollups idempotently, `gohealthcli status` summarizes the
local Health Archive, `gohealthcli query` runs guarded read-only SQL over the
archive, `gohealthcli export daily-steps` writes CSV or JSONL from the
normalized daily steps view, `gohealthcli raw` prints provider JSON for
exploration, and `gohealthcli --version` is available.

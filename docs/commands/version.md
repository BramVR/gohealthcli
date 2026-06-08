---
title: "gohealthcli --version"
description: "Build-stamped version, commit, and built identifiers in plain or JSON shape."
---

`--version` is a top-level flag (not a subcommand) that prints the
build-stamped identifiers and exits 0. The flag is checked before any
subcommand dispatch happens, so it works with every flag combination the
mutual-exclusion check accepts (`--version`, `--version --json`,
`--version --plain`).

The three identifiers are package-level `var`s wired by the Makefile's
production `build` target via `-X` ldflags:

| Field | Source (production build) | Default when unstamped |
| ----- | ------------------------- | ---------------------- |
| `version` | `git describe --tags --always --dirty` | `dev` |
| `commit`  | `git rev-parse HEAD`                   | `dev` |
| `built`   | `date -u +%Y-%m-%dT%H:%M:%SZ`          | `dev` |

A bare `go build ./...` produces a binary whose `--version` reports
`gohealthcli dev (dev built dev)` — usable, but unstamped. Use
`make build` to embed real values; `make build-info` prints the values
the next `make build` would embed without round-tripping through the
binary.

## Plain shape

`--version` (default mode, equivalent to `--version --plain`) prints a
single human-readable line terminated by exactly one newline:

```
gohealthcli <version> (<commit> built <built>)
```

Example (production build):

```
gohealthcli v0.3.1 (58da9fdac4 built 2026-06-08T11:42:13Z)
```

## JSON shape

`--version --json` prints a single-line JSON object with stable key
order (`version`, `commit`, `built` — declaration order is the property
`encoding/json` guarantees), terminated by exactly one newline:

```json
{"version":"<version>","commit":"<commit>","built":"<built>"}
```

Example (production build):

```json
{"version":"v0.3.1","commit":"58da9fdac4","built":"2026-06-08T11:42:13Z"}
```

The shape is suitable for `jq` and any tool that pattern-matches on
keys rather than positional fields. The keys mirror the
package-level vars in `cmd/gohealthcli/version.go`.

## Mutual exclusion

`--plain` and `--json` are mutually exclusive everywhere the binary
emits structured output — `--version` included. Passing both is
rejected before any output is written:

```
$ gohealthcli --plain --json --version
gohealthcli: --plain and --json are mutually exclusive
```

The failure routes through the unified Failure Reporter (PRD #143
slice 7) with status `flag_invalid` and exit code 1.

## Exit codes

- `0` — the version line was printed successfully (plain or JSON).
- `1` — `--plain` and `--json` were both passed (mutual-exclusion
  rejection). The version line is not printed.

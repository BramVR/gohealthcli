package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// version, commit, and built are the three identifiers the build pipeline
// stamps into the binary via `-ldflags "-X main.version=... -X main.commit=...
// -X main.built=..."`. They default to "dev" so a `go build ./...` invocation
// without ldflags still produces a sensible (if unstamped) value; the
// production Makefile target (see the repo Makefile) wires them to
// `git describe --tags --always --dirty`, `git rev-parse HEAD`, and
// `date -u +%Y-%m-%dT%H:%M:%SZ`.
//
// They are package-level `var`s (not `const`) precisely so the linker can
// override them. PRD #143 slice 5 (issue #174).
var (
	version = "dev"
	commit  = "dev"
	built   = "dev"
)

// versionJSON is the on-the-wire shape of `--version --json`. Field order
// (version, commit, built) is fixed by the json tag declarations so the
// emitted bytes are stable across builds — downstream tooling can pattern-
// match without worrying about Go map iteration order.
type versionJSON struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
}

// RenderVersion writes the version line to stdout in either the plain or
// JSON shape, terminating with a single newline. The plain shape is the
// canonical human form:
//
//	gohealthcli <version> (<commit> built <built>)
//
// The JSON shape is a single-line object whose keys mirror the package
// vars:
//
//	{"version":"<v>","commit":"<c>","built":"<b>"}
//
// Mutual exclusion of --plain and --json is enforced upstream (in
// runWithRuntime, before RenderVersion is called) so this function does
// not need to defend against mode.json && mode.plain.
func RenderVersion(mode outputMode, stdout io.Writer) {
	if mode.json {
		// json.Marshal (not Encoder) so we control the trailing newline
		// exactly — Encoder always appends its own, but we want the same
		// single-newline contract as the plain branch.
		payload, _ := json.Marshal(versionJSON{
			Version: version,
			Commit:  commit,
			Built:   built,
		})
		fmt.Fprintf(stdout, "%s\n", payload)
		return
	}
	fmt.Fprintf(stdout, "gohealthcli %s (%s built %s)\n", version, commit, built)
}

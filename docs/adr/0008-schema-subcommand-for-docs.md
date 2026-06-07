---
status: "accepted"
summary: "Ship a hidden `gohealthcli schema --json` subcommand so the Project Site command reference has a single source of truth in the binary."
read_when:
  - "Adding, renaming, or removing a subcommand or flag."
  - "Changing the JSON shape emitted by `gohealthcli schema --json`."
  - "Editing the Project Site command-reference generator or `make docs-commands`."
---
# Auto-generate the Project Site Command Reference from `schema --json`

The Project Site needs a per-subcommand reference page. The binary owns the canonical list of subcommands, short descriptions, long descriptions, and flags, so it ships a hidden `schema --json` subcommand that emits that list as a stable JSON document. The schema is emitted from a single in-binary command registry that will also drive dispatch and `--help` once the follow-up slices land (#75 and #76), so those three surfaces cannot drift apart. The Project Site build script consumes the JSON and renders one `docs/commands/<name>.md` page per subcommand; the auto-generated `docs/commands.md` index and the CI drift check land in separate slices (#73 and #74) so each can be reviewed on its own.

Rejected alternatives. Parsing `gohealthcli <cmd> --help` output is brittle because that text is shaped for humans and is allowed to change formatting freely. A Go AST walker over `cmd/gohealthcli/*.go` couples the Project Site build to the source file layout and breaks when commands are refactored. Hand-maintaining the reference drifts from the binary on every flag change.

Generated `docs/commands/<name>.md` files are committed to the repository. Committing them means pull requests show flag and description drift in the diff, the Pages build does not need the Go toolchain, and the public `docs/commands/` URLs remain stable across builds. `make docs-check` (#74) fails CI when the committed files do not match a fresh `schema --json` so the binary and the Project Site cannot diverge silently.

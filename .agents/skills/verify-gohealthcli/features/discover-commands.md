# Discover commands

Users can inspect version metadata, discover commands, generate shell completion, and reach the two hidden documentation contracts without local setup.

## Sub-features

- `discover-bare` prints top-level help to stdout with exit `0`.
- `discover-help-command` prints one command's long help and flags.
- `discover-schema` emits the machine-readable Command Registry.
- `discover-version` emits stamped build identity.
- `discover-completion` generates Bash, Zsh, Fish, and PowerShell completion from the registry.
- `discover-docs-datasets` applies the normalized-dataset generator to a staged README copy.
- `discover-suggestion` suggests close visible command names for a typo and exits nonzero.

## How to get to it (user POV)

- Run bare `gohealthcli` or `gohealthcli help` for top-level help.
- Run `gohealthcli help <command>` or `gohealthcli <command> --help` for command help.
- Run `gohealthcli schema --json` for the hidden registry contract.
- Run `gohealthcli --plain --version` for stamped build identity.
- Run `gohealthcli completion <bash|zsh|fish|powershell>` for shell-native completion.
- Run `gohealthcli docs-export-datasets --readme <path>` only as a build-time documentation check.
- Run a misspelled command such as `gohealthcli stauts` for a human suggestion.

## Driving it with verify-gohealthcli

Preconditions:

- `launch` and `doctor` pass. No initialized archive is required.

- **Bare entry.** Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" discover`. `discover-bare.typescript` contains the `Subcommands` and `Global flags` blocks; separate stream evidence proves the help text is on stdout and stderr is empty.
- **Top-level help entry.** The drive runs `help`. `discover-help.typescript` contains the same help blocks; separate stream evidence proves the explicit-help text is on stderr and stdout is empty.
- **Command-help entries.** The drive runs both `help sync` and `sync --help`. The first contains sync's long planning prose and flags; the second contains the flag-package usage block. Both include `--types` and exit `0`.
- **Registry entry.** The drive runs `schema --json`. `discover-schema.typescript` contains registry schema version and the sync command definition; exit is `0`.
- **Version entry.** `discover-version-plain.typescript` contains the stamped single-line form. Launch separately captured JSON version and matching commit.
- **Completion entries.** Four transcripts retain native completion scripts for Bash, Zsh, Fish, and PowerShell.
- **Dataset generator.** The drive rewrites only the staged task-owned README and requires it to remain byte-identical to the committed README.
- **Suggestion entry.** The drive runs `stauts` through an expected-failure PTY. `discover-suggestion.typescript` contains the unknown-command failure, `Did you mean: status?`, and the help remediation; its recorded exit is `1`.

## Gotchas

- Bare `gohealthcli` writes help to stdout; `gohealthcli help` follows flag-package stderr conventions. They are distinct output contracts.
- Hidden `schema` is omitted from user suggestions even though `help schema` works.
- Hidden `docs-export-datasets` can rewrite its target. Never point verification at the repository README.
- Completion generation proves emitted scripts, not installation into a user's shell profile.
- A nonzero typo route is expected behavior only when the failure envelope and suggestion both match.
- macOS/Linux proof does not establish PowerShell completion or Windows console behavior.

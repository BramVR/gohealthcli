---
title: "gohealthcli help"
description: "Discoverability verbs for the gohealthcli command surface."
---

`help` is an alias verb introduced by PRD #143 slice 2 so the binary feels
the same as `git`, `kubectl`, or `docker`: a discoverability surface that
does not require remembering whether the flag was `-h`, `--help`, or
`help`.

The verb does not appear in the Command Registry — it is a top-level
dispatch path inside `runWithRuntime`, so `gohealthcli schema --json` (the
machine-readable registry) does not list it. The contract below is the
authoritative reference instead.

## Usage

```
gohealthcli help
gohealthcli help <command>
```

Both forms are equivalent to their `--help` siblings:

- `gohealthcli help` ≡ `gohealthcli --help` — prints the same top-level
  Subcommands block plus the Global flags listing. The output goes to
  stderr (matching the stdlib `flag` package convention; a bare
  `gohealthcli` with no arguments prints the same block to stdout and
  exits 0, so scripts that want the help text on stdout should use the
  bare form).
- `gohealthcli help <command>` ≡ `gohealthcli <command> --help` — prepends
  the registry entry's `Long` prose, then prints the subcommand's flag
  block as the stdlib `flag` package would. Hidden registry entries (the
  build-time `schema` command) are accepted by name so the prose is still
  reachable.

A trailing positional after the top-level form (`gohealthcli help --help
status`) is rejected with `unexpected arguments: status` so a typo fails
loudly instead of being silently dropped. Likewise, anything after the
subcommand name (`gohealthcli help status extra`) errors with
`unexpected arguments after status: extra`.

An unknown subcommand name (`gohealthcli help bogus`) is rejected with
`unknown command: bogus`. The error routes through the unified Failure
Reporter (PRD #143 slice 7), so the failure envelope honours the global
`--json` / `--plain` mode the same way every other subcommand does.

## Did-you-mean suggestions

When an unknown verb is passed to the top-level dispatch (any token that
is neither a registered subcommand nor `help`), the binary prints:

```
gohealthcli: unknown command: <typo>
Did you mean: <suggestion>[, <suggestion>]?
Run 'gohealthcli --help' for a list of commands.
```

- The `unknown command:` line is routed through the unified Failure
  Reporter and lands on stderr in default and `--plain` modes, or as a
  JSON envelope on stdout in `--json` mode.
- The `Did you mean` line is computed by `commandRegistry.Suggest`. It
  uses the classic Levenshtein edit distance with a fixed cutoff of `2`
  — enough to catch one transposition or two character edits
  (`stauts` → `status`, `intii` → `init`), tight enough to suppress
  noise on genuinely unrelated input (`xyz` produces no suggestion).
  Suggestions are sorted by ascending distance and then by registry
  order so the output is deterministic. At most two candidates are
  printed.
- Hidden registry entries (`schema`) are filtered from the suggestion
  pool so they never leak to end users.
- In `--json` mode the discoverability lines are suppressed entirely so
  scripts parsing the stdout envelope do not see human-targeted stderr
  noise. The `Run 'gohealthcli --help'` line stays for default and
  `--plain` modes.
- An unknown command with no suggestion within the cutoff prints only
  the `unknown command:` line and the `--help` hint, with no `Did you
  mean` line at all.

## Examples

```bash
# Discoverability verbs.
gohealthcli                       # prints the help block to stdout, exit 0
gohealthcli help                  # prints the help block to stderr, exit 0
gohealthcli help status           # status's Long prose + status's --help block
gohealthcli help schema           # hidden entry; prose is still reachable

# Did-you-mean.
gohealthcli stauts                # "unknown command: stauts" + "Did you mean: status?"
gohealthcli intii                 # "unknown command: intii" + "Did you mean: init?"
gohealthcli xyz                   # no suggestion; just the --help hint.
gohealthcli --json stauts         # JSON failure envelope, no hint lines.
```

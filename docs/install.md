---
title: Install
description: Install gohealthcli with Homebrew, go install, or a local source build.
---

Pick the path that fits your setup. The CLI runs entirely on your machine — there is no service to sign up for and nothing to configure beyond your local Google OAuth client.

## Homebrew

Homebrew is the preferred install path on macOS and Linux:

```bash
brew install BramVR/tap/gohealthcli
gohealthcli --version
```

Upgrade with:

```bash
brew update
brew upgrade BramVR/tap/gohealthcli
```

## Go install

If you have a Go toolchain installed (1.22 or later), this is the fastest path. The binary lands in `$GOPATH/bin` (or `$HOME/go/bin` if `GOPATH` is unset). Make sure that directory is on your `PATH`.

```bash
go install github.com/BramVR/gohealthcli/cmd/gohealthcli@latest
gohealthcli --version
```

Upgrade with the same command — Go fetches the latest tag.

## From source

For local development or to track an unreleased branch, build from a clone.

```bash
git clone https://github.com/BramVR/gohealthcli.git
cd gohealthcli
go test ./...
go run ./cmd/gohealthcli --help
```

`go test ./...` exercises the full test suite locally. `go run ./cmd/gohealthcli` skips the install step and runs the binary directly from source.

## Verify the install

After any of the paths above, confirm the binary is on your `PATH` and reports a version:

```bash
gohealthcli --version
gohealthcli --version --json
gohealthcli --help
```

`--version` prints the build-stamped identifiers as
`gohealthcli <version> (<commit> built <built>)`; `--version --json` prints
the same three values as a single-line `{"version":..., "commit":..., "built":...}`
object.

The three identifiers are wired by `-ldflags "-X main.version=... -X
main.commit=... -X main.built=..."`. Only the repo's `make build` target
sets those flags; a plain `go install github.com/BramVR/gohealthcli/cmd/gohealthcli@latest`
and `go build ./...` both leave all three as `dev` (still a usable binary,
just unstamped). Clone the repo and run `make build` if you need a stamped
binary — see [docs/commands/version.html](commands/version.html).

`--help` lists the available subcommands. `gohealthcli help` and
`gohealthcli help <command>` are equivalent verbs that prepend the
registry's long-form prose to the standard flag block.

The first thing to run next is `gohealthcli init` — see the
[Quickstart](quickstart.html).

## What gets installed

The binary is statically linked and self-contained. It does not install a daemon, a launch agent, or a background service. Running `gohealthcli` only reads or writes when you ask it to.

Default local paths once `gohealthcli init` is run:

- Config: `~/.config/gohealthcli/config.toml`
- Health Archive: `~/.local/share/gohealthcli/gohealthcli.sqlite`
- Credential Store fallback: `~/.config/gohealthcli/tokens.json` (only if you opt out of the OS-native Credential Store)

These paths are visible to `doctor` and can be moved or backed up like any other file.

## Uninstall

There is no uninstaller. Remove the binary and the directories above:

```bash
rm "$(command -v gohealthcli)"
rm -rf ~/.config/gohealthcli ~/.local/share/gohealthcli
```

Run `gohealthcli doctor --plain` first if you want a summary of what is on disk.

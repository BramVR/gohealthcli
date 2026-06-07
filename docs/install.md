---
title: Install
description: Install gohealthcli with go install, the upcoming Homebrew tap, or a local source build.
---

Pick the path that fits your setup. The CLI runs entirely on your machine — there is no service to sign up for and nothing to configure beyond your local Google OAuth client.

## Go install (works today)

If you have a Go toolchain installed (1.22 or later), this is the fastest path. The binary lands in `$GOPATH/bin` (or `$HOME/go/bin` if `GOPATH` is unset). Make sure that directory is on your `PATH`.

```bash
go install github.com/BramVR/gohealthcli/cmd/gohealthcli@latest
gohealthcli --version
```

Upgrade with the same command — Go fetches the latest tag.

## Homebrew (coming soon)

A Homebrew tap is planned. Once it ships, the install command will be:

```bash
brew install BramVR/tap/gohealthcli
```

Until the tap is live, prefer `go install` above. The homepage will drop the **Coming soon** badge from this line when the tap is published.

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
gohealthcli --help
```

`--help` lists the available subcommands. The first thing to run next is `gohealthcli init` — see the [Quickstart](quickstart.html).

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

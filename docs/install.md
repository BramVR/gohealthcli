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

## Windows

Windows releases are unsigned amd64 ZIP archives. In PowerShell, replace
`vX.Y.Z` with the release tag:

```powershell
$Tag = "vX.Y.Z"
$ReleaseVersion = $Tag.TrimStart("v")
$Archive = "gohealthcli_${ReleaseVersion}_windows_amd64.zip"
$ReleaseBase = "https://github.com/BramVR/gohealthcli/releases/download/$Tag"

Invoke-WebRequest "$ReleaseBase/$Archive" -OutFile $Archive
Invoke-WebRequest "$ReleaseBase/checksums.txt" -OutFile checksums.txt

$ChecksumLine = Get-Content checksums.txt |
  Where-Object { $_ -match "  $([regex]::Escape($Archive))$" } |
  Select-Object -First 1
if (-not $ChecksumLine) { throw "No checksum found for $Archive" }
$ExpectedHash = ($ChecksumLine -split "\s+")[0]
$ActualHash = (Get-FileHash $Archive -Algorithm SHA256).Hash.ToLowerInvariant()
if ($ActualHash -ne $ExpectedHash.ToLowerInvariant()) {
  throw "Checksum mismatch for $Archive"
}

$InstallDir = Join-Path $HOME "bin\gohealthcli"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Expand-Archive $Archive -DestinationPath $InstallDir -Force

$UserPath = [string][Environment]::GetEnvironmentVariable("Path", "User")
if (($UserPath -split ";") -notcontains $InstallDir) {
  $NewUserPath = ($UserPath.TrimEnd(";") + ";" + $InstallDir).TrimStart(";")
  [Environment]::SetEnvironmentVariable(
    "Path",
    $NewUserPath,
    "User"
  )
}
$env:Path = "$InstallDir;$env:Path"
& (Join-Path $InstallDir "gohealthcli.exe") --version
```

The ZIP contains `gohealthcli.exe`, `LICENSE`, and `README.md`. Windows
SmartScreen may warn because the executable is not code-signed. After verifying
the SHA-256 checksum above, choose **More info** and **Run anyway** if you trust
this release. The warning does not prevent installation; the checksum verifies
the downloaded bytes but does not establish a signed publisher identity.

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

## Shell completion

`gohealthcli completion` generates a deterministic script from the same
Command Registry that drives dispatch, help, and the command reference. Script
generation does not read your config, Health Archive, Connection, credentials,
or the Provider. Add `--no-descriptions` after the shell name when you want
shorter candidates.

The generated script completes canonical export datasets, Data Types, raw
targets, scope keywords, Rollup modes, and source families. It preserves typed
comma-separated prefixes and omits duplicates. SQL, dates, credentials,
tokens, profiles, users, and providers are not completed; native filesystem
completion remains available only for path values.

### Bash

Load completion in the current session:

```bash
source <(gohealthcli completion bash)
```

For persistent completion with Homebrew's `bash-completion`:

```bash
gohealthcli completion bash > "$(brew --prefix)/etc/bash_completion.d/gohealthcli"
```

On Linux, write the script to the system completion directory instead:

```bash
gohealthcli completion bash | sudo tee /etc/bash_completion.d/gohealthcli >/dev/null
```

### Zsh

Load completion in the current session:

```zsh
autoload -Uz compinit && compinit
source <(gohealthcli completion zsh)
```

For persistent completion with Homebrew:

```zsh
gohealthcli completion zsh > "$(brew --prefix)/share/zsh/site-functions/_gohealthcli"
```

If you manage `fpath` yourself, write `_gohealthcli` to any directory already
listed there, then run `autoload -Uz compinit && compinit`.

### Fish

Load completion in the current session:

```fish
gohealthcli completion fish | source
```

For persistent completion:

```fish
mkdir -p ~/.config/fish/completions
gohealthcli completion fish > ~/.config/fish/completions/gohealthcli.fish
```

### PowerShell

Load completion in the current session:

```powershell
gohealthcli completion powershell | Out-String | Invoke-Expression
```

For persistent completion, add that line to your PowerShell profile:

```powershell
New-Item -ItemType Directory -Force (Split-Path $PROFILE) | Out-Null
Add-Content $PROFILE 'gohealthcli completion powershell | Out-String | Invoke-Expression'
```

## What gets installed

The binary is statically linked and self-contained. It does not install a daemon, a launch agent, or a background service. Running `gohealthcli` only reads or writes when you ask it to.

Default local paths once `gohealthcli init` is run:

- Config: `~/.config/gohealthcli/config.toml`
- Health Archive: `~/.local/share/gohealthcli/gohealthcli.sqlite`
- Credential Store: OS-native (`init` always writes `type = "os_native"`). The file fallback has no default path — opting out means setting `[credential_store] type = "file"` plus an explicit `path = ...` of your choosing in the config.

These paths are visible to `doctor` and can be moved or backed up like any other file.

## Uninstall

There is no uninstaller. Remove the binary and the directories above:

```bash
rm "$(command -v gohealthcli)"
rm -rf ~/.config/gohealthcli ~/.local/share/gohealthcli
```

Run `gohealthcli doctor --plain` first if you want a summary of what is on disk.

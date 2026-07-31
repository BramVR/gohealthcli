---
title: "gohealthcli completion"
description: "Generate a shell completion script."
---

Generate a shell-native completion script from the Command Registry for Bash, Zsh, Fish, or PowerShell. The output is deterministic and can be redirected to the shell's completion directory; generation does not read configuration, the Health Archive, a Connection, credentials, or the Provider.

Pass `--no-descriptions` to omit command and flag descriptions from shells that display them. See the [Install](../install.html#shell-completion) page for current-session and persistent setup commands for all four shells.

## Usage

```
gohealthcli completion <shell>
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--no-descriptions` | bool | `false` | disable completion descriptions |

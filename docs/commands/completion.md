---
title: "gohealthcli completion"
description: "Generate a shell completion script."
---

Generate a shell-native completion script from the Command Registry for Bash, Zsh, Fish, or PowerShell. The output is deterministic and can be redirected to the shell's completion directory; generation does not read configuration, the Health Archive, a Connection, credentials, or the Provider.

The generated script completes catalog-backed values such as export datasets, Data Types, raw targets, scope keywords, Rollup modes, and source families. Comma-separated values preserve the part already typed and omit duplicates. SQL, dates, credentials, tokens, profiles, users, and providers are never suggested; native filesystem completion remains enabled only for path values.

Pass `--no-descriptions` to omit command and flag descriptions from shells that display them. See the [Install](../install.html#shell-completion) page for current-session and persistent setup commands for all four shells.

## Usage

```
gohealthcli completion <shell>
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--no-descriptions` | bool | `false` | disable completion descriptions |

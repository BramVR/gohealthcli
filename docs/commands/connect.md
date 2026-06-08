---
title: "gohealthcli connect"
description: "Run the browser OAuth flow and anchor one Google Identity."
---

<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->

Open the system browser, run the installed-app OAuth flow against the OAuth client supplied at `init`, and store the resulting tokens in the OS-native Credential Store (Keychain on macOS, Credential Manager on Windows, Secret Service on Linux).

A Health Archive holds exactly one Connection. Running `connect` against an archive that already has a Connection refreshes the token material in place rather than adding a second identity.

`--add-scopes` extends an existing grant with optional scope keywords (`irn`, `ecg`) without re-running setup; Google's `include_granted_scopes=true` makes the resulting token cover the union of prior + new scopes. Use `connect --add-scopes irn` to unlock `gohealthcli irn-profile` and Tier 2 ECG / IRN Data Types.

`--no-input` makes the command fail with a non-zero exit code if the browser flow would block (useful in CI smoke tests after the tokens are already provisioned).

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |
| `--add-scopes` | string | — | extend the OAuth grant with optional scope keywords (csv): irn, ecg |

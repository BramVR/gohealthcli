---
title: "gohealthcli connect"
description: "Run the browser OAuth flow and anchor one Google Identity."
---

Open the system browser, run the installed-app OAuth flow against the OAuth client supplied at `init`, and store the resulting tokens in the OS-native Credential Store (Keychain on macOS, Credential Manager on Windows, Secret Service on Linux).

A Health Archive holds exactly one Connection. Running `connect` against an archive that already has a Connection refreshes the token material in place rather than adding a second identity.

`--add-scopes` extends an existing grant with optional scope keywords (`irn`, `ecg`, `nutrition`, `tcx`, `settings`) without re-running setup; Google's `include_granted_scopes=true` makes the resulting token cover the union of prior + new scopes. Use `connect --add-scopes irn` to unlock `gohealthcli irn-profile` and Tier 2 ECG / IRN Data Types; use `connect --add-scopes nutrition` to unlock hydration-log and nutrition-log; use `connect --add-scopes tcx` to unlock TCX route archival on exercise sync (grants `googlehealth.location.readonly`, required on top of `activity_and_fitness.readonly` for Google's `exportExerciseTcx` endpoint); use `connect --add-scopes settings` to unlock `gohealthcli settings` and `gohealthcli devices` (grants `googlehealth.settings.readonly`, which Google requires for `users.getSettings` and `users.pairedDevices.list`).

`--no-input` makes the command fail with a non-zero exit code if the browser flow would block (useful in CI smoke tests after the tokens are already provisioned).

For a host without a local browser, `connect --headless-start` performs the same local preflight, stores a ten-minute PKCE pending authorization only in the configured Credential Store, and prints `authorization_url` plus `expires_at`. Open that URL in a trusted browser. Then pipe the complete redirected loopback URL to `connect --complete`; the completion command accepts exactly one URL on stdin and never accepts a bare authorization code or a URL argument. Repeat the same `--add-scopes` value on both commands. Because completion must read stdin, `--complete` cannot be combined with `--no-input`.

Pending authorization is bound to the config, Health Archive, OAuth client, exact redirect, requested scopes, and existing Google Identity expectation. It is atomically single-use across concurrent completion processes. Invalid state, redirect, binding, input, or expiry never changes the Connection or token material; a state-valid cancellation and a claimed completion consume the pending authorization. Starting again replaces the prior pending authorization. Treat both printed and redirected URLs as sensitive transfer material: keep them out of shell history, logs, screenshots, and shared artifacts. Interactive `connect` remains the default when the browser and CLI share a host.

Setup and Connection failures may add `remediation` to JSON results and zero-based `remediation.N` fields to plain results; human output stays unchanged. Steps are Reporter-owned and diagnosis-first: `doctor` before `init` or `connect`, `doctor --online` before reconnecting or choosing a new archive, and missing-scope consent uses only sorted `--add-scopes` keywords from the public catalog. Building or rendering these steps never performs Provider I/O, starts OAuth, or interpolates error text.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |
| `--add-scopes` | string | — | extend the OAuth grant with optional scope keywords (csv): ecg, irn, nutrition, settings, tcx |
| `--headless-start` | bool | `false` | start headless OAuth and print the authorization URL |
| `--complete` | bool | `false` | complete headless OAuth from a redirected URL read on stdin |

---
title: "gohealthcli init"
description: "Create local config and an empty Health Archive."
---

Initialise a fresh `gohealthcli` install: write the config file, create the Health Archive on disk, and run the initial schema migration. After `init` finishes the binary is ready for `connect`.

`--oauth-client-file` points at a Google OAuth Desktop-app client JSON downloaded from the Google Cloud console (see the [Install](../install.html) page). `--secret-provider` and `--oauth-client-item` are an alternative path that pulls the client from a Secret Provider (for example, 1Password) instead of a file.

`init` never overwrites an existing Health Archive; rerun with a different `--db` to create a second archive in a separate location.

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--no-input` | bool | `false` | never prompt, never wait for browser input |
| `--oauth-client-file` | string | — | OAuth client JSON file reference |
| `--secret-provider` | string | — | Secret Provider name for OAuth client setup |
| `--oauth-client-item` | string | — | Secret Provider item name for OAuth client setup |

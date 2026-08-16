---
title: "gohealthcli backup"
description: "Initialize, push, and inspect encrypted Health Archive backups."
---

Manage the explicit age-encrypted Git backup for the current local Health Archive. `backup init` creates or reuses a local X25519 age identity, writes an owner-only backup config, initializes or clones the configured Git checkout, writes its recovery README, commits it, and pushes unless `--no-push` is set. `backup push` exports only the current local Health Archive into deterministic logical JSONL shards, compresses them with fixed gzip metadata, encrypts every shard with age before Git sees it, updates the cleartext manifest, commits, and pushes unless `--no-push` is set. It does not sync, refresh Identity Snapshots, read the Credential Store, or contact Google Health. `backup status` reads only cleartext manifest metadata; it does not read the private age identity, decrypt health data, open the Health Archive, contact Google Health, or mutate the backup checkout.

The default backup config is `backup.json` under the XDG-style gohealthcli config directory, and the default age identity is stored beside it. In this command family `--config` selects that backup config. `--db` selects the Health Archive for `backup push`. The Git checkout defaults to `~/Projects/backup-gohealthcli`, and `--remote`, `--repo`, `--identity`, and repeatable `--recipient` values override the saved backup configuration.

The age identity and backup config stay local and owner-only. Never commit either file. Keep a private recovery copy of the identity: if every configured identity is lost, the encrypted backup cannot be restored. The backup repository still reveals cleartext metadata such as backup time, public recipients, Health Archive schema version, table names, record counts, shard paths, encrypted sizes, plaintext hashes, cadence, and changed shards.

## Usage

```
gohealthcli backup <init|push|status>
```

## Flags

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | — | config file path |
| `--db` | string | — | SQLite Health Archive path |
| `--json` | bool | `false` | write stable JSON to stdout |
| `--plain` | bool | `false` | write plain key/value output to stdout |
| `--repo` | string | — | local encrypted backup Git checkout |
| `--remote` | string | — | Git remote URL or path |
| `--identity` | string | — | local age identity file |
| `--recipient` | string | — | additional age public `string` recipient (repeatable) |
| `--no-push` | bool | `false` | commit locally without pushing |

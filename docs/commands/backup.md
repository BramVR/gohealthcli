---
title: "gohealthcli backup"
description: "Initialize, push, pull, and inspect encrypted Health Archive backups."
---

Manage the explicit age-encrypted Git backup for the current local Health Archive. `backup init` creates or reuses a local X25519 age identity, writes an owner-only backup config, initializes or clones the configured Git checkout, writes its recovery README, commits it, and pushes unless `--no-push` is set. `backup push` exports only the current local Health Archive into deterministic logical JSONL shards, compresses them with fixed gzip metadata, encrypts every shard with age before Git sees it, updates the cleartext manifest, commits, and pushes unless `--no-push` is set. It does not sync, refresh Identity Snapshots, read the Credential Store, or contact Google Health. `backup pull` pulls/rebases the configured Git checkout, decrypts and verifies every manifest-owned shard, validates the complete Health Archive Snapshot, and restores it only into a fresh Health Archive path. Data Point Attachment rows and encrypted payloads restore as owner-only sidecars that satisfy the same missing-row and orphan-file integrity rules reported by `doctor`. `backup status` reads only cleartext manifest metadata; it does not read the private age identity, decrypt health data, open the Health Archive, contact Google Health, or mutate the backup checkout.

The default backup config is `backup.json` under the XDG-style gohealthcli config directory, and the default age identity is stored beside it. In this command family `--config` selects that backup config. `--db` selects the source Health Archive for `backup push` and the fresh target Health Archive for `backup pull`. The Git checkout defaults to `~/Projects/backup-gohealthcli`, and `--remote`, `--repo`, `--identity`, and repeatable `--recipient` values override the saved backup configuration. To add or rotate recipients, rerun `backup init` with the complete desired set of additional public recipients, then run `backup push`. Recipients are trimmed, deduplicated, and sorted; a changed set re-encrypts unchanged shards, while a later unchanged push reuses authenticated ciphertext and leaves Git clean. Shards absent from a new Snapshot are removed from the current backup tree without rewriting earlier Git history.

The age identity and backup config stay local and owner-only. Never commit either file. Private Credential Store token material, OAuth client secrets, and Secret Provider contents are not backed up; Provider commands may require reconnecting after restore. Keep a private recovery copy of the identity: if every configured identity is lost, the encrypted backup cannot be restored. The backup repository still reveals cleartext metadata such as backup time, public recipients, Health Archive schema version, table names, record counts, shard paths, encrypted sizes, plaintext hashes, cadence, and changed shards.

## Usage

```
gohealthcli backup <init|push|pull|status>
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

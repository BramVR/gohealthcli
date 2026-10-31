---
title: Encrypted backups
description: Back up the current Health Archive to age-encrypted Git shards and prove recovery with a throwaway restore.
---

`gohealthcli backup` is an explicit encrypted Git workflow. It never runs in
the background. `backup push` uploads only after it has compressed and
age-encrypted every Health Archive payload shard; Git also receives a cleartext
manifest containing metadata about that encrypted Snapshot.

## Initialise the backup

Choose an owner-controlled Git remote. Do not put credentials in its URL; use
your normal Git credential helper, SSH key, or agent. Start with `--no-push` if
you want to inspect the local setup commit before sending it anywhere:

```bash
gohealthcli backup init --remote https://github.com/OWNER/backup-gohealthcli.git --no-push --plain
gohealthcli backup status --plain
```

`backup init` creates or reuses a local X25519 age identity, saves the backup
checkout, remote, identity, and recipients in an owner-only backup config,
initialises or clones the checkout, and commits its recovery README. It does
not export the Health Archive. Rerun without `--no-push` to push the saved setup
commit:

```bash
gohealthcli backup init --plain
```

The default config and identity live in the gohealthcli config directory as
`backup.json` and `backup-age-identity.txt`. Neither belongs in the Git
checkout.

## Back up the current Health Archive

Refresh Provider data separately when wanted, then back up the archive exactly
as it exists on disk:

```bash
gohealthcli backup push --db /path/to/gohealthcli.sqlite --no-push --plain
gohealthcli backup status --plain
gohealthcli backup push --db /path/to/gohealthcli.sqlite --plain
```

`--no-push` is valid for `backup init` and `backup push`: it still creates the
local commit but does not send it to the configured remote. A normal `backup
push` writes deterministic logical JSONL shards, uses fixed gzip metadata,
encrypts each shard for every configured age recipient, updates the cleartext
manifest, commits, and pushes. It does not run `sync`, refresh Identity
Snapshots, read the Credential Store, or contact Google Health. An unchanged
rerun reuses authenticated ciphertext and leaves the checkout clean.

Data Point Attachment rows and their sidecar bytes are part of the encrypted
Snapshot. Private Credential Store token material, OAuth client secrets, and
Secret Provider contents are not. A restored Health Archive may therefore need
`connect` before Provider commands work.

## Inspect visible metadata

`backup status` reads the config, checkout, and cleartext manifest without the
private age identity. It does not decrypt shards, open the Health Archive,
contact Google Health, or mutate Git. Before the first Snapshot it reports an
explicit uninitialised or empty state; afterwards it reports encryption,
export time, shard count, and Health Archive counts.

Encryption hides health payloads and Attachment bytes, but the Git repository
remains sensitive metadata. The manifest and history can reveal export time,
public recipients, Health Archive schema version, logical table names, row
counts, shard paths, encrypted sizes, plaintext hashes, backup cadence, and
which shards changed. Restrict repository write access: age authenticates
ciphertext for a recipient, not the Git publisher.

## Prove recovery with a throwaway restore

Restore periodically to a path that does not exist. Never point `backup pull`
at the current archive or any other existing path:

```bash
gohealthcli backup pull --db /path/to/throwaway-restored.sqlite --plain
gohealthcli status --db /path/to/throwaway-restored.sqlite --plain
```

`backup pull` first pulls/rebases the configured checkout. It confines every
manifest path to the backup data tree, decrypts every shard with the configured
identity, verifies its declared plaintext hash, validates all Snapshot
relationships, and only then creates the fresh target archive. Attachment rows
and payloads restore together as owner-only sidecars; a successful restore must
have no missing Attachment rows or orphan files under the same integrity model
reported by `doctor`.

The `status` command above inspects the throwaway Health Archive directly. You
can also compare bounded `query` or `export` results with the source before
removing the throwaway archive and its adjacent `.attachments` directory.

## Protect recovery identities

Keep a private recovery copy of the age identity outside the backup repository,
with owner-only access. If every identity matching the configured recipients is
lost, the encrypted backup cannot be restored. Adding a recipient and running
`backup push` re-encrypts unchanged shards so the new identity can restore the
current Snapshot. Removing a recipient protects current and future backup
trees only; ciphertext decryptable by that identity remains in earlier Git
history.

For every flag and stable output field, see the [`backup` command
reference](commands/backup.html). For the complete trust boundary, see the
[security model in the repository](https://github.com/BramVR/gohealthcli/blob/main/docs/security.md).

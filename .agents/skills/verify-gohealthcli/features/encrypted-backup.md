# Back up and restore an archive

Backup exports the current Health Archive, encrypts logical shards with age, commits only ciphertext and documented metadata to Git, and restores into a new archive. It never refreshes Provider data.

## Sub-features

- `backup-init` creates a local age identity, config, checkout, and recovery README.
- `backup-push` exports and encrypts the current archive before Git sees payloads.
- `backup-idempotence` reuses unchanged ciphertext and leaves the checkout unchanged.
- `backup-pull` restores into a missing target and supports a separate CLI status read.

## How to get to it (user POV)

- Run `gohealthcli backup init --config <backup-config> --repo <checkout> --remote <remote> --identity <identity> --json`.
- Run `gohealthcli backup push --config <backup-config> --db <archive> --json`.
- Run `gohealthcli backup status --config <backup-config> --json`.
- Run `gohealthcli backup pull --config <backup-config> --db <missing-restore-path> --json`.

## Driving it with verify-gohealthcli

Preconditions:

- `launch`, `drive initialize`, and helper doctor pass for the same run.
- `/tmp/gohealthcli-verify-$RUN_ID/backup` is absent.

- Run `.agents/skills/verify-gohealthcli/helpers/verify-gohealthcli drive "$RUN_ID" backup`.
- The route creates a bare Git remote inside the owned run root, so every push stays local.
- `backup-push.typescript` reports all nine encrypted Snapshot shards. The helper requires nine tracked, non-empty age ciphertext files, independently checks each manifest byte count against its ciphertext and each SHA-256 against decrypted JSONL, and requires equality between local HEAD and the task-owned remote ref. Around the repeated push it compares checkout status and both refs again.
- `backup-history-proof.txt` confirms every reachable local and remote commit contains only the fixed recovery README, current cleartext manifest, and age ciphertext shard allowlist.
- The recovery README must match its pinned safe hash. A strict decoder rejects unknown manifest fields, invalid counts/shards, and every synthetic or credential-bearing sentinel in cleartext metadata. The manifest recipient must equal the single generated identity, every age header must contain exactly one recipient stanza, and every decrypted line must be valid JSON with the declared row count.
- Before push, a task-local seeder inserts one synthetic Connection and Google Identity. The compiled CLI fingerprints every seeded field, including raw metadata JSON and timestamps; source and restored fingerprints must both equal the exact sentinel. Separate compiled-CLI queries require exact matching counts for all eight SQLite-backed Snapshot tables before push and after restore; independent decrypted-JSONL validation covers the ninth Attachment-payload shard.
- `backup-pull.typescript` restores to a new task-owned archive. `backup-restored-status.typescript` reads it through the compiled CLI.
- `backup-proof.txt` records no Provider or Credential Store access. Cleanup removes the identity, checkout, remote, source archive, and restored archive together.

## Gotchas

- A Git remote can reveal backup metadata even when payloads are encrypted. This route uses a disposable local bare repository only.
- The age identity is private. Keep it inside the owned run root and never copy it to evidence.
- The synthetic Connection proves an exact non-empty round trip without using personal health or authentication data. Other Snapshot tables remain empty, so this route does not prove real health row contents.
- Never restore over an existing archive. The route requires a missing target.

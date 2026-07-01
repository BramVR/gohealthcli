# Vision

gohealthcli is a local-first, read-only Google Health archive CLI for personal health and fitness data. It should preserve raw provider JSON, expose stable normalized query/export surfaces, and keep health data local, owner-controlled, and safe for agent workflows.

## Merge by Default

- Bug fixes with clear cause in provider, connection, archive, sync, cursor, export, or command behavior.
- New Data Type support that follows the catalog, scope, raw JSON, normalized view, and docs patterns.
- Read-only CLI, JSON/plain output, docs, and Project Site improvements that match command registry behavior.
- Credential Store, OAuth setup, output hygiene, and local file-permission fixes.
- Tests and drift guards that keep README, command docs, and export datasets aligned with code.

## Needs Sign-Off

These changes require explicit maintainer sign-off before implementation or merge.
- Write/delete/upload/share behavior for health data or exports.
- New providers, scopes, OAuth flows, or credential storage strategies.
- Multi-identity archive support, schema migrations, cursor semantics, or sidecar attachment rules.
- Live Google Health behavior that cannot be verified with required account/API access.
- Real health data in tests, docs, examples, logs, screenshots, or commits.

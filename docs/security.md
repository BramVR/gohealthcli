---
summary: "Health data safety model, OAuth token handling, local files, and exports."
read_when:
  - "Handling OAuth credentials or refresh tokens."
  - "Changing local file paths or permissions."
  - "Adding export, delete, upload, webhook, or write behavior."
---
# Security

## Model

`gohealthcli` stores sensitive personal health data locally. Treat the archive
and token material as private.

The first version is read-only:

- No health writes.
- No deletes.
- No webhook receiver.
- No cloud service.
- No automatic sharing.

## OAuth

Google Health API access needs a Google Cloud project, OAuth client, configured
scopes, and test-user setup while unverified.

Token material must not print in normal command output. `doctor` may report
presence, expiry shape, and scopes, but not token values.

Potential token storage options:

- Local encrypted or permission-restricted file.
- macOS keychain integration.
- Environment variables for development only.

## Local Files

Default paths should follow the `gobankcli` pattern:

- config: `~/.config/gohealthcli/config.toml`
- archive: `~/.local/share/gohealthcli/gohealthcli.sqlite`
- tokens or credential references: `~/.config/gohealthcli/`

Config and token files should be created with owner-only permissions.

## Exports

Exports can reveal sensitive health history. Commands should require explicit
output paths or write to stdout intentionally. Avoid silent background exports.

## Out of Scope

- 1Password automation.
- Remote secret storage.
- Browser scraping.
- Android device scraping.
- Medical interpretation.

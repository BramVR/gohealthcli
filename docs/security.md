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

The First Release is read-only:

- No health writes.
- No deletes.
- No webhook receiver.
- No cloud service.
- No automatic sharing.

## OAuth

Google Health API access needs a Google Cloud project, OAuth client, configured
scopes, and test-user setup while unverified.

`connect` should request scopes for the configured Data Types. `sync` must not
start an unexpected browser consent flow; if a requested Data Type needs missing
scopes, fail with a clear re-connect instruction.

Token material must not print in normal command output. `doctor` may report
presence, expiry shape, and scopes, but not token values. Default `doctor`
should stay local; `doctor --online` is the explicit path for token refresh and
provider reachability checks.

OAuth token material should live in a Credential Store:

- macOS: Keychain.
- Windows: Windows Credential Manager.
- Linux: Secret Service/libsecret when available.
- File fallback: permission-restricted local file, explicit opt-in for
  development or unsupported environments.

1Password may be used as a Secret Provider for bootstrap material such as a
Google OAuth client secret. `init` stores exact Secret Provider references;
`connect` consumes resolved OAuth client config and should not search 1Password.
1Password should not be the default runtime token backend.

Environment variables are for development only.

Expired or unrefreshable tokens are Connection health problems, not Health
Archive corruption. `connect` may re-authorize the same Google Identity and keep
using the existing archive. If re-authorization returns a different Google
Identity, require an explicit new archive or a future multi-identity decision.

## Local Files

Default paths should follow the `gobankcli` pattern:

- config: `~/.config/gohealthcli/config.toml`
- archive: `~/.local/share/gohealthcli/gohealthcli.sqlite`
- credential references or file fallback tokens: `~/.config/gohealthcli/`

Config and token files should be created with owner-only permissions.
The default archive path does not include Google Identity. The archive stores
that identity internally; multiple identities require explicit future design or
explicit alternate `--db` paths.

## Exports

Exports can reveal sensitive health history. Commands should require explicit
output paths or explicit `--stdout`. Avoid silent background exports.

## Out of Scope

- Remote secret storage.
- Browser scraping.
- Android device scraping.
- Medical interpretation.

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

Structured authentication remediation is output-only metadata. JSON may expose
an ordered `remediation` array and plain output the matching zero-based
`remediation.N` fields; human output is unchanged. Recovery commands come from
the Failure Reporter's fixed catalog, with the sole parameterized form limited
to sorted `connect --add-scopes` keywords already present in the public scope
catalog. Error messages, tokens, authorization codes, identities, paths,
Provider payloads, SQL, health data, and user input are never converted into
actions. Building or rendering actions performs no Provider I/O and cannot
start an OAuth flow.

Sync and Health Archive recoveries use the same output-only boundary. The only
Sync-specific action is the fixed
`gohealthcli sync --from YYYY-MM-DD` Initial Backfill template; it never copies
the operator's Data Types, dates, paths, or other arguments. Fan-out actions
remain on the affected child. Missing archives may suggest diagnosis followed
by initialisation, but canceled, corrupt, invalid-query, and unknown failures
remain actionless. Classification uses typed causes only and does not read or
mutate the Provider, OAuth state, Sync Cursor, audit trail, or archive.

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

Headless authorization uses the same Desktop-client loopback and PKCE S256
flow as interactive `connect`. `connect --headless-start` stores its verifier,
state, exact redirect, expiry, and config/archive/client/scope/identity binding
only in the configured Credential Store for ten minutes. `connect --complete`
accepts one complete redirected URL on stdin only. It validates the exact
origin and path, binding, expiry, state, and full requested scope grant before
identity validation or token storage. A claim is atomic across processes, so
only one concurrent completion can exchange the code. Treat authorization and
redirected URLs as sensitive transfer material even though neither is stored
in the Health Archive.

## Local Files

Default paths should follow the `gobankcli` pattern:

- config: `~/.config/gohealthcli/config.toml`
- archive: `~/.local/share/gohealthcli/gohealthcli.sqlite`
- credential references or file fallback tokens: `~/.config/gohealthcli/`

Config and token files should be created with owner-only permissions.
The default archive path does not include Google Identity. The archive stores
that identity internally; multiple identities require explicit future design or
explicit alternate `--db` paths.

The defaults are anchored to the user's home directory. When `HOME` is unset and
no absolute `XDG_CONFIG_HOME`/`XDG_DATA_HOME` override is set (a relative `XDG_*`
value is ignored per the XDG Base Directory spec), the binary cannot anchor the
default and fails loudly — "cannot determine home directory; set HOME or pass
--config/--db explicitly" — instead of silently writing personal health data to
a current-working-directory-relative path. Passing `--config`/`--db` explicitly
is unaffected.

## Exports

Exports can reveal sensitive health history. Commands should require explicit
output paths or explicit `--stdout`. Avoid silent background exports.

## Out of Scope

- Remote secret storage.
- Browser scraping.
- Android device scraping.
- Medical interpretation.

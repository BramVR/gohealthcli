---
summary: "Local Google Health OAuth setup checklist for manual test runs."
read_when:
  - "Setting up Google Health OAuth for local testing."
  - "Debugging connect, consent, scopes, or credential storage."
---
# Google Auth Setup

## Google Cloud

Use the target Google Cloud project, then configure:

- API: enable Google Health API.
- Google Auth Platform: finish app branding, audience, and data access.
- Audience: keep app in Testing while unverified, and add the Google account as
  a test user.
- Data Access scopes:
  - `https://www.googleapis.com/auth/googlehealth.profile.readonly`
  - `https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly`
  - `https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly`
  - `https://www.googleapis.com/auth/googlehealth.sleep.readonly`
- OAuth client: create a Desktop app client from Google Auth Platform >
  Clients, then download its JSON.

Do not use a Web application client for local CLI auth. `gohealthcli` uses an
installed-app loopback flow and rejects web-client JSON.

## Local Files

Keep the downloaded client JSON owner-only:

```bash
chmod 600 ~/Downloads/client_secret_*.json
```

Initialize with that client when setup is new:

```bash
gohealthcli init --oauth-client-file ~/Downloads/client_secret_*.json
```

For an existing setup, update `~/.config/gohealthcli/config.toml`:

```toml
[oauth_client]
source = "file"
path = "/absolute/path/to/client_secret.json"
```

`doctor --plain` should report `status: ok` before browser auth.

## Credential Store

Default runtime token storage is OS-native. For local testing, the explicit file
fallback is acceptable if it stays owner-only:

```toml
[credential_store]
type = "file"
path = "/absolute/path/to/gohealthcli/tokens.json"
```

Use the file fallback if macOS Keychain storage blocks at a `security` prompt
during `connect`.

After `connect`, verify token-file permissions without printing token material:

```bash
ls -l ~/.config/gohealthcli/tokens.json
```

Expected mode: `-rw-------`.

## Connect And Verify

Run:

```bash
gohealthcli connect --plain
gohealthcli doctor --online --plain
gohealthcli identity --plain
gohealthcli profile --plain
```

The browser may briefly show a localhost callback page after consent. If the CLI
prints `status: connected`, the callback worked.

## Smoke Test

Use a narrow window first:

```bash
gohealthcli raw endpoint getIdentity
gohealthcli sync --types steps --from 2026-01-01 --to 2026-01-02 --plain
gohealthcli status --plain
gohealthcli export daily-steps --format jsonl --stdout
```

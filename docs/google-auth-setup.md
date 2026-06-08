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
- Data Access scopes (base set, required for every `gohealthcli connect`):
  - `https://www.googleapis.com/auth/googlehealth.profile.readonly`
  - `https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly`
  - `https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly`
  - `https://www.googleapis.com/auth/googlehealth.sleep.readonly`
- Optional Data Access scopes (only needed for `connect --add-scopes` and
  the matching opt-in features):
  - `https://www.googleapis.com/auth/googlehealth.irn.readonly` — required
    by `gohealthcli irn-profile` and the Tier 2 `irregular-rhythm-notification`
    Data Type (#104).
  - `https://www.googleapis.com/auth/googlehealth.electrocardiogram.readonly`
    — required by the Tier 2 `electrocardiogram` Data Type (#104).
  - `https://www.googleapis.com/auth/googlehealth.nutrition.readonly` —
    required by `hydration-log` and any future nutrition Data Type (#103).
  - `https://www.googleapis.com/auth/googlehealth.location.readonly` —
    required (on top of `activity_and_fitness.readonly`) by the
    `users.dataTypes.dataPoints.exportExerciseTcx` endpoint that
    archives TCX route bytes during exercise sync (#140). Granted via
    `connect --add-scopes tcx`; without it, exercise sync still
    archives the Data Points themselves but skips the TCX sidecar.
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

## Tier 2 Opt-in Scopes (ECG + IRN)

After enabling `electrocardiogram.readonly` and `irn.readonly` in
the Google Cloud Data Access page, extend the existing local grant
without re-running setup:

```bash
gohealthcli connect --add-scopes ecg,irn --plain
```

`include_granted_scopes=true` makes the browser flow re-issue tokens
covering the union of currently-granted scopes and the two new ones,
so the base set stays untouched. Once `status: connected` prints, the
Tier 2 syncs unlock:

```bash
gohealthcli sync --types electrocardiogram --from 2026-01-01 --plain
gohealthcli sync --types irregular-rhythm-notification --from 2026-01-01 --plain
gohealthcli status --plain  # per-Data-Type newest_data_point_timestamp lands
```

If a Tier 2 sync is called without the matching scope on the stored
Connection, the command exits with a recovery hint pointing at the
keywords for the missing scopes specifically: `--add-scopes ecg` when
only the ECG scope is missing, `--add-scopes irn` for IRN, and
`--add-scopes ecg,irn` when both are. Run that exact line to fix it.
No second base-set browser sign-in is needed.

## TCX Route Archival (`tcx`)

Google's `exportExerciseTcx` endpoint requires both
`activity_and_fitness.readonly` and `location.readonly` on the access
token (#140). The base set already grants the first; the second is
opt-in via the `tcx` keyword:

```bash
gohealthcli connect --add-scopes tcx --plain
```

After enabling `googlehealth.location.readonly` on the Google Cloud
Data Access page and re-running `connect`, exercise sync archives the
TCX route bytes as `tcx`-kind Attachments under
`<archive>.attachments/tcx/<sha256[0:2]>/<sha256>.tcx` (ADR-0009).
Without the scope, exercise sync still upserts the exercise Data
Points themselves but the TCX hook short-circuits with no HTTP
round-trip — `exportExerciseTcx` would return 403 deterministically.

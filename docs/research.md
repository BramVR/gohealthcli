---
summary: "Source-backed Google Health API research and provider feasibility notes."
read_when:
  - "Checking current Google Health API facts."
  - "Revisiting provider choice."
  - "Updating scope after Google API changes."
---
# Research

Last checked: 2026-05-24.

## Findings

Google Health API is the primary candidate. Google describes it as the next
generation of the Fitbit Web API and says it exposes health and fitness data
from Fitbit, Pixel Watch, and other third-party devices and apps through Google
OAuth 2.0.

Google recommends waiting until the end of May 2026 before official integration
launches because breaking changes may occur during the transition period.

Google Health API access requires a Google Cloud project, API enablement, OAuth
client, test users while unverified, and configured scopes.

In OAuth testing mode, refresh tokens expire after 7 days. In production mode,
refresh tokens generally do not expire unless revoked or unused for a prolonged
period. Supporting more than 100 users requires third-party security review.

The API has endpoint families useful for a CLI archive:

- `getIdentity`: fetch Google Health user ID and legacy Fitbit user ID.
- `list`: fetch detailed Data Points for a Data Type.
- `reconcile`: fetch reconciled streams, including source-family filtering.
- `rollUp`: aggregate over arbitrary windows.
- `dailyRollUp`: aggregate across civil days.

The data type catalog includes the first target set: `steps`, `heart-rate`,
`heart-rate-variability`, `daily-heart-rate-variability`,
`daily-resting-heart-rate`, `oxygen-saturation`, `daily-oxygen-saturation`,
`daily-respiratory-rate`, `sleep`, `exercise`, `distance`, `total-calories`,
`weight`, and related activity/fitness types.

Health Connect is not the primary desktop CLI path. It is Android/on-device
infrastructure and may be useful later as an import/export fallback.

Google Fit API should be avoided for new work. It is legacy/deprecated relative
to Google Health API and does not fit a new CLI started in 2026.

Existing `fitbit-cli` tools may be useful references, but they are not the clean
long-term target if Google Health API access works.

## Sources

- Google Health API home: https://developers.google.com/health
- Google Health API setup/OAuth: https://developers.google.com/health/setup
- Google Health API endpoints: https://developers.google.com/health/endpoints
- Google Health API data types: https://developers.google.com/health/data-types
- Google Health API Go client: https://pkg.go.dev/google.golang.org/api/health/v4
- Grill with docs skill: https://github.com/mattpocock/skills/blob/main/skills/engineering/grill-with-docs/SKILL.md

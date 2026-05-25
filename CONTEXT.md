# gohealthcli

`gohealthcli` archives personal health and fitness measurements from a Google
Account into a local, queryable record.

## Language

**Health Archive**: The local collection of imported health and fitness records. A Health Archive has one Google Identity and many archived Data Points.
_Avoid_: database, cache, backup

**Google Identity**: The Google Health API identity for the consenting user, including Google Health user ID and legacy Fitbit user ID when available.
_Avoid_: account, profile, owner

**Provider**: An upstream API family that can supply health records. The first Provider is Google Health API.
_Avoid_: backend, source, integration

**Data Type**: A Google Health API category such as `steps`, `heart-rate`, `daily-resting-heart-rate`, `sleep`, or `exercise`. A Data Type defines the shape of its Data Points, even when the Data Type name describes daily records.
_Avoid_: metric, endpoint, table

**Data Point**: One upstream health record returned for a Data Type. A Data Point belongs to exactly one Data Type and may be an interval, sample, daily, or session record.
_Avoid_: row, event, measurement

**Data Point Revision**: A previous raw version retained when an upstream correction changes the canonical Data Point. A Data Point Revision is not a separate Data Point.
_Avoid_: duplicate, history row, event

**Data Source**: The upstream origin attached to a Data Point, such as a wearable, app, or web client.
_Avoid_: device, platform

**Wearable Data**: Data Points whose Data Source indicates a watch or tracker family. Wearable Data may include Pixel Watch, Fitbit, or other Google-compatible devices.
_Avoid_: watch data, Fitbit data

**Rollup**: An upstream aggregate returned by a `rollUp` or `dailyRollUp` endpoint over a time window. A Rollup summarizes Data Points but does not replace the raw Data Points in the Health Archive.
_Avoid_: summary, aggregate

**Profile Snapshot**: Raw provider profile or settings metadata fetched for a Google Identity at a point in time. A Profile Snapshot is not a Data Point, Rollup, or analytics result.
_Avoid_: profile, settings, account data

**Sync Run**: One attempt to fetch and archive Data Points or Rollups for selected Data Types and time ranges.
_Avoid_: import, scrape, download

**Connection**: The local authorization relationship between `gohealthcli` and one Google Identity. A Connection owns OAuth token material, has a deterministic `provider:google_health_user_id` identifier, and is not itself the person or the archive.
_Avoid_: login, account, session

**Credential Store**: The local place where a Connection's OAuth token material is stored. A Credential Store may be OS-native or an explicit file fallback, but it is part of normal `gohealthcli` runtime.
_Avoid_: secret manager, password manager, token file

**Secret Provider**: A human-operated source for setup secrets such as a Google OAuth client secret. A Secret Provider may be 1Password, but it is not the default runtime Credential Store.
_Avoid_: credential store, token backend

**Health Connect Export**: A separate Android-origin export path for Health Connect data, not the primary Provider. It may become an import fallback when API access is incomplete.
_Avoid_: Google Health export, Fitbit export

## Flagged Ambiguities

**Google Watch 4**: The intended device is probably a Google Pixel Watch or a Wear OS watch, but "Google Watch 4" is not a canonical project term. Use Wearable Data unless the exact device model matters.

**Google Health system**: This can mean Google Health API, Google Health app, Google Fit, or Android Health Connect. Use the precise term in docs and commands.

**Fitbit data**: This can mean legacy Fitbit Web API data, data visible in the Fitbit app, or data exposed through Google Health API. Use Wearable Data or Google Health API Data Point depending on context.

## Example Dialogue

Developer: "Should `sync --type sleep` pull watch data only?"

Domain expert: "No. `sleep` is a Data Type. Sync defaults to all Data Sources. If we need tracker-only records, filter by Data Source family and call it Wearable Data."

Developer: "Do daily step totals replace minute-level step records?"

Domain expert: "No. Daily totals are Rollups. Keep raw Data Points when fetched, store Rollups separately, and fetch Rollups only when requested."

Developer: "Can one archive hold multiple Google accounts?"

Domain expert: "Not in the First Release. One Health Archive has one Google Identity. Multi-identity support needs an explicit decision later."

Developer: "Should we store refresh tokens in 1Password?"

Domain expert: "No. Use a Credential Store for runtime tokens. 1Password can be a Secret Provider during setup."

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

**Data Type**: A Google Health API category such as `steps`, `heart-rate`, `sleep`, or `exercise`. A Data Type defines the shape of its Data Points.
_Avoid_: metric, endpoint, table

**Data Point**: One upstream health record returned for a Data Type. A Data Point belongs to exactly one Data Type and may be an interval, sample, daily, or session record.
_Avoid_: row, event, measurement

**Data Source**: The upstream origin attached to a Data Point, such as a wearable, app, or web client.
_Avoid_: device, platform

**Wearable Data**: Data Points whose Data Source indicates a watch or tracker family. Wearable Data may include Pixel Watch, Fitbit, or other Google-compatible devices.
_Avoid_: watch data, Fitbit data

**Rollup**: An upstream aggregate over a time window, usually daily. A Rollup summarizes Data Points but does not replace the raw Data Points in the Health Archive.
_Avoid_: summary, aggregate

**Sync Run**: One attempt to fetch and archive Data Points or Rollups for selected Data Types and time ranges.
_Avoid_: import, scrape, download

**Connection**: The local authorization relationship between `gohealthcli` and one Google Identity. A Connection owns OAuth token material but is not itself the person or the archive.
_Avoid_: login, account, session

**Health Connect Export**: A separate Android-origin export path for Health Connect data, not the primary Provider. It may become an import fallback when API access is incomplete.
_Avoid_: Google Health export, Fitbit export

## Flagged Ambiguities

**Google Watch 4**: The intended device is probably a Google Pixel Watch or a Wear OS watch, but "Google Watch 4" is not a canonical project term. Use Wearable Data unless the exact device model matters.

**Google Health system**: This can mean Google Health API, Google Health app, Google Fit, or Android Health Connect. Use the precise term in docs and commands.

**Fitbit data**: This can mean legacy Fitbit Web API data, data visible in the Fitbit app, or data exposed through Google Health API. Use Wearable Data or Google Health API Data Point depending on context.

## Example Dialogue

Developer: "Should `sync --type sleep` pull watch data only?"

Domain expert: "No. `sleep` is a Data Type. If we need tracker-only records, filter by Data Source family and call it Wearable Data."

Developer: "Do daily step totals replace minute-level step records?"

Domain expert: "No. Daily totals are Rollups. Keep raw Data Points when fetched, and store Rollups separately."

Developer: "Can one archive hold multiple Google accounts?"

Domain expert: "Not in the first version. One Health Archive has one Google Identity. Multi-identity support needs an explicit decision later."


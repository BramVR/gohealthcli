# Changelog

## Unreleased

- Add daily and physical-window `calories-in-heart-rate-zone` Rollup sync and the `calories-in-heart-rate-zone-rollups` export, one row per Rollup bucket and heart-rate zone, with 14-day Provider chunking, unknown zone labels passed through, empty buckets exporting no rows, and missing `kcal` exported as null.
- Add offline `route <exercise>` to write the newest archived TCX route of one exercise Data Point as its exact bytes to stdout or a new owner-only file, verified against its Attachment row's path, size, and SHA-256.
- Document that daily and Rollup queries and exports preserve absent days as missing records and stored zeros as zeros; warn against averaging absent days as zeros.
- Preserve the latest successful Sync Cursor when Sync Runs complete out of order or re-sync older history, retaining the winning boundary and advancement timestamp on ties.
- List every catalog-backed raw endpoint in help and the command reference, and include supported names and typo suggestions in unsupported-endpoint errors.
- Add catalog-backed one-request `raw data-type <data-type> daily-rollup` and `rollup --window <duration>` exploration with sync-equivalent request construction, zero-effect planning, and safe exact-byte output.
- Add catalog-backed one-page `raw data-type <data-type> reconcile` reads with sync-equivalent request construction, zero-effect planning, and safe exact-byte output.
- Add catalog-backed `raw data-type <data-type> get --id <provider-id>` reads with opaque ID escaping, exact-byte stdout or safe file output, and zero-effect planning.
- Add `raw --output PATH` for exact Provider-byte output to a new private file, with atomic no-clobber publication, Linux and macOS mode `0600`, explicit Windows ACL behavior, and failed-write cleanup.

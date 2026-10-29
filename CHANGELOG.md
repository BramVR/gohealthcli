# Changelog

## Unreleased

- Add offline `route <exercise>` to write the newest archived TCX route of one exercise Data Point as its exact bytes to stdout or a new owner-only file, verified against its Attachment row's path, size, and SHA-256.
- Document that daily and Rollup queries and exports preserve absent days as missing records and stored zeros as zeros; warn against averaging absent days as zeros.
- List every catalog-backed raw endpoint in help and the command reference, and include supported names and typo suggestions in unsupported-endpoint errors.
- Add catalog-backed one-request `raw data-type <data-type> daily-rollup` and `rollup --window <duration>` exploration with sync-equivalent request construction, zero-effect planning, and safe exact-byte output.
- Add catalog-backed one-page `raw data-type <data-type> reconcile` reads with sync-equivalent request construction, zero-effect planning, and safe exact-byte output.
- Add catalog-backed `raw data-type <data-type> get --id <provider-id>` reads with opaque ID escaping, exact-byte stdout or safe file output, and zero-effect planning.
- Add `raw --output PATH` for exact Provider-byte output to a new private file, with atomic no-clobber publication, Linux and macOS mode `0600`, explicit Windows ACL behavior, and failed-write cleanup.

# Changelog

## Unreleased

- Add catalog-backed one-page `raw data-type <data-type> reconcile` reads with sync-equivalent request construction, zero-effect planning, and safe exact-byte output.
- Add catalog-backed `raw data-type <data-type> get --id <provider-id>` reads with opaque ID escaping, exact-byte stdout or safe file output, and zero-effect planning.
- Add `raw --output PATH` for exact Provider-byte output to a new private file, with atomic no-clobber publication, Linux and macOS mode `0600`, explicit Windows ACL behavior, and failed-write cleanup.

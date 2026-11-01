---
status: "accepted"
summary: "Binary Data Point Attachments (TCX routes, future byte-shaped exports) live as owner-only sidecar files next to the SQLite archive, tracked by a `data_point_attachments` table."
read_when:
  - "Implementing exportExerciseTcx ingestion or other byte-shaped Provider exports."
  - "Designing archive backup, move, or vacuum behavior."
  - "Reviewing whether a new binary payload belongs in `data_points.raw_json` or as an Attachment."
---
# Data Point Attachments Stored as Sidecar Files

`exportExerciseTcx` and any future byte-shaped Google Health API export return binary payloads (TCX XML, possibly ECG waveforms) that do not fit `data_points.raw_json TEXT NOT NULL`. Rather than introducing a BLOB column on `data_points`, attachment bytes live as owner-only sidecar files at `<archive-path>.attachments/<kind>/<sha256[0:2]>/<sha256>.<ext>`, tracked by a `data_point_attachments` table `(data_point_id, kind, sha256, path_relative, byte_size, fetched_at)`. The two-character SHA prefix shards the tree so a single kind directory doesn't grow to tens of thousands of entries; `path_relative` is stored with forward slashes so an archive moved between POSIX and Windows resolves consistently.

The trade-off vs an on-row BLOB:

- Sidecar keeps the SQLite slim (TCX files for long rides routinely exceed 1 MB; ECG waveforms could be larger). BLOBs would bloat `VACUUM`, slow backups, and weight every `data_points` row scan even when the bytes are not needed.
- Hash-keyed paths give content-addressed dedupe: re-fetching the same TCX produces the same sidecar.
- The archive remains portable as long as the sidecar directory travels with the SQLite file. The CLI must check both when validating archive integrity.
- Attachments inherit the same owner-only POSIX-permission rules as the SQLite archive itself.

The cost of this stance is that the archive is no longer a single file. `doctor` validates the sidecar root and every referenced Attachment through the shared canonical-path, no-follow read, size, and SHA-256 checks. It reports both kinds of orphan without migrating, creating, or repairing archive state. `init` must create it. Any "move my archive" instructions to users must mention the sidecar directory.

A Data Point Attachment is read-only, append-only, and tied to exactly one Data Point. Re-ingestion that produces the same SHA256 is a no-op; differing bytes for the same Data Point insert a new attachment row (the previous attachment is retained for audit, analogous to Data Point Revisions).

## Reading Attachments

`route` is the first reader of Attachment bytes outside Snapshot export. It opens the Health Archive inspection-only (mode=ro, no migration, no sidecar repair), selects the newest row for the Data Point by `fetched_at` and then `id`, and never falls back to an older row when the newest fails. Before any output it requires the row's canonical `path_relative`, a contained no-follow read of a regular file under a real attachment root, and a byte size and SHA-256 that match the row. A failed check is Attachment corruption for `doctor`, not a missing route. Because re-ingesting bytes that are already archived reuses their row, `fetched_at` records when those bytes were first archived: newest means the most recently first-archived bytes, not the latest Provider response.

`doctor` uses one inspection-only Health Archive handle for schema validation,
token metadata, and the Attachment walk. It rejects missing or unsafe roots
before walking, even when there are no Attachment rows. Root validation uses
the platform no-follow helper, including Windows reparse-point rejection.
Verification failures retain the full diagnostic report with `setup_invalid`
and exit 1. Existing orphan-only reports retain `ok` and exit 0. Recovery of
corrupt content requires a matching archive and sidecar copy known to be good;
re-sync does not repair an existing corrupt sidecar.

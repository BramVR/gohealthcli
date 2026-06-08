---
status: "accepted"
summary: "Binary Data Point Attachments (TCX routes, future byte-shaped exports) live as owner-only sidecar files next to the SQLite archive, tracked by a `data_point_attachments` table."
read_when:
  - "Implementing exportExerciseTcx ingestion or other byte-shaped Provider exports."
  - "Designing archive backup, move, or vacuum behavior."
  - "Reviewing whether a new binary payload belongs in `data_points.raw_json` or as an Attachment."
---
# Data Point Attachments Stored as Sidecar Files

`exportExerciseTcx` and any future byte-shaped Google Health API export return binary payloads (TCX XML, possibly ECG waveforms) that do not fit `data_points.raw_json TEXT NOT NULL`. Rather than introducing a BLOB column on `data_points`, attachment bytes live as owner-only sidecar files at `<archive-path>.attachments/<kind>/<sha256>.<ext>`, tracked by a `data_point_attachments` table `(data_point_id, kind, sha256, path_relative, byte_size, fetched_at)`.

The trade-off vs an on-row BLOB:

- Sidecar keeps the SQLite slim (TCX files for long rides routinely exceed 1 MB; ECG waveforms could be larger). BLOBs would bloat `VACUUM`, slow backups, and weight every `data_points` row scan even when the bytes are not needed.
- Hash-keyed paths give content-addressed dedupe: re-fetching the same TCX produces the same sidecar.
- The archive remains portable as long as the sidecar directory travels with the SQLite file. The CLI must check both when validating archive integrity.
- Attachments inherit the same owner-only POSIX-permission rules as the SQLite archive itself.

The cost of this stance is that the archive is no longer a single file. `doctor` must validate the sidecar directory (presence, ownership, missing files for referenced rows). `init` must create it. Any "move my archive" instructions to users must mention the sidecar directory.

A Data Point Attachment is read-only, append-only, and tied to exactly one Data Point. Re-ingestion that produces the same SHA256 is a no-op; differing bytes for the same Data Point insert a new attachment row (the previous attachment is retained for audit, analogous to Data Point Revisions).

---
status: "accepted"
summary: "Sync Cursors advance only on sync_completed, not on max(archived timestamp), so failure recovery has explicit semantics."
read_when:
  - "Designing incremental sync, resume, or backfill logic."
  - "Deciding what a Sync Run should update on success vs failure."
  - "Reviewing whether a query against data_points can substitute for a cursor read."
---
# Sync Cursors Advance Only on `sync_completed`

To make `gohealthcli sync` incrementally re-runnable without explicit `--from`, the archive carries a `Sync Cursor` per `(Connection, Data Type, source-family filter, rollup kind)` tuple. (Endpoint family is derived from the source-family filter and the rollup kind by the existing planner, so it is not part of the key.) The cursor is a durable highwater mark advanced by the Sync Run module **only when the run finishes with status `sync_completed`**. A partial or failed run leaves the cursor at its prior value even though some Data Points may already be archived.

This is deliberately not `max(start_time_utc, end_time_utc, civil_date, provider_civil_date)` over archived rows. That derivation already mixes columns differently per Data Type (`health_archive_reader.go`), and using it as a cursor would make the COALESCE chain load-bearing for correctness. Failure cases would also become impossible to distinguish from "successfully synced through that point" — archived rows from a partial run would silently advance the implicit watermark.

The cost of this stance is that a Sync Cursor may legitimately trail the visible archive after a partial run, and the next sync may re-fetch some already-archived Data Points. That re-fetch is idempotent because upsert dedupe is independent.

A Sync Cursor is the resume point, not a completeness claim. Gap tracking, if it lands later, is a separate concept.

---
name: verify-sync-cursors
description: "Check Sync Cursor finalization after changes to cursor keys, terminal writes, transaction cleanup, or SQLite."
---

# Verify Sync Cursor finalization

Run from the repository root against synthetic local SQLite only.

```sh
python3 verification/sync-cursors/check.py
python3 verification/sync-cursors/check.py --mutations
python3 -B -m unittest discover -s verification/sync-cursors -p test_check.py
go test -count=1 -v -run '^TestSyncRunCommitFailureReleasesTransaction$' ./cmd/gohealthcli
```

The runner saves logs and TLC traces under ignored `dist/formal-verification/run-*`. Go output is saved as both JSON events and decoded text. Each diagnostic belongs to its named test, including parallel subtests. Exit 0 means every selected check passed. Exit 1 means a property failed. Exit 2 means verification could not complete. Require every named Go result even when another property fails. A build failure, skipped proof, or command with missing tests is incomplete proof.

Read the full [model](../../../verification/sync-cursors/SyncCursors.tla), [config](../../../verification/sync-cursors/SyncCursors.cfg), and failing trace. The [view](../../../verification/sync-cursors/view.mjs) labels states and actions for a statechart. `Safety.cfg` checks completion and transaction release independently; its pass never replaces the full result.

## Properties and current findings

- `CompletedOnly`. Only a committed `sync_completed` run may publish a new cursor for its key.
- `CursorHighwater`. A successful older window must not reduce the cursor.
- `TransactionReleased`. Rejected finalization releases its transaction before retry or recovery.
- The Go safety oracle compares entire cursor tables and both audit rows. It checks statement rollback, counts, and isolation across every key component.

At the reviewed commit below, #472 remains a failing finding. Complete same-key runs through Jan 3, then Jan 2. The cursor becomes Jan 2. TLC and `TestFormalSyncCursorHighwater` must report that failure until the product fix lands. Do not disable the invariant, convert it to an expected-success exception, or claim a full pass.

The historical rejected-COMMIT leak is resolved by the current SQLite dependency. `modernc.org/sqlite v1.53.0` rolls back an active transaction after a COMMIT error and returns the original error. The production finalizer still uses `sql.Tx.Commit` with deferred `Rollback`. No duplicate product cleanup is needed.

## Check correspondence before trusting a pass

Last code-sync commit: `8ef6efe8ba5221db9ba365e607f3c41e87fc44ab`.

Review changes since that commit in these repository-root paths:

- `cmd/gohealthcli/sync_cursor.go`. Cursor key, completed-only guard, reads, and transactional upsert.
- `cmd/gohealthcli/health_archive_writer.go`. `syncRunStart`, `syncRunFinalize`, `runFinish`, `StartSyncRun`, `FinalizeSyncRun`, `finalizeSyncRunAttempt`, and terminal SQL writes.
- `cmd/gohealthcli/sync_run_lifecycle.go`. Recovery outcome mapping and bounded retries. Only control-flow correspondence is reviewed; the Go oracle calls the writer directly.
- `cmd/gohealthcli/health_archive_lifecycle.go`. Context-aware archive openers, single-connection writer pool, and DSN busy timeout.
- `cmd/gohealthcli/health_archive_migrations.go`. Current schema and migration entry point with injected clock.
- `internal/archived/archived.go`. `Connection` inputs to Sync Run start.
- `go.mod`, `go.sum`, and the selected SQLite module's `tx.go`. Review actual COMMIT cleanup when changing the dependency.
- `verification/sync-cursors/`. Model, configs, view, runner, and mutation anchors.
- `cmd/gohealthcli/sync_cursor_formal_test.go` and `cmd/gohealthcli/sync_run_commit_failure_test.go`. Tagged schedule oracle and ordinary automatic contention regression.

`runKey`, `horizon`, and `requestedOutcome` represent finalization inputs after ingestion. Initial running rows represent committed `StartSyncRun` calls. `Finalize` represents one durable transaction commit. Statement failures become `AbortFinalize`. `CommitRejected` preserves durable run/cursor state and leaves the connection clean, matching the driver's rollback. `Recover` represents a separate successful terminal write; `RecoveryExhausted` represents no durable recovery write. `highwater` is an independent specification oracle.

If a trace does not replay against Go, treat it as a correspondence error. For intended behavior changes, update model, code mapping, oracle, and replay together. Advance the sync commit only after reviewing those mappings and rerunning full and mutation checks.

## Bounds and tools

TLC 1.7.4 explores two started runs, two abstract keys, horizons 1 and 2, absent or rank-1 initial cursors, and all terminal outcomes. Both runs may share a key. Statement abort, rejected COMMIT, recovery success, and recovery exhaustion are nondeterministic alternatives. Retries without durable changes collapse into those alternatives. Eventual recovery is not promised.

`TestFormalSyncCursorSafety` runs 1,620 SQLite schedules. It covers five key relationships, absent or seeded cursors, nine outcome pairs, nine statement-fault pairs, and both finalization orders. The expected cursor follows the current unconditional upsert; the separate highwater replay and model retain the stronger requirement.

`TestFormalSyncCursorCommitFailure` calls the ordinary `TestSyncRunCommitFailureReleasesTransaction` regression. Both absent and existing cursors use file-backed rollback-journal SQLite, an independent reader with an acquired SELECT lock, and `busy_timeout=0`. COMMIT must exhaust the real bounded retry budget with a busy cause. After reader release, a new transaction on the same writer must succeed, durable state must be unchanged, and later successful finalization must publish both run and cursor. No sleeps synchronize the lock. No mock replaces COMMIT. The ordinary test runs in the existing Linux and Windows `go test ./...` jobs.

The runner uses the Go toolchain declared by `go.mod` and records its version, SQLite version, and checkout commit. It checksum-verifies TLC 1.7.4 and downloads it into ignored `dist/formal-tools`. Requirements are Python 3, curl, Go, and Java 11+. Set `JAVA` to a Java executable if needed. No Go dependencies are added.

Excluded properties include Provider completeness, pagination, identity verification, ingestion correctness, arbitrary time representations and key strings, more than two runs, scheduler fairness, process crashes, filesystem durability, Attachments, and SQLite internals. Bounded model checks and real-code replays do not prove arbitrary Go executions.

## Prove sensitivity

`--mutations` first requires the completion and transaction baseline to pass. Separate Go overlays remove the completed-only guard, publish terminal status outside its transaction, erase the source-family key, and remove the SQLite driver's COMMIT rollback. Go forbids module-cache overlays, so the driver check first copies the module into a disposable directory outside the repository and requires an unmodified baseline through a task-local modfile. Each mutation must fail its intended assertion. A changed source anchor stops the run for coverage review. Replacement filenames end in `.overlay` so ordinary Go discovery cannot compile scratch sources. Working-tree source and the module cache remain unchanged. The final unchanged baseline must pass again. This does not claim that the full highwater check passes.

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"
)

type healthArchiveWriter interface {
	Close() error
	CurrentConnection(ctx context.Context) (archivedConnection, error)
	StartSyncRun(ctx context.Context, start syncRunStart) (int64, error)
	// HeartbeatSyncRun refreshes the running counts and the
	// last_progress_at heartbeat on an in-flight sync_running row
	// (#236). Heartbeats are advisory snapshots for concurrent
	// `sync --status` readers; FinalizeSyncRun stays the authoritative
	// terminal write.
	HeartbeatSyncRun(ctx context.Context, heartbeat syncRunHeartbeat) error
	// FenceAbandonedSyncRuns drives orphaned sync_running rows to
	// sync_failed on the writer's own handle (#236) — the sync
	// lifecycle runs it on entry so a killed process's corpse row
	// never sits next to a live one. See fenceAbandonedSyncRuns.
	FenceAbandonedSyncRuns(ctx context.Context, now time.Time) (int64, error)
	FinishSyncRun(ctx context.Context, finish syncRunFinish) error
	FinalizeSyncRun(ctx context.Context, finalize syncRunFinalize) error
	UpsertDataPoint(ctx context.Context, point archivedDataPoint, now string) (string, error)
	UpsertRollup(ctx context.Context, rollup archivedRollup, now string) (string, error)
	// StoreAttachment writes a content-addressed sidecar file via the
	// Attachment Store (ADR-0009) and inserts a data_point_attachments
	// row linked to the just-upserted Data Point identified by `point`'s
	// identity columns. Used by exercise sync to archive TCX bytes.
	StoreAttachment(ctx context.Context, point archivedDataPoint, kind string, payload []byte, fetchedAt string) error
	ResolveSyncCursor(ctx context.Context, key syncCursorKey) (string, bool, error)
	CommitSyncCursor(ctx context.Context, key syncCursorKey, outcome syncRunOutcome, to, advancedAt string) error
	// UpdateConnectionTokenMetadata lets sync persist a refreshed access
	// token on the same writer it is already holding open, so the
	// auto-refresh path does not need to open a second archive handle.
	// Deliberately context-free (the connectionTokenWriter seam): the
	// OAuth refresh has already happened by the time this runs, and
	// aborting the metadata write on SIGINT would discard a valid token
	// the Credential Store already holds (#305).
	UpdateConnectionTokenMetadata(connectionID string, token oauthTokenResponse, now time.Time) error
}

// syncRunStart bundles the inputs StartSyncRun writes into the
// sync_runs audit row. Matches the syncRunFinalize parameter-struct
// pattern (#277) so the start/finish/finalize writer chain shares one
// shape language and no call site lines up seven positional arguments.
type syncRunStart struct {
	Connection         archivedConnection
	DataTypes          []string
	From               string
	To                 string
	EndpointFamily     string
	SourceFamilyFilter string
	StartedAt          string
}

// syncRunFinish bundles the terminal-row UPDATE parameters shared by
// FinishSyncRun and the finalize transaction's run-status write (#277).
// The three counts ride named fields (SeenCount/NewCount/UpdatedCount)
// so adjacent bare ints can no longer be silently transposed at a call
// site. Status stays a string here — the lifecycle's recovery write
// legitimately diverges from the original outcome (sync_completed
// downgrades to sync_failed after a rolled-back finalize) and the
// outcome-sealed path already goes through syncRunFinalize.
type syncRunFinish struct {
	SyncRunID    int64
	Status       string
	SeenCount    int
	NewCount     int
	UpdatedCount int
	FinishedAt   string
	ErrorSummary string
}

// syncRunHeartbeat bundles the advisory progress UPDATE parameters for
// HeartbeatSyncRun (#236) — same named-count rationale as
// syncRunFinish, so the trio a concurrent `sync --status` poller reads
// mid-run cannot be transposed either.
type syncRunHeartbeat struct {
	SyncRunID    int64
	SeenCount    int
	NewCount     int
	UpdatedCount int
	At           string
}

// syncRunFinalize bundles the writes that finalize a Sync Run into one
// struct so FinalizeSyncRun does not need a long positional-arg
// signature. Outcome is the single source of truth: the run-status UPDATE
// uses string(Outcome) and Outcome.AdvancesCursor() gates the cursor
// write, so a caller cannot construct an "outcome says completed but
// status says canceled" inconsistency that would re-open the ADR-0008
// race at the struct boundary instead of the storage boundary.
type syncRunFinalize struct {
	SyncRunID      int64
	Outcome        syncRunOutcome
	SeenCount      int
	NewCount       int
	UpdatedCount   int
	FinishedAt     string
	ErrorSummary   string
	CursorKey      syncCursorKey
	CursorTo       string
	CursorAdvanced string
}

// runFinish projects the finalize bundle onto the terminal-row UPDATE
// parameters, so the atomic finalize path and the standalone
// FinishSyncRun write the sync_runs row through one field mapping —
// the advisory and authoritative count columns cannot drift.
func (finalize syncRunFinalize) runFinish() syncRunFinish {
	return syncRunFinish{
		SyncRunID:    finalize.SyncRunID,
		Status:       string(finalize.Outcome),
		SeenCount:    finalize.SeenCount,
		NewCount:     finalize.NewCount,
		UpdatedCount: finalize.UpdatedCount,
		FinishedAt:   finalize.FinishedAt,
		ErrorSummary: finalize.ErrorSummary,
	}
}

type sqliteHealthArchiveWriter struct {
	db *sql.DB
	// archivePath is the path to the SQLite file. The attachment store
	// derives its sidecar root from this (`<archivePath>.attachments`)
	// when StoreAttachment is invoked from the ingestion hook.
	archivePath string
}

func openHealthArchiveWriter(archivePath string) (healthArchiveWriter, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(context.Background(), writeArchive)
	if err != nil {
		return nil, err
	}
	return &sqliteHealthArchiveWriter{db: handle.db, archivePath: archivePath}, nil
}

func (archive *sqliteHealthArchiveWriter) Close() error {
	return archive.db.Close()
}

func (archive *sqliteHealthArchiveWriter) CurrentConnection(ctx context.Context) (archivedConnection, error) {
	connection, err := readCurrentConnection(ctx, archive.db)
	if errors.Is(err, sql.ErrNoRows) {
		return archivedConnection{}, errors.New("no Connection found; run `gohealthcli connect` first")
	}
	return connection, err
}

func (archive *sqliteHealthArchiveWriter) StartSyncRun(ctx context.Context, start syncRunStart) (int64, error) {
	return insertSyncRun(ctx, archive.db, start)
}

func (archive *sqliteHealthArchiveWriter) FinishSyncRun(ctx context.Context, finish syncRunFinish) error {
	return finishSyncRun(ctx, archive.db, finish)
}

// HeartbeatSyncRun is a single autocommit UPDATE — deliberately not a
// transaction and not retried. A heartbeat that loses a SQLITE_BUSY
// race is simply skipped by the caller; the next page writes a fresh
// one, and the finalize transaction still owns the terminal counts.
// The WHERE clause keeps the heartbeat from resurrecting a row that a
// concurrent fence (or finalize) already drove to a terminal status:
// a late heartbeat against a non-running row touches zero rows.
func (archive *sqliteHealthArchiveWriter) HeartbeatSyncRun(ctx context.Context, heartbeat syncRunHeartbeat) error {
	_, err := archive.db.ExecContext(ctx, `UPDATE sync_runs SET
		seen_count = ?,
		new_count = ?,
		updated_count = ?,
		last_progress_at = ?
	WHERE id = ? AND status = 'sync_running'`,
		heartbeat.SeenCount,
		heartbeat.NewCount,
		heartbeat.UpdatedCount,
		heartbeat.At,
		heartbeat.SyncRunID,
	)
	return err
}

func (archive *sqliteHealthArchiveWriter) FenceAbandonedSyncRuns(ctx context.Context, now time.Time) (int64, error) {
	return fenceAbandonedSyncRuns(ctx, archive.db, now)
}

func (archive *sqliteHealthArchiveWriter) UpsertDataPoint(ctx context.Context, point archivedDataPoint, now string) (string, error) {
	return upsertDataPoint(ctx, archive.db, point, now)
}

func (archive *sqliteHealthArchiveWriter) UpsertRollup(ctx context.Context, rollup archivedRollup, now string) (string, error) {
	return upsertRollup(ctx, archive.db, rollup, now)
}

func (archive *sqliteHealthArchiveWriter) ResolveSyncCursor(ctx context.Context, key syncCursorKey) (string, bool, error) {
	return resolveSyncCursor(ctx, archive.db, key)
}

func (archive *sqliteHealthArchiveWriter) CommitSyncCursor(ctx context.Context, key syncCursorKey, outcome syncRunOutcome, to, advancedAt string) error {
	return commitSyncCursor(ctx, archive.db, key, outcome, to, advancedAt)
}

func (archive *sqliteHealthArchiveWriter) UpdateConnectionTokenMetadata(connectionID string, token oauthTokenResponse, now time.Time) error {
	// context.Background(): token persistence is deliberately not
	// cancelable — see the interface comment (#305).
	return updateConnectionTokenMetadata(context.Background(), archive.db, connectionID, token, now)
}

// StoreAttachment resolves the data_point row id for the just-upserted
// Data Point and writes the bytes as a content-addressed sidecar via
// the Attachment Store (ADR-0009). The write reuses the same SQLite
// handle the rest of sync holds so there's only one BEGIN IMMEDIATE
// holder per Sync Run.
func (archive *sqliteHealthArchiveWriter) StoreAttachment(ctx context.Context, point archivedDataPoint, kind string, payload []byte, fetchedAt string) error {
	dataPointID, _, found, err := findExistingDataPoint(ctx, archive.db, point)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("StoreAttachment: data_point row not found for upserted point")
	}
	rootDir := attachmentRootDirForArchive(archive.archivePath)
	if err := ensureOwnerOnlyDir(rootDir); err != nil {
		return err
	}
	store := &attachmentStore{db: archive.db, archivePath: archive.archivePath, rootDir: rootDir}
	_, err = store.Store(ctx, dataPointID, kind, payload, fetchedAt)
	return err
}

// FinalizeSyncRun atomically writes the Sync Run terminal status AND
// advances the Sync Cursor (when the outcome warrants it) inside one
// SQLite transaction. This closes the ADR-0008 race where a crash
// between the legacy FinishSyncRun and CommitSyncCursor calls could
// leave the sync_run row marked sync_completed while the cursor was
// still stale. The cursor advance is gated by the outcome via
// commitSyncCursorTx, so sync_failed and sync_canceled finalize the
// run row without touching the cursor.
//
// The body runs under retryFinalizeSyncRunOnBusy so transient
// SQLITE_BUSY contention from a competing writer process (slice 4 of
// PRD #141) does not surface to the caller as a finalize failure
// before a small bounded number of retries. When the budget is
// exhausted the typed errFinalizeSyncRunBusyExhausted bubbles up so
// the lifecycle module can drive its recovery write.
func (archive *sqliteHealthArchiveWriter) FinalizeSyncRun(ctx context.Context, finalize syncRunFinalize) error {
	return retryFinalizeSyncRunOnBusy(finalizeSyncRunRetryBudget, func() error {
		return archive.finalizeSyncRunAttempt(ctx, finalize)
	})
}

func (archive *sqliteHealthArchiveWriter) finalizeSyncRunAttempt(ctx context.Context, finalize syncRunFinalize) error {
	tx, err := archive.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := finishSyncRun(ctx, tx, finalize.runFinish()); err != nil {
		return err
	}
	if err := commitSyncCursorTx(ctx, tx, finalize.CursorKey, finalize.Outcome, finalize.CursorTo, finalize.CursorAdvanced); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func insertSyncRun(ctx context.Context, db *sql.DB, start syncRunStart) (int64, error) {
	dataTypesJSON, err := json.Marshal(start.DataTypes)
	if err != nil {
		return 0, err
	}
	rangeJSON, err := json.Marshal(map[string]string{"from": start.From, "to": start.To})
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO sync_runs (
		provider_name,
		connection_id,
		data_types_requested,
		range_requested_json,
		endpoint_family,
		source_family_filter,
		status,
		started_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		start.Connection.providerName,
		start.Connection.id,
		string(dataTypesJSON),
		string(rangeJSON),
		start.EndpointFamily,
		nullString(start.SourceFamilyFilter),
		"sync_running",
		start.StartedAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// finishSyncRun is the one terminal-row UPDATE. It accepts the
// sqlExecutor seam so the standalone FinishSyncRun (autocommit on the
// db handle) and the atomic finalize transaction compose the same
// write — there is no second column list to drift.
func finishSyncRun(ctx context.Context, executor sqlExecutor, finish syncRunFinish) error {
	_, err := executor.ExecContext(ctx, `UPDATE sync_runs SET
		status = ?,
		seen_count = ?,
		new_count = ?,
		updated_count = ?,
		finished_at = ?,
		error_summary = ?
	WHERE id = ?`,
		finish.Status,
		finish.SeenCount,
		finish.NewCount,
		finish.UpdatedCount,
		finish.FinishedAt,
		nullString(finish.ErrorSummary),
		finish.SyncRunID,
	)
	return err
}

func upsertDataPoint(ctx context.Context, db *sql.DB, point archivedDataPoint, now string) (string, error) {
	existingID, existingRawJSON, found, err := findExistingDataPoint(ctx, db, point)
	if err != nil {
		return "", err
	}
	if !found {
		_, err := db.ExecContext(ctx, `INSERT INTO data_points (
			provider_name,
			connection_id,
			data_type,
			upstream_resource_name,
			record_kind,
			start_time_utc,
			end_time_utc,
			start_civil_time,
			end_civil_time,
			provider_civil_date,
			timezone_metadata,
			data_source_json,
			source_family_filter,
			raw_json,
			inserted_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			point.providerName,
			point.connectionID,
			point.dataType,
			nullString(point.upstreamResourceName),
			point.recordKind,
			nullString(point.startTimeUTC),
			nullString(point.endTimeUTC),
			nullString(point.startCivilTime),
			nullString(point.endCivilTime),
			nullString(point.providerCivilDate),
			nullString(point.timezoneMetadataJSON),
			point.dataSourceJSON,
			nullString(point.sourceFamilyFilter),
			point.rawJSON,
			now,
			now,
		)
		if err != nil {
			return "", err
		}
		return "new", nil
	}
	if sameJSONValue(existingRawJSON, point.rawJSON) {
		return "unchanged", nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	// Rollback after a successful Commit returns sql.ErrTxDone; the error is
	// deliberately ignored because this defer is only the abort path.
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO data_point_revisions (
		data_point_id,
		previous_raw_json,
		replaced_at,
		replacement_reason
	) VALUES (?, ?, ?, ?)`, existingID, existingRawJSON, now, "provider_correction"); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE data_points SET
		record_kind = ?,
		start_time_utc = ?,
		end_time_utc = ?,
		start_civil_time = ?,
		end_civil_time = ?,
		provider_civil_date = ?,
		timezone_metadata = ?,
		data_source_json = ?,
		source_family_filter = ?,
		raw_json = ?,
		updated_at = ?
	WHERE id = ?`,
		point.recordKind,
		nullString(point.startTimeUTC),
		nullString(point.endTimeUTC),
		nullString(point.startCivilTime),
		nullString(point.endCivilTime),
		nullString(point.providerCivilDate),
		nullString(point.timezoneMetadataJSON),
		point.dataSourceJSON,
		nullString(point.sourceFamilyFilter),
		point.rawJSON,
		now,
		existingID,
	); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return "updated", nil
}

func upsertRollup(ctx context.Context, db *sql.DB, rollup archivedRollup, now string) (string, error) {
	existingID, existingRawJSON, found, err := findExistingRollup(ctx, db, rollup)
	if err != nil {
		return "", err
	}
	if !found {
		_, err := db.ExecContext(ctx, `INSERT INTO rollups (
			provider_name,
			connection_id,
			data_type,
			rollup_kind,
			window_start_utc,
			window_end_utc,
			civil_date,
			timezone_metadata,
			raw_json,
			inserted_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rollup.providerName,
			rollup.connectionID,
			rollup.dataType,
			rollup.rollupKind,
			nullString(rollup.windowStartUTC),
			nullString(rollup.windowEndUTC),
			nullString(rollup.civilDate),
			nullString(rollup.timezoneMetadataJSON),
			rollup.rawJSON,
			now,
			now,
		)
		if err != nil {
			return "", err
		}
		return "new", nil
	}
	if sameJSONValue(existingRawJSON, rollup.rawJSON) {
		return "unchanged", nil
	}
	_, err = db.ExecContext(ctx, `UPDATE rollups SET
		timezone_metadata = ?,
		raw_json = ?,
		updated_at = ?
	WHERE id = ?`,
		nullString(rollup.timezoneMetadataJSON),
		rollup.rawJSON,
		now,
		existingID,
	)
	if err != nil {
		return "", err
	}
	return "updated", nil
}

func findExistingRollup(ctx context.Context, db *sql.DB, rollup archivedRollup) (int64, string, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, raw_json FROM rollups
	WHERE provider_name = ?
		AND connection_id = ?
		AND data_type = ?
		AND rollup_kind = ?
		AND IFNULL(window_start_utc, '') = ?
		AND IFNULL(window_end_utc, '') = ?
		AND IFNULL(civil_date, '') = ?
	ORDER BY id LIMIT 2`,
		rollup.providerName,
		rollup.connectionID,
		rollup.dataType,
		rollup.rollupKind,
		rollup.windowStartUTC,
		rollup.windowEndUTC,
		rollup.civilDate,
	)
	if err != nil {
		return 0, "", false, err
	}
	defer rows.Close()
	type match struct {
		id      int64
		rawJSON string
	}
	var matches []match
	for rows.Next() {
		var item match
		if err := rows.Scan(&item.id, &item.rawJSON); err != nil {
			return 0, "", false, err
		}
		matches = append(matches, item)
	}
	if err := rows.Err(); err != nil {
		return 0, "", false, err
	}
	if len(matches) == 0 {
		return 0, "", false, nil
	}
	if len(matches) > 1 {
		return 0, "", false, errors.New("multiple archived Rollups match provider identity and window")
	}
	return matches[0].id, matches[0].rawJSON, true, nil
}

func findExistingDataPoint(ctx context.Context, db *sql.DB, point archivedDataPoint) (int64, string, bool, error) {
	if point.upstreamResourceName != "" {
		return findExistingDataPointByQuery(ctx, db, `SELECT id, raw_json FROM data_points
		WHERE provider_name = ?
			AND connection_id = ?
			AND data_type = ?
			AND upstream_resource_name = ?
			AND IFNULL(source_family_filter, '') = ?
		ORDER BY id LIMIT 2`,
			point.providerName,
			point.connectionID,
			point.dataType,
			point.upstreamResourceName,
			point.sourceFamilyFilter,
		)
	}
	return findExistingDataPointByQuery(ctx, db, `SELECT id, raw_json FROM data_points
	WHERE provider_name = ?
		AND connection_id = ?
		AND data_type = ?
		AND IFNULL(upstream_resource_name, '') = ?
		AND record_kind = ?
		AND IFNULL(start_time_utc, '') = ?
		AND IFNULL(end_time_utc, '') = ?
		AND IFNULL(start_civil_time, '') = ?
		AND IFNULL(end_civil_time, '') = ?
		AND IFNULL(provider_civil_date, '') = ?
		AND IFNULL(timezone_metadata, '') = ?
		AND data_source_json = ?
		AND IFNULL(source_family_filter, '') = ?
	ORDER BY id LIMIT 2`,
		point.providerName,
		point.connectionID,
		point.dataType,
		point.upstreamResourceName,
		point.recordKind,
		point.startTimeUTC,
		point.endTimeUTC,
		point.startCivilTime,
		point.endCivilTime,
		point.providerCivilDate,
		point.timezoneMetadataJSON,
		point.dataSourceJSON,
		point.sourceFamilyFilter,
	)
}

func sameJSONValue(left, right string) bool {
	if left == right {
		return true
	}
	var leftValue any
	var rightValue any
	if err := json.Unmarshal([]byte(left), &leftValue); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(right), &rightValue); err != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func findExistingDataPointByQuery(ctx context.Context, db *sql.DB, query string, args ...any) (int64, string, bool, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, "", false, err
	}
	defer rows.Close()
	type match struct {
		id      int64
		rawJSON string
	}
	var matches []match
	for rows.Next() {
		var item match
		if err := rows.Scan(&item.id, &item.rawJSON); err != nil {
			return 0, "", false, err
		}
		matches = append(matches, item)
	}
	if err := rows.Err(); err != nil {
		return 0, "", false, err
	}
	if len(matches) == 0 {
		return 0, "", false, nil
	}
	if len(matches) > 1 {
		return 0, "", false, errors.New("multiple archived Data Points match provider identity and time metadata")
	}
	return matches[0].id, matches[0].rawJSON, true, nil
}

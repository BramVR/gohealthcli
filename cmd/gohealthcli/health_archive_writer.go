package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"
)

type healthArchiveWriter interface {
	Close() error
	CurrentConnection() (archivedConnection, error)
	StartSyncRun(connection archivedConnection, dataTypes []string, from, to, endpointFamily, sourceFamilyFilter, startedAt string) (int64, error)
	// HeartbeatSyncRun refreshes the running counts and the
	// last_progress_at heartbeat on an in-flight sync_running row
	// (#236). Heartbeats are advisory snapshots for concurrent
	// `sync --status` readers; FinalizeSyncRun stays the authoritative
	// terminal write.
	HeartbeatSyncRun(id int64, seenCount, newCount, updatedCount int, at string) error
	// FenceAbandonedSyncRuns drives orphaned sync_running rows to
	// sync_failed on the writer's own handle (#236) — the sync
	// lifecycle runs it on entry so a killed process's corpse row
	// never sits next to a live one. See fenceAbandonedSyncRuns.
	FenceAbandonedSyncRuns(now time.Time) (int64, error)
	FinishSyncRun(id int64, status string, seenCount, newCount, updatedCount int, finishedAt, errorSummary string) error
	FinalizeSyncRun(finalize syncRunFinalize) error
	UpsertDataPoint(point archivedDataPoint, now string) (string, error)
	UpsertRollup(rollup archivedRollup, now string) (string, error)
	// StoreAttachment writes a content-addressed sidecar file via the
	// Attachment Store (ADR-0009) and inserts a data_point_attachments
	// row linked to the just-upserted Data Point identified by `point`'s
	// identity columns. Used by exercise sync to archive TCX bytes.
	StoreAttachment(point archivedDataPoint, kind string, payload []byte, fetchedAt string) error
	ResolveSyncCursor(key syncCursorKey) (string, bool, error)
	CommitSyncCursor(key syncCursorKey, outcome syncRunOutcome, to, advancedAt string) error
	// UpdateConnectionTokenMetadata lets sync persist a refreshed access
	// token on the same writer it is already holding open, so the
	// auto-refresh path does not need to open a second archive handle.
	UpdateConnectionTokenMetadata(connectionID string, token oauthTokenResponse, now time.Time) error
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

type sqliteHealthArchiveWriter struct {
	db *sql.DB
	// archivePath is the path to the SQLite file. The attachment store
	// derives its sidecar root from this (`<archivePath>.attachments`)
	// when StoreAttachment is invoked from the ingestion hook.
	archivePath string
}

func openHealthArchiveWriter(archivePath string) (healthArchiveWriter, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(writeArchive)
	if err != nil {
		return nil, err
	}
	return &sqliteHealthArchiveWriter{db: handle.db, archivePath: archivePath}, nil
}

func (archive *sqliteHealthArchiveWriter) Close() error {
	return archive.db.Close()
}

func (archive *sqliteHealthArchiveWriter) CurrentConnection() (archivedConnection, error) {
	connection, err := readCurrentConnection(archive.db)
	if errors.Is(err, sql.ErrNoRows) {
		return archivedConnection{}, errors.New("no Connection found; run `gohealthcli connect` first")
	}
	return connection, err
}

func (archive *sqliteHealthArchiveWriter) StartSyncRun(connection archivedConnection, dataTypes []string, from, to, endpointFamily, sourceFamilyFilter, startedAt string) (int64, error) {
	return insertSyncRun(archive.db, connection, dataTypes, from, to, endpointFamily, sourceFamilyFilter, startedAt)
}

func (archive *sqliteHealthArchiveWriter) FinishSyncRun(id int64, status string, seenCount, newCount, updatedCount int, finishedAt, errorSummary string) error {
	return finishSyncRun(archive.db, id, status, seenCount, newCount, updatedCount, finishedAt, errorSummary)
}

// HeartbeatSyncRun is a single autocommit UPDATE — deliberately not a
// transaction and not retried. A heartbeat that loses a SQLITE_BUSY
// race is simply skipped by the caller; the next page writes a fresh
// one, and the finalize transaction still owns the terminal counts.
// The WHERE clause keeps the heartbeat from resurrecting a row that a
// concurrent fence (or finalize) already drove to a terminal status:
// a late heartbeat against a non-running row touches zero rows.
func (archive *sqliteHealthArchiveWriter) HeartbeatSyncRun(id int64, seenCount, newCount, updatedCount int, at string) error {
	_, err := archive.db.Exec(`UPDATE sync_runs SET
		seen_count = ?,
		new_count = ?,
		updated_count = ?,
		last_progress_at = ?
	WHERE id = ? AND status = 'sync_running'`, seenCount, newCount, updatedCount, at, id)
	return err
}

func (archive *sqliteHealthArchiveWriter) FenceAbandonedSyncRuns(now time.Time) (int64, error) {
	return fenceAbandonedSyncRuns(archive.db, now)
}

func (archive *sqliteHealthArchiveWriter) UpsertDataPoint(point archivedDataPoint, now string) (string, error) {
	return upsertDataPoint(archive.db, point, now)
}

func (archive *sqliteHealthArchiveWriter) UpsertRollup(rollup archivedRollup, now string) (string, error) {
	return upsertRollup(archive.db, rollup, now)
}

func (archive *sqliteHealthArchiveWriter) ResolveSyncCursor(key syncCursorKey) (string, bool, error) {
	return resolveSyncCursor(archive.db, key)
}

func (archive *sqliteHealthArchiveWriter) CommitSyncCursor(key syncCursorKey, outcome syncRunOutcome, to, advancedAt string) error {
	return commitSyncCursor(archive.db, key, outcome, to, advancedAt)
}

func (archive *sqliteHealthArchiveWriter) UpdateConnectionTokenMetadata(connectionID string, token oauthTokenResponse, now time.Time) error {
	return updateConnectionTokenMetadata(archive.db, connectionID, token, now)
}

// StoreAttachment resolves the data_point row id for the just-upserted
// Data Point and writes the bytes as a content-addressed sidecar via
// the Attachment Store (ADR-0009). The write reuses the same SQLite
// handle the rest of sync holds so there's only one BEGIN IMMEDIATE
// holder per Sync Run.
func (archive *sqliteHealthArchiveWriter) StoreAttachment(point archivedDataPoint, kind string, payload []byte, fetchedAt string) error {
	dataPointID, _, found, err := findExistingDataPoint(archive.db, point)
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
	_, err = store.Store(dataPointID, kind, payload, fetchedAt)
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
func (archive *sqliteHealthArchiveWriter) FinalizeSyncRun(finalize syncRunFinalize) error {
	return retryFinalizeSyncRunOnBusy(finalizeSyncRunRetryBudget, func() error {
		return archive.finalizeSyncRunAttempt(finalize)
	})
}

func (archive *sqliteHealthArchiveWriter) finalizeSyncRunAttempt(finalize syncRunFinalize) error {
	tx, err := archive.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := finishSyncRunTx(tx, finalize.SyncRunID, string(finalize.Outcome), finalize.SeenCount, finalize.NewCount, finalize.UpdatedCount, finalize.FinishedAt, finalize.ErrorSummary); err != nil {
		return err
	}
	if err := commitSyncCursorTx(tx, finalize.CursorKey, finalize.Outcome, finalize.CursorTo, finalize.CursorAdvanced); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func insertSyncRun(db *sql.DB, connection archivedConnection, dataTypes []string, from, to, endpointFamily, sourceFamilyFilter, startedAt string) (int64, error) {
	dataTypesJSON, err := json.Marshal(dataTypes)
	if err != nil {
		return 0, err
	}
	rangeJSON, err := json.Marshal(map[string]string{"from": from, "to": to})
	if err != nil {
		return 0, err
	}
	result, err := db.Exec(`INSERT INTO sync_runs (
		provider_name,
		connection_id,
		data_types_requested,
		range_requested_json,
		endpoint_family,
		source_family_filter,
		status,
		started_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		connection.providerName,
		connection.id,
		string(dataTypesJSON),
		string(rangeJSON),
		endpointFamily,
		nullString(sourceFamilyFilter),
		"sync_running",
		startedAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func finishSyncRun(db *sql.DB, syncRunID int64, status string, seen, newCount, updated int, finishedAt, errorSummary string) error {
	return finishSyncRunExec(db, syncRunID, status, seen, newCount, updated, finishedAt, errorSummary)
}

// finishSyncRunTx is the same write as finishSyncRun but bound to an open
// transaction so it can compose with commitSyncCursorTx inside
// FinalizeSyncRun.
func finishSyncRunTx(tx *sql.Tx, syncRunID int64, status string, seen, newCount, updated int, finishedAt, errorSummary string) error {
	return finishSyncRunExec(tx, syncRunID, status, seen, newCount, updated, finishedAt, errorSummary)
}

type sqlExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func finishSyncRunExec(executor sqlExecutor, syncRunID int64, status string, seen, newCount, updated int, finishedAt, errorSummary string) error {
	_, err := executor.Exec(`UPDATE sync_runs SET
		status = ?,
		seen_count = ?,
		new_count = ?,
		updated_count = ?,
		finished_at = ?,
		error_summary = ?
	WHERE id = ?`, status, seen, newCount, updated, finishedAt, nullString(errorSummary), syncRunID)
	return err
}

func upsertDataPoint(db *sql.DB, point archivedDataPoint, now string) (string, error) {
	existingID, existingRawJSON, found, err := findExistingDataPoint(db, point)
	if err != nil {
		return "", err
	}
	if !found {
		_, err := db.Exec(`INSERT INTO data_points (
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
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	// Rollback after a successful Commit returns sql.ErrTxDone; the error is
	// deliberately ignored because this defer is only the abort path.
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO data_point_revisions (
		data_point_id,
		previous_raw_json,
		replaced_at,
		replacement_reason
	) VALUES (?, ?, ?, ?)`, existingID, existingRawJSON, now, "provider_correction"); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE data_points SET
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

func upsertRollup(db *sql.DB, rollup archivedRollup, now string) (string, error) {
	existingID, existingRawJSON, found, err := findExistingRollup(db, rollup)
	if err != nil {
		return "", err
	}
	if !found {
		_, err := db.Exec(`INSERT INTO rollups (
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
	_, err = db.Exec(`UPDATE rollups SET
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

func findExistingRollup(db *sql.DB, rollup archivedRollup) (int64, string, bool, error) {
	rows, err := db.Query(`SELECT id, raw_json FROM rollups
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

func findExistingDataPoint(db *sql.DB, point archivedDataPoint) (int64, string, bool, error) {
	if point.upstreamResourceName != "" {
		return findExistingDataPointByQuery(db, `SELECT id, raw_json FROM data_points
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
	return findExistingDataPointByQuery(db, `SELECT id, raw_json FROM data_points
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

func findExistingDataPointByQuery(db *sql.DB, query string, args ...any) (int64, string, bool, error) {
	rows, err := db.Query(query, args...)
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

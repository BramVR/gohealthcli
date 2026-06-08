package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
)

type healthArchiveWriter interface {
	Close() error
	CurrentConnection() (archivedConnection, error)
	StartSyncRun(connection archivedConnection, dataTypes []string, from, to, endpointFamily, sourceFamilyFilter, startedAt string) (int64, error)
	FinishSyncRun(id int64, status string, seenCount, newCount, updatedCount int, finishedAt, errorSummary string) error
	UpsertDataPoint(point archivedDataPoint, now string) (string, error)
	UpsertRollup(rollup archivedRollup, now string) (string, error)
	ResolveSyncCursor(key syncCursorKey) (string, bool, error)
	CommitSyncCursor(key syncCursorKey, outcome syncRunOutcome, to, advancedAt string) error
}

type sqliteHealthArchiveWriter struct {
	db *sql.DB
}

var finishSyncRunRecord = finishSyncRun

func openHealthArchiveWriter(archivePath string) (healthArchiveWriter, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(writeArchive)
	if err != nil {
		return nil, err
	}
	return &sqliteHealthArchiveWriter{db: handle.db}, nil
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
	return finishSyncRunRecord(archive.db, id, status, seenCount, newCount, updatedCount, finishedAt, errorSummary)
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
	_, err := db.Exec(`UPDATE sync_runs SET
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
	defer tx.Rollback()
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

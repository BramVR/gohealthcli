package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type healthArchiveReader interface {
	Close() error
	StatusSummary() (statusResult, error)
	Query(statement string) (queryResult, error)
	DailySteps() ([]dailyStepsExportRow, error)
	ExportRows(spec exportDatasetSpec) ([]exportRow, error)
}

type sqliteHealthArchiveReader struct {
	archivePath   string
	db            *sql.DB
	schemaVersion int
}

type healthArchiveReaderOpenError = healthArchiveOpenError

func openHealthArchiveReader(archivePath string) (healthArchiveReader, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(readOnlyArchive)
	if err != nil {
		return nil, err
	}
	return &sqliteHealthArchiveReader{
		archivePath:   handle.path,
		db:            handle.db,
		schemaVersion: handle.schemaVersion,
	}, nil
}

func (archive *sqliteHealthArchiveReader) Close() error {
	return archive.db.Close()
}

func (archive *sqliteHealthArchiveReader) StatusSummary() (statusResult, error) {
	result := statusResult{
		Status:        "status_failed",
		ArchivePath:   archive.archivePath,
		SchemaVersion: archive.schemaVersion,
	}
	var err error
	if result.DataPointCount, err = countArchiveRows(archive.db, "data_points"); err != nil {
		return result, err
	}
	if result.RollupCount, err = countArchiveRows(archive.db, "rollups"); err != nil {
		return result, err
	}
	if result.ProfileSnapshotCount, err = countArchiveRows(archive.db, "profile_snapshots"); err != nil {
		return result, err
	}
	if result.IdentitySnapshotCount, err = countArchiveRows(archive.db, "identity_snapshots"); err != nil {
		return result, err
	}
	if result.SyncRunCount, err = countArchiveRows(archive.db, "sync_runs"); err != nil {
		return result, err
	}
	result.DataTypes, err = readStatusDataTypes(archive.db)
	if err != nil {
		return result, err
	}
	result.DataTypes, err = attachStatusSyncCursors(archive.db, result.DataTypes)
	if err != nil {
		return result, err
	}
	result.LatestSuccessfulRun, err = readStatusSyncRun(archive.db, "sync_completed")
	if err != nil {
		return result, err
	}
	result.LatestFailedRun, err = readStatusSyncRun(archive.db, "sync_failed")
	if err != nil {
		return result, err
	}
	result.Status = "ok"
	result.Message = "Health Archive status summarized"
	return result, nil
}

func (archive *sqliteHealthArchiveReader) Query(statement string) (queryResult, error) {
	result := queryResult{
		Status:      "query_failed",
		ArchivePath: archive.archivePath,
	}
	if err := validateQueryStatement(statement); err != nil {
		return result, err
	}

	rows, err := archive.db.Query(statement)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return result, err
	}
	result.Columns = columns
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return result, err
		}
		for index, value := range values {
			values[index] = queryOutputValue(value)
		}
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.Status = "query_completed"
	result.RowCount = len(result.Rows)
	result.Message = "Query completed"
	return result, nil
}

func (archive *sqliteHealthArchiveReader) DailySteps() ([]dailyStepsExportRow, error) {
	rows, err := archive.ExportRows(exportDatasetSpecs["daily-steps"])
	if err != nil {
		return nil, err
	}
	result := make([]dailyStepsExportRow, 0, len(rows))
	for _, row := range rows {
		stepCount, err := strconv.ParseInt(row[3], 10, 64)
		if err != nil {
			return nil, err
		}
		sourceRecordCount, err := strconv.ParseInt(row[6], 10, 64)
		if err != nil {
			return nil, err
		}
		result = append(result, dailyStepsExportRow{
			ProviderName:          row[0],
			ConnectionID:          row[1],
			CivilDate:             row[2],
			StepCount:             stepCount,
			SourceKind:            row[4],
			SourceFamilyFilter:    row[5],
			SourceRecordCount:     sourceRecordCount,
			LatestSourceTimestamp: row[7],
		})
	}
	return result, nil
}

func (archive *sqliteHealthArchiveReader) ExportRows(spec exportDatasetSpec) ([]exportRow, error) {
	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", exportSelectFields(spec), spec.view, spec.orderBy)
	rows, err := archive.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []exportRow
	for rows.Next() {
		values := make([]sql.NullString, len(spec.fields))
		destinations := make([]any, len(spec.fields))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		item := make(exportRow, len(spec.fields))
		for index, value := range values {
			item[index] = value.String
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func exportSelectFields(spec exportDatasetSpec) string {
	fields := make([]string, 0, len(spec.fields))
	for _, field := range spec.fields {
		fields = append(fields, field.name)
	}
	return strings.Join(fields, ", ")
}

func statusSetup(archivePath string) (statusResult, error) {
	result := statusResult{
		Status:      "status_failed",
		ArchivePath: archivePath,
	}
	reader, err := openHealthArchiveReader(archivePath)
	if err != nil {
		var openErr healthArchiveReaderOpenError
		if errors.As(err, &openErr) {
			result.SchemaVersion = openErr.schemaVersion
		}
		return result, err
	}
	defer reader.Close()
	return reader.StatusSummary()
}

func querySetup(archivePath, statement string) (queryResult, error) {
	result := queryResult{
		Status:      "query_failed",
		ArchivePath: archivePath,
	}
	if err := validateQueryStatement(statement); err != nil {
		return result, err
	}
	reader, err := openHealthArchiveReader(archivePath)
	if err != nil {
		return result, err
	}
	defer reader.Close()
	return reader.Query(statement)
}

func dailyStepsExportRows(archivePath string) ([]dailyStepsExportRow, error) {
	reader, err := openHealthArchiveReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader.DailySteps()
}

func countArchiveRows(db *sql.DB, table string) (int, error) {
	query, ok := archiveCountQueryByTable[table]
	if !ok {
		return 0, fmt.Errorf("unsupported Health Archive table: %s", table)
	}
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

var archiveCountQueryByTable = map[string]string{
	"data_points": `SELECT count(*) FROM data_points`,
	"rollups":     `SELECT count(*) FROM rollups`,
	// profile_snapshots is a virtual count: post-#97 the underlying table
	// is identity_snapshots, but the JSON status field keeps its existing
	// name for downstream tooling that pre-dates the rename. We count
	// only kind='profile' rows so it matches what the field used to mean.
	"profile_snapshots":  `SELECT count(*) FROM identity_snapshots WHERE snapshot_kind = 'profile'`,
	"identity_snapshots": `SELECT count(*) FROM identity_snapshots`,
	"sync_runs":          `SELECT count(*) FROM sync_runs`,
}

func readStatusDataTypes(db *sql.DB) ([]statusDataType, error) {
	rows, err := db.Query(`SELECT
		data_type,
		sum(data_point_count),
		sum(rollup_count),
		max(newest_data_point_timestamp),
		max(newest_rollup_timestamp)
	FROM (
		SELECT
			data_type,
			count(*) AS data_point_count,
			0 AS rollup_count,
			max(COALESCE(end_time_utc, start_time_utc, end_civil_time, start_civil_time, provider_civil_date, updated_at, '')) AS newest_data_point_timestamp,
			'' AS newest_rollup_timestamp
		FROM data_points
		GROUP BY data_type
		UNION ALL
		SELECT
			data_type,
			0 AS data_point_count,
			count(*) AS rollup_count,
			'' AS newest_data_point_timestamp,
			max(COALESCE(window_end_utc, window_start_utc, civil_date, updated_at, '')) AS newest_rollup_timestamp
		FROM rollups
		GROUP BY data_type
	)
	GROUP BY data_type
	ORDER BY data_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dataTypes []statusDataType
	for rows.Next() {
		var item statusDataType
		var newestDataPoint, newestRollup sql.NullString
		if err := rows.Scan(&item.DataType, &item.DataPointCount, &item.RollupCount, &newestDataPoint, &newestRollup); err != nil {
			return nil, err
		}
		if newestDataPoint.Valid {
			item.NewestDataPointTimestamp = newestDataPoint.String
		}
		if newestRollup.Valid {
			item.NewestRollupTimestamp = newestRollup.String
		}
		dataTypes = append(dataTypes, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return dataTypes, nil
}

// attachStatusSyncCursors looks up every sync_cursors row and attaches it
// to the matching statusDataType entry. Data Types that have a cursor but
// no archived rows yet (a completed Sync Run that returned nothing
// upstream) surface as zero-count entries so the cursor is still visible
// in status. The returned slice keeps the existing order from
// readStatusDataTypes; cursor-only entries are appended at the end in
// data_type-sorted order.
func attachStatusSyncCursors(db *sql.DB, dataTypes []statusDataType) ([]statusDataType, error) {
	rows, err := db.Query(`SELECT
		data_type,
		IFNULL(source_family_filter, ''),
		rollup_kind,
		cursor_time,
		advanced_at
	FROM sync_cursors
	ORDER BY data_type, rollup_kind, source_family_filter`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cursorsByDataType := map[string][]statusSyncCursor{}
	for rows.Next() {
		var dataType, sourceFamily, rollupKind, cursorTime, advancedAt string
		if err := rows.Scan(&dataType, &sourceFamily, &rollupKind, &cursorTime, &advancedAt); err != nil {
			return nil, err
		}
		cursorsByDataType[dataType] = append(cursorsByDataType[dataType], statusSyncCursor{
			SourceFamilyFilter: sourceFamily,
			RollupKind:         rollupKind,
			CursorTime:         cursorTime,
			AdvancedAt:         advancedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index, dataType := range dataTypes {
		if cursors, ok := cursorsByDataType[dataType.DataType]; ok {
			dataTypes[index].SyncCursors = cursors
			delete(cursorsByDataType, dataType.DataType)
		}
	}
	if len(cursorsByDataType) == 0 {
		return dataTypes, nil
	}
	leftovers := make([]string, 0, len(cursorsByDataType))
	for dataType := range cursorsByDataType {
		leftovers = append(leftovers, dataType)
	}
	sort.Strings(leftovers)
	for _, dataType := range leftovers {
		dataTypes = append(dataTypes, statusDataType{
			DataType:    dataType,
			SyncCursors: cursorsByDataType[dataType],
		})
	}
	return dataTypes, nil
}

func readStatusSyncRun(db *sql.DB, syncStatus string) (*statusSyncRun, error) {
	var item statusSyncRun
	var dataTypesJSON, rangeJSON string
	var sourceFamily, finishedAt, errorSummary sql.NullString
	err := db.QueryRow(`SELECT
		id,
		status,
		data_types_requested,
		range_requested_json,
		endpoint_family,
		source_family_filter,
		seen_count,
		new_count,
		updated_count,
		started_at,
		finished_at,
		error_summary
	FROM sync_runs
	WHERE status = ?
	ORDER BY COALESCE(finished_at, started_at) DESC, id DESC
	LIMIT 1`, syncStatus).Scan(
		&item.ID,
		&item.Status,
		&dataTypesJSON,
		&rangeJSON,
		&item.EndpointFamily,
		&sourceFamily,
		&item.SeenCount,
		&item.NewCount,
		&item.UpdatedCount,
		&item.StartedAt,
		&finishedAt,
		&errorSummary,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(dataTypesJSON), &item.DataTypes); err != nil {
		return nil, fmt.Errorf("Sync Run %d data types are not valid JSON: %w", item.ID, err)
	}
	var requestedRange struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal([]byte(rangeJSON), &requestedRange); err != nil {
		return nil, fmt.Errorf("Sync Run %d range is not valid JSON: %w", item.ID, err)
	}
	item.From = requestedRange.From
	item.To = requestedRange.To
	if sourceFamily.Valid {
		item.SourceFamilyFilter = sourceFamily.String
	}
	if finishedAt.Valid {
		item.FinishedAt = finishedAt.String
	}
	if errorSummary.Valid {
		item.ErrorSummary = shortErrorSummary(errorSummary.String)
	}
	return &item, nil
}

func shortErrorSummary(summary string) string {
	summary = strings.TrimSpace(strings.Split(summary, "\n")[0])
	const maxErrorSummaryLength = 160
	if len(summary) <= maxErrorSummaryLength {
		return summary
	}
	return summary[:maxErrorSummaryLength-3] + "..."
}

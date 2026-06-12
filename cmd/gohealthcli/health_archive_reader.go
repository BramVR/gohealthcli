package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
	"sort"
	"strings"
	"time"
)

type healthArchiveReader interface {
	Close() error
	StatusSummary(ctx context.Context) (statusResult, error)
	Query(ctx context.Context, statement string, encoder queryRowEncoder) (queryResult, error)
	ExportRows(ctx context.Context, spec exportDatasetSpec) ([]exportRow, error)
}

type sqliteHealthArchiveReader struct {
	archivePath   string
	db            *sql.DB
	schemaVersion int
}

type healthArchiveReaderOpenError = healthArchiveOpenError

func openHealthArchiveReader(archivePath string) (healthArchiveReader, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(context.Background(), readOnlyArchive)
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

func (archive *sqliteHealthArchiveReader) StatusSummary(ctx context.Context) (statusResult, error) {
	result := statusResult{
		Status:        "status_failed",
		ArchivePath:   archive.archivePath,
		SchemaVersion: archive.schemaVersion,
	}
	var err error
	if result.DataPointCount, err = countArchiveRows(ctx, archive.db, "data_points"); err != nil {
		return result, err
	}
	if result.RollupCount, err = countArchiveRows(ctx, archive.db, "rollups"); err != nil {
		return result, err
	}
	if result.ProfileSnapshotCount, err = countArchiveRows(ctx, archive.db, "profile_snapshots"); err != nil {
		return result, err
	}
	if result.IdentitySnapshotCount, err = countArchiveRows(ctx, archive.db, "identity_snapshots"); err != nil {
		return result, err
	}
	if result.SyncRunCount, err = countArchiveRows(ctx, archive.db, "sync_runs"); err != nil {
		return result, err
	}
	result.DataTypes, err = readStatusDataTypes(ctx, archive.db)
	if err != nil {
		return result, err
	}
	result.DataTypes, err = attachStatusSyncCursors(ctx, archive.db, result.DataTypes)
	if err != nil {
		return result, err
	}
	// KnownDataTypes is computed once here so the plain and JSON
	// writers share a single source — PRD #144 slice 9 (status key
	// parity). DataTypes is already sorted by readStatusDataTypes +
	// attachStatusSyncCursors, so the flat array tracks the
	// per-Data-Type stanza order exactly.
	result.KnownDataTypes = statusDataTypeNames(result.DataTypes)
	result.IdentitySnapshotsFreshness, err = readStatusSnapshotFreshness(ctx, archive.db)
	if err != nil {
		return result, err
	}
	if result.IdentitySnapshotsFreshness != nil {
		// Hoist paired_device_count to a top-level JSON key so it
		// matches the plain `paired_device_count: N` line (PRD #144
		// slice 9). The nested field stays for back-compat.
		result.PairedDeviceCount = result.IdentitySnapshotsFreshness.PairedDeviceCount
	}
	result.Tier2, err = readStatusTier2(ctx, archive.db, result.DataTypes)
	if err != nil {
		return result, err
	}
	result.LatestSuccessfulRun, err = readStatusSyncRun(ctx, archive.db, "sync_completed")
	if err != nil {
		return result, err
	}
	result.LatestFailedRun, err = readStatusSyncRun(ctx, archive.db, "sync_failed")
	if err != nil {
		return result, err
	}
	result.Status = "ok"
	result.Message = "Health Archive status summarized"
	return result, nil
}

func (archive *sqliteHealthArchiveReader) Query(ctx context.Context, statement string, encoder queryRowEncoder) (queryResult, error) {
	result := queryResult{
		Status:      "query_failed",
		ArchivePath: archive.archivePath,
	}
	if err := validateQueryStatement(statement); err != nil {
		return result, err
	}

	rows, err := archive.db.QueryContext(ctx, statement)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return result, err
	}
	result.Columns = columns
	// rows.ColumnTypes() exposes the per-column DatabaseTypeName the
	// encoders need to detect BLOB columns. The slice is the same
	// length as columns; we pre-extract the type names so the row loop
	// stays cheap.
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return result, err
	}
	databaseTypeNames := make([]string, len(columnTypes))
	for index, columnType := range columnTypes {
		databaseTypeNames[index] = columnType.DatabaseTypeName()
	}
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
			values[index] = encoder.encode(columns[index], databaseTypeNames[index], value)
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

func (archive *sqliteHealthArchiveReader) ExportRows(ctx context.Context, spec exportDatasetSpec) ([]exportRow, error) {
	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", exportSelectFields(spec), spec.view, spec.orderBy)
	rows, err := archive.db.QueryContext(ctx, query)
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

func statusSetup(ctx context.Context, archivePath string, now time.Time) (statusResult, error) {
	result := statusResult{
		Status:      "status_failed",
		ArchivePath: archivePath,
	}
	// Fence abandoned sync_running rows before summarizing (#236), so
	// the latest_failed_sync_run stanza reports the orphan instead of
	// the summary silently skipping a phantom in-flight run.
	// Best-effort: `status` is a read surface, so a fence that cannot
	// write (read-only media, lost SQLITE_BUSY race even after the
	// retry budget) degrades to a slightly stale summary rather than
	// failing the command. Open errors are not lost — the reader open
	// below goes through the same lifecycle and surfaces the identical
	// healthArchiveOpenError for the caller to decode.
	_, _ = fenceAbandonedSyncRunsAtPath(ctx, archivePath, now)
	reader, err := openHealthArchiveReader(archivePath)
	if err != nil {
		var openErr healthArchiveReaderOpenError
		if errors.As(err, &openErr) {
			result.SchemaVersion = openErr.schemaVersion
		}
		return result, err
	}
	defer reader.Close()
	return reader.StatusSummary(ctx)
}

func querySetup(ctx context.Context, archivePath, statement string, encoder queryRowEncoder) (queryResult, error) {
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
	return reader.Query(ctx, statement, encoder)
}

func countArchiveRows(ctx context.Context, db *sql.DB, table string) (int, error) {
	query, ok := archiveCountQueryByTable[table]
	if !ok {
		return 0, fmt.Errorf("unsupported Health Archive table: %s", table)
	}
	var count int
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
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
	"profile_snapshots":  `SELECT count(*) FROM identity_snapshots WHERE snapshot_kind = '` + snapshotKindProfile + `'`,
	"identity_snapshots": `SELECT count(*) FROM identity_snapshots`,
	"sync_runs":          `SELECT count(*) FROM sync_runs`,
}

func readStatusDataTypes(ctx context.Context, db *sql.DB) ([]statusDataType, error) {
	rows, err := db.QueryContext(ctx, `SELECT
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
func attachStatusSyncCursors(ctx context.Context, db *sql.DB, dataTypes []statusDataType) ([]statusDataType, error) {
	rows, err := db.QueryContext(ctx, `SELECT
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

// readStatusSnapshotFreshness reads the latest fetched_at per
// snapshot_kind and (if a paired-devices snapshot exists) counts the
// devices in its raw JSON. Uses ROW_NUMBER() OVER for the per-kind
// newest pick — same window-function pattern as paired_devices and
// current_settings, both clearer and avoids O(n²) growth as snapshots
// accumulate. Returns nil when no snapshots exist at all so the JSON
// shape omits the block entirely.
func readStatusSnapshotFreshness(ctx context.Context, db *sql.DB) (*statusSnapshotFreshness, error) {
	rows, err := db.QueryContext(ctx, `SELECT snapshot_kind, fetched_at, raw_json FROM (
		SELECT
			snapshot_kind,
			fetched_at,
			raw_json,
			ROW_NUMBER() OVER (PARTITION BY snapshot_kind ORDER BY fetched_at DESC, id DESC) AS rank
		FROM identity_snapshots
	) WHERE rank = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	freshness := &statusSnapshotFreshness{LatestFetchedAt: map[string]string{}}
	for rows.Next() {
		var kind, fetchedAt, rawJSON string
		if err := rows.Scan(&kind, &fetchedAt, &rawJSON); err != nil {
			return nil, err
		}
		freshness.LatestFetchedAt[kind] = fetchedAt
		if kind == snapshotKindPairedDevices {
			count, err := countPairedDevicesIn(rawJSON)
			if err != nil {
				return nil, fmt.Errorf("status paired-devices snapshot: %w", err)
			}
			freshness.PairedDeviceCount = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(freshness.LatestFetchedAt) == 0 {
		return nil, nil
	}
	return freshness, nil
}

// countPairedDevicesIn parses a paired-devices snapshot payload and
// returns the number of devices it lists. Invalid JSON surfaces as an
// error so callers (status) can fail loudly instead of silently
// reporting 0 — paired-devices rows in identity_snapshots are
// supposed to be valid JSON the snapshot writer just persisted. The
// list lives under the pairedDevices key in the real
// users.pairedDevices.list payload (#298), not the devices key the
// original #98 paired-devices slice assumed.
func countPairedDevicesIn(rawJSON string) (int, error) {
	var envelope struct {
		PairedDevices []json.RawMessage `json:"pairedDevices"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &envelope); err != nil {
		return 0, fmt.Errorf("paired-devices raw_json is not valid JSON: %w", err)
	}
	return len(envelope.PairedDevices), nil
}

// readStatusTier2 reports Tier 2 (ECG + IRN) coverage: row counts in
// data_points per Data Type plus per-scope grant flags read from the
// stored Connection's token metadata. AC #111: both counts default to
// 0 (never an error, never missing) when the user has not run
// `connect --add-scopes ecg,irn` yet. The plain/JSON writers use the
// scope_granted flags to decide whether to emit the plain lines —
// JSON always carries the block so downstream tooling sees a stable
// shape.
//
// Counts are derived from the already-computed per-Data-Type rows
// instead of running fresh COUNT(*) queries — avoids extra full-table
// scans of data_points and keeps the count source consistent with
// the rest of `status`.
func readStatusTier2(ctx context.Context, db *sql.DB, dataTypes []statusDataType) (*statusTier2, error) {
	scopes, err := readCurrentConnectionScopes(ctx, db)
	if err != nil {
		return nil, err
	}
	tier2 := &statusTier2{
		ElectrocardiogramEventCount:             tier2DataPointCount(dataTypes, "electrocardiogram"),
		IrregularRhythmNotificationCount:        tier2DataPointCount(dataTypes, "irregular-rhythm-notification"),
		ElectrocardiogramScopeGranted:           scopeListContains(scopes, googlehealth.ScopeEcgReadonly),
		IrregularRhythmNotificationScopeGranted: scopeListContains(scopes, googlehealth.ScopeIrnReadonly),
	}
	return tier2, nil
}

// tier2DataPointCount looks up the data_point_count for a single Data
// Type in the already-computed status data types slice. Returns 0
// when the Data Type has no rows (readStatusDataTypes omits empties).
func tier2DataPointCount(dataTypes []statusDataType, dataType string) int {
	for _, item := range dataTypes {
		if item.DataType == dataType {
			return item.DataPointCount
		}
	}
	return 0
}

// readCurrentConnectionScopes returns the scopes stored on the single
// archived Connection. Returns nil (no error) when no Connection has
// been archived yet — status is a read-only summary and must not fail
// just because `connect` hasn't run. Returns nil when the metadata is
// malformed for the same reason: a parse error here would mask the
// data_point counts the rest of `status` reports.
func readCurrentConnectionScopes(ctx context.Context, db *sql.DB) ([]string, error) {
	var metadata string
	err := db.QueryRowContext(ctx, `SELECT token_metadata_json FROM connections LIMIT 1`).Scan(&metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_, scopes, err := connectionTokenExpiryAndScopes(metadata)
	if err != nil {
		return nil, nil
	}
	return scopes, nil
}

func readStatusSyncRun(ctx context.Context, db *sql.DB, syncStatus string) (*statusSyncRun, error) {
	var item statusSyncRun
	var dataTypesJSON, rangeJSON string
	var sourceFamily, finishedAt, errorSummary sql.NullString
	err := db.QueryRowContext(ctx, `SELECT
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

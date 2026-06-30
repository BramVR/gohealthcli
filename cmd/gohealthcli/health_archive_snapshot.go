package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrHealthArchiveSnapshotUnsupportedAttachments = errors.New("Data Point Attachments are not supported by Health Archive Snapshot")

type HealthArchiveSnapshot struct {
	SchemaVersion      int
	Connections        []HealthArchiveSnapshotConnection
	DataPoints         []HealthArchiveSnapshotDataPoint
	DataPointRevisions []HealthArchiveSnapshotDataPointRevision
	Rollups            []HealthArchiveSnapshotRollup
	IdentitySnapshots  []HealthArchiveSnapshotIdentitySnapshot
	SyncRuns           []HealthArchiveSnapshotSyncRun
	SyncCursors        []HealthArchiveSnapshotSyncCursor
}

type HealthArchiveSnapshotConnection struct {
	ID                 string
	ProviderName       string
	GoogleHealthUserID string
	LegacyFitbitUserID *string
	TokenMetadataJSON  string
	CreatedAt          string
	UpdatedAt          string
	GoogleIdentityJSON string
}

type HealthArchiveSnapshotDataPoint struct {
	ID                   int64
	ProviderName         string
	ConnectionID         string
	DataType             string
	UpstreamResourceName *string
	RecordKind           string
	StartTimeUTC         *string
	EndTimeUTC           *string
	StartCivilTime       *string
	EndCivilTime         *string
	ProviderCivilDate    *string
	TimezoneMetadata     *string
	DataSourceJSON       string
	RawJSON              string
	InsertedAt           string
	UpdatedAt            string
	SourceFamilyFilter   *string
}

type HealthArchiveSnapshotDataPointRevision struct {
	ID                int64
	DataPointID       int64
	PreviousRawJSON   string
	ReplacedAt        string
	ReplacementReason *string
}

type HealthArchiveSnapshotRollup struct {
	ID               int64
	ProviderName     string
	ConnectionID     string
	DataType         string
	RollupKind       string
	WindowStartUTC   *string
	WindowEndUTC     *string
	CivilDate        *string
	TimezoneMetadata *string
	RawJSON          string
	InsertedAt       string
	UpdatedAt        string
}

type HealthArchiveSnapshotIdentitySnapshot struct {
	ID           int64
	ProviderName string
	ConnectionID string
	RawJSON      string
	FetchedAt    string
	SnapshotKind string
}

type HealthArchiveSnapshotSyncRun struct {
	ID                 int64
	ProviderName       string
	ConnectionID       *string
	DataTypesRequested string
	RangeRequestedJSON string
	EndpointFamily     string
	Status             string
	SeenCount          int
	NewCount           int
	UpdatedCount       int
	StartedAt          string
	FinishedAt         *string
	ErrorSummary       *string
	SourceFamilyFilter *string
	LastProgressAt     *string
}

type HealthArchiveSnapshotSyncCursor struct {
	ConnectionID       string
	DataType           string
	SourceFamilyFilter string
	RollupKind         string
	CursorTime         string
	AdvancedAt         string
}

func ExportHealthArchiveSnapshot(ctx context.Context, archivePath string) (HealthArchiveSnapshot, error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).Open(ctx, readOnlyArchive)
	if err != nil {
		return HealthArchiveSnapshot{}, err
	}
	defer handle.Close()

	tx, err := handle.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return HealthArchiveSnapshot{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var attachmentCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM data_point_attachments`).Scan(&attachmentCount); err != nil {
		return HealthArchiveSnapshot{}, fmt.Errorf("inspect Data Point Attachments: %w", err)
	}
	if attachmentCount > 0 {
		return HealthArchiveSnapshot{}, fmt.Errorf("%w yet; archives with Data Point Attachment rows cannot be snapshotted until attachment portability lands", ErrHealthArchiveSnapshotUnsupportedAttachments)
	}

	snapshot := HealthArchiveSnapshot{SchemaVersion: handle.schemaVersion}
	if snapshot.Connections, err = exportSnapshotConnections(ctx, tx); err != nil {
		return HealthArchiveSnapshot{}, err
	}
	if snapshot.DataPoints, err = exportSnapshotDataPoints(ctx, tx); err != nil {
		return HealthArchiveSnapshot{}, err
	}
	if snapshot.DataPointRevisions, err = exportSnapshotDataPointRevisions(ctx, tx); err != nil {
		return HealthArchiveSnapshot{}, err
	}
	if snapshot.Rollups, err = exportSnapshotRollups(ctx, tx); err != nil {
		return HealthArchiveSnapshot{}, err
	}
	if snapshot.IdentitySnapshots, err = exportSnapshotIdentitySnapshots(ctx, tx); err != nil {
		return HealthArchiveSnapshot{}, err
	}
	if snapshot.SyncRuns, err = exportSnapshotSyncRuns(ctx, tx); err != nil {
		return HealthArchiveSnapshot{}, err
	}
	if snapshot.SyncCursors, err = exportSnapshotSyncCursors(ctx, tx); err != nil {
		return HealthArchiveSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return HealthArchiveSnapshot{}, err
	}
	committed = true
	return snapshot, nil
}

func ValidateHealthArchiveSnapshot(snapshot HealthArchiveSnapshot) error {
	if snapshot.SchemaVersion != currentSchemaVersion {
		return fmt.Errorf("Health Archive Snapshot schema version %d, want %d", snapshot.SchemaVersion, currentSchemaVersion)
	}

	connectionIDs := map[string]struct{}{}
	connectionIdentities := map[string]struct{}{}
	for _, connection := range snapshot.Connections {
		if connection.ID == "" {
			return errors.New("Connection has empty id")
		}
		if _, exists := connectionIDs[connection.ID]; exists {
			return fmt.Errorf("duplicate Connection id %q", connection.ID)
		}
		connectionIDs[connection.ID] = struct{}{}
		identity := connection.ProviderName + "\x00" + connection.GoogleHealthUserID
		if _, exists := connectionIdentities[identity]; exists {
			return fmt.Errorf("duplicate Connection identity for provider %q and Google Identity %q", connection.ProviderName, connection.GoogleHealthUserID)
		}
		connectionIdentities[identity] = struct{}{}
	}

	dataPointIDs := map[int64]struct{}{}
	dataPointIdentities := map[string]struct{}{}
	for _, point := range snapshot.DataPoints {
		if point.ID <= 0 {
			return fmt.Errorf("Data Point has invalid id %d", point.ID)
		}
		if _, exists := dataPointIDs[point.ID]; exists {
			return fmt.Errorf("duplicate Data Point id %d", point.ID)
		}
		dataPointIDs[point.ID] = struct{}{}
		if _, exists := connectionIDs[point.ConnectionID]; !exists {
			return fmt.Errorf("Data Point %d references unknown Connection %q", point.ID, point.ConnectionID)
		}
		identity := dataPointIdentityKey(point)
		if _, exists := dataPointIdentities[identity]; exists {
			return fmt.Errorf("duplicate Data Point identity for id %d", point.ID)
		}
		dataPointIdentities[identity] = struct{}{}
	}

	revisionIDs := map[int64]struct{}{}
	for _, revision := range snapshot.DataPointRevisions {
		if revision.ID <= 0 {
			return fmt.Errorf("Data Point Revision has invalid id %d", revision.ID)
		}
		if _, exists := revisionIDs[revision.ID]; exists {
			return fmt.Errorf("duplicate Data Point Revision id %d", revision.ID)
		}
		revisionIDs[revision.ID] = struct{}{}
		if _, exists := dataPointIDs[revision.DataPointID]; !exists {
			return fmt.Errorf("Data Point Revision %d references unknown Data Point %d", revision.ID, revision.DataPointID)
		}
	}

	rollupIDs := map[int64]struct{}{}
	rollupIdentities := map[string]struct{}{}
	for _, rollup := range snapshot.Rollups {
		if rollup.ID <= 0 {
			return fmt.Errorf("Rollup has invalid id %d", rollup.ID)
		}
		if _, exists := rollupIDs[rollup.ID]; exists {
			return fmt.Errorf("duplicate Rollup id %d", rollup.ID)
		}
		rollupIDs[rollup.ID] = struct{}{}
		if _, exists := connectionIDs[rollup.ConnectionID]; !exists {
			return fmt.Errorf("Rollup %d references unknown Connection %q", rollup.ID, rollup.ConnectionID)
		}
		identity := rollupIdentityKey(rollup)
		if _, exists := rollupIdentities[identity]; exists {
			return fmt.Errorf("duplicate Rollup identity for id %d", rollup.ID)
		}
		rollupIdentities[identity] = struct{}{}
	}

	identitySnapshotIDs := map[int64]struct{}{}
	for _, item := range snapshot.IdentitySnapshots {
		if item.ID <= 0 {
			return fmt.Errorf("Identity Snapshot has invalid id %d", item.ID)
		}
		if _, exists := identitySnapshotIDs[item.ID]; exists {
			return fmt.Errorf("duplicate Identity Snapshot id %d", item.ID)
		}
		identitySnapshotIDs[item.ID] = struct{}{}
		if _, exists := connectionIDs[item.ConnectionID]; !exists {
			return fmt.Errorf("Identity Snapshot %d references unknown Connection %q", item.ID, item.ConnectionID)
		}
	}

	syncRunIDs := map[int64]struct{}{}
	for _, run := range snapshot.SyncRuns {
		if run.ID <= 0 {
			return fmt.Errorf("Sync Run has invalid id %d", run.ID)
		}
		if _, exists := syncRunIDs[run.ID]; exists {
			return fmt.Errorf("duplicate Sync Run id %d", run.ID)
		}
		syncRunIDs[run.ID] = struct{}{}
		if run.ConnectionID != nil && *run.ConnectionID != "" {
			if _, exists := connectionIDs[*run.ConnectionID]; !exists {
				return fmt.Errorf("Sync Run %d references unknown Connection %q", run.ID, *run.ConnectionID)
			}
		}
	}

	syncCursorKeys := map[string]struct{}{}
	for _, cursor := range snapshot.SyncCursors {
		if _, exists := connectionIDs[cursor.ConnectionID]; !exists {
			return fmt.Errorf("Sync Cursor for Data Type %q references unknown Connection %q", cursor.DataType, cursor.ConnectionID)
		}
		key := cursor.ConnectionID + "\x00" + cursor.DataType + "\x00" + cursor.SourceFamilyFilter + "\x00" + cursor.RollupKind
		if _, exists := syncCursorKeys[key]; exists {
			return fmt.Errorf("duplicate Sync Cursor identity for Connection %q Data Type %q", cursor.ConnectionID, cursor.DataType)
		}
		syncCursorKeys[key] = struct{}{}
	}
	return nil
}

func RestoreHealthArchiveSnapshot(ctx context.Context, snapshot HealthArchiveSnapshot, archivePath string) error {
	if err := ValidateHealthArchiveSnapshot(snapshot); err != nil {
		return err
	}
	lifecycle := healthArchiveLifecycle{path: archivePath}
	if err := lifecycle.Create(ctx); err != nil {
		return err
	}
	handle, err := lifecycle.Open(ctx, writeArchive)
	if err != nil {
		return err
	}
	defer handle.Close()

	tx, err := handle.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, connection := range snapshot.Connections {
		if _, err := tx.ExecContext(ctx, `INSERT INTO connections (
			id,
			provider_name,
			google_health_user_id,
			legacy_fitbit_user_id,
			token_metadata_json,
			created_at,
			updated_at,
			google_identity_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			connection.ID,
			connection.ProviderName,
			connection.GoogleHealthUserID,
			connection.LegacyFitbitUserID,
			connection.TokenMetadataJSON,
			connection.CreatedAt,
			connection.UpdatedAt,
			connection.GoogleIdentityJSON,
		); err != nil {
			return fmt.Errorf("restore Connection %q: %w", connection.ID, err)
		}
	}
	for _, point := range snapshot.DataPoints {
		if _, err := tx.ExecContext(ctx, `INSERT INTO data_points (
			id,
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
			raw_json,
			inserted_at,
			updated_at,
			source_family_filter
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			point.ID,
			point.ProviderName,
			point.ConnectionID,
			point.DataType,
			point.UpstreamResourceName,
			point.RecordKind,
			point.StartTimeUTC,
			point.EndTimeUTC,
			point.StartCivilTime,
			point.EndCivilTime,
			point.ProviderCivilDate,
			point.TimezoneMetadata,
			point.DataSourceJSON,
			point.RawJSON,
			point.InsertedAt,
			point.UpdatedAt,
			point.SourceFamilyFilter,
		); err != nil {
			return fmt.Errorf("restore Data Point %d: %w", point.ID, err)
		}
	}
	for _, revision := range snapshot.DataPointRevisions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO data_point_revisions (
			id,
			data_point_id,
			previous_raw_json,
			replaced_at,
			replacement_reason
		) VALUES (?, ?, ?, ?, ?)`,
			revision.ID,
			revision.DataPointID,
			revision.PreviousRawJSON,
			revision.ReplacedAt,
			revision.ReplacementReason,
		); err != nil {
			return fmt.Errorf("restore Data Point Revision %d: %w", revision.ID, err)
		}
	}
	for _, rollup := range snapshot.Rollups {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rollups (
			id,
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
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rollup.ID,
			rollup.ProviderName,
			rollup.ConnectionID,
			rollup.DataType,
			rollup.RollupKind,
			rollup.WindowStartUTC,
			rollup.WindowEndUTC,
			rollup.CivilDate,
			rollup.TimezoneMetadata,
			rollup.RawJSON,
			rollup.InsertedAt,
			rollup.UpdatedAt,
		); err != nil {
			return fmt.Errorf("restore Rollup %d: %w", rollup.ID, err)
		}
	}
	for _, item := range snapshot.IdentitySnapshots {
		if _, err := tx.ExecContext(ctx, `INSERT INTO identity_snapshots (
			id,
			provider_name,
			connection_id,
			raw_json,
			fetched_at,
			snapshot_kind
		) VALUES (?, ?, ?, ?, ?, ?)`,
			item.ID,
			item.ProviderName,
			item.ConnectionID,
			item.RawJSON,
			item.FetchedAt,
			item.SnapshotKind,
		); err != nil {
			return fmt.Errorf("restore Identity Snapshot %d: %w", item.ID, err)
		}
	}
	for _, run := range snapshot.SyncRuns {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_runs (
			id,
			provider_name,
			connection_id,
			data_types_requested,
			range_requested_json,
			endpoint_family,
			status,
			seen_count,
			new_count,
			updated_count,
			started_at,
			finished_at,
			error_summary,
			source_family_filter,
			last_progress_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			run.ID,
			run.ProviderName,
			run.ConnectionID,
			run.DataTypesRequested,
			run.RangeRequestedJSON,
			run.EndpointFamily,
			run.Status,
			run.SeenCount,
			run.NewCount,
			run.UpdatedCount,
			run.StartedAt,
			run.FinishedAt,
			run.ErrorSummary,
			run.SourceFamilyFilter,
			run.LastProgressAt,
		); err != nil {
			return fmt.Errorf("restore Sync Run %d: %w", run.ID, err)
		}
	}
	for _, cursor := range snapshot.SyncCursors {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_cursors (
			connection_id,
			data_type,
			source_family_filter,
			rollup_kind,
			cursor_time,
			advanced_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
			cursor.ConnectionID,
			cursor.DataType,
			cursor.SourceFamilyFilter,
			cursor.RollupKind,
			cursor.CursorTime,
			cursor.AdvancedAt,
		); err != nil {
			return fmt.Errorf("restore Sync Cursor for Data Type %q: %w", cursor.DataType, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

type healthArchiveSnapshotQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func exportSnapshotConnections(ctx context.Context, db healthArchiveSnapshotQuerier) ([]HealthArchiveSnapshotConnection, error) {
	rows, err := db.QueryContext(ctx, `SELECT
		id,
		provider_name,
		google_health_user_id,
		legacy_fitbit_user_id,
		token_metadata_json,
		created_at,
		updated_at,
		google_identity_json
	FROM connections ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []HealthArchiveSnapshotConnection
	for rows.Next() {
		var item HealthArchiveSnapshotConnection
		var legacy sql.NullString
		if err := rows.Scan(&item.ID, &item.ProviderName, &item.GoogleHealthUserID, &legacy, &item.TokenMetadataJSON, &item.CreatedAt, &item.UpdatedAt, &item.GoogleIdentityJSON); err != nil {
			return nil, err
		}
		item.LegacyFitbitUserID = snapshotStringPtr(legacy)
		items = append(items, item)
	}
	return items, rows.Err()
}

func exportSnapshotDataPoints(ctx context.Context, db healthArchiveSnapshotQuerier) ([]HealthArchiveSnapshotDataPoint, error) {
	rows, err := db.QueryContext(ctx, `SELECT
		id,
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
		raw_json,
		inserted_at,
		updated_at,
		source_family_filter
	FROM data_points ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []HealthArchiveSnapshotDataPoint
	for rows.Next() {
		var item HealthArchiveSnapshotDataPoint
		var upstream, startUTC, endUTC, startCivil, endCivil, civilDate, timezone, sourceFamily sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.ProviderName,
			&item.ConnectionID,
			&item.DataType,
			&upstream,
			&item.RecordKind,
			&startUTC,
			&endUTC,
			&startCivil,
			&endCivil,
			&civilDate,
			&timezone,
			&item.DataSourceJSON,
			&item.RawJSON,
			&item.InsertedAt,
			&item.UpdatedAt,
			&sourceFamily,
		); err != nil {
			return nil, err
		}
		item.UpstreamResourceName = snapshotStringPtr(upstream)
		item.StartTimeUTC = snapshotStringPtr(startUTC)
		item.EndTimeUTC = snapshotStringPtr(endUTC)
		item.StartCivilTime = snapshotStringPtr(startCivil)
		item.EndCivilTime = snapshotStringPtr(endCivil)
		item.ProviderCivilDate = snapshotStringPtr(civilDate)
		item.TimezoneMetadata = snapshotStringPtr(timezone)
		item.SourceFamilyFilter = snapshotStringPtr(sourceFamily)
		items = append(items, item)
	}
	return items, rows.Err()
}

func exportSnapshotDataPointRevisions(ctx context.Context, db healthArchiveSnapshotQuerier) ([]HealthArchiveSnapshotDataPointRevision, error) {
	rows, err := db.QueryContext(ctx, `SELECT
		id,
		data_point_id,
		previous_raw_json,
		replaced_at,
		replacement_reason
	FROM data_point_revisions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []HealthArchiveSnapshotDataPointRevision
	for rows.Next() {
		var item HealthArchiveSnapshotDataPointRevision
		var reason sql.NullString
		if err := rows.Scan(&item.ID, &item.DataPointID, &item.PreviousRawJSON, &item.ReplacedAt, &reason); err != nil {
			return nil, err
		}
		item.ReplacementReason = snapshotStringPtr(reason)
		items = append(items, item)
	}
	return items, rows.Err()
}

func exportSnapshotRollups(ctx context.Context, db healthArchiveSnapshotQuerier) ([]HealthArchiveSnapshotRollup, error) {
	rows, err := db.QueryContext(ctx, `SELECT
		id,
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
	FROM rollups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []HealthArchiveSnapshotRollup
	for rows.Next() {
		var item HealthArchiveSnapshotRollup
		var start, end, civilDate, timezone sql.NullString
		if err := rows.Scan(&item.ID, &item.ProviderName, &item.ConnectionID, &item.DataType, &item.RollupKind, &start, &end, &civilDate, &timezone, &item.RawJSON, &item.InsertedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.WindowStartUTC = snapshotStringPtr(start)
		item.WindowEndUTC = snapshotStringPtr(end)
		item.CivilDate = snapshotStringPtr(civilDate)
		item.TimezoneMetadata = snapshotStringPtr(timezone)
		items = append(items, item)
	}
	return items, rows.Err()
}

func exportSnapshotIdentitySnapshots(ctx context.Context, db healthArchiveSnapshotQuerier) ([]HealthArchiveSnapshotIdentitySnapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT
		id,
		provider_name,
		connection_id,
		raw_json,
		fetched_at,
		snapshot_kind
	FROM identity_snapshots ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []HealthArchiveSnapshotIdentitySnapshot
	for rows.Next() {
		var item HealthArchiveSnapshotIdentitySnapshot
		if err := rows.Scan(&item.ID, &item.ProviderName, &item.ConnectionID, &item.RawJSON, &item.FetchedAt, &item.SnapshotKind); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func exportSnapshotSyncRuns(ctx context.Context, db healthArchiveSnapshotQuerier) ([]HealthArchiveSnapshotSyncRun, error) {
	rows, err := db.QueryContext(ctx, `SELECT
		id,
		provider_name,
		connection_id,
		data_types_requested,
		range_requested_json,
		endpoint_family,
		status,
		seen_count,
		new_count,
		updated_count,
		started_at,
		finished_at,
		error_summary,
		source_family_filter,
		last_progress_at
	FROM sync_runs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []HealthArchiveSnapshotSyncRun
	for rows.Next() {
		var item HealthArchiveSnapshotSyncRun
		var connectionID, finishedAt, errorSummary, sourceFamily, lastProgress sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.ProviderName,
			&connectionID,
			&item.DataTypesRequested,
			&item.RangeRequestedJSON,
			&item.EndpointFamily,
			&item.Status,
			&item.SeenCount,
			&item.NewCount,
			&item.UpdatedCount,
			&item.StartedAt,
			&finishedAt,
			&errorSummary,
			&sourceFamily,
			&lastProgress,
		); err != nil {
			return nil, err
		}
		item.ConnectionID = snapshotStringPtr(connectionID)
		item.FinishedAt = snapshotStringPtr(finishedAt)
		item.ErrorSummary = snapshotStringPtr(errorSummary)
		item.SourceFamilyFilter = snapshotStringPtr(sourceFamily)
		item.LastProgressAt = snapshotStringPtr(lastProgress)
		items = append(items, item)
	}
	return items, rows.Err()
}

func exportSnapshotSyncCursors(ctx context.Context, db healthArchiveSnapshotQuerier) ([]HealthArchiveSnapshotSyncCursor, error) {
	rows, err := db.QueryContext(ctx, `SELECT
		connection_id,
		data_type,
		source_family_filter,
		rollup_kind,
		cursor_time,
		advanced_at
	FROM sync_cursors ORDER BY connection_id, data_type, source_family_filter, rollup_kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []HealthArchiveSnapshotSyncCursor
	for rows.Next() {
		var item HealthArchiveSnapshotSyncCursor
		if err := rows.Scan(&item.ConnectionID, &item.DataType, &item.SourceFamilyFilter, &item.RollupKind, &item.CursorTime, &item.AdvancedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func dataPointIdentityKey(point HealthArchiveSnapshotDataPoint) string {
	sourceFamily := snapshotStringValue(point.SourceFamilyFilter)
	if upstream := snapshotStringValue(point.UpstreamResourceName); upstream != "" {
		return "upstream\x00" + point.ProviderName + "\x00" + point.ConnectionID + "\x00" + point.DataType + "\x00" + upstream + "\x00" + sourceFamily
	}
	return "metadata\x00" + point.ProviderName +
		"\x00" + point.ConnectionID +
		"\x00" + point.DataType +
		"\x00" + point.RecordKind +
		"\x00" + snapshotStringValue(point.StartTimeUTC) +
		"\x00" + snapshotStringValue(point.EndTimeUTC) +
		"\x00" + snapshotStringValue(point.StartCivilTime) +
		"\x00" + snapshotStringValue(point.EndCivilTime) +
		"\x00" + snapshotStringValue(point.ProviderCivilDate) +
		"\x00" + snapshotStringValue(point.TimezoneMetadata) +
		"\x00" + point.DataSourceJSON +
		"\x00" + sourceFamily
}

func rollupIdentityKey(rollup HealthArchiveSnapshotRollup) string {
	return rollup.ProviderName +
		"\x00" + rollup.ConnectionID +
		"\x00" + rollup.DataType +
		"\x00" + rollup.RollupKind +
		"\x00" + snapshotStringValue(rollup.WindowStartUTC) +
		"\x00" + snapshotStringValue(rollup.WindowEndUTC) +
		"\x00" + snapshotStringValue(rollup.CivilDate)
}

func snapshotStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func snapshotStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

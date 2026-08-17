package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

type HealthArchiveSnapshot struct {
	SchemaVersion        int
	Connections          []HealthArchiveSnapshotConnection
	DataPoints           []HealthArchiveSnapshotDataPoint
	DataPointRevisions   []HealthArchiveSnapshotDataPointRevision
	DataPointAttachments []HealthArchiveSnapshotDataPointAttachment
	AttachmentPayloads   []HealthArchiveSnapshotAttachmentPayload
	Rollups              []HealthArchiveSnapshotRollup
	IdentitySnapshots    []HealthArchiveSnapshotIdentitySnapshot
	SyncRuns             []HealthArchiveSnapshotSyncRun
	SyncCursors          []HealthArchiveSnapshotSyncCursor
}

type HealthArchiveSnapshotDataPointAttachment struct {
	ID           int64  `json:"id"`
	DataPointID  int64  `json:"data_point_id"`
	Kind         string `json:"kind"`
	SHA256       string `json:"sha256"`
	PathRelative string `json:"path_relative"`
	ByteSize     int64  `json:"byte_size"`
	FetchedAt    string `json:"fetched_at"`
}

type HealthArchiveSnapshotAttachmentPayload struct {
	SHA256       string `json:"sha256"`
	PathRelative string `json:"path_relative"`
	ByteSize     int64  `json:"byte_size"`
	Payload      []byte `json:"payload"`
}

type HealthArchiveSnapshotConnection struct {
	ID                 string  `json:"id"`
	ProviderName       string  `json:"provider_name"`
	GoogleHealthUserID string  `json:"google_health_user_id"`
	LegacyFitbitUserID *string `json:"legacy_fitbit_user_id"`
	TokenMetadataJSON  string  `json:"token_metadata_json"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	GoogleIdentityJSON string  `json:"google_identity_json"`
}

type HealthArchiveSnapshotDataPoint struct {
	ID                   int64   `json:"id"`
	ProviderName         string  `json:"provider_name"`
	ConnectionID         string  `json:"connection_id"`
	DataType             string  `json:"data_type"`
	UpstreamResourceName *string `json:"upstream_resource_name"`
	RecordKind           string  `json:"record_kind"`
	StartTimeUTC         *string `json:"start_time_utc"`
	EndTimeUTC           *string `json:"end_time_utc"`
	StartCivilTime       *string `json:"start_civil_time"`
	EndCivilTime         *string `json:"end_civil_time"`
	ProviderCivilDate    *string `json:"provider_civil_date"`
	TimezoneMetadata     *string `json:"timezone_metadata"`
	DataSourceJSON       string  `json:"data_source_json"`
	RawJSON              string  `json:"raw_json"`
	InsertedAt           string  `json:"inserted_at"`
	UpdatedAt            string  `json:"updated_at"`
	SourceFamilyFilter   *string `json:"source_family_filter"`
}

type HealthArchiveSnapshotDataPointRevision struct {
	ID                int64   `json:"id"`
	DataPointID       int64   `json:"data_point_id"`
	PreviousRawJSON   string  `json:"previous_raw_json"`
	ReplacedAt        string  `json:"replaced_at"`
	ReplacementReason *string `json:"replacement_reason"`
}

type HealthArchiveSnapshotRollup struct {
	ID               int64   `json:"id"`
	ProviderName     string  `json:"provider_name"`
	ConnectionID     string  `json:"connection_id"`
	DataType         string  `json:"data_type"`
	RollupKind       string  `json:"rollup_kind"`
	WindowStartUTC   *string `json:"window_start_utc"`
	WindowEndUTC     *string `json:"window_end_utc"`
	CivilDate        *string `json:"civil_date"`
	TimezoneMetadata *string `json:"timezone_metadata"`
	RawJSON          string  `json:"raw_json"`
	InsertedAt       string  `json:"inserted_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type HealthArchiveSnapshotIdentitySnapshot struct {
	ID           int64  `json:"id"`
	ProviderName string `json:"provider_name"`
	ConnectionID string `json:"connection_id"`
	RawJSON      string `json:"raw_json"`
	FetchedAt    string `json:"fetched_at"`
	SnapshotKind string `json:"snapshot_kind"`
}

type HealthArchiveSnapshotSyncRun struct {
	ID                 int64   `json:"id"`
	ProviderName       string  `json:"provider_name"`
	ConnectionID       *string `json:"connection_id"`
	DataTypesRequested string  `json:"data_types_requested"`
	RangeRequestedJSON string  `json:"range_requested_json"`
	EndpointFamily     string  `json:"endpoint_family"`
	Status             string  `json:"status"`
	SeenCount          int     `json:"seen_count"`
	NewCount           int     `json:"new_count"`
	UpdatedCount       int     `json:"updated_count"`
	StartedAt          string  `json:"started_at"`
	FinishedAt         *string `json:"finished_at"`
	ErrorSummary       *string `json:"error_summary"`
	SourceFamilyFilter *string `json:"source_family_filter"`
	LastProgressAt     *string `json:"last_progress_at"`
}

type HealthArchiveSnapshotSyncCursor struct {
	ConnectionID       string `json:"connection_id"`
	DataType           string `json:"data_type"`
	SourceFamilyFilter string `json:"source_family_filter"`
	RollupKind         string `json:"rollup_kind"`
	CursorTime         string `json:"cursor_time"`
	AdvancedAt         string `json:"advanced_at"`
}

type HealthArchiveSnapshotJSONLShard struct {
	Table string
	Path  string
	Rows  int
	JSONL []byte
}

func EncodeHealthArchiveSnapshotJSONL(snapshot HealthArchiveSnapshot) ([]HealthArchiveSnapshotJSONLShard, error) {
	if err := ValidateHealthArchiveSnapshot(snapshot); err != nil {
		return nil, err
	}
	shards := make([]HealthArchiveSnapshotJSONLShard, 0, 9)
	add := func(table string, rows int, encode func(*bytes.Buffer) error) error {
		var buffer bytes.Buffer
		if err := encode(&buffer); err != nil {
			return fmt.Errorf("encode Health Archive Snapshot %s JSONL: %w", table, err)
		}
		shards = append(shards, HealthArchiveSnapshotJSONLShard{
			Table: table,
			Path:  "data/" + table + ".jsonl.gz.age",
			Rows:  rows,
			JSONL: buffer.Bytes(),
		})
		return nil
	}
	if err := add("connections", len(snapshot.Connections), func(buffer *bytes.Buffer) error { return encodeSnapshotJSONL(buffer, snapshot.Connections) }); err != nil {
		return nil, err
	}
	if err := add("data_points", len(snapshot.DataPoints), func(buffer *bytes.Buffer) error { return encodeSnapshotJSONL(buffer, snapshot.DataPoints) }); err != nil {
		return nil, err
	}
	if err := add("data_point_revisions", len(snapshot.DataPointRevisions), func(buffer *bytes.Buffer) error { return encodeSnapshotJSONL(buffer, snapshot.DataPointRevisions) }); err != nil {
		return nil, err
	}
	if err := add("data_point_attachments", len(snapshot.DataPointAttachments), func(buffer *bytes.Buffer) error { return encodeSnapshotJSONL(buffer, snapshot.DataPointAttachments) }); err != nil {
		return nil, err
	}
	if err := add("attachment_payloads", len(snapshot.AttachmentPayloads), func(buffer *bytes.Buffer) error { return encodeSnapshotJSONL(buffer, snapshot.AttachmentPayloads) }); err != nil {
		return nil, err
	}
	if err := add("rollups", len(snapshot.Rollups), func(buffer *bytes.Buffer) error { return encodeSnapshotJSONL(buffer, snapshot.Rollups) }); err != nil {
		return nil, err
	}
	if err := add("identity_snapshots", len(snapshot.IdentitySnapshots), func(buffer *bytes.Buffer) error { return encodeSnapshotJSONL(buffer, snapshot.IdentitySnapshots) }); err != nil {
		return nil, err
	}
	if err := add("sync_runs", len(snapshot.SyncRuns), func(buffer *bytes.Buffer) error { return encodeSnapshotJSONL(buffer, snapshot.SyncRuns) }); err != nil {
		return nil, err
	}
	if err := add("sync_cursors", len(snapshot.SyncCursors), func(buffer *bytes.Buffer) error { return encodeSnapshotJSONL(buffer, snapshot.SyncCursors) }); err != nil {
		return nil, err
	}
	return shards, nil
}

func DecodeHealthArchiveSnapshotJSONL(schemaVersion int, shards []HealthArchiveSnapshotJSONLShard) (HealthArchiveSnapshot, error) {
	snapshot := HealthArchiveSnapshot{SchemaVersion: schemaVersion}
	seen := make(map[string]struct{}, len(shards))
	var err error
	for _, shard := range shards {
		if _, duplicate := seen[shard.Table]; duplicate {
			return HealthArchiveSnapshot{}, fmt.Errorf("duplicate Health Archive Snapshot shard table %q", shard.Table)
		}
		seen[shard.Table] = struct{}{}
		wantPath := "data/" + shard.Table + ".jsonl.gz.age"
		if shard.Path != wantPath {
			return HealthArchiveSnapshot{}, fmt.Errorf("Health Archive Snapshot shard %q path %q, want %q", shard.Table, shard.Path, wantPath)
		}
		switch shard.Table {
		case "connections":
			if snapshot.Connections, err = decodeSnapshotJSONL[HealthArchiveSnapshotConnection](shard.JSONL, shard.Rows); err != nil {
				return HealthArchiveSnapshot{}, err
			}
		case "data_points":
			if snapshot.DataPoints, err = decodeSnapshotJSONL[HealthArchiveSnapshotDataPoint](shard.JSONL, shard.Rows); err != nil {
				return HealthArchiveSnapshot{}, err
			}
		case "data_point_revisions":
			if snapshot.DataPointRevisions, err = decodeSnapshotJSONL[HealthArchiveSnapshotDataPointRevision](shard.JSONL, shard.Rows); err != nil {
				return HealthArchiveSnapshot{}, err
			}
		case "data_point_attachments":
			if snapshot.DataPointAttachments, err = decodeSnapshotJSONL[HealthArchiveSnapshotDataPointAttachment](shard.JSONL, shard.Rows); err != nil {
				return HealthArchiveSnapshot{}, err
			}
		case "attachment_payloads":
			if snapshot.AttachmentPayloads, err = decodeSnapshotJSONL[HealthArchiveSnapshotAttachmentPayload](shard.JSONL, shard.Rows); err != nil {
				return HealthArchiveSnapshot{}, err
			}
		case "rollups":
			if snapshot.Rollups, err = decodeSnapshotJSONL[HealthArchiveSnapshotRollup](shard.JSONL, shard.Rows); err != nil {
				return HealthArchiveSnapshot{}, err
			}
		case "identity_snapshots":
			if snapshot.IdentitySnapshots, err = decodeSnapshotJSONL[HealthArchiveSnapshotIdentitySnapshot](shard.JSONL, shard.Rows); err != nil {
				return HealthArchiveSnapshot{}, err
			}
		case "sync_runs":
			if snapshot.SyncRuns, err = decodeSnapshotJSONL[HealthArchiveSnapshotSyncRun](shard.JSONL, shard.Rows); err != nil {
				return HealthArchiveSnapshot{}, err
			}
		case "sync_cursors":
			if snapshot.SyncCursors, err = decodeSnapshotJSONL[HealthArchiveSnapshotSyncCursor](shard.JSONL, shard.Rows); err != nil {
				return HealthArchiveSnapshot{}, err
			}
		default:
			return HealthArchiveSnapshot{}, fmt.Errorf("unknown Health Archive Snapshot shard table %q", shard.Table)
		}
	}
	for _, table := range []string{"connections", "data_points", "data_point_revisions", "data_point_attachments", "attachment_payloads", "rollups", "identity_snapshots", "sync_runs", "sync_cursors"} {
		if _, ok := seen[table]; !ok {
			return HealthArchiveSnapshot{}, fmt.Errorf("Health Archive Snapshot shard %q is missing", table)
		}
	}
	if err := ValidateHealthArchiveSnapshot(snapshot); err != nil {
		return HealthArchiveSnapshot{}, err
	}
	return snapshot, nil
}

func decodeSnapshotJSONL[T any](data []byte, rows int) ([]T, error) {
	if rows < 0 {
		return nil, fmt.Errorf("row count %d must not be negative", rows)
	}
	if rows == 0 {
		if len(data) != 0 {
			return nil, errors.New("zero-row shard contains plaintext bytes")
		}
		return nil, nil
	}
	if !bytes.HasSuffix(data, []byte{'\n'}) || bytes.Count(data, []byte{'\n'}) != rows {
		return nil, fmt.Errorf("JSONL row count does not match %d", rows)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	values := make([]T, 0, rows)
	for index := 0; index < rows; index++ {
		var value T
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("row %d: %w", index+1, err)
		}
		values = append(values, value)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("JSONL has extra values")
		}
		return nil, err
	}
	return values, nil
}

func encodeSnapshotJSONL[T any](buffer *bytes.Buffer, rows []T) error {
	encoder := json.NewEncoder(buffer)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	return nil
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
	if snapshot.DataPointAttachments, snapshot.AttachmentPayloads, err = exportSnapshotDataPointAttachments(ctx, tx, archivePath); err != nil {
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

	attachmentIDs := map[int64]struct{}{}
	attachmentIdentities := map[string]struct{}{}
	attachmentPayloadRefs := map[string]HealthArchiveSnapshotDataPointAttachment{}
	for _, attachment := range snapshot.DataPointAttachments {
		if attachment.ID <= 0 {
			return fmt.Errorf("Data Point Attachment has invalid id %d", attachment.ID)
		}
		if _, exists := attachmentIDs[attachment.ID]; exists {
			return fmt.Errorf("duplicate Data Point Attachment id %d", attachment.ID)
		}
		attachmentIDs[attachment.ID] = struct{}{}
		if _, exists := dataPointIDs[attachment.DataPointID]; !exists {
			return fmt.Errorf("Data Point Attachment %d references unknown Data Point %d", attachment.ID, attachment.DataPointID)
		}
		identity := fmt.Sprintf("%d\x00%s", attachment.DataPointID, attachment.SHA256)
		if _, exists := attachmentIdentities[identity]; exists {
			return fmt.Errorf("duplicate Data Point Attachment identity for id %d", attachment.ID)
		}
		attachmentIdentities[identity] = struct{}{}
		if err := validateSnapshotAttachmentRow(attachment); err != nil {
			return fmt.Errorf("Data Point Attachment %d: %w", attachment.ID, err)
		}
		if previous, exists := attachmentPayloadRefs[attachment.PathRelative]; exists && (previous.SHA256 != attachment.SHA256 || previous.ByteSize != attachment.ByteSize) {
			return fmt.Errorf("Data Point Attachment %d conflicts with payload metadata for path %q", attachment.ID, attachment.PathRelative)
		}
		attachmentPayloadRefs[attachment.PathRelative] = attachment
	}
	attachmentPayloads := map[string]struct{}{}
	for _, payload := range snapshot.AttachmentPayloads {
		if _, exists := attachmentPayloads[payload.PathRelative]; exists {
			return fmt.Errorf("ambiguous duplicate Attachment payload path %q", payload.PathRelative)
		}
		attachmentPayloads[payload.PathRelative] = struct{}{}
		if err := validateSnapshotAttachmentPayload(payload); err != nil {
			return fmt.Errorf("Attachment payload %q: %w", payload.PathRelative, err)
		}
		attachment, exists := attachmentPayloadRefs[payload.PathRelative]
		if !exists {
			return fmt.Errorf("Attachment payload path %q has no Data Point Attachment row", payload.PathRelative)
		}
		if attachment.SHA256 != payload.SHA256 || attachment.ByteSize != payload.ByteSize {
			return fmt.Errorf("Attachment payload path %q does not match its Data Point Attachment metadata", payload.PathRelative)
		}
	}
	for pathRelative := range attachmentPayloadRefs {
		if _, exists := attachmentPayloads[pathRelative]; !exists {
			return fmt.Errorf("Data Point Attachment payload %q is missing", pathRelative)
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

func RestoreHealthArchiveSnapshot(ctx context.Context, snapshot HealthArchiveSnapshot, archivePath string) (err error) {
	if err := ValidateHealthArchiveSnapshot(snapshot); err != nil {
		return err
	}
	attachmentRoot := attachmentRootDirForArchive(archivePath)
	if err := validateSnapshotRestoreAttachmentRoot(attachmentRoot); err != nil {
		return err
	}
	lifecycle := healthArchiveLifecycle{path: archivePath}
	if err := lifecycle.Create(ctx); err != nil {
		return err
	}
	restoreSucceeded := false
	var rootIdentity attachmentRootIdentity
	rootIdentityReady := false
	defer func() {
		if restoreSucceeded {
			return
		}
		if cleanupErr := cleanupFailedHealthArchiveSnapshotRestore(archivePath, attachmentRoot, rootIdentity, rootIdentityReady, snapshot.AttachmentPayloads); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("clean up failed Health Archive Snapshot restore: %w", cleanupErr))
		}
	}()
	handle, err := lifecycle.Open(ctx, writeArchive)
	if err != nil {
		return err
	}
	defer handle.Close()
	rootIdentity, err = captureAttachmentRootIdentity(attachmentRoot)
	if err != nil {
		return err
	}
	rootIdentityReady = true

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
	for _, attachment := range snapshot.DataPointAttachments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO data_point_attachments (
			id,
			data_point_id,
			kind,
			sha256,
			path_relative,
			byte_size,
			fetched_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			attachment.ID,
			attachment.DataPointID,
			attachment.Kind,
			attachment.SHA256,
			attachment.PathRelative,
			attachment.ByteSize,
			attachment.FetchedAt,
		); err != nil {
			return fmt.Errorf("restore Data Point Attachment %d: %w", attachment.ID, err)
		}
	}
	for _, payload := range snapshot.AttachmentPayloads {
		if err := writeContainedSnapshotAttachment(attachmentRoot, payload.PathRelative, payload.Payload, rootIdentity); err != nil {
			return fmt.Errorf("restore Data Point Attachment payload %q: %w", payload.PathRelative, err)
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
	restoreSucceeded = true
	return nil
}

func cleanupFailedHealthArchiveSnapshotRestore(archivePath, attachmentRoot string, rootIdentity attachmentRootIdentity, rootIdentityReady bool, payloads []HealthArchiveSnapshotAttachmentPayload) error {
	var cleanupErrors []error
	if rootIdentityReady {
		for _, payload := range payloads {
			if err := removeAttachmentFileNoFollow(attachmentRoot, payload.PathRelative, rootIdentity); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	if err := os.Remove(archivePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErrors = append(cleanupErrors, err)
	}
	return errors.Join(cleanupErrors...)
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

func exportSnapshotDataPointAttachments(ctx context.Context, db healthArchiveSnapshotQuerier, archivePath string) ([]HealthArchiveSnapshotDataPointAttachment, []HealthArchiveSnapshotAttachmentPayload, error) {
	rows, err := db.QueryContext(ctx, `SELECT
		id,
		data_point_id,
		kind,
		sha256,
		path_relative,
		byte_size,
		fetched_at
	FROM data_point_attachments ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var items []HealthArchiveSnapshotDataPointAttachment
	payloadsByPath := map[string]HealthArchiveSnapshotAttachmentPayload{}
	var rootIdentity attachmentRootIdentity
	rootIdentityReady := false
	for rows.Next() {
		var item HealthArchiveSnapshotDataPointAttachment
		if err := rows.Scan(&item.ID, &item.DataPointID, &item.Kind, &item.SHA256, &item.PathRelative, &item.ByteSize, &item.FetchedAt); err != nil {
			return nil, nil, err
		}
		if err := validateSnapshotAttachmentRow(item); err != nil {
			return nil, nil, fmt.Errorf("export Data Point Attachment %d: %w", item.ID, err)
		}
		if previous, exists := payloadsByPath[item.PathRelative]; exists {
			if previous.SHA256 != item.SHA256 || previous.ByteSize != item.ByteSize {
				return nil, nil, fmt.Errorf("export Data Point Attachment %d: conflicting metadata for payload path %q", item.ID, item.PathRelative)
			}
		} else {
			if !rootIdentityReady {
				rootIdentity, err = captureAttachmentRootIdentity(attachmentRootDirForArchive(archivePath))
				if err != nil {
					return nil, nil, fmt.Errorf("export Data Point Attachment root: %w", err)
				}
				rootIdentityReady = true
			}
			payload, err := readContainedAttachment(attachmentRootDirForArchive(archivePath), item.PathRelative, item.ByteSize, &rootIdentity)
			if err != nil {
				return nil, nil, fmt.Errorf("export Data Point Attachment %d payload: %w", item.ID, err)
			}
			entry := HealthArchiveSnapshotAttachmentPayload{
				SHA256:       item.SHA256,
				PathRelative: item.PathRelative,
				ByteSize:     item.ByteSize,
				Payload:      payload,
			}
			if err := validateSnapshotAttachmentPayload(entry); err != nil {
				return nil, nil, fmt.Errorf("export Data Point Attachment %d: %w", item.ID, err)
			}
			payloadsByPath[item.PathRelative] = entry
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	payloads := make([]HealthArchiveSnapshotAttachmentPayload, 0, len(payloadsByPath))
	for _, payload := range payloadsByPath {
		payloads = append(payloads, payload)
	}
	sort.Slice(payloads, func(i, j int) bool {
		return payloads[i].PathRelative < payloads[j].PathRelative
	})
	return items, payloads, nil
}

func validateSnapshotAttachmentRow(attachment HealthArchiveSnapshotDataPointAttachment) error {
	wantPath, err := canonicalAttachmentPathRelative(attachment.Kind, attachment.SHA256)
	if err != nil {
		return err
	}
	if attachment.PathRelative != wantPath {
		return fmt.Errorf("path_relative %q, want canonical path %q", attachment.PathRelative, wantPath)
	}
	if attachment.ByteSize < 0 {
		return fmt.Errorf("byte_size %d must not be negative", attachment.ByteSize)
	}
	return nil
}

func validateSnapshotAttachmentPayload(payload HealthArchiveSnapshotAttachmentPayload) error {
	if payload.ByteSize != int64(len(payload.Payload)) {
		return fmt.Errorf("byte_size %d does not match payload size %d", payload.ByteSize, len(payload.Payload))
	}
	hash := sha256.Sum256(payload.Payload)
	if got := hex.EncodeToString(hash[:]); got != payload.SHA256 {
		return fmt.Errorf("sha256 %q does not match payload hash %q", payload.SHA256, got)
	}
	return nil
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

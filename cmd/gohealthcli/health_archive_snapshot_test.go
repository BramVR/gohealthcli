package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHealthArchiveSnapshotRoundTripPreservesVisibleArchiveState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, sourcePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, sourcePath)

	snapshot, err := ExportHealthArchiveSnapshot(ctx, sourcePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}
	if err := ValidateHealthArchiveSnapshot(snapshot); err != nil {
		t.Fatalf("ValidateHealthArchiveSnapshot err = %v", err)
	}

	restoreDir := filepath.Join(tempDir, "restore")
	if err := ensureOwnerOnlyDir(restoreDir); err != nil {
		t.Fatalf("create restore dir: %v", err)
	}
	restorePath := filepath.Join(restoreDir, "restored.sqlite")
	if err := RestoreHealthArchiveSnapshot(ctx, snapshot, restorePath); err != nil {
		t.Fatalf("RestoreHealthArchiveSnapshot err = %v", err)
	}

	sourceStatus := snapshotStatus(t, sourcePath)
	restoredStatus := snapshotStatus(t, restorePath)
	sourceStatus.ArchivePath = ""
	restoredStatus.ArchivePath = ""
	if !reflect.DeepEqual(restoredStatus, sourceStatus) {
		t.Fatalf("restored status mismatch\n got: %+v\nwant: %+v", restoredStatus, sourceStatus)
	}

	sourceSteps := snapshotExportRows(t, sourcePath, "daily-steps")
	restoredSteps := snapshotExportRows(t, restorePath, "daily-steps")
	if !reflect.DeepEqual(restoredSteps, sourceSteps) {
		t.Fatalf("restored daily-steps export = %+v, want %+v", restoredSteps, sourceSteps)
	}

	query := `SELECT data_point_id, previous_raw_json, replacement_reason FROM data_point_revisions ORDER BY id`
	sourceRevisions := snapshotQueryRows(t, sourcePath, query)
	restoredRevisions := snapshotQueryRows(t, restorePath, query)
	if !reflect.DeepEqual(restoredRevisions, sourceRevisions) {
		t.Fatalf("restored revision query = %+v, want %+v", restoredRevisions, sourceRevisions)
	}
}

func TestHealthArchiveSnapshotValidationRejectsBrokenReference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)

	snapshot, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}
	snapshot.DataPointRevisions[0].DataPointID = 999999

	err = ValidateHealthArchiveSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "Data Point Revision") || !strings.Contains(err.Error(), "unknown Data Point") {
		t.Fatalf("ValidateHealthArchiveSnapshot err = %v, want broken Data Point Revision reference", err)
	}
}

func TestHealthArchiveSnapshotValidationRejectsDuplicateLogicalIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)

	snapshot, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}
	duplicate := snapshot.DataPoints[0]
	duplicate.ID = snapshot.DataPoints[len(snapshot.DataPoints)-1].ID + 100
	snapshot.DataPoints = append(snapshot.DataPoints, duplicate)

	err = ValidateHealthArchiveSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "duplicate Data Point identity") {
		t.Fatalf("ValidateHealthArchiveSnapshot err = %v, want duplicate Data Point identity", err)
	}
}

func TestHealthArchiveSnapshotExportRejectsAttachments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	insertSnapshotAttachmentRow(t, archivePath)

	_, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if !errors.Is(err, ErrHealthArchiveSnapshotUnsupportedAttachments) {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v, want unsupported attachments", err)
	}
	if err == nil || !strings.Contains(err.Error(), "Data Point Attachments are not supported") {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v, want clear unsupported attachments message", err)
	}
}

func insertHealthArchiveSnapshotFixture(t *testing.T, archivePath string) {
	t.Helper()
	insertStatusFixtureRows(t, archivePath)
	db := openArchiveForTest(t, archivePath)

	var firstDataPointID int64
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM data_points WHERE upstream_resource_name = ?`, "users/me/dataTypes/steps/dataPoints/a").Scan(&firstDataPointID); err != nil {
		t.Fatalf("query fixture Data Point id: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO data_point_revisions (
		data_point_id,
		previous_raw_json,
		replaced_at,
		replacement_reason
	) VALUES (?, ?, ?, ?)`,
		firstDataPointID,
		`{"steps":{"count":"500"}}`,
		"2026-01-04T01:00:00Z",
		"provider_correction",
	); err != nil {
		t.Fatalf("insert fixture Data Point Revision: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO identity_snapshots (
		provider_name,
		connection_id,
		snapshot_kind,
		raw_json,
		fetched_at
	) VALUES
		(?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		"settings",
		`{"measurementSystem":"METRIC","timeZone":{"id":"Europe/Brussels"}}`,
		"2026-01-06T00:00:00Z",
		"googlehealth",
		"googlehealth:111111256096816351",
		"paired-devices",
		`{"pairedDevices":[{"name":"pixel-watch-2","deviceType":"WATCH","batteryStatus":"FULL","batteryLevel":93,"deviceVersion":"2"}]}`,
		"2026-01-07T00:00:00Z",
	); err != nil {
		t.Fatalf("insert fixture Identity Snapshots: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO sync_cursors (
		connection_id,
		data_type,
		source_family_filter,
		rollup_kind,
		cursor_time,
		advanced_at
	) VALUES
		(?, ?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?, ?)`,
		"googlehealth:111111256096816351",
		"steps",
		"",
		"none",
		"2026-01-03T00:00:00Z",
		"2026-01-03T00:00:10Z",
		"googlehealth:111111256096816351",
		"steps",
		"wearable",
		"none",
		"2026-01-04T00:00:00Z",
		"2026-01-04T00:00:10Z",
	); err != nil {
		t.Fatalf("insert fixture Sync Cursors: %v", err)
	}
}

func insertSnapshotAttachmentRow(t *testing.T, archivePath string) {
	t.Helper()
	db := openArchiveForTest(t, archivePath)
	var firstDataPointID int64
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM data_points ORDER BY id LIMIT 1`).Scan(&firstDataPointID); err != nil {
		t.Fatalf("query first Data Point id: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO data_point_attachments (
		data_point_id,
		kind,
		sha256,
		path_relative,
		byte_size,
		fetched_at
	) VALUES (?, ?, ?, ?, ?, ?)`,
		firstDataPointID,
		"tcx",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"tcx/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.tcx",
		7,
		"2026-01-08T00:00:00Z",
	); err != nil {
		t.Fatalf("insert fixture Attachment row: %v", err)
	}
}

func snapshotStatus(t *testing.T, archivePath string) statusResult {
	t.Helper()
	result, err := statusSetup(context.Background(), archivePath, time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("statusSetup(%s): %v", archivePath, err)
	}
	return result
}

func snapshotExportRows(t *testing.T, archivePath, dataset string) []exportRow {
	t.Helper()
	rows, err := exportRows(context.Background(), archivePath, exportDatasetSpecs[dataset])
	if err != nil {
		t.Fatalf("exportRows(%s, %s): %v", archivePath, dataset, err)
	}
	return rows
}

func snapshotQueryRows(t *testing.T, archivePath, statement string) [][]any {
	t.Helper()
	result, err := querySetup(context.Background(), archivePath, statement, newJSONModeEncoder())
	if err != nil {
		t.Fatalf("querySetup(%s): %v", archivePath, err)
	}
	return result.Rows
}

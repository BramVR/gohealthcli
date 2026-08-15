package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
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

func TestHealthArchiveSnapshotRoundTripPreservesAttachments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	payload := []byte(`<?xml version="1.0"?><TrainingCenterDatabase><Courses/></TrainingCenterDatabase>`)
	stored := storeSnapshotAttachment(t, archivePath, payload)

	snapshot, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}
	if len(snapshot.DataPointAttachments) != 1 {
		t.Fatalf("Data Point Attachment count = %d, want 1", len(snapshot.DataPointAttachments))
	}
	if len(snapshot.AttachmentPayloads) != 1 {
		t.Fatalf("Attachment payload count = %d, want 1", len(snapshot.AttachmentPayloads))
	}
	if !bytes.Equal(snapshot.AttachmentPayloads[0].Payload, payload) {
		t.Fatal("snapshot Data Point Attachment payload mismatch")
	}

	restoreDir := filepath.Join(tempDir, "attachment-restore")
	if err := ensureOwnerOnlyDir(restoreDir); err != nil {
		t.Fatalf("create restore dir: %v", err)
	}
	restorePath := filepath.Join(restoreDir, "restored.sqlite")
	if err := RestoreHealthArchiveSnapshot(ctx, snapshot, restorePath); err != nil {
		t.Fatalf("RestoreHealthArchiveSnapshot err = %v", err)
	}
	restoredPath := filepath.Join(attachmentRootDirForArchive(restorePath), filepath.FromSlash(stored.PathRelative))
	restoredPayload, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatalf("read restored Attachment: %v", err)
	}
	if !bytes.Equal(restoredPayload, payload) {
		t.Fatal("restored Data Point Attachment payload mismatch")
	}
	if usesPOSIXPermissions() {
		info, err := os.Stat(restoredPath)
		if err != nil {
			t.Fatalf("stat restored Attachment: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("restored Attachment mode = %04o, want 0600", info.Mode().Perm())
		}
	}
	report, err := collectAttachmentOrphans(ctx, restorePath)
	if err != nil {
		t.Fatalf("collect restored Attachment orphans: %v", err)
	}
	if report != nil {
		t.Fatalf("restored Attachment orphans = %+v, want none", report)
	}
}

func TestHealthArchiveSnapshotExportRejectsAttachmentSymlink(t *testing.T) {
	t.Parallel()
	if !usesPOSIXPermissions() {
		t.Skip("symlink setup requires extra privileges on Windows")
	}
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	payload := []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`)
	stored := storeSnapshotAttachment(t, archivePath, payload)
	outsidePath := filepath.Join(tempDir, "outside.tcx")
	if err := os.WriteFile(outsidePath, payload, 0o600); err != nil {
		t.Fatalf("write outside payload: %v", err)
	}
	if err := os.Remove(stored.AbsolutePath); err != nil {
		t.Fatalf("remove stored Attachment: %v", err)
	}
	if err := os.Symlink(outsidePath, stored.AbsolutePath); err != nil {
		t.Fatalf("replace Attachment with symlink: %v", err)
	}

	_, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v, want symbolic-link rejection", err)
	}
}

func TestHealthArchiveSnapshotRestoreRejectsSymlinkedAttachmentRoot(t *testing.T) {
	t.Parallel()
	if !usesPOSIXPermissions() {
		t.Skip("symlink setup requires extra privileges on Windows")
	}
	ctx := context.Background()
	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "source")
	if err := ensureOwnerOnlyDir(sourceDir); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	_, sourcePath, _ := initializeFileCredentialSetup(t, sourceDir)
	insertHealthArchiveSnapshotFixture(t, sourcePath)
	storeSnapshotAttachment(t, sourcePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	snapshot, err := ExportHealthArchiveSnapshot(ctx, sourcePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}

	restoreDir := filepath.Join(tempDir, "restore")
	if err := ensureOwnerOnlyDir(restoreDir); err != nil {
		t.Fatalf("create restore dir: %v", err)
	}
	restorePath := filepath.Join(restoreDir, "restored.sqlite")
	outsideDir := filepath.Join(tempDir, "outside")
	if err := ensureOwnerOnlyDir(outsideDir); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	if err := os.Symlink(outsideDir, attachmentRootDirForArchive(restorePath)); err != nil {
		t.Fatalf("symlink Attachment root: %v", err)
	}

	err = RestoreHealthArchiveSnapshot(ctx, snapshot, restorePath)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("RestoreHealthArchiveSnapshot err = %v, want symbolic-link rejection", err)
	}
	entries, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("restore wrote through Attachment root symlink: %v", entries)
	}
}

func TestHealthArchiveSnapshotRestoreRejectsNonEmptyAttachmentRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "source")
	if err := ensureOwnerOnlyDir(sourceDir); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	_, sourcePath, _ := initializeFileCredentialSetup(t, sourceDir)
	insertHealthArchiveSnapshotFixture(t, sourcePath)
	storeSnapshotAttachment(t, sourcePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	snapshot, err := ExportHealthArchiveSnapshot(ctx, sourcePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}

	restoreDir := filepath.Join(tempDir, "restore")
	if err := ensureOwnerOnlyDir(restoreDir); err != nil {
		t.Fatalf("create restore dir: %v", err)
	}
	restorePath := filepath.Join(restoreDir, "restored.sqlite")
	attachmentRoot := attachmentRootDirForArchive(restorePath)
	if err := ensureOwnerOnlyDir(attachmentRoot); err != nil {
		t.Fatalf("create Attachment root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(attachmentRoot, "stale.bin"), []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale Attachment root file: %v", err)
	}

	err = RestoreHealthArchiveSnapshot(ctx, snapshot, restorePath)
	if err == nil || !strings.Contains(err.Error(), "must contain only empty directories") {
		t.Fatalf("RestoreHealthArchiveSnapshot err = %v, want non-empty Attachment root rejection", err)
	}
}

func TestHealthArchiveSnapshotFailedRestoreCleansArchiveAndAttachments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, sourcePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, sourcePath)
	storeSnapshotAttachment(t, sourcePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	snapshot, err := ExportHealthArchiveSnapshot(ctx, sourcePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}
	secondPayload := []byte("second payload")
	secondHash := sha256.Sum256(secondPayload)
	secondHashHex := hex.EncodeToString(secondHash[:])
	secondKind := strings.Repeat("z", 300)
	secondPath, err := canonicalAttachmentPathRelative(secondKind, secondHashHex)
	if err != nil {
		t.Fatalf("canonical second Attachment path: %v", err)
	}
	firstAttachment := snapshot.DataPointAttachments[0]
	snapshot.DataPointAttachments = append(snapshot.DataPointAttachments, HealthArchiveSnapshotDataPointAttachment{
		ID:           firstAttachment.ID + 1,
		DataPointID:  snapshot.DataPoints[len(snapshot.DataPoints)-1].ID,
		Kind:         secondKind,
		SHA256:       secondHashHex,
		PathRelative: secondPath,
		ByteSize:     int64(len(secondPayload)),
		FetchedAt:    firstAttachment.FetchedAt,
	})
	snapshot.AttachmentPayloads = append(snapshot.AttachmentPayloads, HealthArchiveSnapshotAttachmentPayload{
		SHA256:       secondHashHex,
		PathRelative: secondPath,
		ByteSize:     int64(len(secondPayload)),
		Payload:      secondPayload,
	})
	restoreDir := filepath.Join(tempDir, "failed-restore")
	if err := ensureOwnerOnlyDir(restoreDir); err != nil {
		t.Fatalf("create restore dir: %v", err)
	}
	restorePath := filepath.Join(restoreDir, "restored.sqlite")

	err = RestoreHealthArchiveSnapshot(ctx, snapshot, restorePath)
	if err == nil {
		t.Fatal("RestoreHealthArchiveSnapshot err = nil, want second Attachment path failure")
	}
	if _, statErr := os.Stat(restorePath); !os.IsNotExist(statErr) {
		t.Fatalf("failed restore archive stat err = %v, want absent", statErr)
	}
	if err := validateSnapshotRestoreAttachmentRoot(attachmentRootDirForArchive(restorePath)); err != nil {
		t.Fatalf("failed restore left a dirty Attachment root: %v", err)
	}
	snapshot.DataPointAttachments = snapshot.DataPointAttachments[:len(snapshot.DataPointAttachments)-1]
	snapshot.AttachmentPayloads = snapshot.AttachmentPayloads[:len(snapshot.AttachmentPayloads)-1]
	if err := RestoreHealthArchiveSnapshot(ctx, snapshot, restorePath); err != nil {
		t.Fatalf("retry RestoreHealthArchiveSnapshot err = %v", err)
	}
}

func TestHealthArchiveSnapshotValidationRejectsAmbiguousAttachmentPayloadPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	storeSnapshotAttachment(t, archivePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	snapshot, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}
	duplicate := snapshot.AttachmentPayloads[0]
	duplicate.Payload = []byte("different payload")
	snapshot.AttachmentPayloads = append(snapshot.AttachmentPayloads, duplicate)

	err = ValidateHealthArchiveSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "ambiguous duplicate Attachment payload path") {
		t.Fatalf("ValidateHealthArchiveSnapshot err = %v, want ambiguous payload-path rejection", err)
	}
}

func TestHealthArchiveSnapshotExportRejectsMissingAttachmentPayload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	stored := storeSnapshotAttachment(t, archivePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	if err := os.Remove(stored.AbsolutePath); err != nil {
		t.Fatalf("remove Attachment payload: %v", err)
	}

	_, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err == nil || !strings.Contains(err.Error(), "Attachment") || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v, want missing Attachment payload rejection", err)
	}
}

func TestHealthArchiveSnapshotExportRejectsMismatchedAttachmentPayload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	payload := []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`)
	stored := storeSnapshotAttachment(t, archivePath, payload)
	corrupt := bytes.Repeat([]byte("x"), len(payload))
	if err := os.WriteFile(stored.AbsolutePath, corrupt, 0o600); err != nil {
		t.Fatalf("corrupt Attachment payload: %v", err)
	}

	_, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err == nil || !strings.Contains(err.Error(), "does not match payload hash") {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v, want Attachment hash rejection", err)
	}
}

func TestHealthArchiveSnapshotExportRejectsAttachmentSizeMismatchBeforeRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	storeSnapshotAttachment(t, archivePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	db := openArchiveForTest(t, archivePath)
	if _, err := db.ExecContext(ctx, `UPDATE data_point_attachments SET byte_size = 1`); err != nil {
		t.Fatalf("tamper Attachment byte_size: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close tampered archive: %v", err)
	}

	_, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err == nil || !strings.Contains(err.Error(), "does not match byte_size") {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v, want pre-read size rejection", err)
	}
}

func TestHealthArchiveSnapshotExportRejectsNonRegularAttachmentPayload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	stored := storeSnapshotAttachment(t, archivePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	if err := os.Remove(stored.AbsolutePath); err != nil {
		t.Fatalf("remove Attachment payload: %v", err)
	}
	if err := os.Mkdir(stored.AbsolutePath, 0o700); err != nil {
		t.Fatalf("replace Attachment payload with directory: %v", err)
	}

	_, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v, want non-regular payload rejection", err)
	}
}

func TestHealthArchiveSnapshotValidationRejectsNonCanonicalAttachmentPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	storeSnapshotAttachment(t, archivePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	snapshot, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}
	attachment := &snapshot.DataPointAttachments[0]
	attachment.PathRelative = "tcx/ff/" + attachment.SHA256 + ".tcx"

	err = ValidateHealthArchiveSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "want canonical path") {
		t.Fatalf("ValidateHealthArchiveSnapshot err = %v, want non-canonical path rejection", err)
	}
}

func TestHealthArchiveSnapshotExportOrdersAttachmentsDeterministically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	storeSnapshotAttachment(t, archivePath, []byte("attachment two"))
	storeSnapshotAttachment(t, archivePath, []byte("attachment one"))

	first, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err != nil {
		t.Fatalf("first ExportHealthArchiveSnapshot err = %v", err)
	}
	second, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err != nil {
		t.Fatalf("second ExportHealthArchiveSnapshot err = %v", err)
	}
	if !reflect.DeepEqual(first.DataPointAttachments, second.DataPointAttachments) || !reflect.DeepEqual(first.AttachmentPayloads, second.AttachmentPayloads) {
		t.Fatalf("Attachment exports differ\nfirst: %+v\nsecond: %+v", first.DataPointAttachments, second.DataPointAttachments)
	}
	if len(first.DataPointAttachments) != 2 || first.DataPointAttachments[0].ID >= first.DataPointAttachments[1].ID {
		t.Fatalf("Attachment order = %+v, want ascending id", first.DataPointAttachments)
	}
}

func TestHealthArchiveSnapshotExportRejectsAttachmentPathTraversal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	storeSnapshotAttachment(t, archivePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	db := openArchiveForTest(t, archivePath)
	if _, err := db.ExecContext(ctx, `UPDATE data_point_attachments SET path_relative = '../../outside.tcx'`); err != nil {
		t.Fatalf("tamper Attachment path: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close tampered archive: %v", err)
	}

	_, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err == nil || !strings.Contains(err.Error(), "canonical path") {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v, want path traversal rejection", err)
	}
}

func TestHealthArchiveSnapshotDeduplicatesSharedAttachmentPayload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	db := openArchiveForTest(t, archivePath)
	var dataPointIDs []int64
	func() {
		rows, err := db.QueryContext(ctx, `SELECT id FROM data_points ORDER BY id LIMIT 2`)
		if err != nil {
			t.Fatalf("query Data Point ids: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan Data Point id: %v", err)
			}
			dataPointIDs = append(dataPointIDs, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate Data Point ids: %v", err)
		}
	}()
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture archive: %v", err)
	}
	if len(dataPointIDs) != 2 {
		t.Fatalf("Data Point count = %d, want at least 2", len(dataPointIDs))
	}
	payload := []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`)
	storeSnapshotAttachmentForDataPoint(t, archivePath, dataPointIDs[0], payload)
	storeSnapshotAttachmentForDataPoint(t, archivePath, dataPointIDs[1], payload)

	snapshot, err := ExportHealthArchiveSnapshot(ctx, archivePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}
	if len(snapshot.DataPointAttachments) != 2 || len(snapshot.AttachmentPayloads) != 1 {
		t.Fatalf("snapshot has %d Attachment rows and %d payloads, want 2 and 1", len(snapshot.DataPointAttachments), len(snapshot.AttachmentPayloads))
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

func storeSnapshotAttachment(t *testing.T, archivePath string, payload []byte) attachmentRecord {
	t.Helper()
	db := openArchiveForTest(t, archivePath)
	var firstDataPointID int64
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM data_points ORDER BY id LIMIT 1`).Scan(&firstDataPointID); err != nil {
		t.Fatalf("query first Data Point id: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close snapshot fixture archive: %v", err)
	}
	return storeSnapshotAttachmentForDataPoint(t, archivePath, firstDataPointID, payload)
}

func storeSnapshotAttachmentForDataPoint(t *testing.T, archivePath string, dataPointID int64, payload []byte) attachmentRecord {
	t.Helper()
	store, err := openAttachmentStoreMode(archivePath, writeArchive)
	if err != nil {
		t.Fatalf("open Attachment Store: %v", err)
	}
	defer store.Close()
	record, err := store.Store(context.Background(), dataPointID, "tcx", payload, "2026-01-08T00:00:00Z")
	if err != nil {
		t.Fatalf("Store snapshot Attachment: %v", err)
	}
	return record
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

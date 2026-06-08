package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHealthArchiveWriterRequiresCurrentConnection(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	_, err = archive.CurrentConnection()
	if err == nil || !strings.Contains(err.Error(), "no Connection found") {
		t.Fatalf("CurrentConnection error = %v, want no Connection found", err)
	}
}

func TestHealthArchiveWriterRecordsDataPointRevisionsRollupsAndSyncRun(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	startedAt := "2026-01-01T00:00:00Z"
	syncRunID, err := archive.StartSyncRun(connection, []string{"steps"}, "2026-01-01", "2026-01-02", "list", "", startedAt)
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}

	point := archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "steps",
		upstreamResourceName: "users/me/dataTypes/steps/dataPoints/archive-writer",
		recordKind:           "interval",
		startTimeUTC:         "2026-01-01T08:00:00Z",
		endTimeUTC:           "2026-01-01T08:15:00Z",
		dataSourceJSON:       `{"platform":"FITBIT"}`,
		rawJSON:              `{"steps":{"count":"123"}}`,
	}
	if status, err := archive.UpsertDataPoint(point, "2026-01-01T00:00:01Z"); err != nil || status != "new" {
		t.Fatalf("first UpsertDataPoint = (%q, %v), want new", status, err)
	}
	if status, err := archive.UpsertDataPoint(point, "2026-01-01T00:00:02Z"); err != nil || status != "unchanged" {
		t.Fatalf("same UpsertDataPoint = (%q, %v), want unchanged", status, err)
	}
	point.rawJSON = `{"steps":{"count":"456"}}`
	if status, err := archive.UpsertDataPoint(point, "2026-01-01T00:00:03Z"); err != nil || status != "updated" {
		t.Fatalf("corrected UpsertDataPoint = (%q, %v), want updated", status, err)
	}

	rollup := archivedRollup{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "steps",
		rollupKind:           "dailyRollUp",
		civilDate:            "2026-01-01",
		timezoneMetadataJSON: "{}",
		rawJSON:              `{"steps":{"countSum":"123"}}`,
	}
	if status, err := archive.UpsertRollup(rollup, "2026-01-01T00:00:04Z"); err != nil || status != "new" {
		t.Fatalf("first UpsertRollup = (%q, %v), want new", status, err)
	}
	rollup.rawJSON = `{"steps":{"countSum":"456"}}`
	if status, err := archive.UpsertRollup(rollup, "2026-01-01T00:00:05Z"); err != nil || status != "updated" {
		t.Fatalf("corrected UpsertRollup = (%q, %v), want updated", status, err)
	}

	if err := archive.FinishSyncRun(syncRunID, "sync_completed", 4, 2, 2, "2026-01-01T00:00:06Z", ""); err != nil {
		t.Fatalf("FinishSyncRun: %v", err)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertSyncRunForDataType(t, archivePath, syncRunID, "sync_completed", "steps", "list", 4, 2, 2, "")
}

// TestHealthArchiveWriterStoreAttachmentWritesSidecarRow pins the
// archive impl of StoreAttachment (#107 slice D): it resolves the
// upserted Data Point row, writes the bytes as a content-addressed
// sidecar at <archive>.attachments/<kind>/<sha[0:2]>/<sha>.<ext>, and
// inserts a data_point_attachments row keyed by the resolved id.
// Owner-only POSIX permissions on the sidecar file are enforced.
func TestHealthArchiveWriterStoreAttachmentWritesSidecarRow(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	point := archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "exercise",
		upstreamResourceName: "users/me/dataTypes/exercise/dataPoints/tcx-writer",
		recordKind:           "session",
		startTimeUTC:         "2026-01-01T08:00:00Z",
		endTimeUTC:           "2026-01-01T08:30:00Z",
		dataSourceJSON:       `{"platform":"FITBIT"}`,
		rawJSON:              `{"exercise":{"exerciseType":"RUNNING"}}`,
	}
	if status, err := archive.UpsertDataPoint(point, "2026-01-01T00:00:01Z"); err != nil || status != "new" {
		t.Fatalf("UpsertDataPoint = (%q, %v), want new", status, err)
	}

	tcxBody := []byte(`<?xml version="1.0"?><TrainingCenterDatabase>writer</TrainingCenterDatabase>`)
	if err := archive.StoreAttachment(point, "tcx", tcxBody, "2026-01-01T00:00:02Z"); err != nil {
		t.Fatalf("StoreAttachment: %v", err)
	}

	expectedHash := sha256.Sum256(tcxBody)
	expectedHex := hex.EncodeToString(expectedHash[:])
	wantPath := filepath.Join(archivePath+".attachments", "tcx", expectedHex[0:2], expectedHex+".tcx")
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read sidecar at %s: %v", wantPath, err)
	}
	if !bytes.Equal(body, tcxBody) {
		t.Fatalf("sidecar bytes mismatch")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(wantPath)
		if err != nil {
			t.Fatalf("stat sidecar: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("sidecar mode = %o, want 0600", info.Mode().Perm())
		}
	}
	assertArchiveTableCount(t, archivePath, "data_point_attachments", 1)

	// Idempotent: re-store identical bytes does not duplicate the row.
	if err := archive.StoreAttachment(point, "tcx", tcxBody, "2026-01-01T00:00:03Z"); err != nil {
		t.Fatalf("idempotent StoreAttachment: %v", err)
	}
	assertArchiveTableCount(t, archivePath, "data_point_attachments", 1)
}

// TestHealthArchiveWriterStoreAttachmentErrorsWhenDataPointMissing
// pins the error surface: trying to attach to a Data Point that was
// not upserted first must fail (the data_point row id can't be
// resolved). The bytes must not land on disk in that case — we don't
// want orphan sidecars from misuse of the API.
func TestHealthArchiveWriterStoreAttachmentErrorsWhenDataPointMissing(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	point := archivedDataPoint{
		providerName:         "googlehealth",
		connectionID:         "googlehealth:phantom",
		dataType:             "exercise",
		upstreamResourceName: "users/me/dataTypes/exercise/dataPoints/missing",
		recordKind:           "session",
	}
	if err := archive.StoreAttachment(point, "tcx", []byte("<?xml?>"), "2026-01-01T00:00:00Z"); err == nil {
		t.Fatalf("expected error attaching to missing Data Point")
	}
}

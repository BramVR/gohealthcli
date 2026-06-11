package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorJSONReportsAttachmentOrphans is the slice tracer for #108:
// when the archive has orphan attachments (rows without files, or
// files without rows), doctor --json surfaces them under an
// `attachments` block. Reporting only — the archive is not modified.
func TestDoctorJSONReportsAttachmentOrphans(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}
	store, err := openAttachmentStoreMode(archivePath, writeArchive)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Seed a minimal exercise Data Point so the FK on the orphan
	// attachment row is satisfied. Capture LastInsertId so the FK
	// stays valid if other fixtures begin inserting data_points.
	dpResult, err := store.db.Exec(`INSERT INTO data_points (provider_name, connection_id, data_type, upstream_resource_name, record_kind, start_time_utc, end_time_utc, data_source_json, raw_json, inserted_at, updated_at) VALUES ('googlehealth', 'googlehealth:111111256096816351', 'exercise', 'users/me/dataTypes/exercise/dataPoints/orphan-test', 'session', '2026-06-08T17:00:00Z', '2026-06-08T17:30:00Z', '{}', '{}', '2026-06-08T17:31:00Z', '2026-06-08T17:31:00Z')`)
	if err != nil {
		store.Close()
		t.Fatalf("seed data_point: %v", err)
	}
	dataPointID, err := dpResult.LastInsertId()
	if err != nil {
		store.Close()
		t.Fatalf("LastInsertId: %v", err)
	}
	// Seed an orphan row (DB row, no file).
	if _, err := store.db.Exec(`INSERT INTO data_point_attachments (data_point_id, kind, sha256, path_relative, byte_size, fetched_at) VALUES (?, 'tcx', ?, ?, 7, ?)`,
		dataPointID,
		"deadbeef00000000000000000000000000000000000000000000000000000000",
		"tcx/de/deadbeef00000000000000000000000000000000000000000000000000000000.tcx",
		"2026-06-08T17:36:00Z"); err != nil {
		store.Close()
		t.Fatalf("seed orphan row: %v", err)
	}
	store.Close()
	// Seed an orphan file (file on disk, no row).
	rootDir := attachmentRootDirForArchive(archivePath)
	orphanDir := filepath.Join(rootDir, "tcx", "f0")
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	orphanFile := filepath.Join(orphanDir, "f00dface00000000000000000000000000000000000000000000000000000000.tcx")
	if err := os.WriteFile(orphanFile, []byte("orphan"), 0o600); err != nil {
		t.Fatalf("write orphan file: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, stderr=%s\nstdout=%s", code, stderr.String(), stdout.String())
	}

	var report struct {
		Attachments struct {
			OrphanRows []struct {
				SHA256       string `json:"sha256"`
				PathRelative string `json:"path_relative"`
				DataPointID  int64  `json:"data_point_id"`
			} `json:"orphan_rows"`
			OrphanFiles []struct {
				AbsolutePath string `json:"absolute_path"`
			} `json:"orphan_files"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout.String())
	}
	if len(report.Attachments.OrphanRows) != 1 {
		t.Fatalf("orphan_rows = %d, want 1; output=%s", len(report.Attachments.OrphanRows), stdout.String())
	}
	if report.Attachments.OrphanRows[0].SHA256 != "deadbeef00000000000000000000000000000000000000000000000000000000" {
		t.Fatalf("orphan row SHA = %q, want deadbeef…", report.Attachments.OrphanRows[0].SHA256)
	}
	if len(report.Attachments.OrphanFiles) != 1 {
		t.Fatalf("orphan_files = %d, want 1", len(report.Attachments.OrphanFiles))
	}
	if !strings.HasSuffix(report.Attachments.OrphanFiles[0].AbsolutePath, "f00dface00000000000000000000000000000000000000000000000000000000.tcx") {
		t.Fatalf("orphan file path = %q, want f00dface… suffix", report.Attachments.OrphanFiles[0].AbsolutePath)
	}
}

// TestDoctorPlainReportsAttachmentOrphanCounts pins the plain-mode
// shape: each `attachments_orphan_*: N` line is emitted only when N
// is greater than 0 (so a clean archive emits no orphan line at all).
// This test seeds only an orphan file and asserts only the file-side
// count line.
func TestDoctorPlainReportsAttachmentOrphanCounts(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommandWithRuntime(t, configPath, archivePath, testRuntime); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}
	rootDir := attachmentRootDirForArchive(archivePath)
	orphanDir := filepath.Join(rootDir, "tcx", "f0")
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "f00dface00000000000000000000000000000000000000000000000000000000.tcx"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"doctor", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("doctor exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "attachments_orphan_files: 1") {
		t.Errorf("plain output missing attachments_orphan_files: 1\n%s", stdout.String())
	}
}

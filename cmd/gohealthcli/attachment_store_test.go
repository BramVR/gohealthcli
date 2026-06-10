package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAttachmentStoreStoreWritesSidecarAndRow is the slice A tracer for
// #107: Store(dataPointID, kind, bytes) writes a sidecar file under
// <archive>.attachments/<kind>/<sha256[0:2]>/<sha256>.<ext>, inserts a
// data_point_attachments row, and returns the sha256.
func TestAttachmentStoreStoreWritesSidecarAndRow(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType:     "exercise",
		resourceName: "users/me/dataTypes/exercise/dataPoints/tcx-1",
		recordKind:   "session",
		startUTC:     "2026-06-08T17:00:00Z",
		endUTC:       "2026-06-08T17:30:00Z",
		startCivil:   "2026-06-08T18:00:00",
		endCivil:     "2026-06-08T18:30:00",
		civilDate:    "2026-06-08",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"exercise":{"exerciseType":"RUNNING"}}`,
	})

	store, err := openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("open attachment store: %v", err)
	}
	defer store.Close()

	payload := []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`)
	expectedHash := sha256.Sum256(payload)
	expectedHex := hex.EncodeToString(expectedHash[:])

	got, err := store.Store(1, "tcx", payload, "2026-06-08T17:35:00Z")
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if got.SHA256 != expectedHex {
		t.Fatalf("Store SHA256 = %q, want %q", got.SHA256, expectedHex)
	}
	wantSubdir := filepath.Join(archivePath+".attachments", "tcx", expectedHex[0:2])
	wantFile := filepath.Join(wantSubdir, expectedHex+".tcx")
	if got.AbsolutePath != wantFile {
		t.Fatalf("Store AbsolutePath = %q, want %q", got.AbsolutePath, wantFile)
	}
	body, err := os.ReadFile(wantFile)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("sidecar bytes mismatch")
	}
}

// TestAttachmentStoreStoreIsContentAddressedAndIdempotent pins ADR-0009:
// storing the same bytes twice returns the same sha256, reuses the
// same sidecar path, and does NOT insert a duplicate row.
func TestAttachmentStoreStoreIsContentAddressedAndIdempotent(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType: "exercise", resourceName: "users/me/dataTypes/exercise/dataPoints/dup",
		recordKind: "session", startUTC: "2026-06-08T17:00:00Z", endUTC: "2026-06-08T17:30:00Z",
		startCivil: "2026-06-08T18:00:00", endCivil: "2026-06-08T18:30:00", civilDate: "2026-06-08",
		dataSource: `{"platform":"FITBIT"}`, rawJSON: `{"exercise":{}}`,
	})

	store, err := openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	payload := []byte("same bytes")

	first, err := store.Store(1, "tcx", payload, "2026-06-08T17:35:00Z")
	if err != nil {
		t.Fatalf("first Store: %v", err)
	}
	second, err := store.Store(1, "tcx", payload, "2026-06-08T17:36:00Z")
	if err != nil {
		t.Fatalf("second Store: %v", err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("SHA mismatch: %q vs %q", first.SHA256, second.SHA256)
	}
	if first.AbsolutePath != second.AbsolutePath {
		t.Fatalf("path mismatch: %q vs %q", first.AbsolutePath, second.AbsolutePath)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent re-store changed row id: %d → %d", first.ID, second.ID)
	}

	count := countAttachmentRows(t, archivePath)
	if count != 1 {
		t.Fatalf("attachment row count = %d, want 1 (idempotent)", count)
	}
}

// TestAttachmentStoreResolveReturnsAbsolutePath: Resolve(sha) returns
// the same absolute path Store(...) wrote; Resolve on a missing hash
// surfaces a clear error so doctor/orphan reporting can act on it.
func TestAttachmentStoreResolveReturnsAbsolutePath(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType: "exercise", resourceName: "users/me/dataTypes/exercise/dataPoints/r",
		recordKind: "session", startUTC: "2026-06-08T17:00:00Z", endUTC: "2026-06-08T17:30:00Z",
		startCivil: "2026-06-08T18:00:00", endCivil: "2026-06-08T18:30:00", civilDate: "2026-06-08",
		dataSource: `{}`, rawJSON: `{}`,
	})
	store, err := openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	stored, err := store.Store(1, "tcx", []byte("payload"), "2026-06-08T17:35:00Z")
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	resolved, err := store.Resolve(stored.SHA256)
	if err != nil || resolved != stored.AbsolutePath {
		t.Fatalf("Resolve = (%q, %v), want %q", resolved, err, stored.AbsolutePath)
	}
	_, err = store.Resolve("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Resolve(missing) = %v, want not-found error", err)
	}
}

// TestAttachmentStoreSidecarFilesAreOwnerOnly verifies the POSIX
// permission contract on the sidecar file (mode 0600) and the kind
// subdir (mode 0700).
func TestAttachmentStoreSidecarFilesAreOwnerOnly(t *testing.T) {
	if !usesPOSIXPermissions() {
		t.Skip("POSIX permission test")
	}
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType: "exercise", resourceName: "users/me/dataTypes/exercise/dataPoints/p",
		recordKind: "session", startUTC: "2026-06-08T17:00:00Z", endUTC: "2026-06-08T17:30:00Z",
		startCivil: "2026-06-08T18:00:00", endCivil: "2026-06-08T18:30:00", civilDate: "2026-06-08",
		dataSource: `{}`, rawJSON: `{}`,
	})
	store, err := openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	stored, err := store.Store(1, "tcx", []byte("xxx"), "2026-06-08T17:35:00Z")
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	info, err := os.Stat(stored.AbsolutePath)
	if err != nil {
		t.Fatalf("stat sidecar: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode = %04o, want 0600", info.Mode().Perm())
	}
	parentInfo, err := os.Stat(filepath.Dir(stored.AbsolutePath))
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if parentInfo.Mode().Perm() != 0o700 {
		t.Fatalf("parent dir mode = %04o, want 0700", parentInfo.Mode().Perm())
	}
}

// TestAttachmentStoreResolveErrorsOnAmbiguousSHA pins the Copilot
// finding: the same sha256 archived as two kinds would have made
// Resolve return an arbitrary path. The current impl detects the
// ambiguity and surfaces a clear error so a caller never silently
// gets the wrong file.
func TestAttachmentStoreResolveErrorsOnAmbiguousSHA(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	// Two different exercise sessions, both happening to share the same
	// payload SHA but stored under different kinds. Real-world scenario:
	// the same payload was archived as a TCX once and as an ECG
	// blob another time — same content hash, different sidecar path.
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType: "exercise", resourceName: "users/me/dataTypes/exercise/dataPoints/a1",
		recordKind: "session", startUTC: "2026-06-08T17:00:00Z", endUTC: "2026-06-08T17:30:00Z",
		startCivil: "2026-06-08T18:00:00", endCivil: "2026-06-08T18:30:00", civilDate: "2026-06-08",
		dataSource: `{}`, rawJSON: `{}`,
	})
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType: "exercise", resourceName: "users/me/dataTypes/exercise/dataPoints/a2",
		recordKind: "session", startUTC: "2026-06-08T19:00:00Z", endUTC: "2026-06-08T19:30:00Z",
		startCivil: "2026-06-08T20:00:00", endCivil: "2026-06-08T20:30:00", civilDate: "2026-06-08",
		dataSource: `{}`, rawJSON: `{}`,
	})
	store, err := openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(`INSERT INTO data_point_attachments (data_point_id, kind, sha256, path_relative, byte_size, fetched_at) VALUES (1, 'tcx', ?, ?, 7, ?)`,
		"aaaabbbb00000000000000000000000000000000000000000000000000000000",
		"tcx/aa/aaaabbbb00000000000000000000000000000000000000000000000000000000.tcx",
		"2026-06-08T17:35:00Z"); err != nil {
		t.Fatalf("seed tcx row: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO data_point_attachments (data_point_id, kind, sha256, path_relative, byte_size, fetched_at) VALUES (2, 'ecg', ?, ?, 7, ?)`,
		"aaaabbbb00000000000000000000000000000000000000000000000000000000",
		"ecg/aa/aaaabbbb00000000000000000000000000000000000000000000000000000000.bin",
		"2026-06-08T19:35:00Z"); err != nil {
		t.Fatalf("seed ecg row: %v", err)
	}

	if _, err := store.Resolve("aaaabbbb00000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("Resolve returned no error for SHA appearing under two kinds; want ambiguity error")
	} else if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Resolve error = %v, want 'ambiguous' substring", err)
	}
}

// TestAttachmentStoreWalkReportsOrphansBothSides pins the slice B
// behaviour: Walk(fn) yields orphan sidecar files (file on disk, no
// row) AND orphan rows (row, no resolvable file). v1 doesn't prune;
// the seam exists so `doctor` (#108) can report archive integrity.
func TestAttachmentStoreWalkReportsOrphansBothSides(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType: "exercise", resourceName: "users/me/dataTypes/exercise/dataPoints/w",
		recordKind: "session", startUTC: "2026-06-08T17:00:00Z", endUTC: "2026-06-08T17:30:00Z",
		startCivil: "2026-06-08T18:00:00", endCivil: "2026-06-08T18:30:00", civilDate: "2026-06-08",
		dataSource: `{}`, rawJSON: `{}`,
	})
	store, err := openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Healthy attachment — should not appear in Walk's orphan list.
	healthy, err := store.Store(1, "tcx", []byte("healthy bytes"), "2026-06-08T17:35:00Z")
	if err != nil {
		t.Fatalf("Store healthy: %v", err)
	}

	// Orphan row: insert a row whose sidecar path is never written.
	if _, err := store.db.Exec(`INSERT INTO data_point_attachments (data_point_id, kind, sha256, path_relative, byte_size, fetched_at) VALUES (1, 'tcx', ?, ?, ?, ?)`,
		"deadbeef00000000000000000000000000000000000000000000000000000000",
		filepath.Join("tcx", "de", "deadbeef00000000000000000000000000000000000000000000000000000000.tcx"),
		7, "2026-06-08T17:36:00Z"); err != nil {
		t.Fatalf("seed orphan row: %v", err)
	}

	// Orphan file: write a sidecar bytes whose row is never inserted.
	orphanSubdir := filepath.Join(store.rootDir, "tcx", "f0")
	if err := os.MkdirAll(orphanSubdir, 0o700); err != nil {
		t.Fatalf("create orphan subdir: %v", err)
	}
	orphanPath := filepath.Join(orphanSubdir, "f00dface00000000000000000000000000000000000000000000000000000000.tcx")
	if err := os.WriteFile(orphanPath, []byte("orphan bytes"), 0o600); err != nil {
		t.Fatalf("write orphan file: %v", err)
	}

	store.Close()
	store, err = openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	var orphans []attachmentOrphan
	if err := store.Walk(func(o attachmentOrphan) error {
		orphans = append(orphans, o)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	gotRowOrphan := false
	gotFileOrphan := false
	for _, o := range orphans {
		if o.SHA256 == healthy.SHA256 {
			t.Errorf("Walk reported healthy attachment as orphan: %+v", o)
		}
		if o.Kind == attachmentOrphanRowMissingFile && o.SHA256 == "deadbeef00000000000000000000000000000000000000000000000000000000" {
			gotRowOrphan = true
		}
		if o.Kind == attachmentOrphanFileMissingRow && strings.HasSuffix(o.AbsolutePath, "f00dface00000000000000000000000000000000000000000000000000000000.tcx") {
			gotFileOrphan = true
		}
	}
	if !gotRowOrphan {
		t.Errorf("Walk did not report the row-without-file orphan; orphans=%+v", orphans)
	}
	if !gotFileOrphan {
		t.Errorf("Walk did not report the file-without-row orphan; orphans=%+v", orphans)
	}
}

// TestInitCreatesAttachmentRootDirOwnerOnly is the slice C tracer: a
// fresh `gohealthcli init` materialises the attachments root next to
// the SQLite file, with owner-only perms on POSIX. Sync paths can
// then assume the dir exists without lazy-creating it.
func TestInitCreatesAttachmentRootDirOwnerOnly(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	rootDir := attachmentRootDirForArchive(archivePath)
	info, err := os.Stat(rootDir)
	if err != nil {
		t.Fatalf("init did not create attachment root %q: %v", rootDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", rootDir)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o700 {
		t.Fatalf("attachment root mode = %04o, want 0700", info.Mode().Perm())
	}
}

// TestAttachmentStoreWalkRejectsTraversalPathRelative pins #248: a
// tampered/foreign archive can carry a data_point_attachments row whose
// path_relative escapes the attachment root ("../../../../etc/passwd").
// Walk must treat that row as an integrity violation (row-missing-file
// orphan) rather than statting outside rootDir — i.e. the reported
// AbsolutePath must never resolve outside the attachment root.
func TestAttachmentStoreWalkRejectsTraversalPathRelative(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType: "exercise", resourceName: "users/me/dataTypes/exercise/dataPoints/trav",
		recordKind: "session", startUTC: "2026-06-08T17:00:00Z", endUTC: "2026-06-08T17:30:00Z",
		startCivil: "2026-06-08T18:00:00", endCivil: "2026-06-08T18:30:00", civilDate: "2026-06-08",
		dataSource: `{}`, rawJSON: `{}`,
	})
	store, err := openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Seed a row whose path_relative climbs out of the attachment root.
	if _, err := store.db.Exec(`INSERT INTO data_point_attachments (data_point_id, kind, sha256, path_relative, byte_size, fetched_at) VALUES (1, 'tcx', ?, ?, 7, ?)`,
		"cafe000000000000000000000000000000000000000000000000000000000000",
		"../../../../etc/passwd",
		"2026-06-08T17:35:00Z"); err != nil {
		t.Fatalf("seed traversal row: %v", err)
	}
	// And a row whose path_relative is absolute.
	if _, err := store.db.Exec(`INSERT INTO data_point_attachments (data_point_id, kind, sha256, path_relative, byte_size, fetched_at) VALUES (1, 'tcx', ?, ?, 7, ?)`,
		"beef000000000000000000000000000000000000000000000000000000000000",
		"/etc/passwd",
		"2026-06-08T17:36:00Z"); err != nil {
		t.Fatalf("seed absolute row: %v", err)
	}

	store.Close()
	store, err = openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	var orphans []attachmentOrphan
	if err := store.Walk(func(o attachmentOrphan) error {
		orphans = append(orphans, o)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	gotTraversalOrphan := false
	gotAbsoluteOrphan := false
	for _, o := range orphans {
		switch o.SHA256 {
		case "cafe000000000000000000000000000000000000000000000000000000000000":
			gotTraversalOrphan = true
		case "beef000000000000000000000000000000000000000000000000000000000000":
			gotAbsoluteOrphan = true
		default:
			continue
		}
		if o.Kind != attachmentOrphanRowMissingFile {
			t.Errorf("escaping row %s reported as %q, want %q", o.SHA256, o.Kind, attachmentOrphanRowMissingFile)
		}
		// The reported absolute path must never escape the root.
		cleaned := filepath.Clean(o.AbsolutePath)
		if cleaned != store.rootDir && !strings.HasPrefix(cleaned, store.rootDir+string(filepath.Separator)) {
			t.Errorf("escaping row %s AbsolutePath %q escapes attachment root %q", o.SHA256, o.AbsolutePath, store.rootDir)
		}
	}
	if !gotTraversalOrphan {
		t.Errorf("Walk did not flag the traversal row as an orphan; orphans=%+v", orphans)
	}
	if !gotAbsoluteOrphan {
		t.Errorf("Walk did not flag the absolute-path row as an orphan; orphans=%+v", orphans)
	}
}

// TestAttachmentStoreResolveRejectsEscapingPathRelative pins #248 for
// the Resolve seam: a stored path_relative that is absolute or contains
// a "../" traversal must surface an error instead of handing back an
// arbitrary-file-read path outside the attachment root.
func TestAttachmentStoreResolveRejectsEscapingPathRelative(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)
	insertExportDataPoint(t, archivePath, exportDataPointFixture{
		dataType: "exercise", resourceName: "users/me/dataTypes/exercise/dataPoints/resolvetrav",
		recordKind: "session", startUTC: "2026-06-08T17:00:00Z", endUTC: "2026-06-08T17:30:00Z",
		startCivil: "2026-06-08T18:00:00", endCivil: "2026-06-08T18:30:00", civilDate: "2026-06-08",
		dataSource: `{}`, rawJSON: `{}`,
	})
	store, err := openAttachmentStore(archivePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	cases := []struct {
		name         string
		sha          string
		pathRelative string
	}{
		{"traversal", "cafe000000000000000000000000000000000000000000000000000000000000", "../../../../etc/passwd"},
		{"absolute", "beef000000000000000000000000000000000000000000000000000000000000", "/etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.db.Exec(`INSERT INTO data_point_attachments (data_point_id, kind, sha256, path_relative, byte_size, fetched_at) VALUES (1, 'tcx', ?, ?, 7, ?)`,
				tc.sha, tc.pathRelative, "2026-06-08T17:35:00Z"); err != nil {
				t.Fatalf("seed row: %v", err)
			}
			resolved, err := store.Resolve(tc.sha)
			if err == nil {
				t.Fatalf("Resolve(%s) = %q, want error for escaping path_relative", tc.name, resolved)
			}
		})
	}
}

func countAttachmentRows(t *testing.T, archivePath string) int {
	t.Helper()
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM data_point_attachments`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

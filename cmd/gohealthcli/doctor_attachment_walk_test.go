package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	store, err := openAttachmentStoreMode(archivePath, writeArchive)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Seed a minimal exercise Data Point so the FK on the orphan
	// attachment row is satisfied. Capture LastInsertId so the FK
	// stays valid if other fixtures begin inserting data_points.
	dpResult, err := store.db.ExecContext(context.Background(), `INSERT INTO data_points (provider_name, connection_id, data_type, upstream_resource_name, record_kind, start_time_utc, end_time_utc, data_source_json, raw_json, inserted_at, updated_at) VALUES ('googlehealth', 'googlehealth:111111256096816351', 'exercise', 'users/me/dataTypes/exercise/dataPoints/orphan-test', 'session', '2026-06-08T17:00:00Z', '2026-06-08T17:30:00Z', '{}', '{}', '2026-06-08T17:31:00Z', '2026-06-08T17:31:00Z')`)
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
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO data_point_attachments (data_point_id, kind, sha256, path_relative, byte_size, fetched_at) VALUES (?, 'tcx', ?, ?, 7, ?)`,
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

	if strings.Contains(stdout.String(), `"verification_failures"`) {
		t.Fatalf("orphan-only JSON gained verification failures: %s", stdout)
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
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
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

func TestDoctorReportsMissingAttachmentRootWithoutRecreatingIt(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	rootDir := attachmentRootDirForArchive(archivePath)
	movedRootDir := rootDir + ".moved"
	if err := os.Rename(rootDir, movedRootDir); err != nil {
		t.Fatalf("rename attachment root: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"doctor",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "setup_invalid")
	if message, ok := got["message"].(string); !ok || !strings.Contains(message, "attachment root") || !strings.Contains(message, "missing") {
		t.Fatalf("message = %T(%v), want missing attachment root", got["message"], got["message"])
	}
	if _, err := os.Stat(rootDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attachment root stat err = %v, want missing root", err)
	}
}

func TestDoctorAttachmentVerification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, reason string
		mutate       func(*testing.T, routeArchive, attachmentRecord)
	}{
		{"hash", "payload hash", func(t *testing.T, a routeArchive, r attachmentRecord) {
			b := append([]byte(nil), routeNewerTCX...)
			b[0] = '!'
			if err := os.WriteFile(a.sidecarPath(r), b, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"size", "byte_size", func(t *testing.T, a routeArchive, r attachmentRecord) {
			a.updateAttachment(t, r.ID, "byte_size", r.ByteSize+1)
		}},
		{"path", "canonical path", func(t *testing.T, a routeArchive, r attachmentRecord) {
			a.updateAttachment(t, r.ID, "path_relative", "tcx/./"+strings.TrimPrefix(r.PathRelative, "tcx/"))
		}},
		{"leaf-symlink", "symbolic link", func(t *testing.T, a routeArchive, r attachmentRecord) {
			p := a.sidecarPath(r)
			if err := os.Rename(p, p+".target"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+".target", p); err != nil {
				t.Skip(err)
			}
		}},
		{"dangling-symlink", "symbolic link", func(t *testing.T, a routeArchive, r attachmentRecord) {
			p := a.sidecarPath(r)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+".absent", p); err != nil {
				t.Skip(err)
			}
		}},
		{"parent-symlink", "symbolic link", func(t *testing.T, a routeArchive, r attachmentRecord) {
			p := filepath.Dir(a.sidecarPath(r))
			if err := os.Rename(p, p+".target"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+".target", p); err != nil {
				t.Skip(err)
			}
		}},
		{"directory", "regular file", func(t *testing.T, a routeArchive, r attachmentRecord) {
			p := a.sidecarPath(r)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newDoctorAttachmentArchive(t)
			id := a.insertDataPoint(t, "exercise", routeExerciseResource)
			r := a.storeTCX(t, id, routeNewerTCX, "2026-06-09T08:00:00Z")
			tc.mutate(t, a, r)
			before := snapshotRouteArchive(t, a.archivePath)
			for _, online := range []bool{false, true} {
				args := []string{"doctor", "--config", a.configPath, "--db", a.archivePath, "--json"}
				if online {
					args = append(args, "--online")
				}
				stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
				code := runWithRuntime(args, stdout, stderr, forbiddenRouteRuntime(t))
				var got struct {
					Status      string `json:"status"`
					Attachments struct {
						Failures []struct {
							ID          int64  `json:"attachment_id"`
							DataPointID int64  `json:"data_point_id"`
							SHA         string `json:"sha256"`
							Path        string `json:"path_relative"`
							Reason      string `json:"reason"`
						} `json:"verification_failures"`
						Rows []any `json:"orphan_rows"`
					} `json:"attachments"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if code != 1 || got.Status != "setup_invalid" || len(got.Attachments.Failures) != 1 {
					t.Fatalf("exit = %d stdout = %s stderr = %s, want one verification failure", code, stdout, stderr)
				}
				f := got.Attachments.Failures[0]
				if f.ID != r.ID || f.DataPointID != id || f.SHA != r.SHA256 || f.Path == "" || !strings.Contains(f.Reason, tc.reason) || len(got.Attachments.Rows) != 0 || stderr.Len() != 0 {
					t.Fatalf("invalid diagnostic: %s stderr=%s", stdout, stderr)
				}
				if after := snapshotRouteArchive(t, a.archivePath); !maps.Equal(before, after) {
					t.Fatal("doctor changed archive files")
				}
			}
		})
	}
}

func TestDoctorReportsAllAttachmentFailuresInEveryMode(t *testing.T) {
	t.Parallel()
	a := newDoctorAttachmentArchive(t)
	id := a.insertDataPoint(t, "exercise", routeExerciseResource)
	records := []attachmentRecord{a.storeTCX(t, id, routeNewerTCX, "2026-06-09T08:00:00Z"), a.storeTCX(t, id, routeOlderTCX, "2026-06-08T08:00:00Z")}
	for _, r := range records {
		a.updateAttachment(t, r.ID, "byte_size", r.ByteSize+1)
	}
	for _, mode := range []string{"--json", "--plain", ""} {
		args := []string{"doctor", "--config", a.configPath, "--db", a.archivePath}
		if mode != "" {
			args = append(args, mode)
		}
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		code := runWithRuntime(args, stdout, stderr, forbiddenRouteRuntime(t))
		if code != 1 || stderr.Len() != 0 {
			t.Fatalf("exit=%d stderr=%s", code, stderr)
		}
		for i, r := range records {
			if mode == "--plain" {
				prefix := fmt.Sprintf("attachments_verification_failures.%d.", i)
				for _, want := range []string{
					fmt.Sprintf("%sattachment_id: %d", prefix, r.ID),
					fmt.Sprintf("%sdata_point_id: %d", prefix, id),
					prefix + "sha256: " + r.SHA256,
					prefix + "path_relative: " + r.PathRelative,
					prefix + "reason: Data Point Attachment failed verification:",
				} {
					if !strings.Contains(stdout.String(), want) {
						t.Fatalf("missing %q in plain output: %s", want, stdout)
					}
				}
			}
			if mode == "" && !strings.Contains(stdout.String(), fmt.Sprintf("Attachment %d (Data Point %d, sha256 %s, path %q):", r.ID, id, r.SHA256, r.PathRelative)) {
				t.Fatalf("missing human diagnostic fields: %s", stdout)
			}
			if !strings.Contains(stdout.String(), fmt.Sprintf("attachment %d:", r.ID)) {
				t.Fatalf("missing attachment %d reason: %s", r.ID, stdout)
			}
		}
		if strings.Index(stdout.String(), fmt.Sprintf("attachment %d:", records[0].ID)) >= strings.Index(stdout.String(), fmt.Sprintf("attachment %d:", records[1].ID)) {
			t.Fatalf("verification findings out of order: %s", stdout)
		}
		if strings.Contains(stdout.String(), "gohealthcli init") || strings.Contains(stdout.String(), "gohealthcli doctor") || strings.Contains(stdout.String(), "gohealthcli sync") {
			t.Fatalf("unsupported remediation: %s", stdout)
		}
	}
}

func TestDoctorRejectsSymlinkRootWithNoAttachments(t *testing.T) {
	t.Parallel()
	a := newDoctorAttachmentArchive(t)
	root := attachmentRootDirForArchive(a.archivePath)
	if err := os.Rename(root, root+".target"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root+".target", root); err != nil {
		t.Skip(err)
	}
	before := snapshotRouteArchive(t, a.archivePath)
	for _, online := range []bool{false, true} {
		args := []string{"doctor", "--config", a.configPath, "--db", a.archivePath, "--json"}
		if online {
			args = append(args, "--online")
		}
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		code := runWithRuntime(args, stdout, stderr, forbiddenRouteRuntime(t))
		if code != 1 || !strings.Contains(stdout.String(), `"status": "setup_invalid"`) || (!strings.Contains(stdout.String(), "symbolic link") && !strings.Contains(stdout.String(), "reparse point")) {
			t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
		if after := snapshotRouteArchive(t, a.archivePath); !maps.Equal(before, after) {
			t.Fatal("doctor changed symlink root or target")
		}
	}
}

func newDoctorAttachmentArchive(t *testing.T) routeArchive {
	t.Helper()
	a := newRouteArchive(t)
	db := openArchiveForTest(t, a.archivePath)
	if _, err := db.ExecContext(context.Background(), `UPDATE connections SET token_metadata_json = '{"credential_store_key":"synthetic-doctor-key","expires_at":"2026-06-09T08:00:00Z","scopes":["https://www.googleapis.com/auth/fitness.activity.read"]}'`); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDoctorCleanAttachmentReportIsOmitted(t *testing.T) {
	t.Parallel()
	a := newDoctorAttachmentArchive(t)
	id := a.insertDataPoint(t, "exercise", routeExerciseResource)
	a.storeTCX(t, id, routeNewerTCX, "2026-06-09T08:00:00Z")
	before := snapshotRouteArchive(t, a.archivePath)
	code, stdout, stderr := runCommand(t, "doctor", "--config", a.configPath, "--db", a.archivePath, "--json")
	if code != 0 || stderr.Len() != 0 || strings.Contains(stdout.String(), `"attachments":`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if after := snapshotRouteArchive(t, a.archivePath); !maps.Equal(before, after) {
		t.Fatal("doctor changed healthy archive")
	}
}

func TestDoctorAttachmentFailureWriterErrors(t *testing.T) {
	t.Parallel()
	a := newDoctorAttachmentArchive(t)
	id := a.insertDataPoint(t, "exercise", routeExerciseResource)
	r := a.storeTCX(t, id, routeNewerTCX, "2026-06-09T08:00:00Z")
	a.updateAttachment(t, r.ID, "byte_size", r.ByteSize+1)
	for _, mode := range []string{"--json", "--plain", ""} {
		args := []string{"doctor", "--config", a.configPath, "--db", a.archivePath}
		if mode != "" {
			args = append(args, mode)
		}
		stderr := new(bytes.Buffer)
		code := runWithRuntime(args, failingWriter{}, stderr, forbiddenRouteRuntime(t))
		if code != 1 || strings.Count(stderr.String(), "write output") != 1 {
			t.Fatalf("exit=%d stderr=%s", code, stderr)
		}
	}
}

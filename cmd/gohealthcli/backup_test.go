package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	backupmodule "github.com/BramVR/gohealthcli/internal/backup"
)

func TestBackupInitAndStatusCLI(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runBackupTestGit(t, "", "init", "--bare", remote)
	configPath := filepath.Join(root, "private", "backup.json")
	repoPath := filepath.Join(root, "checkout")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")

	code, stdout, stderr := runCommand(t,
		"backup", "init",
		"--config", configPath,
		"--repo", repoPath,
		"--remote", remote,
		"--identity", identityPath,
		"--no-push",
		"--json",
	)
	if code != 0 {
		t.Fatalf("backup init exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	var initialized map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &initialized); err != nil {
		t.Fatalf("backup init JSON: %v\n%s", err, stdout.String())
	}
	if initialized["status"] != "backup_initialized" || initialized["repo_path"] != repoPath || initialized["changed"] != true || initialized["pushed"] != false {
		t.Fatalf("backup init result = %#v", initialized)
	}
	if recipient, _ := initialized["recipient"].(string); !strings.HasPrefix(recipient, "age1") {
		t.Fatalf("recipient = %#v, want public age recipient", initialized["recipient"])
	}

	code, stdout, stderr = runCommand(t, "backup", "status", "--config", configPath, "--plain")
	if code != 0 {
		t.Fatalf("backup status exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	wantLines := []string{
		"status: backup_empty",
		"repo_path: " + escapePlainControlChars(repoPath),
		"encrypted: false",
		"shard_count: 0",
	}
	for _, want := range wantLines {
		if !strings.Contains(stdout.String(), want+"\n") {
			t.Errorf("backup status stdout missing %q\n%s", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("backup status stderr = %q, want empty", stderr.String())
	}
}

func TestBackupPushCLIUsesCurrentHealthArchiveWithoutProviderContact(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	archiveSetup := filepath.Join(root, "archive-setup")
	if err := os.Mkdir(archiveSetup, 0o700); err != nil {
		t.Fatal(err)
	}
	_, archivePath, _ := initializeFileCredentialSetup(t, archiveSetup)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	storeSnapshotAttachment(t, archivePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase><Courses/></TrainingCenterDatabase>`))
	remote := filepath.Join(root, "remote.git")
	runBackupTestGit(t, "", "init", "--bare", remote)
	backupConfig := filepath.Join(root, "private", "backup.json")
	repoPath := filepath.Join(root, "checkout")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")
	code, stdout, stderr := runCommand(t,
		"backup", "init",
		"--config", backupConfig,
		"--repo", repoPath,
		"--remote", remote,
		"--identity", identityPath,
		"--no-push",
		"--plain",
	)
	if code != 0 {
		t.Fatalf("backup init exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	providerCalls := 0
	runtime := runtimeAdapters{
		now: func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
		httpDoer: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			providerCalls++
			return nil, errors.New("unexpected Provider request")
		})},
	}
	var plainOut, plainErr bytes.Buffer
	code = runWithRuntime([]string{
		"backup", "--db", archivePath, "push",
		"--config", backupConfig,
		"--no-push",
		"--plain",
	}, &plainOut, &plainErr, runtime)
	if code != 0 {
		t.Fatalf("backup push exit = %d\nstdout: %s\nstderr: %s", code, plainOut.String(), plainErr.String())
	}
	for _, want := range []string{
		"status: backup_pushed",
		"repo_path: " + escapePlainControlChars(repoPath),
		"changed: true",
		"pushed: false",
		"encrypted: true",
		"shard_count: 9",
		"health_archive.connections: 1",
		"health_archive.data_points: 3",
		"health_archive.data_point_revisions: 1",
		"health_archive.data_point_attachments: 1",
		"health_archive.attachment_payloads: 1",
	} {
		if !strings.Contains(plainOut.String(), want+"\n") {
			t.Errorf("backup push plain output missing %q\n%s", want, plainOut.String())
		}
	}
	if plainErr.Len() != 0 {
		t.Fatalf("backup push stderr = %q", plainErr.String())
	}
	if providerCalls != 0 {
		t.Fatalf("backup push made %d Provider requests, want zero", providerCalls)
	}
	manifestData, err := os.ReadFile(filepath.Join(repoPath, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Shards []struct {
			Path string `json:"path"`
		} `json:"shards"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	plaintextMarkers := [][]byte{
		[]byte(`"count":"512"`),
		[]byte("pixel-watch-2"),
		[]byte("TrainingCenterDatabase"),
	}
	for _, shard := range manifest.Shards {
		ciphertext, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(shard.Path)))
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range plaintextMarkers {
			if bytes.Contains(ciphertext, marker) {
				t.Fatalf("encrypted shard %q contains private snapshot marker", shard.Path)
			}
		}
	}

	var jsonOut, jsonErr bytes.Buffer
	code = runWithRuntime([]string{
		"backup", "push",
		"--config", backupConfig,
		"--db", archivePath,
		"--no-push",
		"--json",
	}, &jsonOut, &jsonErr, runtime)
	if code != 0 {
		t.Fatalf("unchanged backup push exit = %d\nstdout: %s\nstderr: %s", code, jsonOut.String(), jsonErr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &result); err != nil {
		t.Fatalf("backup push JSON: %v\n%s", err, jsonOut.String())
	}
	if result["status"] != "backup_pushed" || result["changed"] != false || result["pushed"] != false || result["encrypted"] != true || result["shard_count"] != float64(9) {
		t.Fatalf("unchanged backup push result = %#v", result)
	}
	if providerCalls != 0 {
		t.Fatalf("unchanged backup push made %d Provider requests, want zero", providerCalls)
	}
	var humanOut, humanErr bytes.Buffer
	code = runWithRuntime([]string{
		"backup", "push",
		"--config", backupConfig,
		"--db", archivePath,
		"--no-push",
	}, &humanOut, &humanErr, runtime)
	if code != 0 {
		t.Fatalf("human backup push exit = %d\nstdout: %s\nstderr: %s", code, humanOut.String(), humanErr.String())
	}
	for _, want := range []string{
		"status: backup_pushed",
		"changed: false",
		"encrypted: true",
		"message: Health Archive Snapshot unchanged; backup checkout clean",
	} {
		if !strings.Contains(humanOut.String(), want+"\n") {
			t.Errorf("backup push human output missing %q\n%s", want, humanOut.String())
		}
	}
	if humanErr.Len() != 0 {
		t.Fatalf("human backup push stderr = %q", humanErr.String())
	}
}

func TestBackupPushThenPullRestoresSeparateHealthArchive(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := ensureOwnerOnlyDir(sourceDir); err != nil {
		t.Fatal(err)
	}
	_, sourcePath, _ := initializeFileCredentialSetup(t, sourceDir)
	insertHealthArchiveSnapshotFixture(t, sourcePath)
	payload := []byte(`<?xml version="1.0"?><TrainingCenterDatabase><Courses><Course><Name>synthetic recovery route</Name></Course></Courses></TrainingCenterDatabase>`)
	stored := storeSnapshotAttachment(t, sourcePath, payload)

	remote := filepath.Join(root, "remote.git")
	runBackupTestGit(t, "", "init", "--bare", remote)
	backupConfig := filepath.Join(root, "private", "backup.json")
	repoPath := filepath.Join(root, "checkout")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")
	code, stdout, stderr := runCommand(t,
		"backup", "init",
		"--config", backupConfig,
		"--repo", repoPath,
		"--remote", remote,
		"--identity", identityPath,
		"--plain",
	)
	if code != 0 {
		t.Fatalf("backup init exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	code, stdout, stderr = runCommand(t,
		"backup", "push",
		"--config", backupConfig,
		"--db", sourcePath,
		"--plain",
	)
	if code != 0 {
		t.Fatalf("backup push exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	restoreDir := filepath.Join(root, "restore")
	if err := ensureOwnerOnlyDir(restoreDir); err != nil {
		t.Fatal(err)
	}
	restorePath := filepath.Join(restoreDir, "restored.sqlite")
	code, stdout, stderr = runCommand(t,
		"backup", "pull",
		"--config", backupConfig,
		"--db", restorePath,
		"--plain",
	)
	if code != 0 {
		t.Fatalf("backup pull exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"status: backup_pulled",
		"repo_path: " + escapePlainControlChars(repoPath),
		"archive_path: " + escapePlainControlChars(restorePath),
		"changed: true",
		"encrypted: true",
		"shard_count: 9",
		"health_archive.connections: 1",
		"health_archive.data_points: 3",
		"health_archive.data_point_revisions: 1",
		"health_archive.data_point_attachments: 1",
		"health_archive.attachment_payloads: 1",
	} {
		if !strings.Contains(stdout.String(), want+"\n") {
			t.Errorf("backup pull plain output missing %q\n%s", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("backup pull stderr = %q", stderr.String())
	}

	sourceStatus := snapshotStatus(t, sourcePath)
	restoredStatus := snapshotStatus(t, restorePath)
	sourceStatus.ArchivePath = ""
	restoredStatus.ArchivePath = ""
	if !reflect.DeepEqual(restoredStatus, sourceStatus) {
		t.Fatalf("restored status mismatch\n got: %+v\nwant: %+v", restoredStatus, sourceStatus)
	}
	if got, want := snapshotExportRows(t, restorePath, "daily-steps"), snapshotExportRows(t, sourcePath, "daily-steps"); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored daily-steps export mismatch\n got: %+v\nwant: %+v", got, want)
	}
	query := `SELECT data_point_id, previous_raw_json, replacement_reason FROM data_point_revisions ORDER BY id`
	if got, want := snapshotQueryRows(t, restorePath, query), snapshotQueryRows(t, sourcePath, query); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored revision query mismatch\n got: %+v\nwant: %+v", got, want)
	}
	restoredAttachment := filepath.Join(attachmentRootDirForArchive(restorePath), filepath.FromSlash(stored.PathRelative))
	gotPayload, err := os.ReadFile(restoredAttachment)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatal("restored Data Point Attachment payload mismatch")
	}
	attachmentReport, err := collectAttachmentOrphans(context.Background(), restorePath)
	if err != nil {
		t.Fatal(err)
	}
	if attachmentReport != nil {
		t.Fatalf("restored Data Point Attachment orphans = %+v", attachmentReport)
	}

	jsonRestoreDir := filepath.Join(root, "json-restore")
	if err := ensureOwnerOnlyDir(jsonRestoreDir); err != nil {
		t.Fatal(err)
	}
	jsonRestorePath := filepath.Join(jsonRestoreDir, "restored.sqlite")
	code, stdout, stderr = runCommand(t,
		"backup", "pull",
		"--config", backupConfig,
		"--db", jsonRestorePath,
		"--json",
	)
	if code != 0 {
		t.Fatalf("JSON backup pull exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	var jsonResult struct {
		Status       string              `json:"status"`
		RepoPath     string              `json:"repo_path"`
		ArchivePath  string              `json:"archive_path"`
		Changed      bool                `json:"changed"`
		Encrypted    bool                `json:"encrypted"`
		ShardCount   int                 `json:"shard_count"`
		ArchiveCount backupmodule.Counts `json:"health_archive_counts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &jsonResult); err != nil {
		t.Fatal(err)
	}
	if jsonResult.Status != "backup_pulled" || jsonResult.RepoPath != repoPath || jsonResult.ArchivePath != jsonRestorePath || !jsonResult.Changed || !jsonResult.Encrypted || jsonResult.ShardCount != 9 || jsonResult.ArchiveCount.DataPoints != 3 || jsonResult.ArchiveCount.DataPointAttachments != 1 {
		t.Fatalf("JSON backup pull result = %+v", jsonResult)
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON backup pull stderr = %q", stderr.String())
	}
}

func TestPulledSnapshotInvalidJSONLFailsBeforeTargetMutation(t *testing.T) {
	input := snapshotPullInput(t)
	for index := range input.Shards {
		if input.Shards[index].Table == "connections" {
			input.Shards[index].JSONL = []byte("{invalid-json}\n")
		}
	}
	target := filepath.Join(t.TempDir(), "restored.sqlite")
	err := restorePulledHealthArchiveSnapshot(context.Background(), input, target)
	if err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("restorePulledHealthArchiveSnapshot error = %v, want invalid JSONL", err)
	}
	assertPullTargetAbsent(t, target)
}

func TestPulledSnapshotBrokenReferenceFailsBeforeTargetMutation(t *testing.T) {
	input := snapshotPullInput(t)
	for index := range input.Shards {
		if input.Shards[index].Table != "data_point_revisions" {
			continue
		}
		var revision map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(input.Shards[index].JSONL), &revision); err != nil {
			t.Fatal(err)
		}
		revision["data_point_id"] = 999999
		data, err := json.Marshal(revision)
		if err != nil {
			t.Fatal(err)
		}
		input.Shards[index].JSONL = append(data, '\n')
	}
	target := filepath.Join(t.TempDir(), "restored.sqlite")
	err := restorePulledHealthArchiveSnapshot(context.Background(), input, target)
	if err == nil || !strings.Contains(err.Error(), "Data Point Revision") || !strings.Contains(err.Error(), "unknown Data Point") {
		t.Fatalf("restorePulledHealthArchiveSnapshot error = %v, want broken reference", err)
	}
	assertPullTargetAbsent(t, target)
}

func TestPulledSnapshotRefusesExistingHealthArchive(t *testing.T) {
	input := snapshotPullInput(t)
	target := filepath.Join(t.TempDir(), "existing.sqlite")
	if err := os.Chmod(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := []byte("existing archive marker")
	if err := os.WriteFile(target, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	err := restorePulledHealthArchiveSnapshot(context.Background(), input, target)
	if err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("restorePulledHealthArchiveSnapshot error = %v, want existing-target refusal", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, marker) {
		t.Fatalf("existing target changed to %q", got)
	}
	if _, err := os.Lstat(attachmentRootDirForArchive(target)); !os.IsNotExist(err) {
		t.Fatalf("existing-target refusal attachment root stat error = %v, want absent", err)
	}
}

func TestBackupStatusMissingConfigIsExplicitlyUninitialized(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	code, stdout, stderr := runCommand(t, "backup", "status", "--json")
	if code != 0 {
		t.Fatalf("backup status exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "backup_uninitialized"`) {
		t.Fatalf("backup status JSON = %s, want uninitialized", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, "xdg", "gohealthcli")); !os.IsNotExist(err) {
		t.Fatalf("backup status created config state: %v", err)
	}
}

func TestBackupRejectsUnknownAction(t *testing.T) {
	const action = "definitely-not-an-action"
	code, _, stderr := runCommand(t, "backup", action)
	if code != 1 || !strings.Contains(stderr.String(), "unknown backup action") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}

	code, _, stderr = runCommand(t, "backup", action, "--db", "synthetic.db")
	if code == 0 || !strings.Contains(stderr.String(), "unknown backup action") {
		t.Fatalf("unknown action with archive flag exit = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "backup "+action) {
		t.Fatalf("unsupported global error included unvalidated action: %q", stderr.String())
	}
}

func TestBackupAcceptsFlagsOnBothSidesOfAction(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCommand(t,
		"backup", "--json", "status",
		"--config", filepath.Join(root, "missing.json"),
		"--repo", repo,
	)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "backup_empty"`) {
		t.Fatalf("stdout = %s, want backup_empty", stdout.String())
	}
}

func TestCompletionProtocolSuggestsBackupActions(t *testing.T) {
	code, stdout, stderr := runCommand(t, "__complete", "backup", "")
	if code != 0 {
		t.Fatalf("completion exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	for _, action := range []string{"init", "push", "pull", "status"} {
		if !strings.Contains(stdout.String(), action+"\n") {
			t.Errorf("completion missing %q: %s", action, stdout.String())
		}
	}
}

func snapshotPullInput(t *testing.T) backupmodule.PullInput {
	t.Helper()
	root := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, root)
	insertHealthArchiveSnapshotFixture(t, archivePath)
	storeSnapshotAttachment(t, archivePath, []byte(`<?xml version="1.0"?><TrainingCenterDatabase/>`))
	snapshot, err := ExportHealthArchiveSnapshot(context.Background(), archivePath)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeHealthArchiveSnapshotJSONL(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	input := backupmodule.PullInput{
		SchemaVersion: snapshot.SchemaVersion,
		Counts:        healthArchiveSnapshotCounts(snapshot),
		Shards:        make([]backupmodule.PlaintextShard, 0, len(encoded)),
	}
	for _, shard := range encoded {
		input.Shards = append(input.Shards, backupmodule.PlaintextShard{
			Table: shard.Table,
			Path:  shard.Path,
			Rows:  shard.Rows,
			JSONL: shard.JSONL,
		})
	}
	return input
}

func assertPullTargetAbsent(t *testing.T, archivePath string) {
	t.Helper()
	for _, target := range []string{archivePath, attachmentRootDirForArchive(archivePath)} {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("failed backup pull target %s stat error = %v, want absent", target, err)
		}
	}
}

func runBackupTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	if code == 0 || !strings.Contains(stderr.String(), "not supported by backup") {
		t.Fatalf("unsupported global exit = %d, stderr = %q", code, stderr.String())
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
	for _, action := range []string{"init", "status"} {
		if !strings.Contains(stdout.String(), action+"\n") {
			t.Errorf("completion missing %q: %s", action, stdout.String())
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

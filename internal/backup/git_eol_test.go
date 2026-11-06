package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Git for Windows ships core.autocrlf=true. A small age ciphertext can pass
// Git's text heuristic, so checkout must never rewrite its line endings.
func TestBackupGitCheckoutIgnoresGlobalAutocrlf(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\tautocrlf = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "checkout")
	if err := runGit(ctx, "", "init", "-q", repo); err != nil {
		t.Fatal(err)
	}
	want := "age-encryption.org/v1\n-> X25519 header\n--- mac\npayload\n"
	shardPath := filepath.Join(repo, "data", "connections.jsonl.gz.age")
	if err := os.MkdirAll(filepath.Dir(shardPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shardPath, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	blob, err := gitOutput(ctx, repo, "hash-object", "-w", "--no-filters", "--", "data/connections.jsonl.gz.age")
	if err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, repo, "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(blob)+",data/connections.jsonl.gz.age"); err != nil {
		t.Fatal(err)
	}
	tree, err := gitOutput(ctx, repo, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(shardPath); err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, repo, "read-tree", "--reset", "-u", strings.TrimSpace(tree)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(shardPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("checked-out shard = %q, want unchanged %q", got, want)
	}
}

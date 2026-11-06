package backup

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Tests that lower the shard limits must not call t.Parallel: Go finishes
// every sequential top-level test before any paused parallel test resumes, so
// the overridden package variables are never read concurrently.
func lowerShardLimits(t *testing.T, target int, maxBlob int64) {
	t.Helper()
	previousTarget, previousMax := shardPartTargetBytes, maxBackupBlobBytes
	shardPartTargetBytes, maxBackupBlobBytes = target, maxBlob
	t.Cleanup(func() { shardPartTargetBytes, maxBackupBlobBytes = previousTarget, previousMax })
}

func incompressibleDataPoints(rows int, seed int64) []byte {
	random := rand.New(rand.NewSource(seed))
	payload := make([]byte, 80)
	var jsonl bytes.Buffer
	for row := range rows {
		random.Read(payload)
		fmt.Fprintf(&jsonl, "{\"id\":%d,\"value\":\"%x\"}\n", row, payload)
	}
	return jsonl.Bytes()
}

func inputWithDataPoints(jsonl []byte) PushInput {
	input := pullTestInput("first")
	rows := bytes.Count(jsonl, []byte{'\n'})
	for index := range input.Shards {
		if input.Shards[index].Table == "data_points" {
			input.Shards[index].Rows = rows
			input.Shards[index].JSONL = jsonl
		}
	}
	input.Counts.DataPoints = rows
	return input
}

func trackedDataPaths(t *testing.T, repo, commit string) []string {
	t.Helper()
	return strings.Fields(gitCommand(t, repo, "ls-tree", "-r", "--name-only", commit, "--", "data"))
}

func pulledDataPoints(t *testing.T, configPath string) PlaintextShard {
	t.Helper()
	var dataPoints PlaintextShard
	if _, err := PullCurrent(context.Background(), Options{ConfigPath: configPath}, func(input PullInput) error {
		if len(input.Shards) != len(knownGeneratedShardTables) {
			t.Fatalf("pulled %d logical shards, want %d", len(input.Shards), len(knownGeneratedShardTables))
		}
		for _, shard := range input.Shards {
			if shard.Table == "data_points" {
				dataPoints = shard
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("PullCurrent: %v", err)
	}
	return dataPoints
}

func TestPushSplitsLargeTableIntoPartsAndPullRebuildsIt(t *testing.T) {
	lowerShardLimits(t, 4096, 16384)
	small := inputWithDataPoints(incompressibleDataPoints(10, 1))
	_, configPath, repo, remote := setupPullTestBackup(t, small)
	if got := trackedDataPaths(t, repo, "HEAD"); !slices.Contains(got, "data/data_points.jsonl.gz.age") {
		t.Fatalf("small table paths = %v, want single-file data_points shard", got)
	}

	large := incompressibleDataPoints(600, 2)
	result, err := Push(context.Background(), Options{ConfigPath: configPath, Push: true}, inputWithDataPoints(large))
	if err != nil {
		t.Fatalf("Push large table: %v", err)
	}
	if !result.Changed || !result.Pushed {
		t.Fatalf("Push result = %+v, want changed and pushed", result)
	}
	manifest, _, err := readManifestIfPresent(repo)
	if err != nil {
		t.Fatal(err)
	}
	var partPaths []string
	partRows := 0
	for _, shard := range manifest.Shards {
		if shard.Bytes >= maxBackupBlobBytes {
			t.Fatalf("shard %s is %d bytes, limit %d", shard.Path, shard.Bytes, maxBackupBlobBytes)
		}
		if shard.Table == "data_points" {
			partPaths = append(partPaths, shard.Path)
			partRows += shard.Rows
		} else if shard.Path != singleShardPath(shard.Table) {
			t.Fatalf("small table %s path = %s, want single-file layout", shard.Table, shard.Path)
		}
	}
	if len(partPaths) < 3 {
		t.Fatalf("data_points parts = %v, want at least three", partPaths)
	}
	for index, partPath := range partPaths {
		if want := fmt.Sprintf("data/data_points.part-%04d.jsonl.gz.age", index+1); partPath != want {
			t.Fatalf("part %d path = %s, want %s", index+1, partPath, want)
		}
	}
	if partRows != 600 {
		t.Fatalf("data_points part rows = %d, want 600", partRows)
	}
	remoteTree := trackedDataPaths(t, repo, strings.TrimSpace(gitCommand(t, "", "--git-dir", remote, "rev-parse", "HEAD")))
	if slices.Contains(remoteTree, "data/data_points.jsonl.gz.age") {
		t.Fatalf("remote tree still has stale single-file shard: %v", remoteTree)
	}

	pulled := pulledDataPoints(t, configPath)
	if pulled.Rows != 600 || pulled.Path != "data/data_points.jsonl.gz.age" || !bytes.Equal(pulled.JSONL, large) {
		t.Fatalf("pulled data_points rows=%d path=%s equal=%t, want 600 rows rebuilt in order", pulled.Rows, pulled.Path, bytes.Equal(pulled.JSONL, large))
	}

	headBefore := gitCommand(t, repo, "rev-parse", "HEAD")
	unchanged, err := Push(context.Background(), Options{ConfigPath: configPath, Push: true}, inputWithDataPoints(large))
	if err != nil || unchanged.Changed {
		t.Fatalf("unchanged Push = %+v, %v; want reused parts and no commit", unchanged, err)
	}
	if gitCommand(t, repo, "rev-parse", "HEAD") != headBefore {
		t.Fatal("unchanged Push created a commit")
	}

	if _, err := Push(context.Background(), Options{ConfigPath: configPath, Push: true}, small); err != nil {
		t.Fatalf("Push shrunk table: %v", err)
	}
	if got := trackedDataPaths(t, repo, "HEAD"); slices.Contains(got, partPaths[0]) || !slices.Contains(got, "data/data_points.jsonl.gz.age") {
		t.Fatalf("shrunk table paths = %v, want stale parts removed and single file restored", got)
	}
}

func TestPushRebuildsOnOriginWhenUnpushedSnapshotHoldsOversizeBlob(t *testing.T) {
	lowerShardLimits(t, 1<<30, 1<<30)
	_, configPath, repo, remote := setupPullTestBackup(t, pullTestInput("first"))
	originHead := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	large := inputWithDataPoints(incompressibleDataPoints(600, 3))
	if _, err := Push(context.Background(), Options{ConfigPath: configPath, Push: false}, large); err != nil {
		t.Fatalf("Push single-file snapshot without pushing: %v", err)
	}
	oversizeHead := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))

	lowerShardLimits(t, 4096, 16384)
	result, err := Push(context.Background(), Options{ConfigPath: configPath, Push: true}, large)
	if err != nil {
		t.Fatalf("Push after oversize local snapshot: %v", err)
	}
	if !result.Changed || !result.Pushed {
		t.Fatalf("Push result = %+v, want changed and pushed", result)
	}
	head := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	if parent := strings.TrimSpace(gitCommand(t, repo, "rev-parse", head+"^")); parent != originHead {
		t.Fatalf("new snapshot parent = %s, want origin commit %s", parent, originHead)
	}
	if remoteHead := strings.TrimSpace(gitCommand(t, "", "--git-dir", remote, "rev-parse", "HEAD")); remoteHead != head {
		t.Fatalf("remote HEAD = %s, want %s", remoteHead, head)
	}
	if strings.Contains(gitCommand(t, "", "--git-dir", remote, "rev-list", "--all"), oversizeHead) {
		t.Fatal("remote received the superseded oversize snapshot commit")
	}
	omitted := gitCommand(t, "", "--git-dir", remote, "rev-list", "--objects", "--all", "--filter=blob:limit="+strconv.FormatInt(maxBackupBlobBytes, 10), "--filter-print-omitted")
	if strings.Contains(omitted, "~") {
		t.Fatalf("remote holds blobs of %d bytes or more:\n%s", maxBackupBlobBytes, omitted)
	}
	cfg, _, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PendingCommits) != 0 {
		t.Fatalf("pending commits = %v, want none after push", cfg.PendingCommits)
	}
}

func TestPushRejectsShardCiphertextAtBlobLimit(t *testing.T) {
	lowerShardLimits(t, 1<<30, 1<<30)
	_, configPath, repo, _ := setupPullTestBackup(t, pullTestInput("first"))
	headBefore := gitCommand(t, repo, "rev-parse", "HEAD")
	lowerShardLimits(t, 1<<30, 16384)
	_, err := Push(context.Background(), Options{ConfigPath: configPath, Push: true}, inputWithDataPoints(incompressibleDataPoints(600, 4)))
	if err == nil || !strings.Contains(err.Error(), "data/data_points.jsonl.gz.age") || !strings.Contains(err.Error(), "reject files of 16384 bytes or more") {
		t.Fatalf("Push error = %v, want blob limit error naming the shard", err)
	}
	if gitCommand(t, repo, "rev-parse", "HEAD") != headBefore {
		t.Fatal("rejected Push moved HEAD")
	}
	if status := gitCommand(t, repo, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("rejected Push left checkout changes:\n%s", status)
	}
}

func TestPushExactCommitRefusesOversizeOutgoingBlob(t *testing.T) {
	lowerShardLimits(t, 4096, 4096)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	repo := filepath.Join(root, "work")
	gitCommand(t, "", "init", "--bare", remote)
	gitCommand(t, "", "init", "-b", "main", repo)
	gitCommand(t, repo, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(repo, "big"), bytes.Repeat([]byte{'x'}, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "add", "big")
	gitCommand(t, repo, "commit", "-m", "big")
	head := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	err := pushExactCommit(context.Background(), repo, head, "main")
	if err == nil || !strings.Contains(err.Error(), "4096 bytes or more") {
		t.Fatalf("pushExactCommit error = %v, want blob limit refusal", err)
	}
	if refs := gitCommand(t, "", "--git-dir", remote, "for-each-ref"); strings.TrimSpace(refs) != "" {
		t.Fatalf("remote refs = %q, want nothing pushed", refs)
	}
}

func TestShardLayoutRowsRejectsNonCanonicalParts(t *testing.T) {
	t.Parallel()
	entry := func(path string) ShardEntry { return ShardEntry{Table: "data_points", Path: path, Rows: 1} }
	cases := map[string][]ShardEntry{
		"lone part":         {entry("data/data_points.part-0001.jsonl.gz.age")},
		"gap":               {entry("data/data_points.part-0001.jsonl.gz.age"), entry("data/data_points.part-0003.jsonl.gz.age")},
		"starts after one":  {entry("data/data_points.part-0002.jsonl.gz.age"), entry("data/data_points.part-0003.jsonl.gz.age")},
		"single plus part":  {entry("data/data_points.jsonl.gz.age"), entry("data/data_points.part-0001.jsonl.gz.age")},
		"part zero":         {entry("data/data_points.part-0000.jsonl.gz.age"), entry("data/data_points.part-0001.jsonl.gz.age")},
		"short part number": {entry("data/data_points.part-1.jsonl.gz.age"), entry("data/data_points.part-2.jsonl.gz.age")},
		"wrong table":       {{Table: "rollups", Path: "data/data_points.jsonl.gz.age"}},
	}
	for name, shards := range cases {
		if _, err := shardLayoutRows(Manifest{Shards: shards}); err == nil {
			t.Errorf("%s: shardLayoutRows accepted %v", name, shards)
		}
	}
	rows, err := shardLayoutRows(Manifest{Shards: []ShardEntry{
		entry("data/data_points.part-0001.jsonl.gz.age"),
		entry("data/data_points.part-0002.jsonl.gz.age"),
		{Table: "rollups", Path: "data/rollups.jsonl.gz.age", Rows: 4},
	}})
	if err != nil || rows["data_points"] != 2 || rows["rollups"] != 4 {
		t.Fatalf("shardLayoutRows = %v, %v; want data_points 2 and rollups 4", rows, err)
	}
}

// Run with GOHEALTHCLI_BACKUP_REALISTIC=1. It pushes a data_points table whose
// single-file ciphertext would exceed the production blob limit through the
// production thresholds to a local bare remote, then restores it.
func TestRealisticSizePushSplitsUnderBlobLimit(t *testing.T) {
	if os.Getenv("GOHEALTHCLI_BACKUP_REALISTIC") != "1" {
		t.Skip("set GOHEALTHCLI_BACKUP_REALISTIC=1 to push about 120 MB of ciphertext")
	}
	large := incompressibleDataPoints(1_300_000, 5)
	_, configPath, repo, remote := setupPullTestBackup(t, inputWithDataPoints(large))
	manifest, _, err := readManifestIfPresent(repo)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, shard := range manifest.Shards {
		if shard.Table != "data_points" {
			continue
		}
		total += shard.Bytes
		t.Logf("%s rows=%d bytes=%d", shard.Path, shard.Rows, shard.Bytes)
		if shard.Bytes >= maxBackupBlobBytes || shard.Bytes > int64(shardPartTargetBytes)+1<<20 {
			t.Fatalf("part %s is %d bytes, want near target %d and under limit %d", shard.Path, shard.Bytes, shardPartTargetBytes, maxBackupBlobBytes)
		}
	}
	if total < maxBackupBlobBytes {
		t.Fatalf("data_points ciphertext totals %d bytes, want more than one blob limit %d", total, maxBackupBlobBytes)
	}
	t.Logf("plaintext=%d ciphertext total=%d", len(large), total)
	if local, pushed := gitCommand(t, repo, "rev-parse", "HEAD"), gitCommand(t, "", "--git-dir", remote, "rev-parse", "HEAD"); local != pushed {
		t.Fatalf("remote HEAD = %s, want %s", pushed, local)
	}
	if pulled := pulledDataPoints(t, configPath); !bytes.Equal(pulled.JSONL, large) {
		t.Fatal("pulled data_points JSONL differs from pushed rows")
	}
}

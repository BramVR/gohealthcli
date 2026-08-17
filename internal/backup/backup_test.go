package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func TestConfigRoundTripUsesOwnerOnlyFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "config", "backup.json")
	want := Config{
		Repo:       filepath.Join(root, "checkout"),
		Remote:     filepath.Join(root, "remote.git"),
		Identity:   filepath.Join(root, "config", "backup-age-identity.txt"),
		Recipients: []string{"age1example"},
	}
	if err := SaveConfig(path, want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, found, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadConfig = (%+v, %t), want (%+v, true)", got, found, want)
	}
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
	want.Remote = "https://example.invalid/updated.git"
	if err := SaveConfig(path, want); err != nil {
		t.Fatalf("replace SaveConfig: %v", err)
	}
	got, found, err = LoadConfig(path)
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("replaced LoadConfig = (%+v, %t, %v), want (%+v, true, nil)", got, found, err, want)
	}
}

func TestEnsureIdentityCreatesAndReusesX25519Identity(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private", "backup-age-identity.txt")
	first, err := EnsureIdentity(path)
	if err != nil {
		t.Fatalf("first EnsureIdentity: %v", err)
	}
	if !strings.HasPrefix(first, "age1") {
		t.Fatalf("recipient = %q, want age1 prefix", first)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	second, err := EnsureIdentity(path)
	if err != nil {
		t.Fatalf("second EnsureIdentity: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reused identity: %v", err)
	}
	if second != first || !reflect.DeepEqual(after, before) {
		t.Fatal("EnsureIdentity did not reuse the existing identity")
	}
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
}

func TestPrivateFilesRejectNonRegularPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		run  func(string) error
	}{
		{name: "load config", run: func(path string) error { _, _, err := LoadConfig(path); return err }},
		{name: "save config", run: func(path string) error { return SaveConfig(path, Config{}) }},
		{name: "identity", run: func(path string) error { _, err := EnsureIdentity(path); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-"))
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := test.run(path); err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Fatalf("error = %v, want regular-file rejection", err)
			}
		})
	}
}

func TestPrivateFilesRejectInsecureExistingParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode check")
	}
	root := t.TempDir()
	privateDir := filepath.Join(root, "private")
	configPath := filepath.Join(privateDir, "backup.json")
	identityPath := filepath.Join(privateDir, "backup-age-identity.txt")
	if err := SaveConfig(configPath, Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureIdentity(identityPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("LoadConfig error = %v, want insecure-parent rejection", err)
	}
	if _, err := EnsureIdentity(identityPath); err == nil || !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("EnsureIdentity error = %v, want insecure-parent rejection", err)
	}
}

func TestPrivateFilesRejectSymlinkedParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "private")
	if err := os.Symlink(outside, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	configPath := filepath.Join(alias, "backup.json")
	if err := SaveConfig(configPath, Config{}); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("SaveConfig error = %v, want symlinked-parent rejection", err)
	}
	identityPath := filepath.Join(alias, "backup-age-identity.txt")
	if _, err := EnsureIdentity(identityPath); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("EnsureIdentity error = %v, want symlinked-parent rejection", err)
	}
	for _, path := range []string{filepath.Join(outside, "backup.json"), filepath.Join(outside, "backup-age-identity.txt")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("outside private file was written: %v", err)
		}
	}
	checkout := filepath.Join(root, "checkout")
	_, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "safe", "backup.json"),
		Repo:       checkout,
		Identity:   identityPath,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("Init error = %v, want identity parent rejection", err)
	}
	if _, err := os.Lstat(checkout); !os.IsNotExist(err) {
		t.Fatalf("checkout was written before identity path rejection: %v", err)
	}
}

func TestSaveConfigUsesTheNormalizedPathItValidates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	safe := filepath.Join(root, "safe")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{safe, filepath.Join(outside, "nested")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(safe, "link")
	if err := os.Symlink(filepath.Join(outside, "nested"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	requested := link + string(filepath.Separator) + ".." + string(filepath.Separator) + "backup.json"
	if err := SaveConfig(requested, Config{}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(safe, "backup.json")); err != nil {
		t.Fatalf("normalized config was not written: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "backup.json")); !os.IsNotExist(err) {
		t.Fatalf("config escaped through cleaned-away symlink: %v", err)
	}
}

func TestValidateRecipientsRejectsInvalidRecipient(t *testing.T) {
	t.Parallel()
	if err := ValidateRecipients([]string{"not-an-age-recipient"}); err == nil || !strings.Contains(err.Error(), "parse age recipient") {
		t.Fatalf("ValidateRecipients error = %v, want parse age recipient", err)
	}
	if err := ValidateRecipients(nil); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("ValidateRecipients(nil) error = %v, want at least one", err)
	}
}

func TestInitAndStatusAgainstTemporaryBareRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)

	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	result, err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !result.Changed || result.Pushed {
		t.Fatalf("Init result = %+v, want changed local commit without push", result)
	}
	gitCommand(t, result.RepoPath, "rev-parse", "HEAD")
	if out := gitCommand(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname)"); strings.TrimSpace(out) != "" {
		t.Fatalf("--no-push remote refs = %q, want empty", out)
	}

	status, err := Status(context.Background(), Options{ConfigPath: opts.ConfigPath})
	if err != nil {
		t.Fatalf("Status before manifest: %v", err)
	}
	if status.Status != StatusEmpty || status.Encrypted || status.ShardCount != 0 || status.ExportedAt != "" || status.Counts != nil {
		t.Fatalf("Status before manifest = %+v, want explicit empty state", status)
	}

	manifest := Manifest{
		Format:     1,
		Encrypted:  true,
		ExportedAt: "2026-08-15T10:00:00Z",
		Recipients: []string{result.Recipient},
		Counts: Counts{
			Connections:          1,
			DataPoints:           7,
			DataPointRevisions:   2,
			DataPointAttachments: 1,
			AttachmentPayloads:   1,
		},
		Shards: []ShardEntry{{Table: "data_points", Path: "data/data_points.jsonl.gz.age", Rows: 7, SHA256: strings.Repeat("a", 64), Bytes: 128}},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(result.RepoPath, ManifestFilename), append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	status, err = Status(context.Background(), Options{ConfigPath: opts.ConfigPath})
	if err != nil {
		t.Fatalf("Status with manifest: %v", err)
	}
	if status.Status != StatusReady || !status.Encrypted || status.ShardCount != 1 || status.ExportedAt != manifest.ExportedAt || status.Counts == nil || *status.Counts != manifest.Counts {
		t.Fatalf("Status with manifest = %+v, want manifest metadata", status)
	}
}

func TestPushWritesEncryptedSnapshotCommitAndLeavesUnchangedRerunClean(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}

	const privateMarker = "synthetic-private-health-value"
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 10, 30, 0, 0, time.UTC),
		Counts: Counts{
			Connections:        1,
			DataPoints:         1,
			AttachmentPayloads: 1,
		},
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
			{Table: "data_points", Path: "data/data_points.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"raw_json\":\"" + privateMarker + "\"}\n")},
			{Table: "attachment_payloads", Path: "data/attachment_payloads.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"payload\":\"c3ludGhldGljLWF0dGFjaG1lbnQtYnl0ZXM=\"}\n")},
		},
	}
	result, err := Push(context.Background(), opts, input)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !result.Changed || result.Pushed || !result.Encrypted || result.ShardCount != len(input.Shards) || result.Counts != input.Counts {
		t.Fatalf("Push result = %+v", result)
	}
	firstHead := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))

	manifestData, err := os.ReadFile(filepath.Join(opts.Repo, ManifestFilename))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Format != 1 || !manifest.Encrypted || manifest.HealthArchiveSchemaVersion != input.SchemaVersion || manifest.ExportedAt != input.ExportedAt.Format(time.RFC3339) || !reflect.DeepEqual(manifest.Recipients, normalizedStrings(manifest.Recipients)) || manifest.Counts != input.Counts || len(manifest.Shards) != len(input.Shards) {
		t.Fatalf("manifest = %+v", manifest)
	}

	identityData, err := os.ReadFile(opts.Identity)
	if err != nil {
		t.Fatal(err)
	}
	var identity age.Identity
	for _, line := range strings.Split(string(identityData), "\n") {
		if strings.HasPrefix(line, "AGE-SECRET-KEY-") {
			identity, err = age.ParseX25519Identity(line)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if identity == nil {
		t.Fatal("generated age identity not found")
	}
	plaintextByPath := make(map[string][]byte, len(input.Shards))
	firstCiphertexts := make(map[string][]byte, len(input.Shards))
	for _, shard := range input.Shards {
		plaintextByPath[shard.Path] = shard.JSONL
	}
	for _, entry := range manifest.Shards {
		ciphertext, err := os.ReadFile(filepath.Join(opts.Repo, filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(ciphertext, []byte(privateMarker)) || bytes.Contains(ciphertext, []byte("synthetic-attachment-bytes")) {
			t.Fatalf("encrypted shard %q contains plaintext", entry.Path)
		}
		firstCiphertexts[entry.Path] = append([]byte(nil), ciphertext...)
		decrypted, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
		if err != nil {
			t.Fatalf("decrypt %q: %v", entry.Path, err)
		}
		reader, err := gzip.NewReader(decrypted)
		if err != nil {
			t.Fatalf("gzip %q: %v", entry.Path, err)
		}
		if !reader.ModTime.IsZero() {
			t.Errorf("gzip %q modtime = %v, want fixed zero value", entry.Path, reader.ModTime)
		}
		plaintext, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(plaintext, plaintextByPath[entry.Path]) {
			t.Fatalf("decrypted shard %q mismatch", entry.Path)
		}
	}

	input.ExportedAt = input.ExportedAt.Add(time.Hour)
	second, err := Push(context.Background(), opts, input)
	if err != nil {
		t.Fatalf("unchanged Push: %v", err)
	}
	if second.Changed || second.Pushed {
		t.Fatalf("unchanged Push result = %+v", second)
	}
	if head := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD")); head != firstHead {
		t.Fatalf("unchanged Push HEAD = %s, want %s", head, firstHead)
	}
	if status := gitCommand(t, opts.Repo, "status", "--porcelain=v1", "--untracked-files=all"); strings.TrimSpace(status) != "" {
		t.Fatalf("unchanged Push left checkout dirty: %q", status)
	}
	corruptedPath := filepath.Join(opts.Repo, filepath.FromSlash(manifest.Shards[0].Path))
	corrupted, err := os.ReadFile(corruptedPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupted[len(corrupted)-1] ^= 0xff
	if err := os.WriteFile(corruptedPath, corrupted, 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, opts.Repo, "add", "--", manifest.Shards[0].Path)
	gitCommand(t, opts.Repo, "commit", "--no-gpg-sign", "-m", "test: commit corrupted ciphertext")
	input.ExportedAt = input.ExportedAt.Add(time.Hour)
	repaired, err := Push(context.Background(), opts, input)
	if err != nil {
		t.Fatalf("corrupted-ciphertext Push: %v", err)
	}
	if !repaired.Changed {
		t.Fatalf("corrupted-ciphertext Push result = %+v, want changed repair", repaired)
	}
	repairedCiphertext, err := os.ReadFile(corruptedPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(repairedCiphertext, corrupted) {
		t.Fatal("corrupted ciphertext was reused")
	}

	additionalIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	opts.Recipients = []string{manifest.Recipients[0], additionalIdentity.Recipient().String()}
	input.ExportedAt = input.ExportedAt.Add(time.Hour)
	reencrypted, err := Push(context.Background(), opts, input)
	if err != nil {
		t.Fatalf("recipient-change Push: %v", err)
	}
	if !reencrypted.Changed {
		t.Fatalf("recipient-change Push result = %+v, want changed", reencrypted)
	}
	updatedManifestData, err := os.ReadFile(filepath.Join(opts.Repo, ManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	var updatedManifest Manifest
	if err := json.Unmarshal(updatedManifestData, &updatedManifest); err != nil {
		t.Fatal(err)
	}
	if len(updatedManifest.Recipients) != 2 {
		t.Fatalf("recipient-change manifest recipients = %v, want two", updatedManifest.Recipients)
	}
	for _, entry := range updatedManifest.Shards {
		ciphertext, err := os.ReadFile(filepath.Join(opts.Repo, filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(ciphertext, firstCiphertexts[entry.Path]) {
			t.Errorf("recipient change did not re-encrypt shard %q", entry.Path)
		}
	}
	stalePath := filepath.Join(opts.Repo, filepath.FromSlash("data/attachment_payloads.jsonl.gz.age"))
	input.Shards = input.Shards[:2]
	input.Counts.AttachmentPayloads = 0
	input.ExportedAt = input.ExportedAt.Add(time.Hour)
	trimmed, err := Push(context.Background(), opts, input)
	if err != nil {
		t.Fatalf("stale-shard Push: %v", err)
	}
	if !trimmed.Changed {
		t.Fatalf("stale-shard Push result = %+v, want changed", trimmed)
	}
	if _, err := os.Lstat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale shard still exists: %v", err)
	}
	manifestBeforeRejectedPush, err := os.ReadFile(filepath.Join(opts.Repo, ManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	operatorPath := filepath.Join(opts.Repo, "data", "operator-note")
	if err := os.WriteFile(operatorPath, []byte("preserve me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input.ExportedAt = input.ExportedAt.Add(time.Hour)
	if _, err := Push(context.Background(), opts, input); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("Push with operator file error = %v, want preflight rejection", err)
	}
	operatorData, err := os.ReadFile(operatorPath)
	if err != nil || string(operatorData) != "preserve me\n" {
		t.Fatalf("operator file after rejected Push = %q, %v", operatorData, err)
	}
	manifestAfterRejectedPush, err := os.ReadFile(filepath.Join(opts.Repo, ManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifestBeforeRejectedPush, manifestAfterRejectedPush) {
		t.Fatal("rejected Push changed the manifest")
	}
}

func TestPushPushesSnapshotCommitToTemporaryBareRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	opts.Push = true
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC),
		Counts:        Counts{Connections: 1},
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	result, err := Push(context.Background(), opts, input)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !result.Changed || !result.Pushed {
		t.Fatalf("Push result = %+v, want changed and pushed", result)
	}
	localHead := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	remoteHead := strings.TrimSpace(gitCommand(t, "", "--git-dir", remote, "rev-parse", "HEAD"))
	if localHead != remoteHead {
		t.Fatalf("remote HEAD = %s, want local %s", remoteHead, localHead)
	}

	input.ExportedAt = input.ExportedAt.Add(time.Hour)
	unchanged, err := Push(context.Background(), opts, input)
	if err != nil {
		t.Fatalf("unchanged Push: %v", err)
	}
	if unchanged.Changed || !unchanged.Pushed {
		t.Fatalf("unchanged Push result = %+v, want unchanged push", unchanged)
	}
	if head := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD")); head != localHead {
		t.Fatalf("unchanged Push created commit %s, want %s", head, localHead)
	}
}

func TestPullRebasesConfiguredCheckoutBeforeRestoringSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	identity := filepath.Join(root, "private", "backup-age-identity.txt")
	firstConfig := filepath.Join(root, "first-private", "backup.json")
	firstRepo := filepath.Join(root, "first-checkout")
	if _, err := Init(ctx, Options{ConfigPath: firstConfig, Repo: firstRepo, Remote: remote, Identity: identity, Push: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, Options{ConfigPath: firstConfig, Push: true}, pullTestInput("first")); err != nil {
		t.Fatal(err)
	}

	secondConfig := filepath.Join(root, "second-private", "backup.json")
	secondRepo := filepath.Join(root, "second-checkout")
	if _, err := Init(ctx, Options{ConfigPath: secondConfig, Repo: secondRepo, Remote: remote, Identity: identity, Push: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, Options{ConfigPath: secondConfig, Push: true}, pullTestInput("second")); err != nil {
		t.Fatal(err)
	}

	firstHeadBefore := gitCommand(t, firstRepo, "rev-parse", "HEAD")
	secondHead := gitCommand(t, secondRepo, "rev-parse", "HEAD")
	if firstHeadBefore == secondHead {
		t.Fatal("first checkout was not stale before backup pull")
	}
	called := false
	result, err := PullCurrent(ctx, Options{ConfigPath: firstConfig}, func(input PullInput) error {
		called = true
		if input.Counts.Connections != 1 {
			t.Fatalf("pulled Connection count = %d, want remote update", input.Counts.Connections)
		}
		for _, shard := range input.Shards {
			if shard.Table == "connections" && !bytes.Contains(shard.JSONL, []byte(`"marker":"second"`)) {
				t.Fatalf("pulled Connection shard = %q, want second remote snapshot", shard.JSONL)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || !result.Changed || result.Counts.Connections != 1 {
		t.Fatalf("pull result = %+v, callback called=%t", result, called)
	}
	if got := gitCommand(t, firstRepo, "rev-parse", "HEAD"); got != secondHead {
		t.Fatalf("pulled HEAD = %s, want %s", got, secondHead)
	}
}

func TestPullRebasesDivergentGeneratedSnapshotAndKeepsLocalPendingSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	identity := filepath.Join(root, "private", "backup-age-identity.txt")
	firstConfig := filepath.Join(root, "first-private", "backup.json")
	firstRepo := filepath.Join(root, "first-checkout")
	if _, err := Init(ctx, Options{ConfigPath: firstConfig, Repo: firstRepo, Remote: remote, Identity: identity, Push: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, Options{ConfigPath: firstConfig, Push: true}, pullTestInput("initial")); err != nil {
		t.Fatal(err)
	}

	secondConfig := filepath.Join(root, "second-private", "backup.json")
	secondRepo := filepath.Join(root, "second-checkout")
	if _, err := Init(ctx, Options{ConfigPath: secondConfig, Repo: secondRepo, Remote: remote, Identity: identity, Push: false}); err != nil {
		t.Fatal(err)
	}
	localInput := pullTestInput("initial")
	localInput.Counts.DataPoints = 1
	for index := range localInput.Shards {
		if localInput.Shards[index].Table == "data_points" {
			localInput.Shards[index].Rows = 1
			localInput.Shards[index].JSONL = []byte("{\"marker\":\"local-pending\"}\n")
		}
	}
	if _, err := Push(ctx, Options{ConfigPath: firstConfig, Push: false}, localInput); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, Options{ConfigPath: secondConfig, Push: true}, pullTestInput("remote-newer")); err != nil {
		t.Fatal(err)
	}
	remoteHead := strings.TrimSpace(gitCommand(t, "", "--git-dir", remote, "rev-parse", "HEAD"))

	_, err := PullCurrent(ctx, Options{ConfigPath: firstConfig}, func(input PullInput) error {
		for _, shard := range input.Shards {
			switch shard.Table {
			case "connections":
				if !bytes.Contains(shard.JSONL, []byte(`"marker":"initial"`)) {
					t.Fatalf("pulled Connection shard = %q, want complete local pending snapshot", shard.JSONL)
				}
			case "data_points":
				if !bytes.Contains(shard.JSONL, []byte(`"marker":"local-pending"`)) {
					t.Fatalf("pulled Data Point shard = %q, want complete local pending snapshot", shard.JSONL)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	localHead := strings.TrimSpace(gitCommand(t, firstRepo, "rev-parse", "HEAD"))
	gitCommand(t, firstRepo, "merge-base", "--is-ancestor", remoteHead, localHead)
	if _, err := os.Stat(filepath.Join(firstRepo, ".git", "rebase-merge")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rebase state remains after pull: %v", err)
	}
	cfg, _, err := LoadConfig(firstConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.PendingCommits, []string{localHead}) {
		t.Fatalf("pending commits = %v, want rebased local snapshot %s", cfg.PendingCommits, localHead)
	}
}

func TestPullWrongIdentityFailsBeforeRestore(t *testing.T) {
	ctx := context.Background()
	root, configPath, _, _ := setupPullTestBackup(t, pullTestInput("second"))
	wrongIdentity := filepath.Join(root, "wrong-private", "backup-age-identity.txt")
	if _, err := EnsureIdentity(wrongIdentity); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := PullCurrent(ctx, Options{ConfigPath: configPath, Identity: wrongIdentity}, func(PullInput) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "authenticate age ciphertext") {
		t.Fatalf("PullCurrent error = %v, want wrong-identity failure", err)
	}
	if called {
		t.Fatal("wrong identity reached Health Archive Snapshot restore callback")
	}
}

func TestPullRejectsConfigBeforeIdentityWhenBothAreInsideCheckout(t *testing.T) {
	_, configPath, repo, _ := setupPullTestBackup(t, pullTestInput("second"))
	cfg, _, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	identityData, err := os.ReadFile(cfg.Identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Identity = filepath.Join(repo, "private-identity.txt")
	if err := os.WriteFile(cfg.Identity, identityData, 0o600); err != nil {
		t.Fatal(err)
	}
	insideConfig := filepath.Join(repo, "private-config.json")
	if err := SaveConfig(insideConfig, cfg); err != nil {
		t.Fatal(err)
	}

	_, err = PullCurrent(context.Background(), Options{ConfigPath: insideConfig}, func(PullInput) error {
		t.Fatal("private path validation reached restore callback")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "backup config path must be outside the Git checkout") {
		t.Fatalf("PullCurrent error = %v, want deterministic config-path failure", err)
	}
}

func TestPullMissingShardFailsBeforeRestore(t *testing.T) {
	_, configPath, repo, _ := setupPullTestBackup(t, pullTestInput("second"))
	manifest, found, err := readManifestIfPresent(repo)
	if err != nil || !found {
		t.Fatalf("read manifest: found=%t err=%v", found, err)
	}
	missing := manifest.Shards[0].Path
	gitCommand(t, repo, "rm", "--", missing)
	gitCommand(t, repo, "commit", "--no-gpg-sign", "-m", "test: remove encrypted shard")
	gitCommand(t, repo, "push", "origin", "HEAD")
	called := false
	_, err = PullCurrent(context.Background(), Options{ConfigPath: configPath}, func(PullInput) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "not tracked") {
		t.Fatalf("PullCurrent error = %v, want missing-shard failure", err)
	}
	if called {
		t.Fatal("missing shard reached Health Archive Snapshot restore callback")
	}
}

func TestPullBadPlaintextHashFailsBeforeRestore(t *testing.T) {
	_, configPath, repo, _ := setupPullTestBackup(t, pullTestInput("second"))
	manifest, found, err := readManifestIfPresent(repo)
	if err != nil || !found {
		t.Fatalf("read manifest: found=%t err=%v", found, err)
	}
	manifest.Shards[0].SHA256 = strings.Repeat("0", sha256.Size*2)
	writePullTestManifest(t, repo, manifest)
	gitCommand(t, repo, "add", "--", ManifestFilename)
	gitCommand(t, repo, "commit", "--no-gpg-sign", "-m", "test: corrupt plaintext hash")
	gitCommand(t, repo, "push", "origin", "HEAD")
	called := false
	_, err = PullCurrent(context.Background(), Options{ConfigPath: configPath}, func(PullInput) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("PullCurrent error = %v, want bad-hash failure", err)
	}
	if called {
		t.Fatal("bad plaintext hash reached Health Archive Snapshot restore callback")
	}
}

func TestPullCorruptCiphertextFailsBeforeRestore(t *testing.T) {
	_, configPath, repo, _ := setupPullTestBackup(t, pullTestInput("second"))
	manifest, found, err := readManifestIfPresent(repo)
	if err != nil || !found {
		t.Fatalf("read manifest: found=%t err=%v", found, err)
	}
	shardPath := filepath.Join(repo, filepath.FromSlash(manifest.Shards[0].Path))
	ciphertext, err := os.ReadFile(shardPath)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)/2] ^= 0xff
	if err := os.WriteFile(shardPath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "add", "--", manifest.Shards[0].Path)
	gitCommand(t, repo, "commit", "--no-gpg-sign", "-m", "test: corrupt encrypted shard")
	gitCommand(t, repo, "push", "origin", "HEAD")
	called := false
	_, err = PullCurrent(context.Background(), Options{ConfigPath: configPath}, func(PullInput) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "authenticate age ciphertext") && !strings.Contains(err.Error(), "read gzip payload") {
		t.Fatalf("PullCurrent error = %v, want corrupt-ciphertext failure", err)
	}
	if called {
		t.Fatal("corrupt ciphertext reached Health Archive Snapshot restore callback")
	}
}

func TestPullUnsupportedManifestFailsBeforeRestore(t *testing.T) {
	_, configPath, repo, _ := setupPullTestBackup(t, pullTestInput("second"))
	manifest, found, err := readManifestIfPresent(repo)
	if err != nil || !found {
		t.Fatalf("read manifest: found=%t err=%v", found, err)
	}
	manifest.Format++
	writePullTestManifest(t, repo, manifest)
	gitCommand(t, repo, "add", "--", ManifestFilename)
	gitCommand(t, repo, "commit", "--no-gpg-sign", "-m", "test: use unsupported manifest")
	gitCommand(t, repo, "push", "origin", "HEAD")
	called := false
	_, err = PullCurrent(context.Background(), Options{ConfigPath: configPath}, func(PullInput) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported backup manifest format") {
		t.Fatalf("PullCurrent error = %v, want unsupported-manifest failure", err)
	}
	if called {
		t.Fatal("unsupported manifest reached Health Archive Snapshot restore callback")
	}
}

func TestPullRejectsShardPathOutsideBackupDataTreeBeforeRestore(t *testing.T) {
	_, configPath, repo, _ := setupPullTestBackup(t, pullTestInput("second"))
	manifest, found, err := readManifestIfPresent(repo)
	if err != nil || !found {
		t.Fatalf("read manifest: found=%t err=%v", found, err)
	}
	manifest.Shards[0].Path = "data/../outside.jsonl.gz.age"
	writePullTestManifest(t, repo, manifest)
	gitCommand(t, repo, "add", "--", ManifestFilename)
	gitCommand(t, repo, "commit", "--no-gpg-sign", "-m", "test: escape backup data tree")
	gitCommand(t, repo, "push", "origin", "HEAD")
	called := false
	_, err = PullCurrent(context.Background(), Options{ConfigPath: configPath}, func(PullInput) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid path") {
		t.Fatalf("PullCurrent error = %v, want shard-path confinement failure", err)
	}
	if called {
		t.Fatal("escaping shard path reached Health Archive Snapshot restore callback")
	}
}

func TestPushRejectsCheckoutWithoutSetupCommitBeforeBuildingSnapshot(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	gitCommand(t, opts.Repo, "update-ref", "-d", "HEAD")

	built := false
	_, err := PushCurrent(context.Background(), opts, func() (PushInput, error) {
		built = true
		return PushInput{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "backup init") {
		t.Fatalf("PushCurrent error = %v, want backup init remediation", err)
	}
	if built {
		t.Fatal("PushCurrent built a Health Archive Snapshot before rejecting the uninitialized checkout")
	}
}

func TestPushRejectsTrackedFilesOutsideCurrentManifest(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 11, 30, 0, 0, time.UTC),
		Counts:        Counts{Connections: 1},
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	foreignPath := filepath.Join(opts.Repo, "data", "foreign.jsonl.gz.age")
	if err := os.WriteFile(foreignPath, []byte("foreign tracked content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, opts.Repo, "add", "--", "data/foreign.jsonl.gz.age")
	gitCommand(t, opts.Repo, "commit", "--no-gpg-sign", "-m", "test: add foreign tracked backup file")
	foreignHead := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	input.ExportedAt = input.ExportedAt.Add(time.Hour)
	if _, err := Push(context.Background(), opts, input); err == nil || !strings.Contains(err.Error(), "not owned by the current manifest") {
		t.Fatalf("Push with foreign tracked file error = %v, want ownership rejection", err)
	}
	foreignData, err := os.ReadFile(foreignPath)
	if err != nil || string(foreignData) != "foreign tracked content\n" {
		t.Fatalf("foreign tracked file after rejected Push = %q, %v", foreignData, err)
	}
	if head := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD")); head != foreignHead {
		t.Fatalf("rejected Push HEAD = %s, want %s", head, foreignHead)
	}
}

func TestPushRejectsIgnoredOperatorFilesWithoutDeletingThem(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.Mkdir(filepath.Join(opts.Repo, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	operatorPath := filepath.Join(opts.Repo, "data", "operator-note")
	if err := os.WriteFile(operatorPath, []byte("preserve ignored file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.Repo, ".git", "info", "exclude"), []byte("data/operator-note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 11, 45, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 0},
		},
	}
	if _, err := Push(context.Background(), opts, input); err == nil || !strings.Contains(err.Error(), "not owned by the current manifest") {
		t.Fatalf("Push with ignored operator file error = %v, want ownership rejection", err)
	}
	operatorData, err := os.ReadFile(operatorPath)
	if err != nil || string(operatorData) != "preserve ignored file\n" {
		t.Fatalf("ignored operator file after rejected Push = %q, %v", operatorData, err)
	}
}

func TestPushForceAddsGeneratedShardsDespiteIgnoreRules(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opts.Repo, ".git", "info", "exclude"), []byte("data/*.age\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := strings.TrimSpace(gitCommand(t, opts.Repo, "ls-tree", "--name-only", "HEAD", "--", input.Shards[0].Path)); got != input.Shards[0].Path {
		t.Fatalf("committed ignored shard = %q, want %q", got, input.Shards[0].Path)
	}
}

func TestPushRejectsGitContentAttributesBeforePublication(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	attributesPath := filepath.Join(opts.Repo, ".gitattributes")
	if err := os.WriteFile(attributesPath, []byte("data/*.age filter=corrupt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, opts.Repo, "add", "--", ".gitattributes")
	gitCommand(t, opts.Repo, "commit", "--no-gpg-sign", "-m", "test: add ciphertext filter")
	beforeHead := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 12, 5, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err == nil || !strings.Contains(err.Error(), "Git content attribute applies") {
		t.Fatalf("Push with content attribute error = %v, want rejection", err)
	}
	if head := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD")); head != beforeHead {
		t.Fatalf("rejected Push HEAD = %s, want %s", head, beforeHead)
	}
	for _, path := range []string{filepath.Join(opts.Repo, ManifestFilename), filepath.Join(opts.Repo, "data")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected Push published path %s: %v", path, err)
		}
	}
}

func TestGitContentAttributesUnspecifiedAcceptsCRLF(t *testing.T) {
	t.Parallel()
	output := "data/example.age: filter: unspecified\r\ndata/example.age: text: unspecified\r\n"
	if !gitContentAttributesUnspecified(output) {
		t.Fatal("unspecified CRLF attribute output was rejected")
	}
	if gitContentAttributesUnspecified("data/example.age: filter: corrupt\r\n") {
		t.Fatal("specified CRLF attribute output was accepted")
	}
}

func TestPushRestoresPreviousCheckoutWhenCommitFails(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell hook fixture is Unix-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	beforeHead := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	gitDir := filepath.Join(opts.Repo, ".git")
	if err := os.Chmod(gitDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(gitDir, 0o700) }()
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 12, 15, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err == nil {
		t.Fatal("Push with read-only Git metadata succeeded")
	}
	if err := os.Chmod(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if head := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD")); head != beforeHead {
		t.Fatalf("failed Push HEAD = %s, want %s", head, beforeHead)
	}
	if status := strings.TrimSpace(gitCommand(t, opts.Repo, "status", "--porcelain=v1", "--untracked-files=all")); status != "" {
		t.Fatalf("failed Push left checkout dirty: %q", status)
	}
	for _, path := range []string{filepath.Join(opts.Repo, ManifestFilename), filepath.Join(opts.Repo, "data")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed Push left published path %s: %v", path, err)
		}
	}
}

func TestPushDisablesRepositoryHooksBeforeCommitting(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell hook fixture is Unix-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	hookMarker := filepath.Join(opts.Repo, "hook-ran")
	hookBody := "#!/bin/sh\nprintf hook-ran > " + hookMarker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(opts.Repo, ".git", "hooks", "pre-commit"), []byte(hookBody), 0o700); err != nil {
		t.Fatal(err)
	}
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 12, 30, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := os.Lstat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository hook ran during Push: %v", err)
	}
	if !isGeneratedSnapshotCommit(context.Background(), opts.Repo, "HEAD") {
		t.Fatal("Push did not create a verified generated snapshot commit")
	}
}

func TestPushRepairsCommittedMissingReadmeWithValidatedSnapshot(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 12, 45, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	if err := os.Remove(filepath.Join(opts.Repo, recoveryReadmeFilename)); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, opts.Repo, "add", "-u", "--", recoveryReadmeFilename)
	gitCommand(t, opts.Repo, "commit", "--no-gpg-sign", "-m", "test: remove generated README")
	input.ExportedAt = input.ExportedAt.Add(time.Hour)
	repaired, err := Push(context.Background(), opts, input)
	if err != nil {
		t.Fatalf("README repair Push: %v", err)
	}
	if !repaired.Changed {
		t.Fatalf("README repair Push = %+v, want changed", repaired)
	}
	if !isGeneratedSnapshotCommit(context.Background(), opts.Repo, "HEAD") {
		t.Fatal("README-only repair is not a verified generated snapshot commit")
	}
	if status := strings.TrimSpace(gitCommand(t, opts.Repo, "status", "--porcelain=v1", "--untracked-files=all")); status != "" {
		t.Fatalf("README repair left checkout dirty: %q", status)
	}
}

func TestPushRejectsForgedGeneratedCommitContainingPlaintext(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC),
		Counts:        Counts{Connections: 1},
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	manifestData, err := os.ReadFile(filepath.Join(opts.Repo, ManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	forgedPlaintext := []byte("plaintext health data in forged history\n")
	forgedPath := filepath.Join(opts.Repo, filepath.FromSlash(manifest.Shards[0].Path))
	if err := os.WriteFile(forgedPath, forgedPlaintext, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Shards[0].Bytes = int64(len(forgedPlaintext))
	forgedManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	forgedManifest = append(forgedManifest, '\n')
	if err := os.WriteFile(filepath.Join(opts.Repo, ManifestFilename), forgedManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, opts.Repo, "add", "--", ManifestFilename, manifest.Shards[0].Path)
	gitCommand(t, opts.Repo, "commit", "--no-gpg-sign", "-m", "backup: update encrypted Health Archive snapshot")
	opts.Push = true
	input.ExportedAt = input.ExportedAt.Add(time.Hour)
	if _, err := Push(context.Background(), opts, input); err == nil || !strings.Contains(err.Error(), "authenticated backup commands") {
		t.Fatalf("Push with forged generated history error = %v, want authenticated-history rejection", err)
	}
	if _, err := gitOutput(context.Background(), "", "--git-dir", remote, "rev-parse", "HEAD"); err == nil {
		t.Fatal("forged generated history reached remote")
	}
}

func TestGeneratedHistoryRejectsPlaintextCommitMessageBody(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 13, 15, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err != nil {
		t.Fatalf("Push: %v", err)
	}
	gitCommand(t, opts.Repo, "commit", "--allow-empty", "--no-gpg-sign", "-m", "backup: update encrypted Health Archive snapshot", "-m", "plaintext health data in commit body")
	identity, err := identityFromFile(opts.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if isAuthorizedBackupHead(context.Background(), opts.Repo, identity) {
		t.Fatal("generated history authorization accepted plaintext commit message body")
	}
}

func TestPushDoesNotFollowAnnotatedTags(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	gitCommand(t, opts.Repo, "tag", "-a", "untrusted-annotation", "-m", "plaintext must not be uploaded")
	gitCommand(t, opts.Repo, "config", "push.followTags", "true")
	opts.Push = true
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 13, 30, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := gitOutput(context.Background(), "", "--git-dir", remote, "rev-parse", "refs/tags/untrusted-annotation"); err == nil {
		t.Fatal("Push uploaded an annotated tag despite exact branch refspec")
	}
}

func TestInitPersistsPendingCommitAfterPushFailure(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("bare remote hook fixture is Unix-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	hookPath := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       true,
	}
	if _, err := Init(context.Background(), opts); err == nil {
		t.Fatal("Init with rejecting remote succeeded")
	}
	cfg, found, err := LoadConfig(opts.ConfigPath)
	if err != nil || !found {
		t.Fatalf("LoadConfig after failed push = (%+v, %t, %v)", cfg, found, err)
	}
	head := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	if !reflect.DeepEqual(cfg.PendingCommits, []string{head}) {
		t.Fatalf("pending commits after failed push = %v, want [%s]", cfg.PendingCommits, head)
	}
	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}
	result, err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("retry Init: %v", err)
	}
	if result.Changed || !result.Pushed {
		t.Fatalf("retry Init result = %+v, want unchanged push", result)
	}
	cfg, _, err = LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PendingCommits) != 0 {
		t.Fatalf("pending commits after successful retry = %v, want empty", cfg.PendingCommits)
	}
}

func TestInitMigratesLegacyPendingSetupCommit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg, _, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PendingCommits = nil
	if err := SaveConfig(opts.ConfigPath, cfg); err != nil {
		t.Fatal(err)
	}
	opts.Push = true
	result, err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("legacy retry Init: %v", err)
	}
	if result.Changed || !result.Pushed {
		t.Fatalf("legacy retry Init result = %+v, want unchanged push", result)
	}
	cfg, _, err = LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PendingCommits) != 0 {
		t.Fatalf("pending commits after legacy migration push = %v, want empty", cfg.PendingCommits)
	}
}

func TestPushRollsBackWhenPendingCommitCannotBeSaved(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("directory mode failure fixture is Unix-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	privateDir := filepath.Join(root, "private")
	opts := Options{
		ConfigPath: filepath.Join(privateDir, "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(privateDir, "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	beforeHead := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	beforeConfig, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(privateDir, 0o700) }()
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 13, 45, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := PushCurrent(context.Background(), opts, func() (PushInput, error) {
		if err := os.Chmod(privateDir, 0o500); err != nil {
			return PushInput{}, err
		}
		return input, nil
	}); err == nil {
		t.Fatal("Push with unwritable config directory succeeded")
	}
	if err := os.Chmod(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if head := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD")); head != beforeHead {
		t.Fatalf("failed Push HEAD = %s, want %s", head, beforeHead)
	}
	if status := strings.TrimSpace(gitCommand(t, opts.Repo, "status", "--porcelain=v1", "--untracked-files=all")); status != "" {
		t.Fatalf("failed Push left checkout dirty: %q", status)
	}
	afterConfig, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatal("failed Push changed pending commit provenance")
	}
}

func TestInterruptedPublicationRecoversCheckoutFromHead(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	head := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	if err := beginSnapshotPublication(opts.Repo, head); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(opts.Repo, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.Repo, "data", "connections.jsonl.gz.age"), []byte("partial encrypted shard"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.Repo, ManifestFilename), []byte("partial manifest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoverInterruptedSnapshotPublication(context.Background(), cfg, opts.ConfigPath); err != nil {
		t.Fatalf("recoverInterruptedSnapshotPublication: %v", err)
	}
	if status := strings.TrimSpace(gitCommand(t, opts.Repo, "status", "--porcelain=v1", "--untracked-files=all")); status != "" {
		t.Fatalf("recovered checkout is dirty: %q", status)
	}
	if _, err := os.Lstat(filepath.Join(opts.Repo, ".git", publicationMarkerFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("publication marker remains after recovery: %v", err)
	}
}

func TestInterruptedCommittedPublicationRecoversPendingProvenance(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	oldHead := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	input := PushInput{
		SchemaVersion: 12,
		ExportedAt:    time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
		Shards: []PlaintextShard{
			{Table: "connections", Path: "data/connections.jsonl.gz.age", Rows: 1, JSONL: []byte("{\"id\":\"synthetic-connection\"}\n")},
		},
	}
	if _, err := Push(context.Background(), opts, input); err != nil {
		t.Fatalf("Push: %v", err)
	}
	newHead := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	cfg, _, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PendingCommits = []string{oldHead}
	if err := SaveConfig(opts.ConfigPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := beginSnapshotPublication(opts.Repo, oldHead); err != nil {
		t.Fatal(err)
	}
	if err := recordSnapshotPublicationCommit(opts.Repo, newHead); err != nil {
		t.Fatal(err)
	}
	cfg, err = recoverInterruptedSnapshotPublication(context.Background(), cfg, opts.ConfigPath)
	if err != nil {
		t.Fatalf("recoverInterruptedSnapshotPublication: %v", err)
	}
	if !reflect.DeepEqual(cfg.PendingCommits, []string{oldHead, newHead}) {
		t.Fatalf("recovered pending commits = %v, want [%s %s]", cfg.PendingCommits, oldHead, newHead)
	}
}

func TestInitPushesSetupCommitToTemporaryBareRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	result, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       true,
	})
	if err != nil {
		t.Fatalf("Init with push: %v", err)
	}
	if !result.Changed || !result.Pushed {
		t.Fatalf("Init result = %+v, want changed and pushed", result)
	}
	if out := gitCommand(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname)"); !strings.Contains(out, "refs/heads/") {
		t.Fatalf("remote refs = %q, want pushed branch", out)
	}
}

func TestInitPushesPreviouslyLocalSetupCommit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("local Init: %v", err)
	}
	opts.Push = true
	result, err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("pushing Init retry: %v", err)
	}
	if result.Changed || !result.Pushed {
		t.Fatalf("retry result = %+v, want unchanged but pushed", result)
	}
	if out := gitCommand(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname)"); !strings.Contains(out, "refs/heads/") {
		t.Fatalf("remote refs = %q, want pushed setup commit", out)
	}
}

func TestInitRejectsExistingCheckoutWithDifferentOrigin(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	gitCommand(t, repo, "remote", "add", "origin", filepath.Join(root, "wrong.git"))
	configPath := filepath.Join(root, "private", "backup.json")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")
	_, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       repo,
		Remote:     filepath.Join(root, "expected.git"),
		Identity:   identityPath,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "checkout origin") {
		t.Fatalf("Init error = %v, want checkout origin mismatch", err)
	}
	for _, path := range []string{configPath, identityPath} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("Init wrote %s despite remote mismatch: %v", path, statErr)
		}
	}
}

func TestInitKeepsLocalIdentityWithAdditionalRecipients(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	additionalIdentity := filepath.Join(root, "additional", "identity.txt")
	additionalRecipient, err := EnsureIdentity(additionalIdentity)
	if err != nil {
		t.Fatalf("additional EnsureIdentity: %v", err)
	}
	configPath := filepath.Join(root, "private", "backup.json")
	result, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Recipients: []string{additionalRecipient, additionalRecipient},
		Push:       false,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg, found, err := LoadConfig(configPath)
	if err != nil || !found {
		t.Fatalf("LoadConfig = found %t, err %v", found, err)
	}
	want := []string{result.Recipient, additionalRecipient}
	if !reflect.DeepEqual(cfg.Recipients, want) {
		t.Fatalf("recipients = %v, want local identity plus deduplicated additional %v", cfg.Recipients, want)
	}
	if cfg.LocalRecipient != result.Recipient {
		t.Fatalf("local recipient = %q, want %q", cfg.LocalRecipient, result.Recipient)
	}
}

func TestInitIdentityRotationDropsPreviousLocalRecipient(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "private", "backup.json")
	first, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "first-identity.txt"),
		Push:       false,
	})
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	second, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Identity:   filepath.Join(root, "private", "second-identity.txt"),
		Push:       false,
	})
	if err != nil {
		t.Fatalf("rotating Init: %v", err)
	}
	cfg, found, err := LoadConfig(configPath)
	if err != nil || !found {
		t.Fatalf("LoadConfig = found %t, err %v", found, err)
	}
	if cfg.LocalRecipient != second.Recipient || !reflect.DeepEqual(cfg.Recipients, []string{second.Recipient}) {
		t.Fatalf("rotated config = local %q recipients %v, want only %q", cfg.LocalRecipient, cfg.Recipients, second.Recipient)
	}
	if first.Recipient == second.Recipient {
		t.Fatal("test generated identical recipients for two identities")
	}
}

func TestInitIdentityRotationKeepsExplicitPreviousRecipient(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "private", "backup.json")
	first, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       filepath.Join(root, "checkout"),
		Identity:   filepath.Join(root, "private", "first-identity.txt"),
		Push:       false,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Identity:   filepath.Join(root, "private", "second-identity.txt"),
		Recipients: []string{first.Recipient},
		Push:       false,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{second.Recipient, first.Recipient}
	if !reflect.DeepEqual(cfg.Recipients, want) {
		t.Fatalf("recipients = %v, want explicitly retained old recipient %v", cfg.Recipients, want)
	}
}

func TestInitPropagatesCloneFailureWithoutWritingPrivateState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "private", "backup.json")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")
	_, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       filepath.Join(root, "checkout"),
		Remote:     filepath.Join(root, "missing-remote.git"),
		Identity:   identityPath,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "git clone") {
		t.Fatalf("Init error = %v, want clone failure", err)
	}
	for _, path := range []string{configPath, identityPath} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("Init wrote %s after clone failure: %v", path, statErr)
		}
	}
}

func TestInitRejectsSymlinkedRecoveryReadmeWithoutOutsideWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	outside := filepath.Join(root, "outside.md")
	if err := os.Symlink(outside, filepath.Join(repo, "README.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       repo,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("Init error = %v, want symlink rejection", err)
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatalf("outside README was written: %v", statErr)
	}
}

func TestInitRejectsSymlinkedGitMetadataBeforeWritingPrivateState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outsideRepo := filepath.Join(root, "outside")
	if err := os.Mkdir(outsideRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, outsideRepo, "init", "-b", "main")
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outsideRepo, ".git"), filepath.Join(checkout, ".git")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	configPath := filepath.Join(root, "private", "backup.json")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")

	_, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       checkout,
		Identity:   identityPath,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "symlinked .git") {
		t.Fatalf("Init error = %v, want symlinked Git metadata rejection", err)
	}
	for _, path := range []string{configPath, identityPath, filepath.Join(outsideRepo, recoveryReadmeFilename)} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("state written after symlinked Git metadata rejection: %s: %v", path, statErr)
		}
	}
}

func TestInitRejectsSymlinkedCheckoutBeforeOutsideWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outsideRepo := filepath.Join(root, "outside")
	if err := os.Mkdir(outsideRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(root, "checkout")
	if err := os.Symlink(outsideRepo, checkout); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	configPath := filepath.Join(root, "private", "backup.json")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")

	_, err := Init(context.Background(), Options{ConfigPath: configPath, Repo: checkout, Identity: identityPath, Push: false})
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("Init error = %v, want symlinked-checkout rejection", err)
	}
	for _, path := range []string{configPath, identityPath, filepath.Join(outsideRepo, recoveryReadmeFilename), filepath.Join(outsideRepo, ".git")} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("state written after symlinked checkout rejection: %s: %v", path, statErr)
		}
	}
}

func TestInitRejectsSymlinkedCheckoutAncestorBeforeOutsideWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	checkout := filepath.Join(alias, "nested", "checkout")
	configPath := filepath.Join(root, "private", "backup.json")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")

	_, err := Init(context.Background(), Options{ConfigPath: configPath, Repo: checkout, Identity: identityPath, Push: false})
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("Init error = %v, want symlinked-ancestor rejection", err)
	}
	for _, path := range []string{configPath, identityPath, filepath.Join(outside, "nested")} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("state written after symlinked checkout-ancestor rejection: %s: %v", path, statErr)
		}
	}
}

func TestInitDefaultsIdentityBesideExplicitConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "isolated", "backup.json")
	result, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       filepath.Join(root, "checkout"),
		Push:       false,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	want := filepath.Join(filepath.Dir(configPath), "backup-age-identity.txt")
	if result.Identity != want {
		t.Fatalf("identity = %q, want %q", result.Identity, want)
	}
}

func TestInitCommitsOnlyGeneratedReadmeAndLeavesOtherIndexEntriesStaged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	sensitive := filepath.Join(repo, "private.sqlite")
	if err := os.WriteFile(sensitive, []byte("synthetic private archive marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "add", "--", "private.sqlite")
	if _, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       repo,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	committed := gitCommand(t, repo, "show", "--pretty=format:", "--name-only", "HEAD")
	if strings.Contains(committed, "private.sqlite") || !strings.Contains(committed, recoveryReadmeFilename) {
		t.Fatalf("committed paths = %q, want only recovery README", committed)
	}
	staged := gitCommand(t, repo, "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "private.sqlite") {
		t.Fatalf("unrelated staged entry was consumed: %q", staged)
	}
}

func TestInitRejectsAndPreservesStagedReadmeChange(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, recoveryReadmeFilename), []byte("user README\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "add", "--", recoveryReadmeFilename)
	stagedBefore := gitCommand(t, repo, "show", ":"+recoveryReadmeFilename)
	configPath := filepath.Join(root, "private", "backup.json")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")

	_, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       repo,
		Identity:   identityPath,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "staged changes") {
		t.Fatalf("Init error = %v, want staged README rejection", err)
	}
	if stagedAfter := gitCommand(t, repo, "show", ":"+recoveryReadmeFilename); stagedAfter != stagedBefore {
		t.Fatalf("staged README changed: got %q, want %q", stagedAfter, stagedBefore)
	}
	for _, path := range []string{configPath, identityPath} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("private setup file written after staged README rejection: %s: %v", path, statErr)
		}
	}
}

func TestInitRejectsCredentialBearingRemoteBeforeWritingState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const secret = "synthetic-secret"
	remote := "https://backup-user:" + secret + "@example.invalid/owner/backup.git?token=" + secret
	configPath := filepath.Join(root, "private", "backup.json")
	_, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Identity:   filepath.Join(root, "private", "backup-age-identity.txt"),
		Push:       false,
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Init error = %q, want credential-safe rejection", err)
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Fatalf("config written after credential remote rejection: %v", statErr)
	}
}

func TestInitRedactsCredentialFromMalformedRemote(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const secret = "synthetic-malformed-secret"
	_, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     "https://user:" + secret + "@example.invalid/%zz",
		Push:       false,
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Init error = %q, want credential-safe malformed URL rejection", err)
	}
}

func TestValidateRemoteRejectsOptionAndExternalHelperSyntax(t *testing.T) {
	t.Parallel()
	for _, remote := range []string{"--upload-pack=synthetic-command", "::address", "ext::synthetic-command", "synthetic::address"} {
		if err := validateRemote(remote); err == nil {
			t.Errorf("validateRemote(%q) succeeded", remote)
		}
	}
}

func TestValidateRemoteAcceptsIPv6URLs(t *testing.T) {
	t.Parallel()
	for _, remote := range []string{"ssh://git@[fe80::1]/backup.git", "https://[2001:db8::1]/backup.git"} {
		if err := validateRemote(remote); err != nil {
			t.Errorf("validateRemote(%q): %v", remote, err)
		}
	}
}

func TestValidateRemoteRejectsQueryAndFragmentSyntax(t *testing.T) {
	t.Parallel()
	for _, remote := range []string{
		"https://example.invalid/backup.git?token=synthetic",
		"example.invalid/backup.git?token=synthetic",
		"git@example.invalid:backup.git#synthetic",
		"/local/backup.git#synthetic",
		`C:\backups\gohealth.git?token=synthetic`,
		`\\server\share\gohealth.git?token=synthetic`,
		`\\?\C:\backups\gohealth.git?token=synthetic`,
	} {
		if err := validateRemote(remote); err == nil || !strings.Contains(err.Error(), "query parameters or fragments") {
			t.Errorf("validateRemote(%q) error = %v, want query/fragment rejection", remote, err)
		}
	}
}

func TestValidateRemoteAcceptsWindowsPathsAndSSHUsernames(t *testing.T) {
	t.Parallel()
	for _, remote := range []string{`C:\backups\gohealth.git`, `\\server\share\gohealth.git`, `\\?\C:\backups\gohealth.git`, `\\?\UNC\server\share\gohealth.git`, "ssh://git@github.com/owner/backup.git", "git@github.com:owner/backup.git", "git@github.com:owner/repo@backup.git", "github.com:owner/backup.git"} {
		if err := validateRemote(remote); err != nil {
			t.Errorf("validateRemote(%q): %v", remote, err)
		}
	}
	for _, remote := range []string{"ssh://git:secret@example.invalid/repo.git", "https://token@example.invalid/repo.git"} {
		if err := validateRemote(remote); err == nil {
			t.Errorf("validateRemote(%q) accepted inline credential", remote)
		}
	}
}

func TestInitRejectsInvalidRecipientBeforeMutation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "private", "backup.json")
	identityPath := filepath.Join(root, "private", "identity.txt")
	repo := filepath.Join(root, "checkout")
	_, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       repo,
		Identity:   identityPath,
		Recipients: []string{"not-an-age-recipient"},
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "parse age recipient") {
		t.Fatalf("Init error = %v, want recipient rejection", err)
	}
	for _, path := range []string{configPath, identityPath, repo} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("Init mutated %s before recipient rejection: %v", path, statErr)
		}
	}
}

func TestStatusRejectsUnencryptedManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "private", "backup.json")
	if err := SaveConfig(configPath, Config{Repo: repo, Identity: filepath.Join(root, "private", "identity.txt")}); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Format: 1, Encrypted: false, ExportedAt: "2026-08-15T10:00:00Z"}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ManifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Status(context.Background(), Options{ConfigPath: configPath})
	if err == nil || !strings.Contains(err.Error(), "encrypted=false") {
		t.Fatalf("Status error = %v, want unencrypted rejection", err)
	}
}

func TestInitRefusesToPushArbitraryExistingHistory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, recoveryReadmeFilename), []byte(backupReadmeBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "private.sqlite"), []byte("synthetic private marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "add", ".")
	gitCommand(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "unrelated history")
	gitCommand(t, repo, "remote", "add", "origin", remote)
	_, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       repo,
		Remote:     remote,
		Push:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to push existing backup checkout history") {
		t.Fatalf("Init error = %v, want history rejection", err)
	}
	if refs := gitCommand(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname)"); strings.TrimSpace(refs) != "" {
		t.Fatalf("arbitrary history reached remote: %q", refs)
	}
}

func TestInitPushRefusalLeavesGeneratedReadmeUnstagedAndRetryable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("unrelated history\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "add", "--", "unrelated.txt")
	gitCommand(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "unrelated history")
	gitCommand(t, repo, "remote", "add", "origin", remote)
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       repo,
		Remote:     remote,
		Push:       true,
	}

	_, err := Init(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "not present on origin") {
		t.Fatalf("Init error = %v, want history rejection", err)
	}
	if staged := gitCommand(t, repo, "diff", "--cached", "--name-only", "--", recoveryReadmeFilename); strings.TrimSpace(staged) != "" {
		t.Fatalf("generated README left staged after rejection: %q", staged)
	}
	opts.Push = false
	result, err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("no-push retry: %v", err)
	}
	if !result.Changed || result.Pushed {
		t.Fatalf("no-push retry result = %+v, want local commit only", result)
	}
}

func TestInitDisablesRepositoryHooks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	gitCommand(t, repo, "config", "commit.gpgSign", "true")
	gitCommand(t, repo, "config", "user.signingKey", filepath.Join(root, "missing-signing-key"))
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       repo,
		Push:       false,
	}

	result, err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !result.Changed {
		t.Fatalf("Init result = %+v, want setup commit", result)
	}
}

func TestInitPrunesStaleOriginRefsBeforePushAuthorization(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote := filepath.Join(root, "empty-remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, recoveryReadmeFilename), []byte(backupReadmeBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "private.sqlite"), []byte("synthetic private marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "add", ".")
	gitCommand(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "unrelated history")
	gitCommand(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitCommand(t, repo, "remote", "add", "origin", remote)
	_, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       repo,
		Remote:     remote,
		Push:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to push existing backup checkout history") {
		t.Fatalf("Init error = %v, want stale-ref-safe rejection", err)
	}
	if refs := gitCommand(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname)"); strings.TrimSpace(refs) != "" {
		t.Fatalf("arbitrary history reached remote: %q", refs)
	}
}

func TestInitRetriesSetupCommitOnVerifiedRemoteParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, seed, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "existing.txt"), []byte("public backup repository metadata\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, seed, "add", "existing.txt")
	gitCommand(t, seed, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "existing remote history")
	gitCommand(t, seed, "remote", "add", "origin", remote)
	gitCommand(t, seed, "push", "-u", "origin", "main")
	gitCommand(t, "", "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")

	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Push:       false,
	}
	first, err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("local Init: %v", err)
	}
	if !first.Changed || first.Pushed {
		t.Fatalf("local Init result = %+v", first)
	}
	opts.Push = true
	second, err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("retrying Init: %v", err)
	}
	if second.Changed || !second.Pushed {
		t.Fatalf("retry result = %+v, want unchanged and pushed", second)
	}
	remoteHead := strings.TrimSpace(gitCommand(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main"))
	localHead := strings.TrimSpace(gitCommand(t, opts.Repo, "rev-parse", "HEAD"))
	if remoteHead != localHead {
		t.Fatalf("remote HEAD %s != local setup HEAD %s", remoteHead, localHead)
	}
}

func TestInitRejectsMismatchedOriginPushURL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fetchRemote := filepath.Join(root, "fetch.git")
	pushRemote := filepath.Join(root, "push.git")
	gitCommand(t, "", "init", "--bare", fetchRemote)
	gitCommand(t, "", "init", "--bare", pushRemote)
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	gitCommand(t, repo, "remote", "add", "origin", fetchRemote)
	gitCommand(t, repo, "remote", "set-url", "--push", "origin", pushRemote)
	_, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       repo,
		Remote:     fetchRemote,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "push URL") {
		t.Fatalf("Init error = %v, want mismatched push URL rejection", err)
	}
}

func TestInitIsIdempotentWithWhitespaceInLocalRemotePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote := filepath.Join(root, "Health Backup.git")
	gitCommand(t, "", "init", "--bare", remote)
	opts := Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       filepath.Join(root, "checkout"),
		Remote:     remote,
		Push:       false,
	}
	if _, err := Init(context.Background(), opts); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	result, err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if result.Changed || result.Pushed {
		t.Fatalf("second Init result = %+v, want unchanged local setup", result)
	}
}

func TestStatusRejectsSymlinkedAndOversizedManifest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, manifestPath, outsidePath string)
		want  string
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, manifestPath, outsidePath string) {
				t.Helper()
				if err := os.WriteFile(outsidePath, []byte(`{"format":1,"encrypted":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outsidePath, manifestPath); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			want: "regular file",
		},
		{
			name: "oversized",
			setup: func(t *testing.T, manifestPath, _ string) {
				t.Helper()
				file, err := os.Create(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(maxManifestBytes + 1); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
			want: "too large",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			if err := os.Mkdir(repo, 0o700); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(root, "private", "backup.json")
			if err := SaveConfig(configPath, Config{Repo: repo, Identity: filepath.Join(root, "private", "identity.txt")}); err != nil {
				t.Fatal(err)
			}
			test.setup(t, filepath.Join(repo, ManifestFilename), filepath.Join(root, "outside.json"))
			_, err := Status(context.Background(), Options{ConfigPath: configPath})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Status error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStatusRejectsManifestTimestampWithOutputControls(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "private", "backup.json")
	if err := SaveConfig(configPath, Config{Repo: repo, Identity: filepath.Join(root, "private", "identity.txt")}); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Format: 1, Encrypted: true, ExportedAt: "2026-08-15T10:00:00Z\nforged: true"}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ManifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Status(context.Background(), Options{ConfigPath: configPath})
	if err == nil || !strings.Contains(err.Error(), "not RFC3339") {
		t.Fatalf("Status error = %v, want timestamp validation", err)
	}
}

func TestStatusInspectsExplicitRepoWithoutBackupConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Format: 1, Encrypted: true, ExportedAt: "2026-08-15T10:00:00Z", Shards: []ShardEntry{{Path: "data/example.age"}}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ManifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := Status(context.Background(), Options{ConfigPath: filepath.Join(root, "missing.json"), Repo: repo})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Status != StatusReady || !status.Encrypted || status.ShardCount != 1 {
		t.Fatalf("Status = %+v, want explicit repo manifest", status)
	}
}

func TestCloneRequiresOwnerOnlyExistingCheckout(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode check")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       repo,
		Remote:     remote,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("Init error = %v, want owner-only checkout rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("clone wrote into insecure checkout: %v", statErr)
	}
}

func TestInitRejectsInsecureExistingCheckoutBeforeGitOperation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode check")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	if err := os.Chmod(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "private", "backup.json")
	_, err := Init(context.Background(), Options{ConfigPath: configPath, Repo: repo, Push: false})
	if err == nil || !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("Init error = %v, want existing checkout permission rejection", err)
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Fatalf("config written despite insecure checkout: %v", statErr)
	}
}

func TestInitRejectsConfigIdentityPathCollisionBeforeWriting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sharedPath := filepath.Join(root, "private", "shared.json")
	_, err := Init(context.Background(), Options{
		ConfigPath: sharedPath,
		Repo:       filepath.Join(root, "checkout"),
		Identity:   sharedPath,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "paths must be different") {
		t.Fatalf("Init error = %v, want path collision rejection", err)
	}
	if _, statErr := os.Stat(sharedPath); !os.IsNotExist(statErr) {
		t.Fatalf("colliding path was written: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "checkout")); !os.IsNotExist(statErr) {
		t.Fatalf("checkout was created before path collision rejection: %v", statErr)
	}
}

func TestGitErrorRedactsResolvedRemoteURLs(t *testing.T) {
	t.Parallel()
	const secret = "synthetic-query-secret"
	message := "fatal: unable to access 'https://backup.example/repo.git?token=" + secret + "': connection failed"
	got := redactGitError(message, []string{"push", "origin", "HEAD"})
	if strings.Contains(got, secret) || !strings.Contains(got, "https://backup.example/repo.git") {
		t.Fatalf("redacted Git error = %q", got)
	}
}

func TestGitSafeEnvironmentDropsRepositoryAndProcessOverrides(t *testing.T) {
	t.Parallel()
	input := []string{
		"PATH=/usr/bin",
		"SSH_AUTH_SOCK=/private/agent.sock",
		"GIT_DIR=/outside/repo.git",
		"GIT_WORK_TREE=/outside/tree",
		"GIT_INDEX_FILE=/outside/index",
		"GIT_SSH_COMMAND=synthetic-command",
		"GIT_CONFIG_GLOBAL=/outside/global.gitconfig",
		"GIT_CONFIG_SYSTEM=/outside/system.gitconfig",
		"GIT_CONFIG_NOSYSTEM=1",
		"SSH_ASKPASS=/outside/askpass",
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=:99",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/outside/hooks",
		"Git_Dir=/outside/mixed-case.git",
		"git_work_tree=/outside/mixed-case-tree",
	}
	got := gitSafeEnvironment(input)
	want := []string{"PATH=/usr/bin", "SSH_AUTH_SOCK=/private/agent.sock"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gitSafeEnvironment = %v, want %v", got, want)
	}
}

func TestInitRejectsConfigSymlinkedParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	configPath := filepath.Join(aliasDir, "shared.json")
	identityPath := filepath.Join(realDir, "shared.json")
	_, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       filepath.Join(root, "checkout"),
		Identity:   identityPath,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("Init error = %v, want symlinked-parent rejection", err)
	}
	if _, statErr := os.Stat(identityPath); !os.IsNotExist(statErr) {
		t.Fatalf("aliased identity/config path was written: %v", statErr)
	}
}

func TestPrivatePathRecheckDetectsAliasCreatedAfterIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard links require platform-specific privileges")
	}
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "backup.json")
	identityPath := filepath.Join(root, "backup-age-identity.txt")
	if same, err := pathsReferToSameFile(configPath, identityPath); err != nil || same {
		t.Fatalf("initial path comparison = (%t, %v), want distinct nonexistent paths", same, err)
	}
	if _, err := EnsureIdentity(identityPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(identityPath, configPath); err != nil {
		t.Fatal(err)
	}
	if same, err := pathsReferToSameFile(configPath, identityPath); err != nil || !same {
		t.Fatalf("post-creation path comparison = (%t, %v), want same file", same, err)
	}
}

func TestInitRejectsPrivatePathsInsideCheckout(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		configPath func(root, repo string) string
		identity   func(root, repo string) string
	}{
		{
			name:       "config",
			configPath: func(_, repo string) string { return filepath.Join(repo, "private", "backup.json") },
			identity:   func(root, _ string) string { return filepath.Join(root, "private", "identity.txt") },
		},
		{
			name:       "identity",
			configPath: func(root, _ string) string { return filepath.Join(root, "private", "backup.json") },
			identity:   func(_, repo string) string { return filepath.Join(repo, "private", "identity.txt") },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := filepath.Join(root, "checkout")
			_, err := Init(context.Background(), Options{
				ConfigPath: test.configPath(root, repo),
				Repo:       repo,
				Identity:   test.identity(root, repo),
				Push:       false,
			})
			if err == nil || !strings.Contains(err.Error(), "must be outside the Git checkout") {
				t.Fatalf("Init error = %v, want private-path containment rejection", err)
			}
			if _, statErr := os.Stat(repo); !os.IsNotExist(statErr) {
				t.Fatalf("checkout created despite path rejection: %v", statErr)
			}
		})
	}
}

func TestResolveOptionsMakesRelativeLocalRemoteAbsolute(t *testing.T) {
	t.Parallel()
	cfg, err := ResolveOptions(Options{
		ConfigPath: filepath.Join(t.TempDir(), "backup.json"),
		Repo:       filepath.Join(t.TempDir(), "repo"),
		Identity:   filepath.Join(t.TempDir(), "identity.txt"),
		Remote:     filepath.Join("relative", "remote.git"),
	})
	if err != nil {
		t.Fatalf("ResolveOptions: %v", err)
	}
	if !filepath.IsAbs(cfg.Remote) {
		t.Fatalf("remote = %q, want absolute local path", cfg.Remote)
	}
}

func TestSaveConfigFailurePreservesExistingConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory mode failure")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "private")
	path := filepath.Join(dir, "backup.json")
	want := Config{Repo: filepath.Join(root, "original"), Identity: filepath.Join(dir, "identity.txt")}
	if err := SaveConfig(path, want); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	err := SaveConfig(path, Config{Repo: filepath.Join(root, "replacement"), Identity: want.Identity})
	if chmodErr := os.Chmod(dir, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil {
		t.Fatal("SaveConfig unexpectedly succeeded in non-writable directory")
	}
	got, found, loadErr := LoadConfig(path)
	if loadErr != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("preserved config = (%+v, %t, %v), want %+v", got, found, loadErr, want)
	}
}

func TestInitAddsRemoteAfterLocalOnlyInitialization(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	configPath := filepath.Join(root, "private", "backup.json")
	repo := filepath.Join(root, "checkout")
	if _, err := Init(context.Background(), Options{ConfigPath: configPath, Repo: repo, Push: false}); err != nil {
		t.Fatalf("local Init: %v", err)
	}
	if _, err := Init(context.Background(), Options{ConfigPath: configPath, Remote: remote, Push: false}); err != nil {
		t.Fatalf("remote-configuring Init: %v", err)
	}
	if got := strings.TrimSpace(gitCommand(t, repo, "remote", "get-url", "origin")); got != remote {
		t.Fatalf("origin = %q, want %q", got, remote)
	}
}

func TestInitRejectsDifferentExistingReadme(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, recoveryReadmeFilename), []byte("unrelated repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Init(context.Background(), Options{
		ConfigPath: filepath.Join(root, "private", "backup.json"),
		Repo:       repo,
		Push:       false,
	})
	if err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("Init error = %v, want README content rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "private", "backup.json")); !os.IsNotExist(statErr) {
		t.Fatalf("config written despite README rejection: %v", statErr)
	}
}

func TestWriteBackupReadmeAcceptsGeneratedCRLFWorktree(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"current": backupReadmeBodyCRLF(),
		"legacy":  legacyBackupReadmeBodyCRLF(),
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, recoveryReadmeFilename)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := writeBackupReadme(repo); err != nil {
				t.Fatalf("writeBackupReadme rejected generated CRLF worktree: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != body {
				t.Fatal("writeBackupReadme rewrote generated CRLF worktree")
			}
		})
	}
}

func TestGeneratedSnapshotCommitAndPulledTreeAcceptExactCRLFReadme(t *testing.T) {
	_, _, repo, _ := setupPullTestBackup(t, pullTestInput("crlf"))
	readmePath := filepath.Join(repo, recoveryReadmeFilename)
	if err := os.WriteFile(readmePath, []byte(backupReadmeBodyCRLF()), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := runGit(ctx, repo, "add", "--", recoveryReadmeFilename); err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, repo, "commit", "--amend", "--no-edit", "--no-gpg-sign"); err != nil {
		t.Fatal(err)
	}
	if !isGeneratedSnapshotCommit(ctx, repo, "HEAD") {
		t.Fatal("generated Snapshot commit with exact CRLF README was rejected")
	}
	manifest, found, err := readManifestIfPresent(repo)
	if err != nil || !found {
		t.Fatalf("read manifest: found=%t err=%v", found, err)
	}
	if err := validatePulledSnapshotTreeAtCommit(ctx, repo, "HEAD", manifest); err != nil {
		t.Fatalf("validate pulled CRLF Snapshot tree: %v", err)
	}
}

func TestResolveOptionsMakesPersistedPathsAbsolute(t *testing.T) {
	t.Parallel()
	cfg, err := ResolveOptions(Options{
		ConfigPath: filepath.Join(t.TempDir(), "backup.json"),
		Repo:       filepath.Join("relative", "backup-repo"),
		Identity:   filepath.Join("relative", "identity.txt"),
	})
	if err != nil {
		t.Fatalf("ResolveOptions: %v", err)
	}
	if !filepath.IsAbs(cfg.Repo) || !filepath.IsAbs(cfg.Identity) {
		t.Fatalf("resolved paths = repo %q identity %q, want absolute", cfg.Repo, cfg.Identity)
	}
}

func TestStatusMissingConfigStaysUninitializedWhenDefaultRepoExists(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	defaultRepo := filepath.Join(root, "home", "Projects", "backup-gohealthcli")
	if err := os.MkdirAll(defaultRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	status, err := Status(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Status != StatusUninitialized {
		t.Fatalf("status = %q, want %q", status.Status, StatusUninitialized)
	}
}

func TestStatusConfiguredMissingRepoReturnsError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "private", "backup.json")
	missingRepo := filepath.Join(root, "missing-checkout")
	if err := SaveConfig(configPath, Config{
		Repo:     missingRepo,
		Identity: filepath.Join(root, "private", "backup-age-identity.txt"),
	}); err != nil {
		t.Fatal(err)
	}

	_, err := Status(context.Background(), Options{ConfigPath: configPath})
	if err == nil || !strings.Contains(err.Error(), "configured backup repo path") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Status error = %v, want missing configured checkout error", err)
	}
}

func TestStatusExplicitMissingRepoReturnsError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	missingRepo := filepath.Join(root, "missing-checkout")

	_, err := Status(context.Background(), Options{
		ConfigPath: filepath.Join(root, "missing-config.json"),
		Repo:       missingRepo,
	})
	if err == nil || !strings.Contains(err.Error(), "backup repo path") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Status error = %v, want explicit missing checkout error", err)
	}
}

func TestStatusDoesNotRequireInitializationIdentityDefaults(t *testing.T) {
	t.Run("missing config without HOME", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("HOME", "")
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
		status, err := Status(context.Background(), Options{})
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.Status != StatusUninitialized {
			t.Fatalf("status = %q, want %q", status.Status, StatusUninitialized)
		}
	})

	t.Run("saved repo without identity", func(t *testing.T) {
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		if err := os.Mkdir(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(root, "private", "backup.json")
		if err := SaveConfig(configPath, Config{Repo: repo}); err != nil {
			t.Fatal(err)
		}
		status, err := Status(context.Background(), Options{ConfigPath: configPath})
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.Status != StatusEmpty {
			t.Fatalf("status = %q, want %q", status.Status, StatusEmpty)
		}
	})
}

func TestInitWithoutRemoteRequiresNoPushBeforeWritingState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "private", "backup.json")
	identityPath := filepath.Join(root, "private", "backup-age-identity.txt")
	_, err := Init(context.Background(), Options{
		ConfigPath: configPath,
		Repo:       filepath.Join(root, "checkout"),
		Identity:   identityPath,
		Push:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "pass --remote or use --no-push") {
		t.Fatalf("Init error = %v, want remote/no-push guidance", err)
	}
	for _, path := range []string{configPath, identityPath} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("Init wrote %s before rejecting missing remote: %v", path, statErr)
		}
	}
}

func TestStatusDoesNotRequireOrReadPrivateIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "private", "backup.json")
	if err := SaveConfig(configPath, Config{Repo: repo, Identity: filepath.Join(root, "missing-identity")}); err != nil {
		t.Fatal(err)
	}
	status, err := Status(context.Background(), Options{ConfigPath: configPath})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Status != StatusEmpty {
		t.Fatalf("Status = %q, want %q", status.Status, StatusEmpty)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}

func pullTestInput(marker string) PushInput {
	tables := []string{
		"connections",
		"data_points",
		"data_point_revisions",
		"data_point_attachments",
		"attachment_payloads",
		"rollups",
		"identity_snapshots",
		"sync_runs",
		"sync_cursors",
	}
	shards := make([]PlaintextShard, 0, len(tables))
	counts := Counts{}
	for _, table := range tables {
		shard := PlaintextShard{Table: table, Path: "data/" + table + ".jsonl.gz.age"}
		if table == "connections" && marker != "first" {
			shard.Rows = 1
			shard.JSONL = []byte(`{"marker":"` + marker + `"}` + "\n")
			counts.Connections = 1
		}
		shards = append(shards, shard)
	}
	return PushInput{
		SchemaVersion: 1,
		ExportedAt:    time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC),
		Counts:        counts,
		Shards:        shards,
	}
}

func setupPullTestBackup(t *testing.T, input PushInput) (root, configPath, repo, remote string) {
	t.Helper()
	root = t.TempDir()
	remote = filepath.Join(root, "remote.git")
	gitCommand(t, "", "init", "--bare", remote)
	configPath = filepath.Join(root, "private", "backup.json")
	repo = filepath.Join(root, "checkout")
	identity := filepath.Join(root, "private", "backup-age-identity.txt")
	if _, err := Init(context.Background(), Options{ConfigPath: configPath, Repo: repo, Remote: remote, Identity: identity, Push: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(context.Background(), Options{ConfigPath: configPath, Push: true}, input); err != nil {
		t.Fatal(err)
	}
	return root, configPath, repo, remote
}

func writePullTestManifest(t *testing.T, repo string, manifest Manifest) {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(repo, ManifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+t.TempDir(),
		"GIT_AUTHOR_NAME=gohealthcli test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=gohealthcli test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

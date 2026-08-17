package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
)

// PullCurrent refreshes the configured encrypted backup checkout, authenticates
// every manifest-owned shard, and passes one complete plaintext Snapshot capture
// to restore. The callback owns Snapshot decoding, validation, and target restore.
func PullCurrent(ctx context.Context, opts Options, restore func(PullInput) error) (result PullResult, err error) {
	if restore == nil {
		return PullResult{}, errors.New("Health Archive Snapshot restore callback is required")
	}
	configPath, err := absoluteConfigPath(opts.ConfigPath)
	if err != nil {
		return PullResult{}, err
	}
	if _, statErr := os.Lstat(configPath); errors.Is(statErr, os.ErrNotExist) {
		return PullResult{}, errors.New("backup is not initialized; run `gohealthcli backup init` first")
	} else if statErr != nil {
		return PullResult{}, statErr
	}
	configLock, err := lockBackupConfig(configPath)
	if err != nil {
		return PullResult{}, fmt.Errorf("lock backup config: %w", err)
	}
	defer func() {
		if unlockErr := unlockBackupConfig(configLock); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock backup config: %w", unlockErr))
		}
	}()
	cfg, found, err := resolveOptions(opts)
	if err != nil {
		return PullResult{}, err
	}
	if !found {
		return PullResult{}, errors.New("backup is not initialized; run `gohealthcli backup init` first")
	}
	if strings.TrimSpace(cfg.Remote) == "" {
		return PullResult{}, errors.New("backup pull requires a configured Git remote; run `gohealthcli backup init --remote <url>` first")
	}
	if err := validateRemote(cfg.Remote); err != nil {
		return PullResult{}, err
	}
	privatePaths := []struct {
		label string
		path  string
	}{
		{label: "config", path: configPath},
		{label: "age identity", path: cfg.Identity},
	}
	for _, privatePath := range privatePaths {
		inside, err := pathIsWithin(cfg.Repo, privatePath.path)
		if err != nil {
			return PullResult{}, fmt.Errorf("compare backup repo and %s paths: %w", privatePath.label, err)
		}
		if inside {
			return PullResult{}, fmt.Errorf("backup %s path must be outside the Git checkout", privatePath.label)
		}
	}
	identityPath, _, identityFound, err := inspectIdentity(cfg.Identity)
	if err != nil {
		return PullResult{}, err
	}
	if !identityFound {
		return PullResult{}, errors.New("backup age identity is missing; run `gohealthcli backup init` first")
	}
	identity, err := identityFromFile(identityPath)
	if err != nil {
		return PullResult{}, err
	}
	if err := prepareCheckoutLockParent(cfg.Repo); err != nil {
		return PullResult{}, err
	}
	checkoutLock, err := lockBackupCheckout(cfg.Repo)
	if err != nil {
		return PullResult{}, fmt.Errorf("lock backup checkout: %w", err)
	}
	defer func() {
		if unlockErr := unlockBackupCheckout(checkoutLock); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock backup checkout: %w", unlockErr))
		}
	}()
	gitDir := filepath.Join(cfg.Repo, ".git")
	if info, statErr := os.Lstat(gitDir); errors.Is(statErr, os.ErrNotExist) {
		return PullResult{}, errors.New("backup checkout is missing; run `gohealthcli backup init` first")
	} else if statErr != nil {
		return PullResult{}, statErr
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return PullResult{}, fmt.Errorf("backup checkout %s has an invalid .git entry", cfg.Repo)
	}
	if err := ensureRepo(ctx, cfg); err != nil {
		return PullResult{}, err
	}
	cfg, err = recoverInterruptedSnapshotPublication(ctx, cfg, configPath)
	if err != nil {
		return PullResult{}, err
	}
	pendingCommits, err := pullBackupCheckout(ctx, cfg.Repo, identity)
	if err != nil {
		return PullResult{}, err
	}
	if !slices.Equal(cfg.PendingCommits, pendingCommits) {
		cfg.PendingCommits = pendingCommits
		if err := SaveConfig(configPath, cfg); err != nil {
			return PullResult{}, fmt.Errorf("record rebased local backup commits: %w", err)
		}
	}
	manifest, found, err := readManifestIfPresent(cfg.Repo)
	if err != nil {
		return PullResult{}, err
	}
	if !found {
		return PullResult{}, errors.New("backup checkout has no encrypted Health Archive manifest; run `gohealthcli backup push` first")
	}
	if err := validatePullManifest(manifest); err != nil {
		return PullResult{}, err
	}
	if err := validatePulledSnapshotCheckout(ctx, cfg.Repo, manifest); err != nil {
		return PullResult{}, err
	}
	shards := make([]PlaintextShard, 0, len(manifest.Shards))
	for _, entry := range manifest.Shards {
		plaintext, err := decryptVerifiedShard(cfg.Repo, entry, identity)
		if err != nil {
			return PullResult{}, fmt.Errorf("restore Health Archive Snapshot shard %q: %w", entry.Table, err)
		}
		shards = append(shards, PlaintextShard{Table: entry.Table, Path: entry.Path, Rows: entry.Rows, JSONL: plaintext})
	}
	if err := restore(PullInput{SchemaVersion: manifest.HealthArchiveSchemaVersion, Counts: manifest.Counts, Shards: shards}); err != nil {
		return PullResult{}, err
	}
	return PullResult{
		RepoPath:   cfg.Repo,
		Changed:    true,
		Encrypted:  true,
		ShardCount: len(manifest.Shards),
		Counts:     manifest.Counts,
	}, nil
}

func pullBackupCheckout(ctx context.Context, repo string, identity age.Identity) ([]string, error) {
	status, err := gitOutput(ctx, repo, "status", "--porcelain=v1", "--untracked-files=all", "--", recoveryReadmeFilename, ManifestFilename, "data")
	if err != nil {
		return nil, fmt.Errorf("inspect backup checkout before pull: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return nil, errors.New("backup-owned paths have uncommitted changes; commit or discard them before backup pull")
	}
	currentManifest, currentFound, err := readManifestIfPresent(repo)
	if err != nil {
		return nil, err
	}
	if currentFound {
		if err := validatePullManifest(currentManifest); err != nil {
			return nil, err
		}
		if err := validatePulledSnapshotCheckout(ctx, repo, currentManifest); err != nil {
			return nil, err
		}
	} else if err := validateEmptyBackupCheckout(ctx, repo); err != nil {
		return nil, err
	}
	originalHead, branch, err := exactHeadAndBranch(ctx, repo)
	if err != nil {
		return nil, err
	}
	if !isAuthorizedBackupHead(ctx, repo, identity) {
		return nil, errors.New("local backup history is not owned by authenticated backup commands")
	}
	if err := runGit(ctx, repo, "fetch", "--prune", "--no-tags", "origin", branch); err != nil {
		return nil, fmt.Errorf("pull encrypted backup checkout: %w", err)
	}
	remoteManifest, err := readPullManifestAtCommit(ctx, repo, "FETCH_HEAD")
	if err != nil {
		return nil, err
	}
	if err := validatePullManifest(remoteManifest); err != nil {
		return nil, err
	}
	if err := validatePulledSnapshotTreeAtCommit(ctx, repo, "FETCH_HEAD", remoteManifest); err != nil {
		return nil, err
	}
	return rebaseBackupCommitsByTree(ctx, repo, branch, originalHead, identity)
}

func rebaseBackupCommitsByTree(ctx context.Context, repo, branch, originalHead string, identity age.Identity) ([]string, error) {
	remoteHeadText, err := gitOutput(ctx, repo, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return nil, err
	}
	remoteHead := strings.TrimSpace(remoteHeadText)
	commitsText, err := gitOutput(ctx, repo, "rev-list", "--reverse", originalHead, "--not", remoteHead)
	if err != nil {
		return nil, err
	}
	parent := remoteHead
	pending := make([]string, 0, len(strings.Fields(commitsText)))
	for _, commit := range strings.Fields(commitsText) {
		if isGeneratedReadmeCommitAt(ctx, repo, commit) {
			// The fetched snapshot already has a verified recovery README. A local
			// README-only repair carries no Snapshot state and need not be replayed.
			continue
		}
		if !isAuthenticatedGeneratedSnapshotCommit(ctx, repo, commit, identity) {
			return nil, errors.New("local backup history is not owned by authenticated backup commands")
		}
		treeText, err := gitOutput(ctx, repo, "rev-parse", commit+"^{tree}")
		if err != nil {
			return nil, err
		}
		tree := strings.TrimSpace(treeText)
		parentTreeText, err := gitOutput(ctx, repo, "rev-parse", parent+"^{tree}")
		if err != nil {
			return nil, err
		}
		if tree == strings.TrimSpace(parentTreeText) {
			continue
		}
		rebasedText, err := gitOutput(ctx, repo, "commit-tree", tree, "-p", parent, "-m", "backup: update encrypted Health Archive snapshot")
		if err != nil {
			return nil, fmt.Errorf("rebase encrypted backup snapshot as a complete tree: %w", err)
		}
		parent = strings.TrimSpace(rebasedText)
		pending = append(pending, parent)
	}
	if err := replaceCleanCheckoutHead(ctx, repo, branch, originalHead, parent); err != nil {
		return nil, fmt.Errorf("finish encrypted backup rebase: %w", err)
	}
	return pending, nil
}

func replaceCleanCheckoutHead(ctx context.Context, repo, branch, originalHead, targetHead string) error {
	if originalHead == targetHead {
		return nil
	}
	if err := runGit(ctx, repo, "read-tree", "--reset", "-u", targetHead); err != nil {
		_ = runGit(ctx, repo, "read-tree", "--reset", "-u", originalHead)
		return err
	}
	ref := "refs/heads/" + branch
	if err := runGit(ctx, repo, "update-ref", ref, targetHead, originalHead); err != nil {
		if restoreErr := runGit(ctx, repo, "read-tree", "--reset", "-u", originalHead); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore checkout after failed ref update: %w", restoreErr))
		}
		return err
	}
	return nil
}

func validateEmptyBackupCheckout(ctx context.Context, repo string) error {
	tracked, err := gitOutput(ctx, repo, "ls-files")
	if err != nil {
		return err
	}
	for _, trackedPath := range strings.Fields(tracked) {
		if trackedPath != recoveryReadmeFilename {
			return fmt.Errorf("tracked backup path %q is not owned by backup setup", trackedPath)
		}
	}
	return nil
}

func readPullManifestAtCommit(ctx context.Context, repo, commit string) (Manifest, error) {
	sizeText, err := gitOutput(ctx, repo, "cat-file", "-s", commit+":"+ManifestFilename)
	if err != nil {
		return Manifest{}, errors.New("pulled backup checkout has no encrypted Health Archive manifest")
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if err != nil || size < 0 || size > maxManifestBytes {
		return Manifest{}, fmt.Errorf("pulled backup manifest size %q is invalid or exceeds %d bytes", strings.TrimSpace(sizeText), maxManifestBytes)
	}
	data, err := gitOutput(ctx, repo, "show", commit+":"+ManifestFilename)
	if err != nil {
		return Manifest{}, err
	}
	if int64(len(data)) != size {
		return Manifest{}, errors.New("pulled backup manifest size changed while reading Git object")
	}
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("read pulled backup manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("pulled backup manifest contains trailing data")
	}
	return manifest, nil
}

func validatePulledSnapshotTreeAtCommit(ctx context.Context, repo, commit string, manifest Manifest) error {
	tree, err := gitOutput(ctx, repo, "ls-tree", "-r", "--name-only", commit)
	if err != nil {
		return err
	}
	want := map[string]struct{}{recoveryReadmeFilename: {}, ManifestFilename: {}}
	for _, shard := range manifest.Shards {
		want[shard.Path] = struct{}{}
	}
	got := make(map[string]struct{}, len(want))
	for _, trackedPath := range strings.Fields(tree) {
		if _, ok := want[trackedPath]; !ok {
			return fmt.Errorf("pulled backup path %q is not owned by the manifest", trackedPath)
		}
		got[trackedPath] = struct{}{}
	}
	for required := range want {
		if _, ok := got[required]; !ok {
			return fmt.Errorf("pulled backup manifest-owned path %q is missing", required)
		}
	}
	readmeSizeText, err := gitOutput(ctx, repo, "cat-file", "-s", commit+":"+recoveryReadmeFilename)
	if err != nil {
		return errors.New("pulled backup recovery README is missing")
	}
	readmeSize, err := strconv.ParseInt(strings.TrimSpace(readmeSizeText), 10, 64)
	if err != nil || readmeSize != int64(len(backupReadmeBody)) && readmeSize != int64(len(legacyBackupReadmeBody)) {
		return errors.New("pulled backup recovery README has unexpected size")
	}
	readme, err := gitOutput(ctx, repo, "show", commit+":"+recoveryReadmeFilename)
	if err != nil || readme != backupReadmeBody && readme != legacyBackupReadmeBody {
		return errors.New("pulled backup recovery README is missing or has unexpected content")
	}
	for _, shard := range manifest.Shards {
		sizeText, err := gitOutput(ctx, repo, "cat-file", "-s", commit+":"+shard.Path)
		if err != nil {
			return fmt.Errorf("inspect pulled backup shard %q: %w", shard.Path, err)
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
		if err != nil || size != shard.Bytes {
			return fmt.Errorf("pulled backup shard %q size %q, manifest declares %d", shard.Path, strings.TrimSpace(sizeText), shard.Bytes)
		}
	}
	return nil
}

func validatePullManifest(manifest Manifest) error {
	if manifest.Format != backupFormatVersion {
		return fmt.Errorf("unsupported backup manifest format %d", manifest.Format)
	}
	if !manifest.Encrypted {
		return errors.New("backup manifest declares encrypted=false")
	}
	if manifest.HealthArchiveSchemaVersion <= 0 {
		return fmt.Errorf("backup manifest Health Archive schema version %d must be positive", manifest.HealthArchiveSchemaVersion)
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.ExportedAt); err != nil {
		return fmt.Errorf("backup manifest exported_at is not RFC3339: %w", err)
	}
	if !slices.Equal(manifest.Recipients, normalizedStrings(manifest.Recipients)) || ValidateRecipients(manifest.Recipients) != nil {
		return errors.New("backup manifest age recipients are missing, invalid, duplicated, or unsorted")
	}
	if !validGeneratedCounts(manifest.Counts) {
		return errors.New("backup manifest has negative Health Archive counts")
	}
	if len(manifest.Shards) != len(knownGeneratedShardTables) {
		return fmt.Errorf("backup manifest has %d shards, want %d", len(manifest.Shards), len(knownGeneratedShardTables))
	}
	seen := make(map[string]struct{}, len(manifest.Shards))
	previousPath := ""
	for _, shard := range manifest.Shards {
		if !isKnownGeneratedShardTable(shard.Table) {
			return fmt.Errorf("unknown backup shard table %q", shard.Table)
		}
		if shard.Path != "data/"+shard.Table+".jsonl.gz.age" {
			return fmt.Errorf("backup shard %q has invalid path %q", shard.Table, shard.Path)
		}
		if _, duplicate := seen[shard.Table]; duplicate {
			return fmt.Errorf("duplicate backup shard table %q", shard.Table)
		}
		if previousPath != "" && shard.Path <= previousPath {
			return errors.New("backup manifest shard paths are not in canonical order")
		}
		seen[shard.Table] = struct{}{}
		previousPath = shard.Path
		if shard.Rows < 0 || shard.Bytes <= 0 || len(shard.SHA256) != sha256.Size*2 {
			return fmt.Errorf("backup shard %q has invalid row, byte, or hash metadata", shard.Table)
		}
		decoded, err := hex.DecodeString(shard.SHA256)
		if err != nil || hex.EncodeToString(decoded) != shard.SHA256 {
			return fmt.Errorf("backup shard %q has invalid SHA-256 %q", shard.Table, shard.SHA256)
		}
		if shard.Rows != manifestCountForTable(manifest.Counts, shard.Table) {
			return fmt.Errorf("backup shard %q declares %d rows, counts declare %d", shard.Table, shard.Rows, manifestCountForTable(manifest.Counts, shard.Table))
		}
	}
	return nil
}

func manifestCountForTable(counts Counts, table string) int {
	switch table {
	case "connections":
		return counts.Connections
	case "data_points":
		return counts.DataPoints
	case "data_point_revisions":
		return counts.DataPointRevisions
	case "data_point_attachments":
		return counts.DataPointAttachments
	case "attachment_payloads":
		return counts.AttachmentPayloads
	case "rollups":
		return counts.Rollups
	case "identity_snapshots":
		return counts.IdentitySnapshots
	case "sync_runs":
		return counts.SyncRuns
	case "sync_cursors":
		return counts.SyncCursors
	default:
		return -1
	}
}

func validatePulledSnapshotCheckout(ctx context.Context, repo string, manifest Manifest) error {
	if err := validateExistingSnapshotPaths(repo, manifest, true); err != nil {
		return err
	}
	tracked, err := gitOutput(ctx, repo, "ls-files")
	if err != nil {
		return fmt.Errorf("inspect pulled backup paths: %w", err)
	}
	allowed := map[string]struct{}{recoveryReadmeFilename: {}, ManifestFilename: {}}
	for _, shard := range manifest.Shards {
		allowed[shard.Path] = struct{}{}
	}
	for _, trackedPath := range strings.Fields(tracked) {
		if _, ok := allowed[trackedPath]; !ok {
			return fmt.Errorf("tracked backup path %q is not owned by the current manifest", trackedPath)
		}
	}
	for required := range allowed {
		if required == recoveryReadmeFilename {
			continue
		}
		output, err := gitOutput(ctx, repo, "ls-files", "--error-unmatch", "--", required)
		if err != nil || strings.TrimSpace(output) != required {
			return fmt.Errorf("backup manifest-owned path %q is not tracked by Git", required)
		}
	}
	return nil
}

func decryptVerifiedShard(repo string, entry ShardEntry, identity age.Identity) ([]byte, error) {
	shardPath, err := resolveShardPath(repo, entry.Path)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlinkedPathComponents(shardPath); err != nil {
		return nil, err
	}
	info, err := os.Lstat(shardPath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("backup shard %s must be a regular file, not a symlink or special file", shardPath)
	}
	if info.Size() != entry.Bytes {
		return nil, fmt.Errorf("backup shard %s size %d, manifest declares %d", shardPath, info.Size(), entry.Bytes)
	}
	file, err := os.Open(shardPath) // #nosec G304 -- manifest path is confined and lstat/fstat identity is checked.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("backup shard %s changed while being opened", shardPath)
	}
	limited := &io.LimitedReader{R: file, N: entry.Bytes}
	decrypted, err := age.Decrypt(limited, identity)
	if err != nil {
		return nil, fmt.Errorf("authenticate age ciphertext: %w", err)
	}
	gzipReader, err := gzip.NewReader(decrypted)
	if err != nil {
		return nil, fmt.Errorf("read gzip header: %w", err)
	}
	var plaintext bytes.Buffer
	hash := sha256.New()
	_, readErr := io.Copy(io.MultiWriter(&plaintext, hash), gzipReader)
	closeErr := gzipReader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read gzip payload: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close gzip payload: %w", closeErr)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != entry.SHA256 {
		return nil, fmt.Errorf("decrypted backup shard SHA-256 %s, manifest declares %s", got, entry.SHA256)
	}
	if limited.N != 0 {
		return nil, fmt.Errorf("backup shard %s was not read completely", shardPath)
	}
	return plaintext.Bytes(), nil
}

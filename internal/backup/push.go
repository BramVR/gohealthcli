package backup

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

const backupFormatVersion = 1

func validatePushInput(input PushInput) error {
	if input.SchemaVersion <= 0 {
		return fmt.Errorf("Health Archive Snapshot schema version %d must be positive", input.SchemaVersion)
	}
	if input.ExportedAt.IsZero() {
		return errors.New("Health Archive Snapshot export time is required")
	}
	if len(input.Shards) == 0 {
		return errors.New("Health Archive Snapshot has no logical shards")
	}
	tables := make(map[string]struct{}, len(input.Shards))
	paths := make(map[string]struct{}, len(input.Shards))
	for _, shard := range input.Shards {
		if shard.Table == "" || strings.Trim(shard.Table, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
			return fmt.Errorf("invalid Health Archive Snapshot shard table %q", shard.Table)
		}
		if _, duplicate := tables[shard.Table]; duplicate {
			return fmt.Errorf("duplicate Health Archive Snapshot shard table %q", shard.Table)
		}
		tables[shard.Table] = struct{}{}
		wantPath := "data/" + shard.Table + ".jsonl.gz.age"
		if shard.Path != wantPath {
			return fmt.Errorf("Health Archive Snapshot shard %q path %q, want %q", shard.Table, shard.Path, wantPath)
		}
		if _, duplicate := paths[shard.Path]; duplicate {
			return fmt.Errorf("duplicate Health Archive Snapshot shard path %q", shard.Path)
		}
		paths[shard.Path] = struct{}{}
		if shard.Rows < 0 {
			return fmt.Errorf("Health Archive Snapshot shard %q rows %d must not be negative", shard.Table, shard.Rows)
		}
		if shard.Rows == 0 && len(shard.JSONL) != 0 {
			return fmt.Errorf("Health Archive Snapshot shard %q has bytes for zero rows", shard.Table)
		}
		if shard.Rows > 0 && (!bytes.HasSuffix(shard.JSONL, []byte{'\n'}) || bytes.Count(shard.JSONL, []byte{'\n'}) != shard.Rows) {
			return fmt.Errorf("Health Archive Snapshot shard %q JSONL row count does not match %d", shard.Table, shard.Rows)
		}
	}
	return nil
}

func prepareEncryptedSnapshot(cfg Config, input PushInput, old Manifest, oldFound bool, identity age.Identity) (Manifest, map[string][]byte, bool, error) {
	recipients := normalizedStrings(cfg.Recipients)
	reuseEncrypted := oldFound && old.Format == backupFormatVersion && old.Encrypted && sameStrings(old.Recipients, recipients)
	entries := make([]ShardEntry, 0, len(input.Shards))
	ciphertexts := make(map[string][]byte, len(input.Shards))
	allReused := true
	for _, shard := range input.Shards {
		hash := sha256Hex(shard.JSONL)
		entry := ShardEntry{Table: shard.Table, Path: shard.Path, Rows: shard.Rows, SHA256: hash}
		if reuseEncrypted {
			if oldEntry, ok := old.entry(shard.Path); ok && oldEntry.Table == shard.Table && oldEntry.Rows == shard.Rows && oldEntry.SHA256 == hash {
				ciphertext, err := readVerifiedCiphertextShard(cfg.Repo, oldEntry, identity, shard.JSONL)
				if err == nil {
					entry.Bytes = int64(len(ciphertext))
					ciphertexts[entry.Path] = ciphertext
					entries = append(entries, entry)
					continue
				}
			}
		}
		allReused = false
		ciphertext, err := encryptJSONLShard(shard.JSONL, recipients)
		if err != nil {
			return Manifest{}, nil, false, fmt.Errorf("encrypt Health Archive Snapshot shard %q: %w", shard.Table, err)
		}
		entry.Bytes = int64(len(ciphertext))
		ciphertexts[entry.Path] = ciphertext
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	manifest := Manifest{
		Format:                     backupFormatVersion,
		HealthArchiveSchemaVersion: input.SchemaVersion,
		Encrypted:                  true,
		ExportedAt:                 input.ExportedAt.UTC().Format(time.RFC3339Nano),
		Recipients:                 recipients,
		Counts:                     input.Counts,
		Shards:                     entries,
	}
	if allReused && equivalentManifest(old, manifest) {
		return old, nil, true, nil
	}
	return manifest, ciphertexts, false, nil
}

func encryptJSONLShard(plaintext []byte, recipientStrings []string) ([]byte, error) {
	recipients := make([]age.Recipient, 0, len(recipientStrings))
	for _, value := range recipientStrings {
		recipient, err := age.ParseX25519Recipient(value)
		if err != nil {
			return nil, fmt.Errorf("parse age recipient: %w", err)
		}
		recipients = append(recipients, recipient)
	}
	if len(recipients) == 0 {
		return nil, errors.New("at least one age recipient is required")
	}
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	gzipWriter.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.Name = ""
	gzipWriter.Comment = ""
	if _, err := gzipWriter.Write(plaintext); err != nil {
		_ = gzipWriter.Close()
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	var encrypted bytes.Buffer
	ageWriter, err := age.Encrypt(&encrypted, recipients...)
	if err != nil {
		return nil, err
	}
	if _, err := ageWriter.Write(compressed.Bytes()); err != nil {
		_ = ageWriter.Close()
		return nil, err
	}
	if err := ageWriter.Close(); err != nil {
		return nil, err
	}
	return encrypted.Bytes(), nil
}

func publishEncryptedSnapshot(repo string, manifest Manifest, ciphertexts map[string][]byte) (err error) {
	stageRoot, err := os.MkdirTemp(filepath.Dir(repo), ".gohealthcli-backup-stage-*")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if published {
			return
		}
		if cleanupErr := os.RemoveAll(stageRoot); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("clean encrypted backup staging directory: %w", cleanupErr))
		}
	}()
	if err := os.Chmod(stageRoot, 0o700); err != nil {
		return err
	}
	for _, shard := range manifest.Shards {
		ciphertext, ok := ciphertexts[shard.Path]
		if !ok {
			return fmt.Errorf("missing prepared ciphertext for shard %q", shard.Path)
		}
		shardPath, err := resolveShardPath(stageRoot, shard.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(shardPath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(shardPath, ciphertext, 0o600); err != nil {
			return err
		}
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(stageRoot, ManifestFilename), manifestData, 0o600); err != nil {
		return err
	}
	if err := swapPublishedSnapshot(repo, stageRoot); err != nil {
		return err
	}
	published = true
	// The swap is authoritative. Cleanup is best-effort because a failure here
	// must not report publication failure after the checkout already changed;
	// the staging directory contains only owner-only encrypted material.
	_ = os.RemoveAll(stageRoot)
	return nil
}

func swapPublishedSnapshot(repo, stageRoot string) error {
	dataPath := filepath.Join(repo, "data")
	manifestPath := filepath.Join(repo, ManifestFilename)
	oldDataPath := filepath.Join(stageRoot, "old-data")
	oldManifestPath := filepath.Join(stageRoot, "old-manifest.json")
	oldData := false
	oldManifest := false
	if info, err := os.Lstat(dataPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("backup data path %s must be a directory, not a symlink or special file", dataPath)
		}
		if err := os.Rename(dataPath, oldDataPath); err != nil {
			return err
		}
		oldData = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rollbackData := func() error {
		var rollbackErrors []error
		if err := os.Rename(dataPath, filepath.Join(stageRoot, "failed-data")); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, err)
		}
		if oldData {
			if err := os.Rename(oldDataPath, dataPath); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
		}
		return errors.Join(rollbackErrors...)
	}
	if err := os.Rename(filepath.Join(stageRoot, "data"), dataPath); err != nil {
		if oldData {
			_ = os.Rename(oldDataPath, dataPath)
		}
		return err
	}
	if info, err := os.Lstat(manifestPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.Join(fmt.Errorf("backup manifest %s must be a regular file, not a symlink or special file", manifestPath), rollbackData())
		}
		if err := os.Rename(manifestPath, oldManifestPath); err != nil {
			return errors.Join(err, rollbackData())
		}
		oldManifest = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(err, rollbackData())
	}
	if err := os.Rename(filepath.Join(stageRoot, ManifestFilename), manifestPath); err != nil {
		var rollbackErrors []error
		if oldManifest {
			rollbackErrors = append(rollbackErrors, os.Rename(oldManifestPath, manifestPath))
		}
		rollbackErrors = append(rollbackErrors, rollbackData())
		return errors.Join(append([]error{err}, rollbackErrors...)...)
	}
	return nil
}

func readManifestIfPresent(repo string) (Manifest, bool, error) {
	manifestPath := filepath.Join(repo, ManifestFilename)
	info, err := os.Lstat(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Manifest{}, false, fmt.Errorf("backup manifest %s must be a regular file, not a symlink or special file", manifestPath)
	}
	if info.Size() > maxManifestBytes {
		return Manifest{}, false, fmt.Errorf("backup manifest is too large: %d bytes, maximum %d", info.Size(), maxManifestBytes)
	}
	file, err := os.Open(manifestPath) // #nosec G304 -- lstat/fstat identity and bounded reads prevent repository-controlled traversal.
	if err != nil {
		return Manifest{}, false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return Manifest{}, false, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return Manifest{}, false, errors.New("backup manifest changed while being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, false, err
	}
	if len(data) > maxManifestBytes {
		return Manifest{}, false, fmt.Errorf("backup manifest exceeds maximum size %d", maxManifestBytes)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, false, fmt.Errorf("read backup manifest: %w", err)
	}
	if manifest.Format != backupFormatVersion {
		return Manifest{}, false, fmt.Errorf("unsupported backup manifest format %d", manifest.Format)
	}
	if !manifest.Encrypted {
		return Manifest{}, false, errors.New("backup manifest declares encrypted=false")
	}
	return manifest, true, nil
}

func validateExistingSnapshotPaths(repo string, manifest Manifest, manifestFound bool) error {
	dataRoot := filepath.Join(repo, "data")
	info, err := os.Lstat(dataRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("backup data path %s must be a directory, not a symlink or special file", dataRoot)
	}
	allowed := make(map[string]struct{}, len(manifest.Shards))
	if manifestFound {
		for _, shard := range manifest.Shards {
			full, err := resolveShardPath(repo, shard.Path)
			if err != nil {
				return err
			}
			allowed[full] = struct{}{}
		}
	}
	return filepath.WalkDir(dataRoot, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == dataRoot {
			return nil
		}
		if entry.IsDir() {
			return fmt.Errorf("backup data path %q is not owned by the current manifest", current)
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("backup data path %q is a symlink or special file", current)
		}
		if _, ok := allowed[current]; !ok {
			return fmt.Errorf("backup data path %q is not owned by the current manifest", current)
		}
		return nil
	})
}

func readVerifiedCiphertextShard(repo string, entry ShardEntry, identity age.Identity, wantPlaintext []byte) ([]byte, error) {
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
	file, err := os.Open(shardPath) // #nosec G304 -- resolveShardPath confines the manifest path; lstat/fstat identity rejects raced aliases.
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
	ciphertext, err := io.ReadAll(io.LimitReader(file, entry.Bytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(ciphertext)) != entry.Bytes {
		return nil, fmt.Errorf("backup shard %s changed size while being read", shardPath)
	}
	decrypted, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("authenticate age ciphertext: %w", err)
	}
	gzipReader, err := gzip.NewReader(decrypted)
	if err != nil {
		return nil, fmt.Errorf("read gzip header: %w", err)
	}
	plaintext, readErr := io.ReadAll(io.LimitReader(gzipReader, int64(len(wantPlaintext))+1))
	closeErr := gzipReader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read gzip payload: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close gzip payload: %w", closeErr)
	}
	if !bytes.Equal(plaintext, wantPlaintext) {
		return nil, errors.New("decrypted backup shard does not match current Health Archive Snapshot")
	}
	return ciphertext, nil
}

func resolveShardPath(repo, relative string) (string, error) {
	if strings.Contains(relative, `\`) || relative != path.Clean(relative) || path.IsAbs(relative) || !strings.HasPrefix(relative, "data/") || !strings.HasSuffix(relative, ".jsonl.gz.age") {
		return "", fmt.Errorf("invalid backup shard path %q", relative)
	}
	full := filepath.Join(repo, filepath.FromSlash(relative))
	dataRoot := filepath.Join(repo, "data")
	rel, err := filepath.Rel(dataRoot, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("backup shard path escapes backup root: %q", relative)
	}
	return full, nil
}

func (manifest Manifest) entry(shardPath string) (ShardEntry, bool) {
	for _, shard := range manifest.Shards {
		if shard.Path == shardPath {
			return shard, true
		}
	}
	return ShardEntry{}, false
}

func equivalentManifest(left, right Manifest) bool {
	if left.Format != right.Format || left.HealthArchiveSchemaVersion != right.HealthArchiveSchemaVersion || left.Encrypted != right.Encrypted || !sameStrings(left.Recipients, right.Recipients) || left.Counts != right.Counts || len(left.Shards) != len(right.Shards) {
		return false
	}
	for index := range left.Shards {
		leftEntry, rightEntry := left.Shards[index], right.Shards[index]
		leftEntry.Bytes = 0
		rightEntry.Bytes = 0
		if leftEntry != rightEntry {
			return false
		}
	}
	return true
}

func normalizedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sameStrings(left, right []string) bool {
	left = normalizedStrings(left)
	right = normalizedStrings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sha256Hex(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

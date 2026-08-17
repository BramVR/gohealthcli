package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ManifestFilename       = "manifest.json"
	recoveryReadmeFilename = "README.md"
	maxManifestBytes       = 16 << 20
	StatusUninitialized    = "backup_uninitialized"
	StatusEmpty            = "backup_empty"
	StatusReady            = "backup_ready"
)

type Counts struct {
	Connections          int `json:"connections"`
	DataPoints           int `json:"data_points"`
	DataPointRevisions   int `json:"data_point_revisions"`
	DataPointAttachments int `json:"data_point_attachments"`
	AttachmentPayloads   int `json:"attachment_payloads"`
	Rollups              int `json:"rollups"`
	IdentitySnapshots    int `json:"identity_snapshots"`
	SyncRuns             int `json:"sync_runs"`
	SyncCursors          int `json:"sync_cursors"`
}

type ShardEntry struct {
	Table  string `json:"table"`
	Path   string `json:"path"`
	Rows   int    `json:"rows"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Manifest struct {
	Format                     int          `json:"format"`
	HealthArchiveSchemaVersion int          `json:"health_archive_schema_version"`
	Encrypted                  bool         `json:"encrypted"`
	ExportedAt                 string       `json:"exported_at"`
	Recipients                 []string     `json:"recipients,omitempty"`
	Counts                     Counts       `json:"counts"`
	Shards                     []ShardEntry `json:"shards"`
}

type PlaintextShard struct {
	Table string
	Path  string
	Rows  int
	JSONL []byte
}

type PushInput struct {
	SchemaVersion int
	ExportedAt    time.Time
	Counts        Counts
	Shards        []PlaintextShard
}

type PushResult struct {
	RepoPath   string `json:"repo_path"`
	Changed    bool   `json:"changed"`
	Pushed     bool   `json:"pushed"`
	Encrypted  bool   `json:"encrypted"`
	ShardCount int    `json:"shard_count"`
	Counts     Counts `json:"health_archive_counts"`
}

type PullInput struct {
	SchemaVersion int
	Counts        Counts
	Shards        []PlaintextShard
}

type PullResult struct {
	RepoPath   string `json:"repo_path"`
	Changed    bool   `json:"changed"`
	Encrypted  bool   `json:"encrypted"`
	ShardCount int    `json:"shard_count"`
	Counts     Counts `json:"health_archive_counts"`
}

type InitResult struct {
	RepoPath  string `json:"repo_path"`
	Remote    string `json:"remote,omitempty"`
	Identity  string `json:"identity_path"`
	Recipient string `json:"recipient"`
	Changed   bool   `json:"changed"`
	Pushed    bool   `json:"pushed"`
}

type StatusResult struct {
	Status     string  `json:"status"`
	RepoPath   string  `json:"repo_path"`
	Encrypted  bool    `json:"encrypted"`
	ShardCount int     `json:"shard_count"`
	ExportedAt string  `json:"exported_at,omitempty"`
	Counts     *Counts `json:"health_archive_counts,omitempty"`
}

func Init(ctx context.Context, opts Options) (result InitResult, err error) {
	configPath, err := absoluteConfigPath(opts.ConfigPath)
	if err != nil {
		return InitResult{}, err
	}
	preflightCfg, _, err := resolveOptions(opts)
	if err != nil {
		return InitResult{}, err
	}
	configInsideRepo, err := pathIsWithin(preflightCfg.Repo, configPath)
	if err != nil {
		return InitResult{}, err
	}
	if configInsideRepo {
		return InitResult{}, errors.New("backup config path must be outside the Git checkout")
	}
	if err := ensurePrivateDir(filepath.Dir(configPath)); err != nil {
		return InitResult{}, err
	}
	configLock, err := lockBackupConfig(configPath)
	if err != nil {
		return InitResult{}, fmt.Errorf("lock backup config: %w", err)
	}
	defer func() {
		if unlockErr := unlockBackupConfig(configLock); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock backup config: %w", unlockErr))
		}
	}()
	cfg, _, err := resolveOptions(opts)
	if err != nil {
		return InitResult{}, err
	}
	if opts.Push && cfg.Remote == "" {
		return InitResult{}, errors.New("cannot push backup setup: pass --remote or use --no-push")
	}
	if err := validateRemote(cfg.Remote); err != nil {
		return InitResult{}, err
	}
	if len(cfg.Recipients) > 0 {
		if err := ValidateRecipients(cfg.Recipients); err != nil {
			return InitResult{}, err
		}
	}
	pathsCollide, err := pathsReferToSameFile(configPath, cfg.Identity)
	if err != nil {
		return InitResult{}, fmt.Errorf("compare backup config and identity paths: %w", err)
	}
	if pathsCollide {
		return InitResult{}, errors.New("backup config and age identity paths must be different")
	}
	for label, privatePath := range map[string]string{"config": configPath, "age identity": cfg.Identity} {
		inside, err := pathIsWithin(cfg.Repo, privatePath)
		if err != nil {
			return InitResult{}, fmt.Errorf("compare backup repo and %s paths: %w", label, err)
		}
		if inside {
			return InitResult{}, fmt.Errorf("backup %s path must be outside the Git checkout", label)
		}
	}
	if err := validatePrivateParentIfPresent(configPath); err != nil {
		return InitResult{}, err
	}
	if err := preflightIdentity(cfg.Identity); err != nil {
		return InitResult{}, err
	}
	if err := prepareCheckoutLockParent(cfg.Repo); err != nil {
		return InitResult{}, err
	}
	checkoutLock, err := lockBackupCheckout(cfg.Repo)
	if err != nil {
		return InitResult{}, fmt.Errorf("lock backup checkout: %w", err)
	}
	defer func() {
		if unlockErr := unlockBackupCheckout(checkoutLock); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock backup checkout: %w", unlockErr))
		}
	}()
	if err := ensureRepo(ctx, cfg); err != nil {
		return InitResult{}, err
	}
	cfg, err = recoverInterruptedSnapshotPublication(ctx, cfg, configPath)
	if err != nil {
		return InitResult{}, err
	}
	cfg, migratedPending, err := migrateLegacyPendingSetupCommit(ctx, cfg)
	if err != nil {
		return InitResult{}, err
	}
	if migratedPending {
		if err := SaveConfig(opts.ConfigPath, cfg); err != nil {
			return InitResult{}, err
		}
	}
	if err := ensureReadmeIndexClean(ctx, cfg.Repo); err != nil {
		return InitResult{}, err
	}
	if err := writeBackupReadme(cfg.Repo); err != nil {
		return InitResult{}, err
	}
	recipient, err := EnsureIdentity(cfg.Identity)
	if err != nil {
		return InitResult{}, err
	}
	pathsCollide, err = pathsReferToSameFile(configPath, cfg.Identity)
	if err != nil {
		return InitResult{}, fmt.Errorf("recheck backup config and identity paths: %w", err)
	}
	if pathsCollide {
		return InitResult{}, errors.New("backup config and age identity paths resolved to the same file after identity creation")
	}
	if cfg.LocalRecipient != "" && cfg.LocalRecipient != recipient && !containsRecipient(opts.Recipients, cfg.LocalRecipient) {
		cfg.Recipients = withoutRecipient(cfg.Recipients, cfg.LocalRecipient)
	}
	cfg.LocalRecipient = recipient
	cfg.Recipients = recipientsWithLocalIdentity(recipient, cfg.Recipients)
	if err := ValidateRecipients(cfg.Recipients); err != nil {
		return InitResult{}, err
	}
	if err := SaveConfig(opts.ConfigPath, cfg); err != nil {
		return InitResult{}, err
	}
	changed, pushed, err := commitReadmeAndMaybePush(ctx, cfg, opts.Push)
	savePending := false
	switch {
	case pushed:
		cfg.PendingCommits = nil
		savePending = true
	case changed && isGeneratedReadmeCommit(ctx, cfg.Repo):
		head, headErr := gitOutput(ctx, cfg.Repo, "rev-parse", "HEAD")
		if headErr != nil {
			err = errors.Join(err, headErr)
		} else {
			head = strings.TrimSpace(head)
			alreadyPending := false
			for _, pending := range cfg.PendingCommits {
				alreadyPending = alreadyPending || pending == head
			}
			if !alreadyPending {
				cfg.PendingCommits = append(cfg.PendingCommits, head)
			}
			savePending = true
		}
	}
	if savePending {
		if saveErr := SaveConfig(opts.ConfigPath, cfg); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}
	result = InitResult{
		RepoPath:  cfg.Repo,
		Remote:    redactRemote(cfg.Remote),
		Identity:  cfg.Identity,
		Recipient: recipient,
		Changed:   changed,
		Pushed:    pushed,
	}
	return result, err
}

func Push(ctx context.Context, opts Options, input PushInput) (result PushResult, err error) {
	return pushCurrent(ctx, opts, func() (PushInput, error) { return input, nil })
}

// PushCurrent holds the backup checkout lock while build captures the current
// Health Archive Snapshot, then encrypts and commits that exact capture.
func PushCurrent(ctx context.Context, opts Options, build func() (PushInput, error)) (result PushResult, err error) {
	if build == nil {
		return PushResult{}, errors.New("Health Archive Snapshot builder is required")
	}
	return pushCurrent(ctx, opts, build)
}

func pushCurrent(ctx context.Context, opts Options, build func() (PushInput, error)) (result PushResult, err error) {
	configPath, err := absoluteConfigPath(opts.ConfigPath)
	if err != nil {
		return PushResult{}, err
	}
	if err := ensurePrivateDir(filepath.Dir(configPath)); err != nil {
		return PushResult{}, err
	}
	configLock, err := lockBackupConfig(configPath)
	if err != nil {
		return PushResult{}, fmt.Errorf("lock backup config: %w", err)
	}
	defer func() {
		if unlockErr := unlockBackupConfig(configLock); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock backup config: %w", unlockErr))
		}
	}()
	cfg, _, err := resolveOptions(opts)
	if err != nil {
		return PushResult{}, err
	}
	if opts.Push && cfg.Remote == "" {
		return PushResult{}, errors.New("cannot push backup: pass --remote or use --no-push")
	}
	if err := validateRemote(cfg.Remote); err != nil {
		return PushResult{}, err
	}
	cfg.Recipients = normalizedStrings(cfg.Recipients)
	if err := ValidateRecipients(cfg.Recipients); err != nil {
		return PushResult{}, err
	}
	identityPath, localRecipient, identityFound, err := inspectIdentity(cfg.Identity)
	if err != nil {
		return PushResult{}, err
	}
	if !identityFound {
		return PushResult{}, errors.New("backup age identity is missing; run `gohealthcli backup init` first")
	}
	if !containsRecipient(cfg.Recipients, localRecipient) {
		return PushResult{}, errors.New("backup recipients do not include the local age identity; run `gohealthcli backup init` to repair the configuration")
	}
	identity, err := identityFromFile(identityPath)
	if err != nil {
		return PushResult{}, err
	}
	if err := prepareCheckoutLockParent(cfg.Repo); err != nil {
		return PushResult{}, err
	}
	checkoutLock, err := lockBackupCheckout(cfg.Repo)
	if err != nil {
		return PushResult{}, fmt.Errorf("lock backup checkout: %w", err)
	}
	defer func() {
		if unlockErr := unlockBackupCheckout(checkoutLock); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock backup checkout: %w", unlockErr))
		}
	}()
	if err := ensureRepo(ctx, cfg); err != nil {
		return PushResult{}, err
	}
	cfg, err = recoverInterruptedSnapshotPublication(ctx, cfg, configPath)
	if err != nil {
		return PushResult{}, err
	}
	cfg, migratedPending, err := migrateLegacyPendingSetupCommit(ctx, cfg)
	if err != nil {
		return PushResult{}, err
	}
	if migratedPending {
		if err := SaveConfig(opts.ConfigPath, cfg); err != nil {
			return PushResult{}, err
		}
	}
	if err := runGit(ctx, cfg.Repo, "rev-parse", "--verify", "HEAD"); err != nil {
		return PushResult{}, errors.New("backup checkout has no setup commit; run `gohealthcli backup init` first")
	}
	input, err := build()
	if err != nil {
		return PushResult{}, err
	}
	if err := validatePushInput(input); err != nil {
		return PushResult{}, err
	}
	oldManifest, oldManifestFound, err := readManifestIfPresent(cfg.Repo)
	if err != nil {
		return PushResult{}, err
	}
	gitState, err := preflightSnapshotCommit(ctx, cfg, opts.Push, oldManifest, oldManifestFound, identity)
	if err != nil {
		return PushResult{}, err
	}
	manifest, ciphertexts, unchanged, err := prepareEncryptedSnapshot(cfg, input, oldManifest, oldManifestFound, identity)
	if err != nil {
		return PushResult{}, err
	}
	if err := validateNoGitContentAttributes(ctx, cfg.Repo, manifest); err != nil {
		return PushResult{}, err
	}
	readmePath := filepath.Join(cfg.Repo, recoveryReadmeFilename)
	_, readmeStatErr := os.Lstat(readmePath)
	readmeWasMissing := errors.Is(readmeStatErr, os.ErrNotExist)
	if readmeStatErr != nil && !readmeWasMissing {
		return PushResult{}, readmeStatErr
	}
	publicationStarted := readmeWasMissing || !unchanged
	if publicationStarted {
		if err := beginSnapshotPublication(cfg.Repo, gitState.headOID); err != nil {
			return PushResult{}, err
		}
		defer func() {
			if !publicationStarted {
				return
			}
			var recoverErr error
			cfg, recoverErr = recoverInterruptedSnapshotPublication(ctx, cfg, configPath)
			if recoverErr != nil {
				err = errors.Join(err, fmt.Errorf("recover snapshot publication: %w", recoverErr))
			}
		}()
	}
	if err := writeBackupReadme(cfg.Repo); err != nil {
		return PushResult{}, err
	}
	if !unchanged {
		if err := publishEncryptedSnapshot(cfg.Repo, manifest, ciphertexts); err != nil {
			if readmeWasMissing {
				err = errors.Join(err, os.Remove(readmePath))
			}
			return PushResult{}, err
		}
	}
	commitResult, err := commitSnapshotAndMaybePush(ctx, cfg, opts.Push, gitState, manifest)
	if err != nil && !commitResult.Changed && (!unchanged || readmeWasMissing) {
		if restoreErr := restorePublishedSnapshot(ctx, cfg.Repo, oldManifest, oldManifestFound, manifest); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore previous backup snapshot after Git failure: %w", restoreErr))
		}
	}
	if commitResult.Pushed {
		previousPending := append([]string(nil), cfg.PendingCommits...)
		cfg.PendingCommits = nil
		if saveErr := SaveConfig(opts.ConfigPath, cfg); saveErr != nil {
			cfg.PendingCommits = previousPending
			err = errors.Join(err, saveErr)
		}
	} else if commitResult.Changed {
		previousPending := append([]string(nil), cfg.PendingCommits...)
		cfg.PendingCommits = append(cfg.PendingCommits, commitResult.HeadOID)
		if saveErr := SaveConfig(opts.ConfigPath, cfg); saveErr != nil {
			if rollbackErr := rollbackUnprovenancedSnapshotCommit(ctx, cfg.Repo, gitState.headOID, commitResult.HeadOID, oldManifest, oldManifestFound, manifest); rollbackErr != nil {
				saveErr = errors.Join(saveErr, rollbackErr)
			}
			cfg.PendingCommits = previousPending
			if restoreConfigErr := SaveConfig(opts.ConfigPath, cfg); restoreConfigErr != nil {
				saveErr = errors.Join(saveErr, restoreConfigErr)
			}
			err = errors.Join(err, saveErr)
		}
	}
	result = PushResult{
		RepoPath:   cfg.Repo,
		Changed:    commitResult.Changed,
		Pushed:     commitResult.Pushed,
		Encrypted:  true,
		ShardCount: len(manifest.Shards),
		Counts:     manifest.Counts,
	}
	return result, err
}

func Status(_ context.Context, opts Options) (StatusResult, error) {
	cfg, configFound, err := resolveStatusOptions(opts)
	if err != nil {
		return StatusResult{}, err
	}
	result := StatusResult{Status: StatusUninitialized, RepoPath: cfg.Repo}
	if !configFound && strings.TrimSpace(opts.Repo) == "" {
		return result, nil
	}
	if info, err := os.Stat(cfg.Repo); errors.Is(err, os.ErrNotExist) {
		if configFound {
			return result, fmt.Errorf("configured backup repo path %s does not exist", cfg.Repo)
		}
		if strings.TrimSpace(opts.Repo) != "" {
			return result, fmt.Errorf("backup repo path %s does not exist", cfg.Repo)
		}
		return result, nil
	} else if err != nil {
		return result, err
	} else if !info.IsDir() {
		return result, fmt.Errorf("backup repo path %s is not a directory", cfg.Repo)
	}

	manifestPath := filepath.Join(cfg.Repo, ManifestFilename)
	info, err := os.Lstat(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		result.Status = StatusEmpty
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return result, fmt.Errorf("backup manifest %s must be a regular file, not a symlink or special file", manifestPath)
	}
	if info.Size() > maxManifestBytes {
		return result, fmt.Errorf("backup manifest is too large: %d bytes, maximum %d", info.Size(), maxManifestBytes)
	}
	file, err := os.Open(manifestPath) // #nosec G304 -- lstat/fstat identity and bounded reads prevent repository-controlled traversal.
	if err != nil {
		return result, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return result, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return result, fmt.Errorf("backup manifest changed while being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	closeErr := file.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	if len(data) > maxManifestBytes {
		return result, fmt.Errorf("backup manifest exceeds maximum size %d", maxManifestBytes)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return result, fmt.Errorf("read backup manifest: %w", err)
	}
	if manifest.Format != 1 {
		return result, fmt.Errorf("unsupported backup manifest format %d", manifest.Format)
	}
	if !manifest.Encrypted {
		return result, errors.New("backup manifest declares encrypted=false")
	}
	exportedAt, err := time.Parse(time.RFC3339, manifest.ExportedAt)
	if err != nil {
		return result, fmt.Errorf("backup manifest exported_at is not RFC3339: %w", err)
	}
	result.Status = StatusReady
	result.Encrypted = manifest.Encrypted
	result.ShardCount = len(manifest.Shards)
	result.ExportedAt = exportedAt.UTC().Format(time.RFC3339Nano)
	result.Counts = &manifest.Counts
	return result, nil
}

func writeBackupReadme(repo string) error {
	path := filepath.Join(repo, recoveryReadmeFilename)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup README %s must not be a symlink", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("backup README %s is not a regular file", path)
		}
		if info.Size() != int64(len(backupReadmeBody)) && info.Size() != int64(len(legacyBackupReadmeBody)) {
			return fmt.Errorf("backup README %s already exists with different content", path)
		}
		data, err := os.ReadFile(path) // #nosec G304 -- regular repository path validated above.
		if err != nil {
			return err
		}
		if string(data) != backupReadmeBody && string(data) != legacyBackupReadmeBody {
			return fmt.Errorf("backup README %s already exists with different content", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(backupReadmeBody); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

const legacyBackupReadmeBody = `# backup-gohealthcli

Encrypted Git backup for a local gohealthcli Health Archive.

Backup payloads are encrypted with age before Git sees them. ` + "`gohealthcli backup init`" + ` only prepares the repository, config, and age identity; it does not export Health Archive data. When encrypted payloads are added, the repository still exposes cleartext metadata: backup time, public recipients, logical table names, record counts, shard paths, encrypted sizes, plaintext hashes, cadence, and changed shards.

Keep repository write access restricted. Never commit the local age identity, gohealthcli config, OAuth client secret, Credential Store material, raw SQLite Health Archive, or plaintext Attachment Store. Anyone who can write this repository can replace encrypted content even though they cannot decrypt it.

Store a private recovery copy of the age identity outside this repository. If every matching identity is lost, the encrypted backup cannot be restored. Removing a recipient protects future backups but does not rewrite old Git history.
`

const backupReadmeBody = `# backup-gohealthcli

Encrypted Git backup for a local gohealthcli Health Archive.

` + "`gohealthcli backup push`" + ` exports the current local Health Archive as deterministic JSONL, compresses each logical shard with fixed gzip metadata, and encrypts every shard with age before Git sees it. It does not sync or contact Google Health.

## Layout

` + "```text" + `
README.md
manifest.json
data/*.jsonl.gz.age
` + "```" + `

` + "`manifest.json`" + ` is cleartext. It reveals format and Health Archive schema versions, export time, public recipients, logical table names, record counts, shard paths, encrypted sizes, plaintext hashes, backup cadence, and which shards changed. Raw health JSON, Identity Snapshot payloads, Data Point values, and Data Point Attachment bytes stay inside age-encrypted shards.

Keep repository write access restricted. Never commit the local age identity, gohealthcli config, OAuth client secret, Credential Store material, raw SQLite Health Archive, or plaintext Attachment Store. Anyone who can write this repository can replace encrypted content even though they cannot decrypt it.

Store a private recovery copy of the age identity outside this repository. If every matching identity is lost, the encrypted backup cannot be restored. Removing a recipient protects future backups but does not rewrite old Git history.
`

func recipientsWithLocalIdentity(local string, configured []string) []string {
	out := make([]string, 0, len(configured)+1)
	seen := make(map[string]struct{}, len(configured)+1)
	for _, recipient := range append([]string{local}, configured...) {
		recipient = strings.TrimSpace(recipient)
		if recipient == "" {
			continue
		}
		if _, duplicate := seen[recipient]; duplicate {
			continue
		}
		seen[recipient] = struct{}{}
		out = append(out, recipient)
	}
	return out
}

func withoutRecipient(recipients []string, remove string) []string {
	out := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		if strings.TrimSpace(recipient) != remove {
			out = append(out, recipient)
		}
	}
	return out
}

func containsRecipient(recipients []string, wanted string) bool {
	for _, recipient := range recipients {
		if strings.TrimSpace(recipient) == wanted {
			return true
		}
	}
	return false
}

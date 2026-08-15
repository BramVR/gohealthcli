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
	Format     int          `json:"format"`
	Encrypted  bool         `json:"encrypted"`
	ExportedAt string       `json:"exported_at"`
	Recipients []string     `json:"recipients,omitempty"`
	Counts     Counts       `json:"counts"`
	Shards     []ShardEntry `json:"shards"`
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

func Init(ctx context.Context, opts Options) (InitResult, error) {
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
	configPath, err := absoluteConfigPath(opts.ConfigPath)
	if err != nil {
		return InitResult{}, err
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
	if err := ensureRepo(ctx, cfg); err != nil {
		return InitResult{}, err
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
	result := InitResult{
		RepoPath:  cfg.Repo,
		Remote:    redactRemote(cfg.Remote),
		Identity:  cfg.Identity,
		Recipient: recipient,
		Changed:   changed,
		Pushed:    pushed,
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
		if info.Size() != int64(len(backupReadmeBody)) {
			return fmt.Errorf("backup README %s already exists with different content", path)
		}
		data, err := os.ReadFile(path) // #nosec G304 -- regular repository path validated above.
		if err != nil {
			return err
		}
		if string(data) != backupReadmeBody {
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

const backupReadmeBody = `# backup-gohealthcli

Encrypted Git backup for a local gohealthcli Health Archive.

Backup payloads are encrypted with age before Git sees them. ` + "`gohealthcli backup init`" + ` only prepares the repository, config, and age identity; it does not export Health Archive data. When encrypted payloads are added, the repository still exposes cleartext metadata: backup time, public recipients, logical table names, record counts, shard paths, encrypted sizes, plaintext hashes, cadence, and changed shards.

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

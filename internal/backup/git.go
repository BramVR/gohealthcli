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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
)

func ensureRepo(ctx context.Context, cfg Config) error {
	if err := rejectSymlinkedPathComponents(cfg.Repo); err != nil {
		return err
	}
	gitDir := filepath.Join(cfg.Repo, ".git")
	if info, err := os.Lstat(gitDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup checkout %s has a symlinked .git entry", cfg.Repo)
		}
		if !info.IsDir() {
			return fmt.Errorf("backup checkout %s has a non-directory .git entry", cfg.Repo)
		}
		if err := ensurePrivateDir(cfg.Repo); err != nil {
			return err
		}
		return verifyRepoRemote(ctx, cfg)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if info, err := os.Lstat(cfg.Repo); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup repo path %s must not be a symlink", cfg.Repo)
		}
		if !info.IsDir() {
			return fmt.Errorf("backup repo path %s is not a directory", cfg.Repo)
		}
		entries, readErr := os.ReadDir(cfg.Repo)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return fmt.Errorf("backup repo path %s exists and is not an empty Git checkout", cfg.Repo)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if strings.TrimSpace(cfg.Remote) != "" {
		if err := ensurePrivateDir(cfg.Repo); err != nil {
			return err
		}
		if err := runGit(ctx, "", "clone", "--", cfg.Remote, cfg.Repo); err != nil {
			return err
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(cfg.Repo, 0o700); err != nil {
				return err
			}
		}
		return verifyRepoRemote(ctx, cfg)
	}
	if err := ensurePrivateDir(cfg.Repo); err != nil {
		return err
	}
	if err := runGit(ctx, cfg.Repo, "init", "-b", "main"); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Remote) != "" {
		if err := runGit(ctx, cfg.Repo, "remote", "add", "origin", cfg.Remote); err != nil {
			return err
		}
	}
	return nil
}

func prepareCheckoutLockParent(repo string) error {
	if err := rejectSymlinkedPathComponents(repo); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Dir(repo), 0o700)
}

func commitReadmeAndMaybePush(ctx context.Context, cfg Config, push bool) (bool, bool, error) {
	headExists := runGit(ctx, cfg.Repo, "rev-parse", "--verify", "HEAD") == nil
	headOnOrigin := false
	if push {
		if err := runGit(ctx, cfg.Repo, "fetch", "--prune", "--no-tags", "origin"); err != nil {
			return false, false, err
		}
		headOnOrigin = headExists && commitIsOnOrigin(ctx, cfg.Repo, "HEAD")
	}
	if err := ensureReadmeIndexClean(ctx, cfg.Repo); err != nil {
		return false, false, err
	}
	readmeStatus, err := gitOutput(ctx, cfg.Repo, "status", "--porcelain=v1", "--untracked-files=all", "--", recoveryReadmeFilename)
	if err != nil {
		return false, false, fmt.Errorf("inspect backup README status: %w", err)
	}
	if strings.TrimSpace(readmeStatus) == "" {
		if !push {
			return false, false, nil
		}
		if !headOnOrigin && !pendingCommitsMatchLocalHistory(ctx, cfg.Repo, cfg.PendingCommits) {
			return false, false, errors.New("refusing to push existing backup checkout history not owned by backup init")
		}
		headOID, branch, err := exactHeadAndBranch(ctx, cfg.Repo)
		if err != nil {
			return false, false, err
		}
		if err := pushExactCommit(ctx, cfg.Repo, headOID, branch); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	if push && headExists && !headOnOrigin && !pendingCommitsMatchLocalHistory(ctx, cfg.Repo, cfg.PendingCommits) {
		return false, false, errors.New("refusing to push existing backup checkout history not present on origin")
	}
	if err := runGit(ctx, cfg.Repo, "add", "--", recoveryReadmeFilename); err != nil {
		return false, false, rollbackReadmeIndexAfterError(ctx, cfg.Repo, headExists, err)
	}
	if err := runGit(ctx, cfg.Repo, "commit", "--no-gpg-sign", "--only", "-m", "docs: describe encrypted gohealthcli backup", "--", recoveryReadmeFilename); err != nil {
		return false, false, rollbackReadmeIndexAfterError(ctx, cfg.Repo, headExists, err)
	}
	if !isGeneratedReadmeCommit(ctx, cfg.Repo) {
		return true, false, errors.New("generated backup setup commit did not contain exactly the recovery README")
	}
	if !push {
		return true, false, nil
	}
	if strings.TrimSpace(cfg.Remote) == "" {
		return true, false, errors.New("cannot push backup: no Git remote is configured")
	}
	headOID, branch, err := exactHeadAndBranch(ctx, cfg.Repo)
	if err != nil {
		return true, false, err
	}
	if err := pushExactCommit(ctx, cfg.Repo, headOID, branch); err != nil {
		return true, false, err
	}
	return true, true, nil
}

type snapshotGitState struct {
	headExists     bool
	headOnOrigin   bool
	headAuthorized bool
	headOID        string
	branch         string
}

func preflightSnapshotCommit(ctx context.Context, cfg Config, push bool, oldManifest Manifest, oldManifestFound bool, identity age.Identity) (snapshotGitState, error) {
	headExists := runGit(ctx, cfg.Repo, "rev-parse", "--verify", "HEAD") == nil
	headOID := ""
	branch := ""
	if headExists {
		var err error
		headOID, err = gitOutput(ctx, cfg.Repo, "rev-parse", "HEAD")
		if err != nil {
			return snapshotGitState{}, err
		}
		headOID = strings.TrimSpace(headOID)
		branch, err = gitOutput(ctx, cfg.Repo, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return snapshotGitState{}, errors.New("backup checkout must be on a branch")
		}
		branch = strings.TrimSpace(branch)
		if err := runGit(ctx, cfg.Repo, "check-ref-format", "--branch", branch); err != nil {
			return snapshotGitState{}, errors.New("backup checkout branch name is invalid")
		}
	}
	headOnOrigin := false
	headAuthorized := false
	if push {
		if err := runGit(ctx, cfg.Repo, "fetch", "--prune", "--no-tags", "origin"); err != nil {
			return snapshotGitState{}, err
		}
		headOnOrigin = headExists && commitIsOnOrigin(ctx, cfg.Repo, "HEAD")
		if headExists && !headOnOrigin {
			headAuthorized = pendingCommitsMatchLocalHistory(ctx, cfg.Repo, cfg.PendingCommits) && isAuthorizedBackupHead(ctx, cfg.Repo, identity)
			if !headAuthorized {
				return snapshotGitState{}, errors.New("refusing to push existing backup checkout history not owned by authenticated backup commands")
			}
		}
	}
	ownedPaths := []string{recoveryReadmeFilename, ManifestFilename, "data"}
	staged, err := gitOutput(ctx, cfg.Repo, "diff", "--cached", "--name-only")
	if err != nil {
		return snapshotGitState{}, fmt.Errorf("inspect staged backup snapshot: %w", err)
	}
	if strings.TrimSpace(staged) != "" {
		return snapshotGitState{}, errors.New("backup checkout has staged changes; commit or unstage them before backup push")
	}
	backupStatus, err := gitOutput(ctx, cfg.Repo, append([]string{"status", "--porcelain=v1", "--untracked-files=all", "--"}, ownedPaths...)...)
	if err != nil {
		return snapshotGitState{}, fmt.Errorf("inspect backup snapshot status: %w", err)
	}
	if strings.TrimSpace(backupStatus) != "" {
		return snapshotGitState{}, errors.New("backup-owned paths have uncommitted changes; commit or discard them before backup push")
	}
	tracked, err := gitOutput(ctx, cfg.Repo, append([]string{"ls-files", "--"}, ownedPaths...)...)
	if err != nil {
		return snapshotGitState{}, fmt.Errorf("inspect tracked backup snapshot paths: %w", err)
	}
	trackedSet := make(map[string]struct{})
	for _, trackedPath := range strings.Split(strings.TrimSpace(tracked), "\n") {
		if trackedPath != "" {
			trackedSet[trackedPath] = struct{}{}
		}
	}
	allowed := map[string]struct{}{recoveryReadmeFilename: {}}
	if oldManifestFound {
		allowed[ManifestFilename] = struct{}{}
		for _, shard := range oldManifest.Shards {
			allowed[shard.Path] = struct{}{}
		}
	}
	for trackedPath := range trackedSet {
		if _, ok := allowed[trackedPath]; !ok {
			return snapshotGitState{}, fmt.Errorf("tracked backup path %q is not owned by the current manifest", trackedPath)
		}
	}
	if oldManifestFound {
		for requiredPath := range allowed {
			if requiredPath == recoveryReadmeFilename {
				continue
			}
			if _, ok := trackedSet[requiredPath]; !ok {
				return snapshotGitState{}, fmt.Errorf("backup manifest-owned path %q is not tracked by Git", requiredPath)
			}
		}
	}
	if err := validateExistingSnapshotPaths(cfg.Repo, oldManifest, oldManifestFound); err != nil {
		return snapshotGitState{}, err
	}
	return snapshotGitState{headExists: headExists, headOnOrigin: headOnOrigin, headAuthorized: headAuthorized, headOID: headOID, branch: branch}, nil
}

func pendingCommitsMatchLocalHistory(ctx context.Context, repo string, pending []string) bool {
	commits, err := gitOutput(ctx, repo, "rev-list", "--reverse", "HEAD", "--not", "--remotes=origin")
	if err != nil {
		return false
	}
	return slices.Equal(strings.Fields(commits), pendingCommitsNotOnOrigin(ctx, repo, pending))
}

func pendingCommitsNotOnOrigin(ctx context.Context, repo string, pending []string) []string {
	localOnly := make([]string, 0, len(pending))
	for _, commit := range pending {
		if !commitIsOnOrigin(ctx, repo, commit) {
			localOnly = append(localOnly, commit)
		}
	}
	return localOnly
}

func migrateLegacyPendingSetupCommit(ctx context.Context, cfg Config) (Config, bool, error) {
	if len(cfg.PendingCommits) != 0 {
		return cfg, false, nil
	}
	if runGit(ctx, cfg.Repo, "rev-parse", "--verify", "HEAD") != nil {
		return cfg, false, nil
	}
	commits, err := gitOutput(ctx, cfg.Repo, "rev-list", "--reverse", "HEAD", "--not", "--remotes=origin")
	if err != nil {
		return cfg, false, err
	}
	localOnly := strings.Fields(commits)
	if len(localOnly) != 1 || !isGeneratedReadmeCommitAt(ctx, cfg.Repo, localOnly[0]) {
		return cfg, false, nil
	}
	cfg.PendingCommits = []string{localOnly[0]}
	return cfg, true, nil
}

type snapshotCommitResult struct {
	Changed bool
	Pushed  bool
	HeadOID string
}

func commitSnapshotAndMaybePush(ctx context.Context, cfg Config, push bool, state snapshotGitState, manifest Manifest) (snapshotCommitResult, error) {
	ownedPaths := []string{recoveryReadmeFilename, ManifestFilename, "data"}
	staged, err := gitOutput(ctx, cfg.Repo, "diff", "--cached", "--name-only")
	if err != nil {
		return snapshotCommitResult{}, fmt.Errorf("recheck staged backup snapshot: %w", err)
	}
	if strings.TrimSpace(staged) != "" {
		return snapshotCommitResult{}, errors.New("backup-owned paths changed in the index during backup push")
	}
	backupStatus, err := gitOutput(ctx, cfg.Repo, append([]string{"status", "--porcelain=v1", "--untracked-files=all", "--"}, ownedPaths...)...)
	if err != nil {
		return snapshotCommitResult{}, fmt.Errorf("inspect prepared backup snapshot status: %w", err)
	}
	if strings.TrimSpace(backupStatus) == "" {
		if !push {
			return snapshotCommitResult{HeadOID: state.headOID}, nil
		}
		if !state.headExists || !state.headOnOrigin && !state.headAuthorized {
			return snapshotCommitResult{}, errors.New("refusing to push existing backup checkout history not owned by backup commands")
		}
		if err := pushExactCommit(ctx, cfg.Repo, state.headOID, state.branch); err != nil {
			return snapshotCommitResult{HeadOID: state.headOID}, err
		}
		return snapshotCommitResult{Pushed: true, HeadOID: state.headOID}, nil
	}
	if err := stageSnapshotExact(ctx, cfg.Repo, manifest); err != nil {
		return snapshotCommitResult{}, rollbackSnapshotIndexAfterError(ctx, cfg.Repo, state.headExists, ownedPaths, err)
	}
	treeOID, err := gitOutput(ctx, cfg.Repo, "write-tree")
	if err != nil {
		return snapshotCommitResult{}, rollbackSnapshotIndexAfterError(ctx, cfg.Repo, state.headExists, ownedPaths, err)
	}
	commitOID, err := gitOutput(ctx, cfg.Repo, "commit-tree", strings.TrimSpace(treeOID), "-p", state.headOID, "-m", "backup: update encrypted Health Archive snapshot")
	if err != nil {
		return snapshotCommitResult{}, rollbackSnapshotIndexAfterError(ctx, cfg.Repo, state.headExists, ownedPaths, err)
	}
	commitOID = strings.TrimSpace(commitOID)
	if !isGeneratedSnapshotCommit(ctx, cfg.Repo, commitOID) {
		cause := errors.New("generated backup commit contains paths outside the encrypted snapshot")
		return snapshotCommitResult{}, rollbackSnapshotIndexAfterError(ctx, cfg.Repo, state.headExists, ownedPaths, cause)
	}
	if err := recordSnapshotPublicationCommit(cfg.Repo, commitOID); err != nil {
		return snapshotCommitResult{}, rollbackSnapshotIndexAfterError(ctx, cfg.Repo, state.headExists, ownedPaths, err)
	}
	if err := runGit(ctx, cfg.Repo, "update-ref", "-m", "gohealthcli backup push", "HEAD", commitOID, state.headOID); err != nil {
		return snapshotCommitResult{}, rollbackSnapshotIndexAfterError(ctx, cfg.Repo, state.headExists, ownedPaths, err)
	}
	if !push {
		return snapshotCommitResult{Changed: true, HeadOID: commitOID}, nil
	}
	if err := pushExactCommit(ctx, cfg.Repo, commitOID, state.branch); err != nil {
		return snapshotCommitResult{Changed: true, HeadOID: commitOID}, err
	}
	return snapshotCommitResult{Changed: true, Pushed: true, HeadOID: commitOID}, nil
}

func pushExactCommit(ctx context.Context, repo, commitOID, branch string) error {
	if commitOID == "" || branch == "" {
		return errors.New("cannot push backup without an exact commit and branch")
	}
	return runGit(ctx, repo, "-c", "push.followTags=false", "push", "-u", "origin", commitOID+":refs/heads/"+branch)
}

func exactHeadAndBranch(ctx context.Context, repo string) (string, string, error) {
	headOID, err := gitOutput(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	branch, err := gitOutput(ctx, repo, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", "", errors.New("backup checkout must be on a branch")
	}
	branch = strings.TrimSpace(branch)
	if err := runGit(ctx, repo, "check-ref-format", "--branch", branch); err != nil {
		return "", "", errors.New("backup checkout branch name is invalid")
	}
	return strings.TrimSpace(headOID), branch, nil
}

func validateNoGitContentAttributes(ctx context.Context, repo string, manifest Manifest) error {
	paths := []string{recoveryReadmeFilename, ManifestFilename}
	for _, shard := range manifest.Shards {
		paths = append(paths, shard.Path)
	}
	for _, generatedPath := range paths {
		attributes, err := gitOutput(ctx, repo, "check-attr", "filter", "text", "eol", "working-tree-encoding", "ident", "--", generatedPath)
		if err != nil {
			return fmt.Errorf("inspect Git content attributes for %q: %w", generatedPath, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(attributes), "\n") {
			if line == "" || strings.HasSuffix(line, ": unspecified") {
				continue
			}
			return fmt.Errorf("Git content attribute applies to generated backup path %q; remove text, eol, filter, working-tree-encoding, or ident attributes before backup push", generatedPath)
		}
	}
	return nil
}

func stageSnapshotExact(ctx context.Context, repo string, manifest Manifest) error {
	desired := []string{recoveryReadmeFilename, ManifestFilename}
	for _, shard := range manifest.Shards {
		desired = append(desired, shard.Path)
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, generatedPath := range desired {
		desiredSet[generatedPath] = struct{}{}
		hash, err := gitOutput(ctx, repo, "hash-object", "-w", "--no-filters", "--", generatedPath)
		if err != nil {
			return fmt.Errorf("store generated backup object %q: %w", generatedPath, err)
		}
		hash = strings.TrimSpace(hash)
		if err := runGit(ctx, repo, "update-index", "--add", "--cacheinfo", "100644,"+hash+","+generatedPath); err != nil {
			return fmt.Errorf("stage generated backup object %q: %w", generatedPath, err)
		}
		indexHash, err := gitOutput(ctx, repo, "rev-parse", ":"+generatedPath)
		if err != nil || strings.TrimSpace(indexHash) != hash {
			return fmt.Errorf("verify staged backup object %q: index does not match generated bytes", generatedPath)
		}
	}
	tracked, err := gitOutput(ctx, repo, "ls-files", "--", recoveryReadmeFilename, ManifestFilename, "data")
	if err != nil {
		return err
	}
	for _, trackedPath := range strings.Split(strings.TrimSpace(tracked), "\n") {
		if trackedPath == "" {
			continue
		}
		if _, keep := desiredSet[trackedPath]; keep {
			continue
		}
		if err := runGit(ctx, repo, "update-index", "--force-remove", "--", trackedPath); err != nil {
			return fmt.Errorf("stage stale backup shard removal %q: %w", trackedPath, err)
		}
	}
	return nil
}

func rollbackSnapshotIndexAfterError(ctx context.Context, repo string, headExists bool, paths []string, cause error) error {
	args := []string{"restore", "--staged", "--"}
	if !headExists {
		args = []string{"rm", "--cached", "-r", "--ignore-unmatch", "--"}
	}
	args = append(args, paths...)
	if err := runGit(ctx, repo, args...); err != nil {
		return errors.Join(cause, fmt.Errorf("restore backup snapshot index after failure: %w", err))
	}
	return cause
}

func restorePublishedSnapshot(ctx context.Context, repo string, oldManifest Manifest, oldManifestFound bool, published Manifest) error {
	trackedText, err := gitOutput(ctx, repo, "ls-tree", "-r", "--name-only", "HEAD", "--", recoveryReadmeFilename, ManifestFilename, "data")
	if err != nil {
		return err
	}
	tracked := make(map[string]struct{})
	for _, trackedPath := range strings.Split(strings.TrimSpace(trackedText), "\n") {
		if trackedPath != "" {
			tracked[trackedPath] = struct{}{}
		}
	}
	restorePaths := make([]string, 0, len(oldManifest.Shards)+2)
	if _, ok := tracked[recoveryReadmeFilename]; ok {
		restorePaths = append(restorePaths, recoveryReadmeFilename)
	}
	oldShardPaths := make(map[string]struct{}, len(oldManifest.Shards))
	if oldManifestFound {
		if _, ok := tracked[ManifestFilename]; ok {
			restorePaths = append(restorePaths, ManifestFilename)
		}
		for _, shard := range oldManifest.Shards {
			oldShardPaths[shard.Path] = struct{}{}
			if _, ok := tracked[shard.Path]; ok {
				restorePaths = append(restorePaths, shard.Path)
			}
		}
	}
	if len(restorePaths) > 0 {
		for _, restorePath := range restorePaths {
			data, showErr := gitOutput(ctx, repo, "show", "HEAD:"+restorePath)
			if showErr != nil {
				return showErr
			}
			destination := filepath.Join(repo, filepath.FromSlash(restorePath))
			if mkdirErr := os.MkdirAll(filepath.Dir(destination), 0o700); mkdirErr != nil {
				return mkdirErr
			}
			if writeErr := os.WriteFile(destination, []byte(data), 0o600); writeErr != nil {
				return writeErr
			}
		}
	}
	var cleanupErrors []error
	for _, shard := range published.Shards {
		if _, existed := oldShardPaths[shard.Path]; existed {
			continue
		}
		shardPath, pathErr := resolveShardPath(repo, shard.Path)
		if pathErr != nil {
			cleanupErrors = append(cleanupErrors, pathErr)
			continue
		}
		if removeErr := os.Remove(shardPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, removeErr)
		}
	}
	if !oldManifestFound {
		if removeErr := os.Remove(filepath.Join(repo, ManifestFilename)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, removeErr)
		}
	}
	if _, wasTracked := tracked[recoveryReadmeFilename]; !wasTracked {
		if removeErr := os.Remove(filepath.Join(repo, recoveryReadmeFilename)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, removeErr)
		}
	}
	if len(oldShardPaths) == 0 {
		if removeErr := os.Remove(filepath.Join(repo, "data")); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, removeErr)
		}
	}
	return errors.Join(cleanupErrors...)
}

func rollbackUnprovenancedSnapshotCommit(ctx context.Context, repo, oldOID, newOID string, oldManifest Manifest, oldManifestFound bool, published Manifest) error {
	if oldOID == "" || newOID == "" {
		return errors.New("cannot roll back snapshot commit without exact old and new object IDs")
	}
	if err := runGit(ctx, repo, "update-ref", "-m", "rollback unprovenanced gohealthcli backup", "HEAD", oldOID, newOID); err != nil {
		return err
	}
	if err := runGit(ctx, repo, "read-tree", oldOID); err != nil {
		return err
	}
	return restorePublishedSnapshot(ctx, repo, oldManifest, oldManifestFound, published)
}

const publicationMarkerFilename = "gohealthcli-publish-in-progress"

type publicationMarker struct {
	OldHeadOID string `json:"old_head_oid"`
	NewHeadOID string `json:"new_head_oid,omitempty"`
}

func beginSnapshotPublication(repo, oldHeadOID string) error {
	if oldHeadOID == "" {
		return errors.New("cannot begin snapshot publication without the current commit")
	}
	path := filepath.Join(repo, ".git", publicationMarkerFilename)
	data, err := json.Marshal(publicationMarker{OldHeadOID: oldHeadOID})
	if err != nil {
		return err
	}
	return writePublicationMarker(path, append(data, '\n'))
}

func recordSnapshotPublicationCommit(repo, newHeadOID string) error {
	path := filepath.Join(repo, ".git", publicationMarkerFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var marker publicationMarker
	if json.Unmarshal(data, &marker) != nil || marker.OldHeadOID == "" || newHeadOID == "" {
		return errors.New("invalid snapshot publication marker")
	}
	marker.NewHeadOID = newHeadOID
	data, err = json.Marshal(marker)
	if err != nil {
		return err
	}
	return writePublicationMarker(path, append(data, '\n'))
}

func writePublicationMarker(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".gohealthcli-publish-marker-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func finishSnapshotPublication(repo string) error {
	err := os.Remove(filepath.Join(repo, ".git", publicationMarkerFilename))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func recoverInterruptedSnapshotPublication(ctx context.Context, cfg Config, configPath string) (Config, error) {
	repo := cfg.Repo
	markerPath := filepath.Join(repo, ".git", publicationMarkerFilename)
	markerData, err := os.ReadFile(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var marker publicationMarker
	if json.Unmarshal(markerData, &marker) != nil || marker.OldHeadOID == "" {
		return cfg, errors.New("invalid snapshot publication marker")
	}
	currentHead, err := gitOutput(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return cfg, err
	}
	currentHead = strings.TrimSpace(currentHead)
	if currentHead == marker.OldHeadOID {
		if err := restoreOwnedCheckoutFromHead(ctx, repo); err != nil {
			return cfg, err
		}
	} else {
		if marker.NewHeadOID == "" || currentHead != marker.NewHeadOID {
			return cfg, errors.New("snapshot publication marker does not match current HEAD")
		}
		identity, err := identityFromFile(cfg.Identity)
		if err != nil || !isAuthenticatedGeneratedSnapshotCommit(ctx, repo, currentHead, identity) {
			return cfg, errors.New("cannot authenticate interrupted snapshot publication commit")
		}
		if commitIsOnOrigin(ctx, repo, currentHead) {
			reconciled := pendingCommitsNotOnOrigin(ctx, repo, cfg.PendingCommits)
			if !slices.Equal(reconciled, cfg.PendingCommits) {
				cfg.PendingCommits = reconciled
				if err := SaveConfig(configPath, cfg); err != nil {
					return cfg, err
				}
			}
		} else if !slices.Contains(cfg.PendingCommits, currentHead) {
			cfg.PendingCommits = append(cfg.PendingCommits, currentHead)
			if err := SaveConfig(configPath, cfg); err != nil {
				return cfg, err
			}
		}
	}
	return cfg, finishSnapshotPublication(repo)
}

func restoreOwnedCheckoutFromHead(ctx context.Context, repo string) error {
	trackedText, err := gitOutput(ctx, repo, "ls-tree", "-r", "--name-only", "HEAD", "--", recoveryReadmeFilename, ManifestFilename, "data")
	if err != nil {
		return err
	}
	tracked := make(map[string]struct{})
	for _, trackedPath := range strings.Fields(trackedText) {
		tracked[trackedPath] = struct{}{}
		data, err := gitOutput(ctx, repo, "show", "HEAD:"+trackedPath)
		if err != nil {
			return err
		}
		destination := filepath.Join(repo, filepath.FromSlash(trackedPath))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(destination, []byte(data), 0o600); err != nil {
			return err
		}
	}
	for _, topLevel := range []string{recoveryReadmeFilename, ManifestFilename} {
		if _, ok := tracked[topLevel]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(repo, topLevel)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	dataRoot := filepath.Join(repo, "data")
	entries, err := os.ReadDir(dataRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		relative := "data/" + entry.Name()
		if _, ok := tracked[relative]; ok {
			continue
		}
		if entry.IsDir() {
			return fmt.Errorf("cannot recover interrupted publication with unexpected directory %q", relative)
		}
		if err := os.Remove(filepath.Join(dataRoot, entry.Name())); err != nil {
			return err
		}
	}
	if len(tracked) == 0 || len(tracked) == 1 {
		_ = os.Remove(dataRoot)
	}
	return runGit(ctx, repo, "read-tree", "HEAD")
}

func isAuthorizedBackupHead(ctx context.Context, repo string, identity age.Identity) bool {
	commits, err := gitOutput(ctx, repo, "rev-list", "HEAD", "--not", "--remotes=origin")
	if err != nil {
		return false
	}
	for _, commit := range strings.Fields(commits) {
		if isGeneratedReadmeCommitAt(ctx, repo, commit) || isAuthenticatedGeneratedSnapshotCommit(ctx, repo, commit, identity) {
			continue
		}
		return false
	}
	return true
}

func isAuthenticatedGeneratedSnapshotCommit(ctx context.Context, repo, commit string, identity age.Identity) bool {
	if !isGeneratedSnapshotCommit(ctx, repo, commit) {
		return false
	}
	manifestJSON, err := gitOutput(ctx, repo, "show", commit+":"+ManifestFilename)
	if err != nil {
		return false
	}
	var manifest Manifest
	if json.Unmarshal([]byte(manifestJSON), &manifest) != nil {
		return false
	}
	for _, shard := range manifest.Shards {
		blob, err := gitOutput(ctx, repo, "show", commit+":"+shard.Path)
		if err != nil || int64(len(blob)) != shard.Bytes {
			return false
		}
		decrypted, err := age.Decrypt(bytes.NewReader([]byte(blob)), identity)
		if err != nil {
			return false
		}
		gzipReader, err := gzip.NewReader(decrypted)
		if err != nil {
			return false
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, gzipReader)
		closeErr := gzipReader.Close()
		if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != shard.SHA256 {
			return false
		}
	}
	return true
}

func isGeneratedSnapshotCommit(ctx context.Context, repo, commit string) bool {
	if !hasGeneratedCommitMetadata(ctx, repo, commit, "backup: update encrypted Health Archive snapshot") {
		return false
	}
	parents, err := gitOutput(ctx, repo, "rev-list", "--parents", "-n", "1", commit)
	if err != nil || len(strings.Fields(parents)) != 2 {
		return false
	}
	paths, err := gitOutput(ctx, repo, "diff-tree", "--root", "--no-commit-id", "--name-only", "-r", commit)
	if err != nil || strings.TrimSpace(paths) == "" {
		return false
	}
	manifestJSON, err := gitOutput(ctx, repo, "show", commit+":"+ManifestFilename)
	if err != nil {
		return false
	}
	manifest, ok := decodeCanonicalGeneratedManifest([]byte(manifestJSON))
	if !ok {
		return false
	}
	for _, changedPath := range strings.Fields(paths) {
		switch {
		case changedPath == recoveryReadmeFilename:
		case changedPath == ManifestFilename:
		case isKnownGeneratedShardPath(changedPath):
		default:
			return false
		}
	}
	readme, err := gitOutput(ctx, repo, "show", commit+":"+recoveryReadmeFilename)
	if err != nil || readme != backupReadmeBody && readme != legacyBackupReadmeBody {
		return false
	}
	treePaths, err := gitOutput(ctx, repo, "ls-tree", "-r", "--name-only", commit, "--", recoveryReadmeFilename, ManifestFilename, "data")
	if err != nil {
		return false
	}
	wantTree := []string{recoveryReadmeFilename, ManifestFilename}
	for _, shard := range manifest.Shards {
		wantTree = append(wantTree, shard.Path)
	}
	sort.Strings(wantTree)
	gotTree := strings.Fields(treePaths)
	sort.Strings(gotTree)
	if !slices.Equal(gotTree, wantTree) {
		return false
	}
	for _, shard := range manifest.Shards {
		sizeText, err := gitOutput(ctx, repo, "cat-file", "-s", commit+":"+shard.Path)
		if err != nil {
			return false
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
		if err != nil || size != shard.Bytes {
			return false
		}
	}
	return true
}

func hasGeneratedCommitMetadata(ctx context.Context, repo, commit, wantMessage string) bool {
	metadata, err := gitOutput(ctx, repo, "log", "-1", "--format=%an%x00%ae%x00%cn%x00%ce%x00%B", commit)
	if err != nil {
		return false
	}
	parts := strings.SplitN(metadata, "\x00", 5)
	if len(parts) != 5 || parts[0] != "gohealthcli" || parts[1] != "gohealthcli@example.invalid" || parts[2] != "gohealthcli" || parts[3] != "gohealthcli@example.invalid" {
		return false
	}
	return strings.TrimRight(parts[4], "\r\n") == wantMessage
}

func decodeCanonicalGeneratedManifest(data []byte) (Manifest, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if decoder.Decode(&manifest) != nil {
		return Manifest{}, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, false
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, false
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(data, canonical) || manifest.Format != backupFormatVersion || manifest.HealthArchiveSchemaVersion <= 0 || !manifest.Encrypted || manifest.ExportedAt == "" {
		return Manifest{}, false
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.ExportedAt); err != nil {
		return Manifest{}, false
	}
	if len(manifest.Recipients) == 0 || !slices.Equal(manifest.Recipients, normalizedStrings(manifest.Recipients)) || ValidateRecipients(manifest.Recipients) != nil || !validGeneratedCounts(manifest.Counts) {
		return Manifest{}, false
	}
	seen := make(map[string]struct{}, len(manifest.Shards))
	previousPath := ""
	for _, shard := range manifest.Shards {
		if !isKnownGeneratedShardTable(shard.Table) || shard.Path != "data/"+shard.Table+".jsonl.gz.age" || shard.Rows < 0 || shard.Bytes <= 0 || len(shard.SHA256) != 64 {
			return Manifest{}, false
		}
		if _, err := hex.DecodeString(shard.SHA256); err != nil {
			return Manifest{}, false
		}
		if _, duplicate := seen[shard.Path]; duplicate || previousPath != "" && shard.Path <= previousPath {
			return Manifest{}, false
		}
		seen[shard.Path] = struct{}{}
		previousPath = shard.Path
	}
	if len(manifest.Shards) == 0 {
		return Manifest{}, false
	}
	return manifest, true
}

func validGeneratedCounts(counts Counts) bool {
	return counts.Connections >= 0 && counts.DataPoints >= 0 && counts.DataPointRevisions >= 0 && counts.DataPointAttachments >= 0 && counts.AttachmentPayloads >= 0 && counts.Rollups >= 0 && counts.IdentitySnapshots >= 0 && counts.SyncRuns >= 0 && counts.SyncCursors >= 0
}

func isKnownGeneratedShardPath(shardPath string) bool {
	for table := range knownGeneratedShardTables {
		if shardPath == "data/"+table+".jsonl.gz.age" {
			return true
		}
	}
	return false
}

func isKnownGeneratedShardTable(table string) bool {
	_, ok := knownGeneratedShardTables[table]
	return ok
}

var knownGeneratedShardTables = map[string]struct{}{
	"attachment_payloads":    {},
	"connections":            {},
	"data_point_attachments": {},
	"data_point_revisions":   {},
	"data_points":            {},
	"identity_snapshots":     {},
	"rollups":                {},
	"sync_cursors":           {},
	"sync_runs":              {},
}

func rollbackReadmeIndexAfterError(ctx context.Context, repo string, headExists bool, cause error) error {
	var err error
	if headExists {
		err = runGit(ctx, repo, "restore", "--staged", "--", recoveryReadmeFilename)
	} else {
		err = runGit(ctx, repo, "rm", "--cached", "--ignore-unmatch", "--", recoveryReadmeFilename)
	}
	if err != nil {
		return errors.Join(cause, fmt.Errorf("restore backup README index after failure: %w", err))
	}
	return cause
}

func ensureReadmeIndexClean(ctx context.Context, repo string) error {
	staged, err := gitOutput(ctx, repo, "diff", "--cached", "--name-only", "--", recoveryReadmeFilename)
	if err != nil {
		return fmt.Errorf("inspect staged backup README: %w", err)
	}
	if strings.TrimSpace(staged) != "" {
		return errors.New("backup README has staged changes; commit or unstage them before backup init")
	}
	return nil
}

func commitIsOnOrigin(ctx context.Context, repo, commit string) bool {
	output, err := gitOutput(ctx, repo, "branch", "--remotes", "--contains", commit)
	if err != nil {
		return false
	}
	for _, ref := range strings.Fields(output) {
		if strings.HasPrefix(strings.TrimSpace(ref), "origin/") {
			return true
		}
	}
	return false
}

func isAuthorizedSetupHead(ctx context.Context, repo string) bool {
	if !isGeneratedReadmeCommit(ctx, repo) {
		return false
	}
	parents, err := gitOutput(ctx, repo, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return false
	}
	parentFields := strings.Fields(parents)
	if len(parentFields) == 1 {
		return true
	}
	return commitIsOnOrigin(ctx, repo, parentFields[1])
}

func isGeneratedReadmeCommit(ctx context.Context, repo string) bool {
	return isGeneratedReadmeCommitAt(ctx, repo, "HEAD")
}

func isGeneratedReadmeCommitAt(ctx context.Context, repo, commit string) bool {
	if !hasGeneratedCommitMetadata(ctx, repo, commit, "docs: describe encrypted gohealthcli backup") {
		return false
	}
	parents, err := gitOutput(ctx, repo, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return false
	}
	parentFields := strings.Fields(parents)
	if len(parentFields) != 1 && len(parentFields) != 2 {
		return false
	}
	var paths string
	if len(parentFields) == 1 {
		paths, err = gitOutput(ctx, repo, "ls-tree", "-r", "--name-only", commit)
	} else {
		paths, err = gitOutput(ctx, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", commit)
	}
	if err != nil || strings.TrimSpace(paths) != recoveryReadmeFilename {
		return false
	}
	readme, err := gitOutput(ctx, repo, "show", commit+":"+recoveryReadmeFilename)
	return err == nil && (readme == backupReadmeBody || readme == legacyBackupReadmeBody)
}

func verifyRepoRemote(ctx context.Context, cfg Config) error {
	if strings.TrimSpace(cfg.Remote) == "" {
		return nil
	}
	remotes, err := gitOutput(ctx, cfg.Repo, "remote")
	if err != nil {
		return fmt.Errorf("verify backup Git remote: %w", err)
	}
	hasOrigin := false
	for _, remote := range strings.Fields(remotes) {
		if remote == "origin" {
			hasOrigin = true
			break
		}
	}
	if !hasOrigin {
		if err := runGit(ctx, cfg.Repo, "remote", "add", "origin", cfg.Remote); err != nil {
			return fmt.Errorf("configure backup Git remote: %w", err)
		}
	}
	actual, err := gitOutput(ctx, cfg.Repo, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("verify backup Git remote: %w", err)
	}
	actual = strings.TrimRight(actual, "\r\n")
	if actual != cfg.Remote {
		return fmt.Errorf("backup checkout origin is %q, configured remote is %q", redactRemote(actual), redactRemote(cfg.Remote))
	}
	pushURLs, err := gitOutput(ctx, cfg.Repo, "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return fmt.Errorf("verify backup Git push remote: %w", err)
	}
	for _, pushURL := range strings.Split(strings.TrimRight(pushURLs, "\r\n"), "\n") {
		pushURL = strings.TrimSuffix(pushURL, "\r")
		if pushURL == "" {
			continue
		}
		if pushURL != cfg.Remote {
			return fmt.Errorf("backup checkout push URL is %q, configured remote is %q", redactRemote(pushURL), redactRemote(cfg.Remote))
		}
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := executeGit(ctx, dir, args...)
	return err
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	return executeGit(ctx, dir, args...)
}

func executeGit(ctx context.Context, dir string, args ...string) (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("find Git executable: %w", err)
	}
	if !filepath.IsAbs(gitPath) {
		return "", fmt.Errorf("refusing non-absolute Git executable path %q", gitPath)
	}
	disabledHooksPath := "/dev/null"
	if runtime.GOOS == "windows" {
		disabledHooksPath = "NUL"
	}
	gitArgs := append([]string{"-c", "core.hooksPath=" + disabledHooksPath}, args...)
	cmd := exec.CommandContext(ctx, gitPath, gitArgs...) // #nosec G204 -- executable is an absolute LookPath result; arguments are fixed operations plus configured local paths/remotes.
	cmd.Dir = dir
	cmd.Env = append(gitSafeEnvironment(os.Environ()),
		"GIT_AUTHOR_NAME=gohealthcli",
		"GIT_AUTHOR_EMAIL=gohealthcli@example.invalid",
		"GIT_COMMITTER_NAME=gohealthcli",
		"GIT_COMMITTER_EMAIL=gohealthcli@example.invalid",
		"GIT_TERMINAL_PROMPT=0",
	)
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		operation := "command"
		if len(args) > 0 {
			operation = args[0]
		}
		if stderr.Len() != 0 {
			return "", fmt.Errorf("git %s: %w: %s", operation, err, redactGitError(stderr.String(), args))
		}
		return "", fmt.Errorf("git %s: %w", operation, err)
	}
	return stdout.String(), nil
}

func gitSafeEnvironment(environ []string) []string {
	blocked := map[string]struct{}{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
		"GIT_ASKPASS":                      {},
		"GIT_CEILING_DIRECTORIES":          {},
		"GIT_COMMON_DIR":                   {},
		"GIT_CONFIG":                       {},
		"GIT_CONFIG_COUNT":                 {},
		"GIT_CONFIG_GLOBAL":                {},
		"GIT_CONFIG_NOSYSTEM":              {},
		"GIT_CONFIG_PARAMETERS":            {},
		"GIT_CONFIG_SYSTEM":                {},
		"GIT_DIR":                          {},
		"GIT_EXEC_PATH":                    {},
		"GIT_INDEX_FILE":                   {},
		"GIT_NAMESPACE":                    {},
		"GIT_OBJECT_DIRECTORY":             {},
		"GIT_SSH":                          {},
		"GIT_SSH_COMMAND":                  {},
		"GIT_TEMPLATE_DIR":                 {},
		"GIT_WORK_TREE":                    {},
		"SSH_ASKPASS":                      {},
		"SSH_ASKPASS_REQUIRE":              {},
		"DISPLAY":                          {},
	}
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		normalizedKey := strings.ToUpper(key)
		if _, denied := blocked[normalizedKey]; denied || strings.HasPrefix(normalizedKey, "GIT_CONFIG_KEY_") || strings.HasPrefix(normalizedKey, "GIT_CONFIG_VALUE_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

var gitHTTPUserinfoPattern = regexp.MustCompile(`(?i)(https?://)[^[:space:]/@]+@`)
var gitHTTPURLPattern = regexp.MustCompile(`(?i)https?://[^[:space:]'"<>]+`)

func redactGitError(message string, args []string) string {
	for _, arg := range args {
		if redacted := redactRemote(arg); redacted != arg {
			message = strings.ReplaceAll(message, arg, redacted)
		}
	}
	message = gitHTTPUserinfoPattern.ReplaceAllString(message, `${1}`)
	message = gitHTTPURLPattern.ReplaceAllStringFunc(message, redactRemote)
	return strings.TrimSpace(message)
}

func redactRemote(remote string) string {
	parsed, err := url.Parse(remote)
	if err != nil {
		redacted := gitHTTPUserinfoPattern.ReplaceAllString(remote, `${1}`)
		if marker := strings.IndexAny(redacted, "?#"); marker >= 0 {
			redacted = redacted[:marker]
		}
		return redacted
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return remote
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

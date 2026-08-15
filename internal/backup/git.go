package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

func ensureRepo(ctx context.Context, cfg Config) error {
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

	if info, err := os.Stat(cfg.Repo); err == nil {
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
		if !headOnOrigin && !isAuthorizedSetupHead(ctx, cfg.Repo) {
			return false, false, errors.New("refusing to push existing backup checkout history not owned by backup init")
		}
		if err := runGit(ctx, cfg.Repo, "push", "-u", "origin", "HEAD"); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	if push && headExists && !headOnOrigin {
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
	if err := runGit(ctx, cfg.Repo, "push", "-u", "origin", "HEAD"); err != nil {
		return true, false, err
	}
	return true, true, nil
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
	parents, err := gitOutput(ctx, repo, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return false
	}
	parentFields := strings.Fields(parents)
	if len(parentFields) != 1 && len(parentFields) != 2 {
		return false
	}
	subject, err := gitOutput(ctx, repo, "log", "-1", "--format=%s", "HEAD")
	if err != nil || strings.TrimSpace(subject) != "docs: describe encrypted gohealthcli backup" {
		return false
	}
	var paths string
	if len(parentFields) == 1 {
		paths, err = gitOutput(ctx, repo, "ls-tree", "-r", "--name-only", "HEAD")
	} else {
		paths, err = gitOutput(ctx, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	}
	if err != nil || strings.TrimSpace(paths) != recoveryReadmeFilename {
		return false
	}
	readme, err := gitOutput(ctx, repo, "show", "HEAD:"+recoveryReadmeFilename)
	return err == nil && readme == backupReadmeBody
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
	disabledHooksPath := "/dev/null"
	if runtime.GOOS == "windows" {
		disabledHooksPath = "NUL"
	}
	gitArgs := append([]string{"-c", "core.hooksPath=" + disabledHooksPath}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...) // #nosec G204 -- arguments are fixed operations plus configured local paths/remotes.
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
		"GIT_CONFIG_PARAMETERS":            {},
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

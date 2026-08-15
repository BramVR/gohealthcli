package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Config struct {
	Repo           string   `json:"repo"`
	Remote         string   `json:"remote"`
	Identity       string   `json:"identity"`
	LocalRecipient string   `json:"local_recipient,omitempty"`
	Recipients     []string `json:"recipients"`
}

type Options struct {
	ConfigPath string
	Repo       string
	Remote     string
	Identity   string
	Recipients []string
	Push       bool
}

func DefaultConfigPath() string {
	dir := defaultConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "backup.json")
}

func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	identity := ""
	if dir := defaultConfigDir(); dir != "" {
		identity = filepath.Join(dir, "backup-age-identity.txt")
	}
	repo := ""
	if home != "" {
		repo = filepath.Join(home, "Projects", "backup-gohealthcli")
	}
	return Config{
		Repo:     repo,
		Identity: identity,
	}
}

func defaultConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "gohealthcli")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "gohealthcli")
}

func LoadConfig(path string) (Config, bool, error) {
	var err error
	path, err = absoluteConfigPath(path)
	if err != nil {
		return Config{}, false, err
	}
	if err := rejectSymlinkedPathComponents(path); err != nil {
		return Config{}, false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Config{}, false, fmt.Errorf("backup config %s must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return Config{}, false, fmt.Errorf("backup config %s must be a regular file", path)
	}
	if err := validatePrivateDir(filepath.Dir(path)); err != nil {
		return Config{}, false, err
	}
	if err := validatePrivateMode(path, info, 0o600); err != nil {
		return Config{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, false, err
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("read backup config: %w", err)
	}
	return cfg, true, nil
}

func SaveConfig(path string, cfg Config) error {
	var err error
	path, err = absoluteConfigPath(path)
	if err != nil {
		return err
	}
	if err := rejectSymlinkedPathComponents(path); err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup config %s must not be a symlink", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("backup config %s must be a regular file", path)
		}
		if err := validatePrivateMode(path, info, 0o600); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), ".backup.json.tmp-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.ReadFrom(bytes.NewReader(data)); err != nil {
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
	if err := hardenPrivatePath(tempPath, false); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return validatePlatformPrivatePath(path, false)
}

func ResolveOptions(opts Options) (Config, error) {
	cfg, _, err := resolveOptions(opts)
	return cfg, err
}

func resolveOptions(opts Options) (Config, bool, error) {
	if strings.TrimSpace(opts.ConfigPath) == "" && DefaultConfigPath() == "" {
		return Config{}, false, errors.New("cannot determine home directory; set HOME or pass --config explicitly")
	}
	cfg, found, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return Config{}, false, err
	}
	if strings.TrimSpace(opts.Repo) != "" {
		cfg.Repo = opts.Repo
	}
	if strings.TrimSpace(opts.Remote) != "" {
		cfg.Remote = opts.Remote
	}
	if strings.TrimSpace(opts.Identity) != "" {
		cfg.Identity = opts.Identity
	} else if !found && opts.ConfigPath != "" {
		cfg.Identity = filepath.Join(filepath.Dir(expandHome(opts.ConfigPath)), "backup-age-identity.txt")
	}
	if len(opts.Recipients) > 0 {
		cfg.Recipients = append([]string(nil), opts.Recipients...)
	}
	cfg.Remote, err = normalizeLocalRemote(cfg.Remote)
	if err != nil {
		return Config{}, false, err
	}
	cfg.Repo = expandHome(cfg.Repo)
	cfg.Identity = expandHome(cfg.Identity)
	if strings.TrimSpace(cfg.Repo) == "" {
		return Config{}, false, errors.New("backup repo path is required")
	}
	if strings.TrimSpace(cfg.Identity) == "" {
		return Config{}, false, errors.New("backup age identity path is required")
	}
	cfg.Repo, err = filepath.Abs(cfg.Repo)
	if err != nil {
		return Config{}, false, fmt.Errorf("resolve backup repo path: %w", err)
	}
	cfg.Identity, err = filepath.Abs(cfg.Identity)
	if err != nil {
		return Config{}, false, fmt.Errorf("resolve backup age identity path: %w", err)
	}
	return cfg, found, nil
}

func resolveStatusOptions(opts Options) (Config, bool, error) {
	var (
		cfg   Config
		found bool
		err   error
	)
	if strings.TrimSpace(opts.ConfigPath) == "" && DefaultConfigPath() == "" {
		cfg = DefaultConfig()
	} else {
		cfg, found, err = LoadConfig(opts.ConfigPath)
		if err != nil {
			return Config{}, false, err
		}
	}
	if strings.TrimSpace(opts.Repo) != "" {
		cfg.Repo = opts.Repo
	}
	if strings.TrimSpace(cfg.Repo) == "" {
		if !found {
			return cfg, false, nil
		}
		return Config{}, false, errors.New("backup repo path is required")
	}
	cfg.Repo, err = filepath.Abs(expandHome(cfg.Repo))
	if err != nil {
		return Config{}, false, fmt.Errorf("resolve backup repo path: %w", err)
	}
	if err := rejectSymlinkedPathComponents(cfg.Repo); err != nil {
		return Config{}, false, err
	}
	return cfg, found, nil
}

func expandHome(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, rest)
	}
	return path
}

func absoluteConfigPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultConfigPath()
	}
	if path == "" {
		return "", errors.New("cannot determine home directory; set HOME or pass --config explicitly")
	}
	resolved, err := filepath.Abs(expandHome(path))
	if err != nil {
		return "", fmt.Errorf("resolve backup config path: %w", err)
	}
	return resolved, nil
}

func pathsReferToSameFile(left, right string) (bool, error) {
	canonicalLeft, err := canonicalPotentialPath(left)
	if err != nil {
		return false, err
	}
	canonicalRight, err := canonicalPotentialPath(right)
	if err != nil {
		return false, err
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if strings.EqualFold(canonicalLeft, canonicalRight) {
			return true, nil
		}
	} else if canonicalLeft == canonicalRight {
		return true, nil
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo), nil
	}
	if leftErr != nil && !errors.Is(leftErr, os.ErrNotExist) {
		return false, leftErr
	}
	if rightErr != nil && !errors.Is(rightErr, os.ErrNotExist) {
		return false, rightErr
	}
	return false, nil
}

func canonicalPotentialPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absPath)
	missing := make([]string, 0, 4)
	for {
		if _, err := os.Lstat(current); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve filesystem path %s", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return filepath.Clean(resolved), nil
}

func validateRemote(remote string) error {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return nil
	}
	if strings.HasPrefix(remote, "-") || strings.Contains(remote, "::") {
		return errors.New("backup Git remote uses unsupported option-like or external-helper syntax")
	}
	if isWindowsLocalPath(remote) {
		return nil
	}
	if strings.ContainsAny(remote, "?#") {
		return errors.New("backup Git remote must not contain query parameters or fragments")
	}
	if isSCPLikeSSHRemote(remote) {
		// SCP syntax has no password field: the first colon starts an opaque
		// repository path, where characters such as '@' are ordinary path data.
		return nil
	}
	parsed, err := url.Parse(remote)
	if err != nil {
		return errors.New("backup Git remote is not a valid URL or local path")
	}
	if parsed.Scheme == "" {
		return nil
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("backup Git remote must not contain query parameters or fragments")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.User != nil {
			return errors.New("HTTPS backup Git remote must not contain inline credentials; use a Git credential helper")
		}
		return nil
	case "ssh":
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return errors.New("SSH backup Git remote must not contain an inline password; use SSH keys or an agent")
			}
		}
		return nil
	case "file":
		if parsed.User != nil {
			return errors.New("file backup Git remote must not contain user information")
		}
		return nil
	default:
		return fmt.Errorf("backup Git remote scheme %q is not supported; use https, ssh, file, or a local path", parsed.Scheme)
	}
}

func isWindowsLocalPath(path string) bool {
	isDrivePath := len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
	return isDrivePath || strings.HasPrefix(path, `\\`)
}

func isSCPLikeSSHRemote(remote string) bool {
	if strings.Contains(remote, "://") || strings.ContainsAny(remote, "\r\n\t ") {
		return false
	}
	colon := strings.IndexByte(remote, ':')
	if colon <= 0 || colon == len(remote)-1 {
		return false
	}
	host := remote[:colon]
	if strings.ContainsAny(host, `/\`) {
		return false
	}
	if at := strings.IndexByte(host, '@'); at == 0 || at == len(host)-1 {
		return false
	}
	return true
}

func normalizeLocalRemote(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" || isSCPLikeSSHRemote(remote) {
		return remote, nil
	}
	if isWindowsLocalPath(remote) {
		if runtime.GOOS == "windows" {
			return filepath.Abs(remote)
		}
		return remote, nil
	}
	parsed, err := url.Parse(remote)
	if err != nil {
		return "", errors.New("backup Git remote is not a valid URL or local path")
	}
	if parsed.Scheme != "" {
		return remote, nil
	}
	resolved, err := filepath.Abs(expandHome(remote))
	if err != nil {
		return "", fmt.Errorf("resolve local backup Git remote: %w", err)
	}
	return resolved, nil
}

func pathIsWithin(root, candidate string) (bool, error) {
	canonicalRoot, err := canonicalPotentialPath(root)
	if err != nil {
		return false, err
	}
	canonicalCandidate, err := canonicalPotentialPath(candidate)
	if err != nil {
		return false, err
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		canonicalRoot = strings.ToLower(canonicalRoot)
		canonicalCandidate = strings.ToLower(canonicalCandidate)
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalCandidate)
	if err != nil {
		return false, err
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative), nil
}

func ensurePrivateDir(path string) error {
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := rejectSymlinkedPathComponents(path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private directory %s must not be a symlink", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", path)
		}
		if runtime.GOOS == "windows" {
			return hardenPrivatePath(path, true)
		}
		return validatePrivateMode(path, info, 0o700)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Chmod(path, 0o700)
	}
	return hardenPrivatePath(path, true)
}

func rejectSymlinkedPathComponents(path string) error {
	current, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current = filepath.Clean(current)
	// Backup paths are local owner-controlled inputs. Reject stationary aliases;
	// owner-only directory validation is the boundary against other principals.
	// Concurrent mutation by another process running as the same owner is outside
	// the local archive threat model because it already has equivalent file access.
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			parent := filepath.Dir(current)
			// Root-level aliases such as macOS /var and /tmp are OS-managed;
			// user-controlled aliases below the filesystem root are rejected.
			if filepath.Dir(parent) != parent {
				return fmt.Errorf("path component %s must not be a symlink", current)
			}
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private directory %s must not be a symlink", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return validatePrivateMode(path, info, 0o700)
}

func validatePrivateMode(path string, info os.FileInfo, want os.FileMode) error {
	if runtime.GOOS == "windows" {
		return validatePlatformPrivatePath(path, info.IsDir())
	}
	if info.Mode().Perm() != want {
		return fmt.Errorf("%s is not owner-only: mode %04o, want %04o", path, info.Mode().Perm(), want)
	}
	return nil
}

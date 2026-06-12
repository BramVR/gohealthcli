package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type oauthClientSource struct {
	kind     string
	path     string
	provider string
	item     string
}

type parsedConfig struct {
	archivePath         string
	defaultDataTypes    []string
	oauthClient         oauthClientSource
	credentialStore     credentialStoreConfig
	credentialStoreSeen bool
}

type configCheck struct {
	oauthClientSource string
	credentialStore   string
}

type fullConfigCheck struct {
	archivePath      string
	defaultDataTypes []string
	oauthClient      oauthClientSource
	credentialStore  credentialStoreConfig
}

// resolveConfiguredArchivePath was the legacy read+write resolver. PRD
// #144 slice 1 (issue #155) moved the four read commands to
// readArchivePathResolver — which owns the read-side relaxation
// (`--db` alone wins over a missing or default config) plus the
// user-facing error wording (names `--db` and `--config`, not the
// internal `archive_path` field). Write commands keep the stricter
// `archive_path` agreement via inspectFullConfig / inspectIdentityConfig
// (this file) so the no-callers `resolveConfiguredArchivePath` symbol
// was deleted; consult `readArchivePathResolver` (read commands) or the
// inspect* helpers (write commands) when reasoning about either side.

func readConfigArchivePath(configPath string) (string, bool, error) {
	info, err := os.Stat(configPath)
	if errors.Is(err, os.ErrNotExist) && configPath == defaultConfigPath() {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if err := validateOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return "", false, err
	}
	if info.IsDir() {
		return "", false, fmt.Errorf("%s is a directory", configPath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		mode := info.Mode().Perm()
		return "", false, fmt.Errorf("%s is not owner-only: mode %04o, want 0600", configPath, mode)
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return "", false, err
	}
	config, err := parseConfig(string(configBytes))
	if err != nil {
		return "", false, err
	}
	if config.archivePath == "" {
		return "", false, errors.New("missing archive_path")
	}
	return config.archivePath, true, nil
}

func parseOAuthClientSource(oauthClientFile, secretProvider, oauthClientItem string) (oauthClientSource, error) {
	if oauthClientFile != "" {
		if secretProvider != "" || oauthClientItem != "" {
			return oauthClientSource{}, errors.New("use either --oauth-client-file or --secret-provider with --oauth-client-item")
		}
		absPath, err := filepath.Abs(oauthClientFile)
		if err != nil {
			return oauthClientSource{}, errors.New("resolve OAuth client file path")
		}
		return oauthClientSource{kind: "file", path: absPath}, nil
	}
	if secretProvider != "" || oauthClientItem != "" {
		// Name the flag the user actually provided first, then the
		// missing one, so the error reads in the right dependency
		// direction (issue #150).
		if secretProvider == "" {
			return oauthClientSource{}, errors.New("--oauth-client-item requires --secret-provider")
		}
		if oauthClientItem == "" {
			return oauthClientSource{}, errors.New("--secret-provider requires --oauth-client-item")
		}
		return oauthClientSource{kind: "secret_provider", provider: secretProvider, item: oauthClientItem}, nil
	}
	return oauthClientSource{}, errors.New("requires --oauth-client-file or --secret-provider with --oauth-client-item")
}

func createConfigFile(configPath, archivePath string, source oauthClientSource) (err error) {
	if err := ensureOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return err
	}

	file, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(configPath)
		}
	}()

	if _, err := fmt.Fprint(file, configContent(configPath, archivePath, source)); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if !usesPOSIXPermissions() {
		return nil
	}
	return os.Chmod(configPath, 0o600)
}

func configContent(configPath, archivePath string, source oauthClientSource) string {
	var builder strings.Builder
	builder.WriteString("# gohealthcli config\n\n")
	builder.WriteString("archive_path = ")
	builder.WriteString(strconv.Quote(archivePath))
	builder.WriteString("\n")
	builder.WriteString("default_data_types = [\n")
	for _, dataType := range defaultDataTypes {
		builder.WriteString("  ")
		builder.WriteString(strconv.Quote(dataType))
		builder.WriteString(",\n")
	}
	builder.WriteString("]\n\n")
	builder.WriteString("[oauth_client]\n")
	builder.WriteString("source = ")
	builder.WriteString(strconv.Quote(source.kind))
	builder.WriteString("\n")
	switch source.kind {
	case "file":
		builder.WriteString("path = ")
		builder.WriteString(strconv.Quote(source.path))
		builder.WriteString("\n")
	case "secret_provider":
		builder.WriteString("provider = ")
		builder.WriteString(strconv.Quote(source.provider))
		builder.WriteString("\nitem = ")
		builder.WriteString(strconv.Quote(source.item))
		builder.WriteString("\n")
	}
	store := defaultCredentialStoreConfig()
	builder.WriteString("\n[credential_store]\n")
	builder.WriteString("type = ")
	builder.WriteString(strconv.Quote(store.kind))
	switch store.kind {
	case "os_native":
		builder.WriteString("\nservice = ")
		builder.WriteString(strconv.Quote(store.service))
	case "file":
		builder.WriteString("\npath = ")
		builder.WriteString(strconv.Quote(store.path))
	}
	builder.WriteString("\n")
	return builder.String()
}

func validateConfig(configPath, archivePath string) error {
	_, err := inspectConfig(configPath, archivePath)
	return err
}

func inspectConfig(configPath, archivePath string) (configCheck, error) {
	config, err := inspectFullConfig(configPath, archivePath)
	if err != nil {
		return configCheck{}, err
	}
	return configCheck{
		oauthClientSource: config.oauthClient.kind,
		credentialStore:   config.credentialStore.kind,
	}, nil
}

func inspectFullConfig(configPath, archivePath string) (fullConfigCheck, error) {
	if err := validateOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return fullConfigCheck{}, err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return fullConfigCheck{}, err
	}
	if info.IsDir() {
		return fullConfigCheck{}, fmt.Errorf("%s is a directory", configPath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		mode := info.Mode().Perm()
		return fullConfigCheck{}, fmt.Errorf("%s is not owner-only: mode %04o, want 0600", configPath, mode)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return fullConfigCheck{}, err
	}
	config, err := parseConfig(string(configBytes))
	if err != nil {
		return fullConfigCheck{}, err
	}
	if config.archivePath == "" {
		return fullConfigCheck{}, errors.New("missing archive_path")
	}
	if config.archivePath != archivePath {
		return fullConfigCheck{}, fmt.Errorf("archive_path points to %s, want %s", config.archivePath, archivePath)
	}
	if err := validateDefaultDataTypes(config.defaultDataTypes); err != nil {
		return fullConfigCheck{}, err
	}
	if err := validateOAuthClientConfig(config.oauthClient); err != nil {
		return fullConfigCheck{}, err
	}
	if !config.credentialStoreSeen && config.credentialStore.kind == "" {
		config.credentialStore = defaultCredentialStoreConfig()
	}
	if err := validateCredentialStoreConfig(config.credentialStore); err != nil {
		return fullConfigCheck{}, err
	}
	return fullConfigCheck{
		archivePath:      config.archivePath,
		defaultDataTypes: config.defaultDataTypes,
		oauthClient:      config.oauthClient,
		credentialStore:  config.credentialStore,
	}, nil
}

func inspectIdentityConfig(configPath, archivePath string) (fullConfigCheck, error) {
	if err := validateOwnerOnlyDir(filepath.Dir(configPath)); err != nil {
		return fullConfigCheck{}, err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return fullConfigCheck{}, err
	}
	if info.IsDir() {
		return fullConfigCheck{}, fmt.Errorf("%s is a directory", configPath)
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		mode := info.Mode().Perm()
		return fullConfigCheck{}, fmt.Errorf("%s is not owner-only: mode %04o, want 0600", configPath, mode)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return fullConfigCheck{}, err
	}
	config, err := parseConfig(string(configBytes))
	if err != nil {
		return fullConfigCheck{}, err
	}
	if config.archivePath == "" {
		return fullConfigCheck{}, errors.New("missing archive_path")
	}
	if config.archivePath != archivePath {
		return fullConfigCheck{}, fmt.Errorf("archive_path points to %s, want %s", config.archivePath, archivePath)
	}
	if err := validateDefaultDataTypes(config.defaultDataTypes); err != nil {
		return fullConfigCheck{}, err
	}
	if !config.credentialStoreSeen && config.credentialStore.kind == "" {
		config.credentialStore = defaultCredentialStoreConfig()
	}
	if err := validateCredentialStoreConfig(config.credentialStore); err != nil {
		return fullConfigCheck{}, err
	}
	return fullConfigCheck{
		archivePath:      config.archivePath,
		defaultDataTypes: config.defaultDataTypes,
		// oauthClient is returned without parsing/shape validation so the
		// sync auto-refresh path can reach it without forcing every
		// identity-only command to fully validate the OAuth client file.
		// The owner-only permission invariant is still enforced when a
		// refresh actually runs: loadOAuthClientConfig reads the file via
		// readOwnerOnlyOAuthClientFile (the same check validateOAuthClientFile
		// uses), so a loose-permission or missing file fails the refresh
		// rather than the happy-path read.
		oauthClient:     config.oauthClient,
		credentialStore: config.credentialStore,
	}, nil
}

func parseConfig(content string) (parsedConfig, error) {
	var config parsedConfig
	section := ""
	lines := strings.Split(content, "\n")
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(stripInlineComment(lines[index]))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			if section == "credential_store" {
				config.credentialStoreSeen = true
			}
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return parsedConfig{}, fmt.Errorf("malformed config line %d", index+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if section == "" && key == "default_data_types" {
			dataTypes, nextIndex, err := parseStringArray(lines, index, value)
			if err != nil {
				return parsedConfig{}, err
			}
			config.defaultDataTypes = dataTypes
			index = nextIndex
			continue
		}

		parsedValue, err := parseQuotedValue(value, key)
		if err != nil {
			return parsedConfig{}, err
		}
		switch section {
		case "":
			if key == "archive_path" {
				config.archivePath = parsedValue
			}
		case "oauth_client":
			switch key {
			case "source":
				config.oauthClient.kind = parsedValue
			case "path":
				config.oauthClient.path = parsedValue
			case "provider":
				config.oauthClient.provider = parsedValue
			case "item":
				config.oauthClient.item = parsedValue
			}
		case "credential_store":
			switch key {
			case "type":
				config.credentialStore.kind = parsedValue
			case "service":
				config.credentialStore.service = parsedValue
			case "path":
				config.credentialStore.path = parsedValue
			}
		}
	}
	return config, nil
}

func parseStringArray(lines []string, startIndex int, firstValue string) ([]string, int, error) {
	if strings.HasPrefix(firstValue, "[") && firstValue != "[" {
		if strings.HasSuffix(firstValue, "]") {
			values, err := parseInlineStringArray(firstValue)
			if err != nil {
				return nil, startIndex, err
			}
			return values, startIndex, nil
		}
		firstLine := strings.TrimSpace(strings.TrimPrefix(firstValue, "["))
		values, err := parseStringArrayItems(firstLine)
		if err != nil {
			return nil, startIndex, err
		}
		return parseStringArrayContinuation(lines, startIndex, values)
	}
	if firstValue != "[" {
		return nil, startIndex, errors.New("default_data_types must be a string array")
	}
	return parseStringArrayContinuation(lines, startIndex, nil)
}

func parseStringArrayContinuation(lines []string, startIndex int, values []string) ([]string, int, error) {
	for index := startIndex + 1; index < len(lines); index++ {
		line := strings.TrimSpace(stripInlineComment(lines[index]))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "]" {
			return values, index, nil
		}
		closeArray := strings.HasSuffix(line, "]")
		if closeArray {
			line = strings.TrimSpace(strings.TrimSuffix(line, "]"))
		}
		lineValues, err := parseStringArrayItems(line)
		if err != nil {
			return nil, startIndex, err
		}
		values = append(values, lineValues...)
		if closeArray {
			return values, index, nil
		}
	}
	return nil, startIndex, errors.New("default_data_types array is not closed")
}

func parseInlineStringArray(value string) ([]string, error) {
	if !strings.HasSuffix(value, "]") {
		return nil, errors.New("default_data_types array is not closed")
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
	if inner == "" {
		return []string{}, nil
	}
	return parseStringArrayItems(inner)
}

func parseStringArrayItems(value string) ([]string, error) {
	var values []string
	start := 0
	inString := false
	escaped := false
	for index, char := range value {
		switch {
		case escaped:
			escaped = false
		case char == '\\' && inString:
			escaped = true
		case char == '"':
			inString = !inString
		case char == ',' && !inString:
			parsedValue, err := parseInlineStringArrayValue(value[start:index])
			if err != nil {
				return nil, err
			}
			values = append(values, parsedValue)
			start = index + 1
		}
	}
	tail := strings.TrimSpace(value[start:])
	if tail == "" {
		return values, nil
	}
	parsedValue, err := parseInlineStringArrayValue(tail)
	if err != nil {
		return nil, err
	}
	values = append(values, parsedValue)
	return values, nil
}

func parseInlineStringArrayValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("default_data_types array contains an empty value")
	}
	parsed, err := parseQuotedValue(value, "default_data_types")
	if err != nil {
		return "", err
	}
	return parsed, nil
}

func stripInlineComment(line string) string {
	inString := false
	escaped := false
	for index, char := range line {
		switch {
		case escaped:
			escaped = false
		case char == '\\' && inString:
			escaped = true
		case char == '"':
			inString = !inString
		case char == '#' && !inString:
			return line[:index]
		}
	}
	return line
}

func parseQuotedValue(value, key string) (string, error) {
	parsed, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("%s must be a quoted string", key)
	}
	return parsed, nil
}

func validateDefaultDataTypes(dataTypes []string) error {
	if dataTypes == nil {
		return errors.New("missing default_data_types")
	}
	if len(dataTypes) == 0 {
		return errors.New("default Data Types must include at least one Data Type")
	}
	seen := make(map[string]struct{}, len(dataTypes))
	for _, dataType := range dataTypes {
		entry, ok := googleHealthDataTypes.Lookup(dataType)
		if !ok || !entry.DefaultConfigType {
			return fmt.Errorf("unsupported default Data Type %s", dataType)
		}
		if _, ok := seen[dataType]; ok {
			return fmt.Errorf("duplicate default Data Type %s", dataType)
		}
		seen[dataType] = struct{}{}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// defaultConfigPath / defaultArchivePath return the home-anchored default
// locations documented in docs/security.md. Per the XDG Base Directory
// spec a RELATIVE XDG_CONFIG_HOME / XDG_DATA_HOME value "MUST be ignored"
// (it must be an absolute path), so a relative override is treated the
// same as unset and the resolver falls through to the HOME-anchored path.
// When HOME is also unset/empty the os.UserHomeDir() error leaves home
// empty and the returned path is RELATIVE — a non-absolute return value
// is the in-band signal that the default could not be anchored. The
// ParseCommon gate (requireAnchoredDefaultPaths, issue #249) rejects that
// case loudly before any command touches the filesystem; these helpers
// stay string-valued so the flag defaults and the
// `configPath == defaultConfigPath()` default-config detection at the call
// site keep comparing cleanly.

func defaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "gohealthcli", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gohealthcli", "config.toml")
}

func defaultArchivePath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "gohealthcli", "gohealthcli.sqlite")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "gohealthcli", "gohealthcli.sqlite")
}

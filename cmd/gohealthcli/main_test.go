package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestNoArgsPrintsHelpToStdout pins the PRD #143 slice 3 behaviour: a bare
// `gohealthcli` invocation now exits 0 and prints the top-level help (the
// "Subcommands:" listing) to stdout, matching `git` / `kubectl` / `docker`
// discoverability conventions. The explicit `--help` path is unchanged and
// keeps writing to stderr per stdlib flag-package convention — see
// TestHelpExitsSuccessfully below.
func TestNoArgsPrintsHelpToStdout(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Subcommands:") {
		t.Fatalf("stdout missing 'Subcommands:' help block; got:\n%s", stdout.String())
	}
	if strings.Contains(stderr.String(), "missing command") {
		t.Fatalf("stderr still emits 'missing command'; got: %q", stderr.String())
	}
}

// TestUnknownCommandPrintsHintAndExitsNonZero covers the unknown-command
// path for an input that has no close Levenshtein match: stderr must carry
// both the "unknown command" line (preserved from the pre-slice-3 shape) and
// the canonical "Run 'gohealthcli --help'" hint introduced by slice 3, and
// the process must exit 1. We deliberately choose "bogus-cmd" because it is
// far from every registered command, ensuring no "Did you mean" line
// interleaves and the hint line is the sole new addition under test.
func TestUnknownCommandPrintsHintAndExitsNonZero(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "bogus-cmd")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "unknown command: bogus-cmd") {
		t.Fatalf("stderr missing 'unknown command: bogus-cmd'; got: %q", out)
	}
	if !strings.Contains(out, "Run 'gohealthcli --help' for a list of commands.") {
		t.Fatalf("stderr missing canonical help hint; got: %q", out)
	}
	if strings.Contains(out, "Did you mean") {
		t.Fatalf("stderr should not suggest for unrelated typo; got: %q", out)
	}
}

// TestUnknownCommandSuggestsCloseMatch closes the loop on the suggestion
// pipeline: when the typo lands within the Levenshtein cutoff, stderr must
// carry a "Did you mean: <name>?" line between the unknown-command banner
// and the help hint. We assert on the full sentence so a future tweak to
// the suggestion phrasing trips this test rather than silently shipping a
// regression in UX.
func TestUnknownCommandSuggestsCloseMatch(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "stauts")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "Did you mean: status?") {
		t.Fatalf("stderr missing 'Did you mean: status?'; got: %q", out)
	}
	if !strings.Contains(out, "Run 'gohealthcli --help' for a list of commands.") {
		t.Fatalf("stderr missing canonical help hint; got: %q", out)
	}
}

func TestHelpExitsSuccessfully(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--help"},
		{"doctor", "--help"},
	} {
		code, stdout, stderr := runCommand(t, args...)

		if code != 0 {
			t.Fatalf("%v exit code = %d, want 0\nstderr: %s", args, code, stderr.String())
		}
		if stdout.String() != "" {
			t.Fatalf("%v stdout = %q, want empty", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "Usage of") {
			t.Fatalf("%v stderr missing usage: %q", args, stderr.String())
		}
	}
}

func TestTopLevelHelpListsVisibleSubcommandsFromRegistry(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	out := stderr.String()
	// Assert each visible command appears on its OWN dedicated line of the form
	// "  <name>   <short>". A bare strings.Contains check would false-pass when
	// the command name shows up inside another command's Short (e.g. "raw" is
	// substring of the "raw" command's own Short "Print raw provider JSON ...").
	lines := strings.Split(out, "\n")
	for _, cmd := range commands {
		if cmd.Hidden {
			continue
		}
		found := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			fields := strings.SplitN(trimmed, " ", 2)
			if len(fields) == 2 && fields[0] == cmd.Name && strings.Contains(line, cmd.Short) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--help missing dedicated line for %q (%q)\noutput:\n%s", cmd.Name, cmd.Short, out)
		}
	}
}

func TestTopLevelHelpOmitsHiddenSubcommands(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	// The "schema" command is the only hidden entry; assert it is filtered.
	// We look for it as a whole word in a "  schema  <short>" line so that
	// substrings inside other words (e.g. "describe-schema") don't false-positive.
	for _, line := range strings.Split(stderr.String(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "schema ") || trimmed == "schema" {
			t.Fatalf("--help should not list hidden subcommand 'schema'; got line %q\nfull output:\n%s", line, stderr.String())
		}
	}
}

func TestTopLevelHelpStillShowsGlobalFlags(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	out := stderr.String()
	for _, flagName := range []string{"-config", "-db", "-json", "-plain", "-no-input", "-version"} {
		if !strings.Contains(out, flagName) {
			t.Errorf("--help missing global flag %q\noutput:\n%s", flagName, out)
		}
	}
}

// TestHelpVerbMatchesTopLevelHelp asserts `gohealthcli help` is an alias for
// `gohealthcli --help`: same exit code, identical stderr bytes. The verb form
// reads more naturally than the flag form and is what users reach for first.
func TestHelpVerbMatchesTopLevelHelp(t *testing.T) {
	t.Parallel()
	codeFlag, _, stderrFlag := runCommand(t, "--help")
	codeVerb, stdoutVerb, stderrVerb := runCommand(t, "help")

	if codeFlag != 0 {
		t.Fatalf("--help exit code = %d, want 0\nstderr: %s", codeFlag, stderrFlag.String())
	}
	if codeVerb != 0 {
		t.Fatalf("help exit code = %d, want 0\nstderr: %s", codeVerb, stderrVerb.String())
	}
	if stdoutVerb.String() != "" {
		t.Fatalf("help stdout = %q, want empty", stdoutVerb.String())
	}
	if stderrVerb.String() != stderrFlag.String() {
		t.Fatalf("help stderr differs from --help stderr\nhelp:\n%s\n--help:\n%s", stderrVerb.String(), stderrFlag.String())
	}
}

// TestHelpVerbWithKnownSubcommand asserts `gohealthcli help status` prints
// the status subcommand's Long description followed by its accepted flags,
// exits 0, and does not write to stdout.
func TestHelpVerbWithKnownSubcommand(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "help", "status")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	// Look up the entry in the same registry the implementation reads.
	var statusDef commandDef
	for _, cmd := range commands {
		if cmd.Name == "status" {
			statusDef = cmd
			break
		}
	}
	if statusDef.Name == "" {
		t.Fatalf("registry missing status entry; cannot validate help output")
	}

	out := stderr.String()
	// The Long body is multi-paragraph; assert on its first sentence so the
	// test stays meaningful without re-encoding the whole prose.
	firstSentence := strings.SplitN(statusDef.Long, ".", 2)[0]
	if !strings.Contains(out, firstSentence) {
		t.Errorf("help status missing Long prefix %q\noutput:\n%s", firstSentence, out)
	}
	// Status accepts the standard common flags — assert each is listed.
	for _, flagName := range []string{"-config", "-db", "-json", "-plain", "-no-input"} {
		if !strings.Contains(out, flagName) {
			t.Errorf("help status missing flag %q\noutput:\n%s", flagName, out)
		}
	}
}

// TestHelpVerbWithUnknownSubcommand asserts `gohealthcli help bogus` exits 1
// with `unknown command: bogus` on stderr. The did-you-mean suggestion list
// is deferred to slice 3 of PRD #143 and explicitly NOT asserted here.
func TestHelpVerbWithUnknownSubcommand(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "help", "bogus")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown command: bogus") {
		t.Fatalf("stderr missing unknown-command message: %q", stderr.String())
	}
}

// TestHelpVerbWithHiddenSubcommand asserts that hidden registry entries (the
// `schema` build-time helper) are still surfaced when looked up explicitly:
// `gohealthcli help schema` prints its Long description and exits 0. Hidden
// only means "filtered from the top-level --help listing"; an explicit help
// lookup must still resolve it.
func TestHelpVerbWithHiddenSubcommand(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "help", "schema")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	var schemaDef commandDef
	for _, cmd := range commands {
		if cmd.Name == "schema" {
			schemaDef = cmd
			break
		}
	}
	if schemaDef.Name == "" || !schemaDef.Hidden {
		t.Fatalf("registry missing hidden schema entry; cannot validate help output")
	}

	firstSentence := strings.SplitN(schemaDef.Long, ".", 2)[0]
	if !strings.Contains(stderr.String(), firstSentence) {
		t.Errorf("help schema missing Long prefix %q\noutput:\n%s", firstSentence, stderr.String())
	}
}

// TestHelpVerbRejectsExtraArguments asserts the `help` verb fails fast when
// given unexpected positional arguments, rather than silently dropping them.
// This mirrors how every other subcommand rejects unknown positionals and
// surfaces typos like `help status extra` instead of masking them.
func TestHelpVerbRejectsExtraArguments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"extras after known cmd", []string{"help", "status", "extra"}},
		{"extras after --help form", []string{"help", "--help", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, tc.args...)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), "unexpected arguments") {
				t.Fatalf("stderr missing 'unexpected arguments' message: %q", stderr.String())
			}
		})
	}
}

func TestCountArchiveRowsRejectsUnknownTable(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open memory archive: %v", err)
	}
	defer db.Close()

	_, err = countArchiveRows(context.Background(), db, "data_points; DROP TABLE data_points")
	if err == nil {
		t.Fatal("countArchiveRows error = nil, want unsupported table")
	}
	if !strings.Contains(err.Error(), "unsupported Health Archive table") {
		t.Fatalf("countArchiveRows error = %v, want unsupported table", err)
	}
}

func TestParseStepsDailyRollupRequiresCivilEndTime(t *testing.T) {
	t.Parallel()
	_, err := parseGoogleHealthRollup(archivedConnection{
		providerName: "googlehealth",
		id:           "googlehealth:111111256096816351",
	}, "steps", "dailyRollUp", json.RawMessage(`{
		"steps": {"countSum": "1234"},
		"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}}
	}`))
	if err == nil || !strings.Contains(err.Error(), "missing civilEndTime") {
		t.Fatalf("parse error = %v, want missing civilEndTime", err)
	}
}

func TestParseOAuthClientConfigContentPinsHTTPSAndGoogleHosts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name:    "http auth_uri rejected",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"http://accounts.google.com/o/oauth2/v2/auth"}}`,
			wantErr: true,
		},
		{
			name:    "attacker-host token_uri rejected",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","token_uri":"https://attacker.example.com/token"}}`,
			wantErr: true,
		},
		{
			name:    "empty uris default to Google and accepted",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret"}}`,
			wantErr: false,
		},
		{
			name:    "valid Google https uris accepted",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"https://accounts.google.com/o/oauth2/v2/auth","token_uri":"https://oauth2.googleapis.com/token"}}`,
			wantErr: false,
		},
		{
			name:    "uppercase scheme and host accepted (case-insensitive)",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"HTTPS://Accounts.Google.Com/o/oauth2/v2/auth","token_uri":"HTTPS://OAuth2.GoogleAPIs.Com/token"}}`,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseOAuthClientConfigContent([]byte(tt.content))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseOAuthClientConfigContent error = nil, want https/Google host rejection")
				}
				if !strings.Contains(err.Error(), "https") || !strings.Contains(err.Error(), "Google OAuth host") {
					t.Fatalf("error = %q, want mention of https and Google OAuth host", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOAuthClientConfigContent error = %v, want accepted", err)
			}
		})
	}
}

func TestOAuthScopesUseRecognizedGoogleHealthScopes(t *testing.T) {
	t.Parallel()
	scopes := oauthScopesForDataTypes(defaultDataTypes)
	wantScopes := []string{
		googleHealthActivityReadonlyScope,
		googleHealthHealthMetricsReadonlyScope,
		googleHealthSleepReadonlyScope,
		googleHealthProfileReadonlyScope,
	}
	if !slices.Equal(scopes, wantScopes) {
		t.Fatalf("scopes = %v, want configured Google Health readonly scopes %v", scopes, wantScopes)
	}
	for _, scope := range scopes {
		for _, invalid := range []string{"settings.readonly"} {
			if strings.Contains(scope, invalid) {
				t.Fatalf("scopes include unrecognized Google Health scope %q: %v", invalid, scopes)
			}
		}
	}
}

func TestOAuthScopesForEmptyDataTypesRequestOnlyProfileScope(t *testing.T) {
	t.Parallel()
	for name, dataTypes := range map[string][]string{"nil": nil, "empty": {}} {
		if scopes := oauthScopesForDataTypes(dataTypes); !slices.Equal(scopes, []string{googleHealthProfileReadonlyScope}) {
			t.Fatalf("scopes for %s dataTypes = %v, want only the profile scope", name, scopes)
		}
	}
}

func TestListenForOAuthRedirectPreservesEmptyLoopbackPath(t *testing.T) {
	t.Parallel()
	listener, redirectURI, err := listenForOAuthRedirect([]string{"http://localhost"})
	if err != nil {
		t.Fatalf("listen for OAuth redirect: %v", err)
	}
	defer listener.Close()

	parsed, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect URI: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" {
		t.Fatalf("redirect URI = %s, want dynamic loopback with empty path", redirectURI)
	}
}

func TestParseOAuthTokenResponseRequiresRefreshToken(t *testing.T) {
	t.Parallel()
	_, err := parseOAuthTokenResponse([]byte(`{
		"access_token": "access-secret-value",
		"expires_in": 3600,
		"scope": "https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly",
		"token_type": "Bearer"
	}`), time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "refresh token") {
		t.Fatalf("parse token response error = %v, want missing refresh token", err)
	}
}

func TestOSNativeCredentialStoreDoesNotSendTokenAsArgument(t *testing.T) {
	t.Parallel()
	testRuntime := productionRuntimeAdapters()
	testRuntime.currentOS = "darwin"

	var gotService string
	var gotKey string
	var gotContent []byte
	testRuntime.runSecurityAddGenericPassword = func(_ context.Context, service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStoreWithRuntime(credentialStoreConfig{kind: "os_native", service: "gohealthcli"}, testRuntime)
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("security command target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("security command content missing token material: %s", string(gotContent))
	}
}

func TestSecurityCredentialStoreFeedsPromptWithoutTokenArgument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake security executable uses POSIX shell")
	}

	tempDir := t.TempDir()
	argvPath := filepath.Join(tempDir, "argv.txt")
	stdinPath := filepath.Join(tempDir, "stdin.txt")
	securityPath := filepath.Join(tempDir, "security")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GOHEALTHCLI_TEST_SECURITY_ARGV\"\ncat > \"$GOHEALTHCLI_TEST_SECURITY_STDIN\"\n"
	if err := os.WriteFile(securityPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake security: %v", err)
	}
	t.Setenv("GOHEALTHCLI_TEST_SECURITY_ARGV", argvPath)
	t.Setenv("GOHEALTHCLI_TEST_SECURITY_STDIN", stdinPath)
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	content := []byte(`{"access_token":"access-secret-value"}`)
	if err := runSecurityAddGenericPasswordCommand(context.Background(), "gohealthcli", "googlehealth:111", content); err != nil {
		t.Fatalf("security command: %v", err)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if bytes.Contains(argv, []byte("access-secret-value")) {
		t.Fatalf("security argv contains token material: %s", string(argv))
	}
	if !bytes.Contains(argv, []byte("-w")) {
		t.Fatalf("security argv missing prompt flag: %s", string(argv))
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	wantStdin := string(content) + "\n" + string(content) + "\n"
	if string(stdin) != wantStdin {
		t.Fatalf("security stdin = %q, want password and confirmation", string(stdin))
	}
}

func TestLinuxOSNativeCredentialStoreUsesSecretToolContent(t *testing.T) {
	t.Parallel()
	testRuntime := productionRuntimeAdapters()
	testRuntime.currentOS = "linux"

	var gotService string
	var gotKey string
	var gotContent []byte
	testRuntime.runSecretToolStore = func(_ context.Context, service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStoreWithRuntime(credentialStoreConfig{kind: "os_native", service: "gohealthcli"}, testRuntime)
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("secret-tool target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("secret-tool content missing token material: %s", string(gotContent))
	}
}

func TestWindowsOSNativeCredentialStoreUsesCredentialManagerContent(t *testing.T) {
	t.Parallel()
	testRuntime := productionRuntimeAdapters()
	testRuntime.currentOS = "windows"

	var gotService string
	var gotKey string
	var gotContent []byte
	testRuntime.runWindowsCredentialWrite = func(_ context.Context, service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStoreWithRuntime(credentialStoreConfig{kind: "os_native", service: "gohealthcli"}, testRuntime)
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("Windows Credential Manager target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("Windows Credential Manager content missing token material: %s", string(gotContent))
	}
}

func TestValidateConfigDoesNotCreateMissingParent(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	parentPath := filepath.Join(tempDir, "missing")

	err := validateConfig(filepath.Join(parentPath, "config.toml"), filepath.Join(tempDir, "archive.sqlite"))
	if err == nil {
		t.Fatal("validateConfig error = nil, want missing parent failure")
	}
	if _, statErr := os.Stat(parentPath); !os.IsNotExist(statErr) {
		t.Fatalf("parent stat err = %v, want not exist", statErr)
	}
}

func TestArchiveDSNUsesAbsoluteFileURI(t *testing.T) {
	t.Parallel()
	dsn, err := archiveDSN("relative.sqlite", false)
	if err != nil {
		t.Fatalf("archiveDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:///") {
		t.Fatalf("dsn = %q, want absolute file URI", dsn)
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys%3Don") {
		t.Fatalf("dsn = %q, want foreign key pragma", dsn)
	}
	readOnlyDSN, err := archiveDSN("relative.sqlite", true)
	if err != nil {
		t.Fatalf("archiveDSN readonly: %v", err)
	}
	if !strings.Contains(readOnlyDSN, "mode=ro") {
		t.Fatalf("dsn = %q, want readonly mode", readOnlyDSN)
	}
}

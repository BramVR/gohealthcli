package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

func TestIdentityRefreshesArchivedGoogleIdentity(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindIdentityFetchFake(t, &testRuntime, "connect-access-secret", googleIdentity{
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"111111256096816351","legacyUserId":"Z9Y8X7","refreshed":true}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("identity exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_refreshed")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "google_health_user_id", "111111256096816351")
	assertJSONString(t, got, "legacy_fitbit_user_id", "Z9Y8X7")
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") || strings.Contains(stdout.String()+stderr.String(), "connect-refresh-secret") {
		t.Fatalf("identity output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}

	db := openArchiveForTest(t, archivePath)
	var legacyUserID, identityJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT legacy_fitbit_user_id, google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&legacyUserID, &identityJSON); err != nil {
		t.Fatalf("query refreshed identity: %v", err)
	}
	if legacyUserID != "Z9Y8X7" {
		t.Fatalf("legacy_fitbit_user_id = %q, want refreshed value", legacyUserID)
	}
	if !strings.Contains(identityJSON, `"refreshed":true`) {
		t.Fatalf("google_identity_json = %s, want refreshed raw identity", identityJSON)
	}
}

func TestIdentityPlainIncludesStableIdentityFields(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindIdentityFetchFake(t, &testRuntime, "connect-access-secret", googleIdentity{
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
		rawJSON:            `{"healthUserId":"111111256096816351","legacyUserId":"A1B2C3"}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"identity", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("identity exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := "status: identity_refreshed\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ngoogle_health_user_id: 111111256096816351\nlegacy_fitbit_user_id: A1B2C3\nmessage: Google Identity refreshed\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestIdentityHumanOutputDistinguishesFailureStatuses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status string
		want   string
	}{
		{status: "identity_mismatch", want: "Google Identity mismatch\n"},
		{status: "identity_unavailable", want: "Google Identity unavailable\n"},
		{status: "identity_failed", want: "Google Identity failed\n"},
	} {
		t.Run(test.status, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			err := writeIdentityResult(identityResult{Status: test.status, Message: "message"}, outputMode{}, stdout)
			if err != nil {
				t.Fatalf("write identity result: %v", err)
			}
			if !strings.HasPrefix(stdout.String(), test.want) {
				t.Fatalf("stdout = %q, want prefix %q", stdout.String(), test.want)
			}
		})
	}
}

func TestIdentityRequiresArchivedConnection(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{failIfCalled: true})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("identity exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_unavailable")
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "gohealthcli connect") {
		t.Fatalf("message = %T(%v), want connect guidance", got["message"], got["message"])
	}
	if _, ok := got["connection_id"]; ok {
		t.Fatalf("connection_id = %v, want omitted when no Connection exists", got["connection_id"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestIdentityReportsAutoRefreshFailureBeforeProviderFetch carries the
// expired-token pin forward across the issue #273 parity change. Before
// #273, identity failed an expired token outright ("Connection token has
// expired"); now it attempts the sibling WithAutoRefresh path first, so
// the guarded behavior becomes: when that refresh fails, identity exits
// non-zero with the auto-refresh failure wording (naming `doctor
// --online` and `connect` recovery) and still never calls the Provider
// identity endpoint with a dead token.
func TestIdentityReportsAutoRefreshFailureBeforeProviderFetch(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	connectAt := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                connectAt,
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnectSetup(t, configPath, archivePath, testRuntime)
	testRuntime.now = func() time.Time {
		return time.Date(2026, 5, 31, 23, 1, 0, 0, time.UTC)
	}
	testRuntime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		return oauthTokenResponse{}, errors.New("OAuth token refresh failed with HTTP 400: invalid_grant")
	}
	testRuntime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		t.Fatalf("identity fetch should not be called when the token refresh failed")
		return googleIdentity{}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("identity exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "auto-refresh of Connection access token failed") || !strings.Contains(message, "gohealthcli connect") {
		t.Fatalf("message = %T(%v), want auto-refresh failure with reconnect guidance", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestIdentityCommandFailsFastWhenScopeMissing pins the second half of
// the issue #273 parity decision: identity's scope request comes from
// the same googleHealthIdentityEndpointScopes catalog its siblings use
// (key "getIdentity") instead of the historical nil. When the stored
// Connection's granted scopes do not cover it, the command exits
// non-zero, sets result.Status to "identity_scope_missing", names the
// recovery `gohealthcli connect` command in result.Message, and does
// NOT issue any Provider identity request — proving the pre-check
// happens before the upstream call, exactly like profile.
func TestIdentityCommandFailsFastWhenScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchiveViaSetup(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	// Strip every scope the catalog ties to getIdentity from the
	// stored Connection so AccessToken's scope pre-check fails. Using
	// the same catalog key the production code reads keeps this test
	// honest across future catalog revisions.
	required := googleHealthIdentityEndpointScopes["getIdentity"]
	requiredSet := make(map[string]struct{}, len(required))
	for _, scope := range required {
		requiredSet[scope] = struct{}{}
	}
	storedScopes := connectionGrantedScopes(t, archivePath)
	filtered := storedScopes[:0]
	for _, scope := range storedScopes {
		if _, drop := requiredSet[scope]; drop {
			continue
		}
		filtered = append(filtered, scope)
	}
	setConnectionTokenScopes(t, archivePath, filtered)

	testRuntime.fetchIdentity = func(string) (googleIdentity, error) {
		t.Fatal("fetchIdentity called despite missing scope")
		return googleIdentity{}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatalf("identity exit code = %d, want non-zero; stdout=%s", code, stdout.String())
	}
	var result identityResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "identity_scope_missing" {
		t.Fatalf("result.Status = %q, want identity_scope_missing", result.Status)
	}
	// The recovery hint either names `--add-scopes <keyword>` (when the
	// missing scope maps to a connectAddScopeKeywords entry) or names
	// `gohealthcli connect` again (generic fallback). Either way, the
	// message must mention `connect`.
	if !strings.Contains(result.Message, "gohealthcli connect") {
		t.Fatalf("result.Message = %q, want it to name `gohealthcli connect` recovery", result.Message)
	}
}

// TestIdentityCommandAutoRefreshesExpiredAccessToken pins the issue
// #273 parity decision: with an expired access token but valid refresh
// token and oauthClient.kind == "file", identity refreshes
// transparently — exactly like its devices/settings/irn-profile/profile
// siblings — persists the new token via UpdateConnectionTokenMetadata
// on the archive (the same handle openHealthArchiveConnectionAPI
// already returns), and exits 0 with status "identity_refreshed" plus
// the refreshed Google Identity archived on the Connection row.
func TestIdentityCommandAutoRefreshesExpiredAccessToken(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	connectAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                connectAt,
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnectSetup(t, configPath, archivePath, testRuntime)
	// Ensure the stored Connection carries every scope the catalog
	// requires for getIdentity, so this test still exercises auto-refresh
	// after a catalog revision moves getIdentity off the default-granted
	// scope set.
	for _, scope := range googleHealthIdentityEndpointScopes["getIdentity"] {
		addStoredConnectionScope(t, archivePath, scope)
	}
	// Force the stored access-token expires_at into the past so
	// AccessToken must take the auto-refresh path.
	setConnectionTokenExpiry(t, archivePath, "2026-01-01T00:00:00Z")

	identityNow := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	refreshedExpiresAt := identityNow.Add(time.Hour)
	testRuntime.now = func() time.Time { return identityNow }
	// Count refresh attempts so the implicit "refresh once, persist
	// once" contract is guarded against a regression where retries
	// would silently double-rotate the stored token.
	refreshCalls := 0
	bindRefreshOAuthTokenFake(t, &testRuntime, fakeRefreshConfig{
		wantRefreshToken: "connect-refresh-secret",
		accessToken:      "rotated-access-secret",
		expiresAt:        refreshedExpiresAt,
		calls:            &refreshCalls,
	})
	// identity reaches the Provider through runtime.fetchIdentity (via
	// FetchVerifiedIdentity), so the rotated-token assertion lives on
	// the runtime adapters seam.
	var calledWithToken string
	testRuntime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		calledWithToken = accessToken
		return googleIdentity{
			healthUserID:       "111111256096816351",
			legacyFitbitUserID: "Z9Y8X7",
			rawJSON:            `{"healthUserId":"111111256096816351","legacyUserId":"Z9Y8X7","refreshed":true}`,
		}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("identity exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}
	if calledWithToken != "rotated-access-secret" {
		t.Fatalf("fetchIdentity access token = %q, want rotated-access-secret", calledWithToken)
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_refreshed")
	assertJSONString(t, got, "legacy_fitbit_user_id", "Z9Y8X7")
	assertNoSecretWords(t, stdout.String()+stderr.String())

	// Refreshed expires_at must have been persisted to the archive's
	// token_metadata_json via UpdateConnectionTokenMetadata.
	gotMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if !strings.Contains(gotMetadata, refreshedExpiresAt.Format(time.RFC3339)) {
		t.Fatalf("archived token_metadata_json = %s, want refreshed expires_at %s", gotMetadata, refreshedExpiresAt.Format(time.RFC3339))
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshOAuthToken call count = %d, want 1 (no retry loop should double-rotate the stored token)", refreshCalls)
	}
	// The refreshed Google Identity must still land on the Connection
	// row — auto-refresh must not short-circuit the command's actual job.
	if !strings.Contains(archivedConnectionIdentityJSON(t, archivePath), `"refreshed":true`) {
		t.Fatalf("google_identity_json = %s, want refreshed raw identity", archivedConnectionIdentityJSON(t, archivePath))
	}
}

func TestIdentityRejectsDifferentGoogleIdentity(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindIdentityFetchFake(t, &testRuntime, "connect-access-secret", googleIdentity{
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"222222222222222222","legacyUserId":"Z9Y8X7"}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"identity", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("identity exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "identity_mismatch")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "different Google Identity") {
		t.Fatalf("message = %T(%v), want different identity refusal", got["message"], got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db := openArchiveForTest(t, archivePath)
	var healthUserID, legacyUserID, identityJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT google_health_user_id, legacy_fitbit_user_id, google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&healthUserID, &legacyUserID, &identityJSON); err != nil {
		t.Fatalf("query identity after mismatch: %v", err)
	}
	if healthUserID != "111111256096816351" || legacyUserID != "A1B2C3" {
		t.Fatalf("archived identity = (%q, %q), want unchanged", healthUserID, legacyUserID)
	}
	if strings.Contains(identityJSON, "222222222222222222") {
		t.Fatalf("different provider identity was archived: %s", identityJSON)
	}
}

func TestProfileArchivesSnapshotAndPrintsSummary(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindProfileFetchFake(t, &testRuntime, "connect-access-secret", googleProfile{
		healthUserID: "111111256096816351",
		rawJSON:      `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}`,
	}, nil)
	testRuntime.now = func() time.Time {
		return time.Date(2026, 6, 1, 10, 30, 0, 0, time.UTC)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("profile exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_archived")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "google_health_user_id", "111111256096816351")
	assertJSONString(t, got, "legacy_fitbit_user_id", "A1B2C3")
	assertJSONString(t, got, "fetched_at", "2026-06-01T10:30:00Z")
	snapshotID, ok := got["snapshot_id"].(float64)
	if !ok || snapshotID != 1 {
		t.Fatalf("snapshot_id = %T(%v), want 1", got["snapshot_id"], got["snapshot_id"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())

	db := openArchiveForTest(t, archivePath)
	var providerName, connectionID, rawJSON, fetchedAt string
	if err := db.QueryRowContext(context.Background(), `SELECT provider_name, connection_id, raw_json, fetched_at FROM identity_snapshots WHERE id = ?`, 1).Scan(&providerName, &connectionID, &rawJSON, &fetchedAt); err != nil {
		t.Fatalf("query profile snapshot: %v", err)
	}
	if providerName != "googlehealth" || connectionID != "googlehealth:111111256096816351" {
		t.Fatalf("snapshot owner = (%q, %q), want archived Connection", providerName, connectionID)
	}
	if rawJSON != `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}` {
		t.Fatalf("raw_json = %s, want provider profile JSON", rawJSON)
	}
	if fetchedAt != "2026-06-01T10:30:00Z" {
		t.Fatalf("fetched_at = %q, want fixed timestamp", fetchedAt)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
}

func TestProfilePlainIncludesStableSnapshotFields(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindProfileFetchFake(t, &testRuntime, "connect-access-secret", googleProfile{
		healthUserID: "111111256096816351",
		rawJSON:      `{"name":"users/111111256096816351/profile"}`,
	}, nil)
	testRuntime.now = func() time.Time {
		return time.Date(2026, 6, 1, 10, 30, 0, 0, time.UTC)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("profile exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := "status: profile_archived\nsnapshot_id: 1\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ngoogle_health_user_id: 111111256096816351\nlegacy_fitbit_user_id: A1B2C3\nfetched_at: 2026-06-01T10:30:00Z\nmessage: Profile Snapshot archived\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestProfileProviderFailureDoesNotArchiveSnapshot(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindProfileFetchFake(t, &testRuntime, "connect-access-secret", googleProfile{}, errors.New("Google Health profile request failed with HTTP 503"))

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_failed")
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, "HTTP 503") {
		t.Fatalf("message = %T(%v), want provider status", got["message"], got["message"])
	}
	if _, ok := got["snapshot_id"]; ok {
		t.Fatalf("snapshot_id = %v, want omitted on failure", got["snapshot_id"])
	}
	assertArchiveTableCount(t, archivePath, "identity_snapshots", 0)
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") || strings.Contains(stdout.String()+stderr.String(), "connect-refresh-secret") {
		t.Fatalf("profile output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestProfileFailsBeforeProviderWhenProfileScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googleHealthActivityReadonlyScope})
	testRuntime.fetchProfile = func(accessToken string) (googleProfile, error) {
		t.Fatalf("profile fetch should not be called when profile scope is missing")
		return googleProfile{}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	message, ok := got["message"].(string)
	if !ok || !strings.Contains(message, googleHealthProfileReadonlyScope) || !strings.Contains(message, "connect") {
		t.Fatalf("message = %T(%v), want profile-scope reconnect guidance", got["message"], got["message"])
	}
	assertArchiveTableCount(t, archivePath, "identity_snapshots", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestProfileRejectsAliasProfileWhenIdentityVerificationDiffers(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindProfileFetchFake(t, &testRuntime, "connect-access-secret", googleProfile{
		rawJSON: `{"name":"users/me/profile","profile":{"unit":"metric"}}`,
	}, nil)
	bindIdentityFetchFake(t, &testRuntime, "connect-access-secret", googleIdentity{
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "Z9Y8X7",
		rawJSON:            `{"healthUserId":"222222222222222222","legacyUserId":"Z9Y8X7"}`,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_mismatch")
	if _, ok := got["snapshot_id"]; ok {
		t.Fatalf("snapshot_id = %v, want omitted on mismatch", got["snapshot_id"])
	}
	assertArchiveTableCount(t, archivePath, "identity_snapshots", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestProfileRejectsDifferentGoogleIdentityWithoutArchiving(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindProfileFetchFake(t, &testRuntime, "connect-access-secret", googleProfile{
		healthUserID: "222222222222222222",
		rawJSON:      `{"name":"users/222222222222222222/profile"}`,
	}, nil)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("profile exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "profile_mismatch")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	if _, ok := got["snapshot_id"]; ok {
		t.Fatalf("snapshot_id = %v, want omitted on mismatch", got["snapshot_id"])
	}
	assertArchiveTableCount(t, archivePath, "identity_snapshots", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusReportsHealthArchiveCountsAndSyncRunsReadOnly(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	testRuntime := runtimeAdapters{}
	testRuntime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		t.Fatal("status should not call Provider identity")
		return googleIdentity{}, nil
	}
	testRuntime.fetchProfile = func(accessToken string) (googleProfile, error) {
		t.Fatal("status should not call Provider profile")
		return googleProfile{}, nil
	}
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("status should not call Provider raw endpoints")
		return nil, nil
	}
	testRuntime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		t.Fatal("status should not refresh tokens")
		return oauthTokenResponse{}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"status",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONString(t, got, "archive_path", archivePath)
	assertJSONNumber(t, got, "schema_version", currentSchemaVersion)
	assertJSONNumber(t, got, "data_point_count", 3)
	assertJSONNumber(t, got, "rollup_count", 1)
	assertJSONNumber(t, got, "profile_snapshot_count", 1)
	assertJSONNumber(t, got, "sync_run_count", 3)

	heartRateStatus := statusDataTypeFromJSON(t, got, "heart-rate")
	assertJSONNumber(t, heartRateStatus, "data_point_count", 1)
	assertJSONNumber(t, heartRateStatus, "rollup_count", 0)
	assertJSONString(t, heartRateStatus, "newest_data_point_timestamp", "2026-01-03T09:00:00Z")
	if _, ok := heartRateStatus["newest_rollup_timestamp"]; ok {
		t.Fatalf("heart-rate newest_rollup_timestamp = %v, want omitted", heartRateStatus["newest_rollup_timestamp"])
	}

	stepsStatus := statusDataTypeFromJSON(t, got, "steps")
	assertJSONNumber(t, stepsStatus, "data_point_count", 2)
	assertJSONNumber(t, stepsStatus, "rollup_count", 1)
	assertJSONString(t, stepsStatus, "newest_data_point_timestamp", "2026-01-02T08:15:00Z")
	assertJSONString(t, stepsStatus, "newest_rollup_timestamp", "2026-01-04")

	success, ok := got["latest_successful_sync_run"].(map[string]any)
	if !ok {
		t.Fatalf("latest_successful_sync_run = %T(%v), want object", got["latest_successful_sync_run"], got["latest_successful_sync_run"])
	}
	assertJSONNumber(t, success, "id", 2)
	assertJSONString(t, success, "status", "sync_completed")
	assertJSONString(t, success, "from", "2026-01-02")
	assertJSONString(t, success, "to", "2026-01-03T00:00:00Z")
	assertJSONString(t, success, "endpoint_family", "reconcile")
	assertJSONString(t, success, "source_family_filter", "wearable")
	assertJSONNumber(t, success, "seen_count", 2)

	failed, ok := got["latest_failed_sync_run"].(map[string]any)
	if !ok {
		t.Fatalf("latest_failed_sync_run = %T(%v), want object", got["latest_failed_sync_run"], got["latest_failed_sync_run"])
	}
	assertJSONNumber(t, failed, "id", 3)
	assertJSONString(t, failed, "status", "sync_failed")
	assertJSONString(t, failed, "error_summary", "Provider timeout after 30s")
	if strings.Contains(stdout.String(), "gap") || strings.Contains(stdout.String(), "completeness") {
		t.Fatalf("status inferred completeness or gaps:\n%s", stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestWriteStatusSyncRunPlainEscapesControlBytes pins issue #244 for the
// status plain writers: provider-influenced fields (error_summary from a Sync
// Run's failure, source_family_filter) must have their C0/C1 control bytes
// rendered in a visible, reversible escape form so terminal escape-sequence
// injection (CWE-150) can never reach the terminal raw. ESC (0x1b) and BEL
// (0x07) stand in for the decoded provider-derived control bytes.
func TestWriteStatusSyncRunPlainEscapesControlBytes(t *testing.T) {
	t.Parallel()
	run := &statusSyncRun{
		ID:                 3,
		Status:             "sync_failed",
		SourceFamilyFilter: "wear\x1bable",
		ErrorSummary:       "Provider \x1btimeout\x07 after 30s",
	}
	stdout := new(bytes.Buffer)
	writer := newStickyWriter(stdout)
	writeStatusSyncRunPlain(writer, "latest_failed_sync_run", run)
	if err := writer.Err(); err != nil {
		t.Fatalf("writeStatusSyncRunPlain: %v", err)
	}
	out := stdout.String()
	wantLines := []string{
		`latest_failed_sync_run_source_family_filter: wear\x1bable` + "\n",
		`latest_failed_sync_run_error_summary: Provider \x1btimeout\x07 after 30s` + "\n",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Fatalf("status plain output missing %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("status plain output contains a raw control byte:\n%q", out)
	}
}

func TestStatusPlainReportsEmptyHealthArchive(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--plain",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	wantLines := []string{
		"status: ok\n",
		"archive_path: " + archivePath + "\n",
		fmt.Sprintf("schema_version: %d\n", currentSchemaVersion),
		"data_point_count: 0\n",
		"rollup_count: 0\n",
		"profile_snapshot_count: 0\n",
		"sync_run_count: 0\n",
		"message: Health Archive status summarized\n",
	}
	for _, want := range wantLines {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "latest_successful_sync_run") || strings.Contains(stdout.String(), "known_data_types") {
		t.Fatalf("stdout reported absent archive details:\n%s", stdout.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusMigratesLegacyV3ArchiveBeforeValidation(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove current archive: %v", err)
	}
	createLegacyV3Archive(t, archivePath)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"status", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "ok")
	assertJSONNumber(t, got, "schema_version", currentSchemaVersion)
	assertArchiveUserVersion(t, archivePath, currentSchemaVersion)
}

func TestStatusRejectsConfigArchiveMismatch(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, _, _ := initializeFileCredentialSetup(t, tempDir)
	otherArchivePath := filepath.Join(tempDir, "other-data", "other.sqlite")
	if err := createArchive(otherArchivePath); err != nil {
		t.Fatalf("create other archive: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--db", otherArchivePath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "status_failed")
	// PRD #144 slice 1 (issue #155): a read-command mismatch error names
	// the user-facing flags directly, never the internal `archive_path`
	// config field. The resolver test matrix pins the exact wording; here
	// we pin only the externally observable surface: both flag names
	// appear, the internal name does not.
	message, _ := got["message"].(string)
	for _, want := range []string{"--db", "--config", otherArchivePath, configPath} {
		if !strings.Contains(message, want) {
			t.Errorf("message = %q, missing substring %q", message, want)
		}
	}
	if strings.Contains(message, "archive_path") {
		t.Errorf("message = %q, must not mention internal archive_path field", message)
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusRejectsExplicitDefaultArchiveMismatch(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "xdg-data"))
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	defaultArchivePath := defaultArchivePath()
	if defaultArchivePath == archivePath {
		t.Fatalf("default archive path unexpectedly matches config archive path: %s", archivePath)
	}
	if err := createArchive(defaultArchivePath); err != nil {
		t.Fatalf("create default archive: %v", err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "status flag",
			args: []string{"status", "--config", configPath, "--db", defaultArchivePath, "--json"},
		},
		{
			name: "global flag",
			args: []string{"--config", configPath, "--db", defaultArchivePath, "status", "--json"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run(tc.args, stdout, stderr)
			if code != 1 {
				t.Fatalf("status exit code = %d, want 1", code)
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "status_failed")
			// PRD #144 slice 1 (issue #155): when --config and --db are
			// both explicit and disagree, the read-command error must
			// name the user-facing flags directly and must NOT mention
			// the internal `archive_path` config field. Both flag values
			// must show up so the user can see what they pointed each
			// flag at.
			message, _ := got["message"].(string)
			for _, want := range []string{"--db", "--config", defaultArchivePath, configPath, archivePath} {
				if !strings.Contains(message, want) {
					t.Errorf("message = %q, missing substring %q", message, want)
				}
			}
			if strings.Contains(message, "archive_path") {
				t.Errorf("message = %q, must not mention internal archive_path field", message)
			}
			assertNoSecretWords(t, stdout.String()+stderr.String())
		})
	}
}

// TestStatusReportsNewestElectrocardiogramEventTimestamp pins the
// AC for #104: once any electrocardiogram session is archived, the
// per-Data-Type status entry projects the latest event timestamp
// through the existing readStatusDataTypes loop. No new code path
// is added — the test catches a regression that strips ECG (or any
// future opt-in Data Type) from the per-Data-Type rollup.
func TestStatusReportsNewestElectrocardiogramEventTimestamp(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO data_points (
		provider_name,
		connection_id,
		data_type,
		upstream_resource_name,
		record_kind,
		start_time_utc,
		end_time_utc,
		data_source_json,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		"electrocardiogram",
		"users/me/dataTypes/electrocardiogram/dataPoints/ecg-2026-05-20",
		"session",
		"2026-05-20T10:30:00Z",
		"2026-05-20T10:30:30Z",
		"{}",
		`{"electrocardiogram":{"classification":"SINUS_RHYTHM"}}`,
		"2026-05-21T00:00:00Z",
		"2026-05-21T00:00:00Z",
	); err != nil {
		db.Close()
		t.Fatalf("insert ECG fixture: %v", err)
	}
	db.Close()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	ecgStatus := statusDataTypeFromJSON(t, got, "electrocardiogram")
	assertJSONNumber(t, ecgStatus, "data_point_count", 1)
	assertJSONString(t, ecgStatus, "newest_data_point_timestamp", "2026-05-20T10:30:30Z")
}

func TestStatusReportsMigrationFailureForUnsupportedSchema(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	setArchiveUserVersion(t, archivePath, currentSchemaVersion+1)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "status_failed")
	if _, ok := got["schema_version"]; ok {
		t.Fatalf("schema_version = %v, want omitted before migration succeeds", got["schema_version"])
	}
	if !strings.Contains(got["message"].(string), fmt.Sprintf("schema version %d", currentSchemaVersion+1)) {
		t.Fatalf("message = %q, want schema version", got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestStatusReportsSchemaVersionForArchiveInspectionFailure(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM schema_migrations WHERE version = ?`, currentSchemaVersion); err != nil {
		_ = db.Close()
		t.Fatalf("delete schema migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{
		"status",
		"--config", configPath,
		"--json",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "status_failed")
	assertJSONNumber(t, got, "schema_version", currentSchemaVersion)
	if !strings.Contains(got["message"].(string), "missing schema migration") {
		t.Fatalf("message = %q, want missing migration", got["message"])
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
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

func TestSyncRejectsInvalidSourceFamilyOptionsBeforeSetup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		args        []string
		wantMessage string
	}{
		{
			name:        "unsupported source family",
			args:        []string{"sync", "--source-family", "phone", "--from", "2026-01-01", "--json"},
			wantMessage: "supports only wearable",
		},
		{
			name:        "source family with rollup",
			args:        []string{"sync", "--source-family", "wearable", "--rollup", "daily", "--from", "2026-01-01", "--json"},
			wantMessage: "cannot be combined with --rollup",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run(tc.args, stdout, stderr)
			if code != 1 {
				t.Fatalf("sync exit code = %d, want 1", code)
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "sync_failed")
			if !strings.Contains(got["message"].(string), tc.wantMessage) {
				t.Fatalf("message = %q, want %q", got["message"], tc.wantMessage)
			}
			if _, ok := got["sync_run_id"]; ok {
				t.Fatalf("sync_run_id = %v, want omitted before setup", got["sync_run_id"])
			}
		})
	}
}

func TestSyncArchivesStepsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	firstPage := `{
		"dataPoints": [{
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a",
			"dataSource": {"platform": "FITBIT", "device": {"manufacturer": "Google", "model": "Pixel Watch"}},
			"steps": {
				"interval": {
					"startTime": "2026-01-01T08:00:00+01:00",
					"startUtcOffset": "3600s",
					"endTime": "2026-01-01T08:15:00+01:00",
					"endUtcOffset": "3600s",
					"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}},
					"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 15}}
				},
				"count": "512"
			}
		}],
		"nextPageToken": "page-2"
	}`
	secondPage := `{
		"dataPoints": [{
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-b",
			"dataSource": {"platform": "FITBIT"},
			"steps": {
				"interval": {
					"startTime": "2026-01-01T09:00:00Z",
					"endTime": "2026-01-01T09:05:00Z"
				},
				"count": "200"
			}
		}]
	}`
	requests := bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"":       firstPage,
		"page-2": secondPage,
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "from", "2026-01-01")
	assertJSONString(t, got, "to", "2026-01-02T00:00:00Z")
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 2)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertJSONNumber(t, got, "rollups_seen", 0)
	assertJSONNumber(t, got, "rollups_new", 0)
	assertJSONNumber(t, got, "rollups_updated", 0)
	if len(*requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(*requests))
	}
	if (*requests)[0].endpointName != "dataTypes.steps.list" || (*requests)[0].dataType != "steps" {
		t.Fatalf("request target = (%q, %q), want steps list", (*requests)[0].endpointName, (*requests)[0].dataType)
	}
	if strings.Contains((*requests)[0].url, "source") {
		t.Fatalf("sync URL unexpectedly includes source filtering: %s", (*requests)[0].url)
	}
	if pageToken := mustURLQuery(t, (*requests)[1].url).Get("pageToken"); pageToken != "page-2" {
		t.Fatalf("second pageToken = %q, want page-2", pageToken)
	}
	assertArchivedStepDataPoint(t, archivePath)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 1, "sync_completed", 2, 2, 0, "")

	requests = bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"":       firstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--plain",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("second sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	wantPlain := "status: sync_completed\nsync_run_id: 2\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ndata_types: steps\nfrom: 2026-01-01\nto: 2026-01-02T00:00:00Z\nendpoint_family: list\ndata_points_seen: 2\ndata_points_new: 0\ndata_points_updated: 0\nrollups_seen: 0\nrollups_new: 0\nrollups_updated: 0\nmessage: Sync Run archived steps Data Points\n"
	if stdout.String() != wantPlain {
		t.Fatalf("stdout = %q, want %q", stdout.String(), wantPlain)
	}
	if len(*requests) != 2 {
		t.Fatalf("second request count = %d, want 2", len(*requests))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 2, "sync_completed", 2, 0, 0, "")

	semanticallySameFirstPage := `{
		"nextPageToken": "page-2",
		"dataPoints": [{
			"steps": {
				"count": "512",
				"interval": {
					"civilEndTime": {"time": {"minutes": 15, "hours": 8}, "date": {"day": 1, "month": 1, "year": 2026}},
					"civilStartTime": {"time": {"hours": 8}, "date": {"day": 1, "month": 1, "year": 2026}},
					"endUtcOffset": "3600s",
					"endTime": "2026-01-01T08:15:00+01:00",
					"startUtcOffset": "3600s",
					"startTime": "2026-01-01T08:00:00+01:00"
				}
			},
			"dataSource": {"device": {"model": "Pixel Watch", "manufacturer": "Google"}, "platform": "FITBIT"},
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a"
		}]
	}`
	bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"":       semanticallySameFirstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("semantic sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("semantic stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 3, "sync_completed", 2, 0, 0, "")

	correctedFirstPage := strings.Replace(firstPage, `"count": "512"`, `"count": "999"`, 1)
	correctedFirstPage = strings.Replace(correctedFirstPage, `"startTime": "2026-01-01T08:00:00+01:00"`, `"startTime": "2026-01-01T08:01:00+01:00"`, 1)
	correctedFirstPage = strings.Replace(correctedFirstPage, `"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}}`, `"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 1}}`, 1)
	bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"":       correctedFirstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertSyncRun(t, archivePath, 4, "sync_completed", 2, 0, 1, "")
	assertCorrectedStepRevision(t, archivePath)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesSampleDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	heartRatePage := string(readTestFixture(t, "googlehealth_heart_rate_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "heart-rate", map[string]string{"": heartRatePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00Z" AND heart_rate.sample_time.physical_time < "2026-01-02T00:00:00Z"` {
		t.Fatalf("heart-rate filter = %q", gotFilter)
	}
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a", "heart-rate", "2026-01-01T07:30:00Z", "2026-01-01T08:30:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"beatsPerMinute":"72"`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "heart-rate", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "heart-rate", map[string]string{"": heartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "heart-rate", "list", 1, 0, 0, "")

	correctedHeartRatePage := strings.Replace(heartRatePage, `"beatsPerMinute": "72"`, `"beatsPerMinute": "75"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "heart-rate", map[string]string{"": correctedHeartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a", "heart-rate", "2026-01-01T07:30:00Z", "2026-01-01T08:30:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"beatsPerMinute":"75"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "heart-rate", "list", 1, 0, 1, "")

	oxygenPage := string(readTestFixture(t, "googlehealth_oxygen_saturation_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "oxygen-saturation", map[string]string{"": oxygenPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "oxygen-saturation",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("oxygen sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("oxygen stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/oxygen-saturation/dataPoints/spo2-2026-01-01-a", "oxygen-saturation", "2026-01-01T22:10:00Z", "2026-01-01T22:10:00", "2026-01-01", "", `"percentage":"97.5"`)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertSyncRunForDataType(t, archivePath, 4, "sync_completed", "oxygen-saturation", "list", 1, 1, 0, "")

	heartRateVariabilityPage := string(readTestFixture(t, "googlehealth_heart_rate_variability_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "heart-rate-variability", map[string]string{"": heartRateVariabilityPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate-variability",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("heart-rate variability sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("heart-rate variability stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/heart-rate-variability/dataPoints/hrv-2026-01-01-a", "heart-rate-variability", "2026-01-01T05:20:00Z", "2026-01-01T05:20:00", "2026-01-01", "", `"rootMeanSquareOfSuccessiveDifferencesMilliseconds":42.125`)
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertSyncRunForDataType(t, archivePath, 5, "sync_completed", "heart-rate-variability", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesWeightDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	weightPage := string(readTestFixture(t, "googlehealth_weight_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "weight", map[string]string{"": weightPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `weight.sample_time.physical_time >= "2026-01-01T00:00:00Z" AND weight.sample_time.physical_time < "2026-01-02T00:00:00Z"` {
		t.Fatalf("weight filter = %q", gotFilter)
	}
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/weight/dataPoints/weight-2026-01-01", "weight", "2026-01-01T05:45:00Z", "2026-01-01T06:45:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"weightGrams":71234.5`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "weight", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "weight", map[string]string{"": weightPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "weight", "list", 1, 0, 0, "")

	correctedWeightPage := strings.Replace(weightPage, `"weightGrams": 71234.5`, `"weightGrams": 71235.25`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "weight", map[string]string{"": correctedWeightPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/weight/dataPoints/weight-2026-01-01", "weight", "2026-01-01T05:45:00Z", "2026-01-01T06:45:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"weightGrams":71235.25`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "weight", "list", 1, 0, 1, "")

	reconciledWeightPage := `{"dataPoints": [{
		"dataPointName": "users/me/dataTypes/weight/dataPoints/weight-2026-01-01-wearable",
		"weight": {
			"sampleTime": {
				"physicalTime": "2026-01-01T06:45:00+01:00",
				"utcOffset": "3600s",
				"civilTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 6, "minutes": 45}}
			},
			"weightGrams": 71234.5
		}
	}]}`
	reconcileRequests := bindDataPointReconcileFetchFake(t, &testRuntime, "connect-access-secret", "weight", map[string]string{"": reconciledWeightPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("wearable weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("wearable weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if gotFamily := mustURLQuery(t, (*reconcileRequests)[0].url).Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("weight dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/weight/dataPoints/weight-2026-01-01-wearable", "weight", "2026-01-01T05:45:00Z", "2026-01-01T06:45:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"weightGrams":71234.5`)
	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, 4, "sync_completed", "weight", "reconcile", "wearable", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesDistanceDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	distancePage := string(readTestFixture(t, "googlehealth_distance_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "distance", map[string]string{"": distancePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `distance.interval.start_time >= "2026-01-01T00:00:00Z" AND distance.interval.start_time < "2026-01-02T00:00:00Z"` {
		t.Fatalf("distance filter = %q", gotFilter)
	}
	assertArchivedIntervalDataPoint(t, archivePath, "users/me/dataTypes/distance/dataPoints/distance-2026-01-01", "distance", "2026-01-01T07:00:00Z", "2026-01-01T07:30:00Z", "2026-01-01T08:00:00", "2026-01-01T08:30:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, "", `"millimeters":"2450"`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "distance", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "distance", map[string]string{"": distancePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "distance", "list", 1, 0, 0, "")

	correctedDistancePage := strings.Replace(distancePage, `"millimeters": "2450"`, `"millimeters": "2500"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "distance", map[string]string{"": correctedDistancePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedIntervalDataPoint(t, archivePath, "users/me/dataTypes/distance/dataPoints/distance-2026-01-01", "distance", "2026-01-01T07:00:00Z", "2026-01-01T07:30:00Z", "2026-01-01T08:00:00", "2026-01-01T08:30:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, "", `"millimeters":"2500"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "distance", "list", 1, 0, 1, "")

	reconciledDistancePage := `{"dataPoints": [{
		"dataPointName": "users/me/dataTypes/distance/dataPoints/distance-2026-01-01-wearable",
		"distance": {
			"interval": {
				"startTime": "2026-01-01T08:00:00+01:00",
				"startUtcOffset": "3600s",
				"endTime": "2026-01-01T08:30:00+01:00",
				"endUtcOffset": "3600s",
				"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}},
				"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 30}}
			},
			"millimeters": "2450"
		}
	}]}`
	reconcileRequests := bindDataPointReconcileFetchFake(t, &testRuntime, "connect-access-secret", "distance", map[string]string{"": reconciledDistancePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("wearable distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("wearable distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if gotFamily := mustURLQuery(t, (*reconcileRequests)[0].url).Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("distance dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertArchivedIntervalDataPoint(t, archivePath, "users/me/dataTypes/distance/dataPoints/distance-2026-01-01-wearable", "distance", "2026-01-01T07:00:00Z", "2026-01-01T07:30:00Z", "2026-01-01T08:00:00", "2026-01-01T08:30:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, "{}", "wearable", `"millimeters":"2450"`)
	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, 4, "sync_completed", "distance", "reconcile", "wearable", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesDailyDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	restingHeartRatePage := string(readTestFixture(t, "googlehealth_daily_resting_heart_rate_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-resting-heart-rate", map[string]string{"": restingHeartRatePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-resting-heart-rate",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("daily resting heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily resting heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONString(t, got, "to", "2026-01-02")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if (*requests)[0].endpointName != "dataTypes.daily-resting-heart-rate.list" {
		t.Fatalf("endpoint = %q, want daily Data Type list", (*requests)[0].endpointName)
	}
	if strings.Contains((*requests)[0].url, "dailyRollUp") {
		t.Fatalf("daily Data Point sync used Rollup URL: %s", (*requests)[0].url)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `daily_resting_heart_rate.date >= "2026-01-01" AND daily_resting_heart_rate.date < "2026-01-02"` {
		t.Fatalf("daily resting heart-rate filter = %q", gotFilter)
	}
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-resting-heart-rate/dataPoints/rhr-2026-01-01", "daily-resting-heart-rate", "2026-01-01", `"beatsPerMinute":"61"`)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "daily-resting-heart-rate", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-resting-heart-rate", map[string]string{"": restingHeartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-resting-heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent daily sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent daily stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "daily-resting-heart-rate", "list", 1, 0, 0, "")

	correctedRestingHeartRatePage := strings.Replace(restingHeartRatePage, `"beatsPerMinute": "61"`, `"beatsPerMinute": "63"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-resting-heart-rate", map[string]string{"": correctedRestingHeartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-resting-heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected daily sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected daily stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-resting-heart-rate/dataPoints/rhr-2026-01-01", "daily-resting-heart-rate", "2026-01-01", `"beatsPerMinute":"63"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "daily-resting-heart-rate", "list", 1, 0, 1, "")

	dailyOxygenPage := string(readTestFixture(t, "googlehealth_daily_oxygen_saturation_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-oxygen-saturation", map[string]string{"": dailyOxygenPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-oxygen-saturation",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("daily oxygen sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily oxygen stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-oxygen-saturation/dataPoints/spo2-daily-2026-01-01", "daily-oxygen-saturation", "2026-01-01", `"averagePercentage":96.8`)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 4, "sync_completed", "daily-oxygen-saturation", "list", 1, 1, 0, "")

	dailyHeartRateVariabilityPage := string(readTestFixture(t, "googlehealth_daily_heart_rate_variability_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-heart-rate-variability", map[string]string{"": dailyHeartRateVariabilityPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-heart-rate-variability",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("daily heart-rate variability sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily heart-rate variability stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-heart-rate-variability/dataPoints/hrv-daily-2026-01-01", "daily-heart-rate-variability", "2026-01-01", `"averageHeartRateVariabilityMilliseconds":45.7`)
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 5, "sync_completed", "daily-heart-rate-variability", "list", 1, 1, 0, "")

	dailyRespiratoryRatePage := string(readTestFixture(t, "googlehealth_daily_respiratory_rate_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-respiratory-rate", map[string]string{"": dailyRespiratoryRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-respiratory-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("daily respiratory-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily respiratory-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-respiratory-rate/dataPoints/resp-daily-2026-01-01", "daily-respiratory-rate", "2026-01-01", `"breathsPerMinute":14.2`)
	assertArchiveTableCount(t, archivePath, "data_points", 4)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 6, "sync_completed", "daily-respiratory-rate", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesSleepSessionDataPoints(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC) }

	sleepPage := string(readTestFixture(t, "googlehealth_sleep_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "sleep", map[string]string{"": sleepPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "sleep",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sleep sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("sleep stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONString(t, got, "to", "2026-01-03")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if (*requests)[0].endpointName != "dataTypes.sleep.list" {
		t.Fatalf("endpoint = %q, want sleep Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `sleep.interval.civil_end_time >= "2026-01-01" AND sleep.interval.civil_end_time < "2026-01-03"` {
		t.Fatalf("sleep filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/sleep/dataPoints/sleep-2026-01-01", "sleep", "2026-01-01T21:30:00Z", "2026-01-02T05:45:00Z", "2026-01-01T22:30:00", "2026-01-02T06:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"type":"LIGHT"`)
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "sleep", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "sleep", map[string]string{"": sleepPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "sleep",
		"--from", "2026-01-01",
		"--to", "2026-01-03",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent sleep sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent sleep stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "sleep", "list", 1, 0, 0, "")

	correctedSleepPage := strings.Replace(sleepPage, `"type": "LIGHT"`, `"type": "REM"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "sleep", map[string]string{"": correctedSleepPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "sleep",
		"--from", "2026-01-01",
		"--to", "2026-01-03",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected sleep sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected sleep stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/sleep/dataPoints/sleep-2026-01-01", "sleep", "2026-01-01T21:30:00Z", "2026-01-02T05:45:00Z", "2026-01-01T22:30:00", "2026-01-02T06:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"type":"REM"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "sleep", "list", 1, 0, 1, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesExerciseSessionDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	exercisePage := string(readTestFixture(t, "googlehealth_exercise_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "exercise", map[string]string{"": exercisePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "exercise",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("exercise sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("exercise stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertJSONString(t, got, "to", "2026-01-02")
	if (*requests)[0].endpointName != "dataTypes.exercise.list" {
		t.Fatalf("endpoint = %q, want exercise Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `exercise.interval.civil_start_time >= "2026-01-01" AND exercise.interval.civil_start_time < "2026-01-02"` {
		t.Fatalf("exercise filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/exercise/dataPoints/exercise-2026-01-01", "exercise", "2026-01-01T16:15:00Z", "2026-01-01T16:45:00Z", "2026-01-01T17:15:00", "2026-01-01T17:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"exerciseType":"RUNNING"`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "exercise", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "exercise", map[string]string{"": exercisePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "exercise",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent exercise sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent exercise stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "exercise", "list", 1, 0, 0, "")

	correctedExercisePage := strings.Replace(exercisePage, `"activeDuration": "1800s"`, `"activeDuration": "2100s"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "exercise", map[string]string{"": correctedExercisePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "exercise",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected exercise sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected exercise stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/exercise/dataPoints/exercise-2026-01-01", "exercise", "2026-01-01T16:15:00Z", "2026-01-01T16:45:00Z", "2026-01-01T17:15:00", "2026-01-01T17:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"activeDuration":"2100s"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "exercise", "list", 1, 0, 1, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestSyncArchivesElectrocardiogramSessionDataPoints pins the
// session-parser contract for the Tier 2 ECG Data Type (#104).
// addStoredConnectionScope simulates the user having run
// `connect --add-scopes ecg`; without that the AccessToken call
// would short-circuit on the missing-scope error. The fixture mirrors
// the sleep/exercise session shape because the live probe is deferred
// until the user grants the scope against the live OAuth client.
func TestSyncArchivesElectrocardiogramSessionDataPoints(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	addStoredConnectionScope(t, archivePath, googleHealthEcgReadonlyScope)
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	ecgPage := string(readTestFixture(t, "googlehealth_electrocardiogram_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "electrocardiogram", map[string]string{"": ecgPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "electrocardiogram",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("electrocardiogram sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("electrocardiogram stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if (*requests)[0].endpointName != "dataTypes.electrocardiogram.list" {
		t.Fatalf("endpoint = %q, want electrocardiogram Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `electrocardiogram.interval.civil_start_time >= "2026-01-01" AND electrocardiogram.interval.civil_start_time < "2026-01-02"` {
		t.Fatalf("electrocardiogram filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/electrocardiogram/dataPoints/ecg-2026-01-01", "electrocardiogram", "2026-01-01T09:30:00Z", "2026-01-01T09:30:30Z", "2026-01-01T10:30:00", "2026-01-01T10:30:30", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"classification":"SINUS_RHYTHM"`)
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "electrocardiogram", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestSyncArchivesIrregularRhythmNotificationSessionDataPoints pins
// the session-parser contract for the Tier 2 IRN Data Type (#104).
// Same harness as the ECG test, with the IRN-specific scope and
// fixture payload.
func TestSyncArchivesIrregularRhythmNotificationSessionDataPoints(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	addStoredConnectionScope(t, archivePath, googleHealthIrnReadonlyScope)
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	irnPage := string(readTestFixture(t, "googlehealth_irregular_rhythm_notification_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "irregular-rhythm-notification", map[string]string{"": irnPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "irregular-rhythm-notification",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("irregular-rhythm-notification sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("irregular-rhythm-notification stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if (*requests)[0].endpointName != "dataTypes.irregular-rhythm-notification.list" {
		t.Fatalf("endpoint = %q, want irregular-rhythm-notification Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `irregular_rhythm_notification.interval.civil_start_time >= "2026-01-01" AND irregular_rhythm_notification.interval.civil_start_time < "2026-01-02"` {
		t.Fatalf("irregular-rhythm-notification filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/irregular-rhythm-notification/dataPoints/irn-2026-01-01", "irregular-rhythm-notification", "2026-01-01T08:00:00Z", "2026-01-01T08:00:30Z", "2026-01-01T09:00:00", "2026-01-01T09:00:30", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"classification":"ATRIAL_FIBRILLATION_SUGGESTED"`)
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "irregular-rhythm-notification", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesWearableStepsViaReconcile(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	defaultPage := `{"dataPoints": [{
		"name": "users/me/dataTypes/steps/dataPoints/shared-step",
		"dataSource": {"platform": "FITBIT", "device": {"manufacturer": "Google", "model": "Pixel Watch"}},
		"steps": {"interval": {"startTime": "2026-01-01T08:00:00Z", "endTime": "2026-01-01T08:15:00Z"}, "count": "512"}
	}]}`
	listRequests := bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{"": defaultPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("default sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if len(*listRequests) != 1 {
		t.Fatalf("default request count = %d, want 1", len(*listRequests))
	}
	if query := mustURLQuery(t, (*listRequests)[0].url); query.Get("dataSourceFamily") != "" {
		t.Fatalf("default sync dataSourceFamily = %q, want empty", query.Get("dataSourceFamily"))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 1, "sync_completed", "list", "", 1, 1, 0, "")

	reconciledPage := `{"dataPoints": [{
		"dataPointName": "users/me/dataTypes/steps/dataPoints/shared-step",
		"steps": {"interval": {"startTime": "2026-01-01T08:00:00Z", "endTime": "2026-01-01T08:15:00Z"}, "count": "512"}
	}]}`
	reconcileRequests := bindStepReconcileFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{"": reconciledPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("wearable sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("wearable stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if len(*reconcileRequests) != 1 {
		t.Fatalf("reconcile request count = %d, want 1", len(*reconcileRequests))
	}
	query := mustURLQuery(t, (*reconcileRequests)[0].url)
	if gotFamily := query.Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	wantFilter := `steps.interval.start_time >= "2026-01-01T00:00:00Z" AND steps.interval.start_time < "2026-01-02T00:00:00Z"`
	if gotFilter := query.Get("filter"); gotFilter != wantFilter {
		t.Fatalf("filter = %q, want %q", gotFilter, wantFilter)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 2, "sync_completed", "reconcile", "wearable", 1, 1, 0, "")

	bindStepReconcileFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{"": reconciledPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--plain",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent wearable sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "source_family: wearable\n") {
		t.Fatalf("plain stdout = %q, want source family", stdout.String())
	}
	if !strings.Contains(stdout.String(), "data_points_new: 0\n") {
		t.Fatalf("plain stdout = %q, want idempotent count", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 3, "sync_completed", "reconcile", "wearable", 1, 0, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesStepsDailyRollupsOnlyWhenRequested(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC) }

	listRequests := bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("default sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("default stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 0)
	assertJSONNumber(t, got, "rollups_seen", 0)
	if len(*listRequests) != 1 {
		t.Fatalf("default request count = %d, want 1", len(*listRequests))
	}
	if (*listRequests)[0].endpointName != "dataTypes.steps.list" {
		t.Fatalf("default endpoint = %q, want list", (*listRequests)[0].endpointName)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunWithEndpointFamily(t, archivePath, 1, "sync_completed", "list", 0, 0, 0, "")

	firstRollupPage := `{
		"rollupDataPoints": [{
			"steps": {"countSum": "1234"},
			"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
			"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
		}]
	}`
	rollupRequests := bindStepDailyRollupFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": firstRollupPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "dailyRollUp")
	assertJSONNumber(t, got, "data_points_seen", 0)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertJSONNumber(t, got, "rollups_seen", 1)
	assertJSONNumber(t, got, "rollups_new", 1)
	assertJSONNumber(t, got, "rollups_updated", 0)
	if len(*rollupRequests) != 1 {
		t.Fatalf("rollup request count = %d, want 1", len(*rollupRequests))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertArchivedStepsDailyRollup(t, archivePath, "1234")
	assertSyncRunWithEndpointFamily(t, archivePath, 2, "sync_completed", "dailyRollUp", 1, 1, 0, "")

	bindStepDailyRollupFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": firstRollupPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "rollups_seen", 1)
	assertJSONNumber(t, got, "rollups_new", 0)
	assertJSONNumber(t, got, "rollups_updated", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertSyncRunWithEndpointFamily(t, archivePath, 3, "sync_completed", "dailyRollUp", 1, 0, 0, "")

	correctedRollupPage := strings.Replace(firstRollupPage, `"countSum": "1234"`, `"countSum": "4321"`, 1)
	bindStepDailyRollupFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": correctedRollupPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "rollups_seen", 1)
	assertJSONNumber(t, got, "rollups_new", 0)
	assertJSONNumber(t, got, "rollups_updated", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertArchivedStepsDailyRollup(t, archivePath, "4321")
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunWithEndpointFamily(t, archivePath, 4, "sync_completed", "dailyRollUp", 1, 0, 1, "")

	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01T12:00:00",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("timed rollup sync exit code = %d, want 1\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	// The gate's preflight message names both supported shapes and the
	// rollup kind so an operator hears exactly what the rollup will
	// accept; this replaces the slice-2 planner-stage "expected
	// YYYY-MM-DD" error with a richer local rejection.
	if !strings.Contains(stdout.String(), "expected YYYY-MM-DD or RFC3339") {
		t.Fatalf("timed rollup stdout = %q, want supported-shapes error", stdout.String())
	}
	if !strings.Contains(stdout.String(), "daily") {
		t.Fatalf("timed rollup stdout = %q, want rollup kind named", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	// PRD #141 slice 3: civil-vs-RFC3339 input-shape errors are caught at
	// the preflight gate before any sync_run row is written. Previously
	// this scenario produced an audit row from the planner-stage parse
	// error; the gate now owns the contract so the archive must show
	// only the 4 rows from the earlier successful invocations.
	assertArchiveTableCount(t, archivePath, "sync_runs", 4)

	longRangeRequests := bindStepDailyRollupFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"2026-01-01/2026-04-01/": `{"rollupDataPoints": [{
			"steps": {"countSum": "9000"},
			"civilStartTime": {"date": {"year": 2026, "month": 4, "day": 1}},
			"civilEndTime": {"date": {"year": 2026, "month": 4, "day": 2}}
		}]}`,
		"2026-04-01/2026-04-15/": `{"rollupDataPoints": [{
			"steps": {"countSum": "1400"},
			"civilStartTime": {"date": {"year": 2026, "month": 4, "day": 2}},
			"civilEndTime": {"date": {"year": 2026, "month": 4, "day": 3}}
		}]}`,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-04-15",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("long rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if len(*longRangeRequests) != 2 {
		t.Fatalf("long rollup request count = %d, want 2", len(*longRangeRequests))
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("long rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "rollups_seen", 2)
	assertJSONNumber(t, got, "rollups_new", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 3)
	// Sync Run id is 5 (not 6) because the preceding civil-shape failure
	// is now caught at the gate and does not write a sync_run row.
	assertSyncRunWithEndpointFamily(t, archivePath, 5, "sync_completed", "dailyRollUp", 2, 2, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
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

func TestSyncProviderFailureRecordsFailedRun(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != "connect-access-secret" {
			t.Fatalf("sync access token = %q, want stored token", accessToken)
		}
		return nil, errors.New("Google Health raw request failed with HTTP 503")
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	message := got["message"].(string)
	if !strings.Contains(message, "HTTP 503") {
		t.Fatalf("message = %q, want provider status", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, "HTTP 503")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncRefusesDifferentProviderIdentityBeforeArchiving(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindIdentityFetchFake(t, &testRuntime, "connect-access-secret", googleIdentity{
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "DIFFERENT",
		rawJSON:            `{"healthUserId":"222222222222222222","legacyUserId":"DIFFERENT"}`,
	})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run after identity mismatch")
		return nil, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	if message := got["message"].(string); !strings.Contains(message, "different Google Identity") {
		t.Fatalf("message = %q, want identity mismatch", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, "different Google Identity")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncReportsFailedWhenCompletionRecordFails(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:15:00Z"
					},
					"count": "512"
				}
			}]
		}`,
	})
	// Wrap the writer so FinalizeSyncRun (the atomic sync_run+cursor write)
	// fails when called for a sync_completed outcome. This exercises the
	// CLI's "atomic finalize failed → recover-as-sync_failed" path without
	// reaching past the adapters seam the executor routes every archive
	// open through.
	testRuntime.openHealthArchiveWriter = func(path string) (healthArchiveWriter, error) {
		inner, err := openHealthArchiveWriter(path)
		if err != nil {
			return nil, err
		}
		return fakeFinalizeWriter{healthArchiveWriter: inner, failOn: failOnCompletedOutcome(errSimulatedFinalizeCompletedFailure)}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	assertJSONNumber(t, got, "data_points_seen", 1)
	if message := got["message"].(string); !strings.Contains(message, "archive finalization failed") {
		t.Fatalf("message = %q, want finalization error", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncFailsBeforeProviderWhenScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googleHealthProfileReadonlyScope})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run with missing scope")
		return nil, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), googleHealthActivityReadonlyScope) || !strings.Contains(stdout.String(), "connect") {
		t.Fatalf("stdout = %q, want missing scope reconnect hint", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, googleHealthActivityReadonlyScope)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncSampleDataTypeFailsBeforeProviderWhenHealthMetricsScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googleHealthProfileReadonlyScope, googleHealthActivityReadonlyScope})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run with missing health metrics scope")
		return nil, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), googleHealthHealthMetricsReadonlyScope) || !strings.Contains(stdout.String(), "connect") {
		t.Fatalf("stdout = %q, want missing health metrics scope reconnect hint", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_failed", "heart-rate", "list", 0, 0, 0, googleHealthHealthMetricsReadonlyScope)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestRawEndpointIdentityPrintsProviderJSONWithoutArchiving(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	beforeIdentityJSON := archivedConnectionIdentityJSON(t, archivePath)
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(request rawProviderRequest) []byte {
		if request.url != googleHealthIdentityURL {
			t.Fatalf("raw URL = %q, want identity URL", request.url)
		}
		return []byte(`{"healthUserId":"999999999999999999","legacyUserId":"RAW"}`)
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("raw exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stdout.String() != `{"healthUserId":"999999999999999999","legacyUserId":"RAW"}` {
		t.Fatalf("stdout = %q, want raw provider JSON", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := archivedConnectionIdentityJSON(t, archivePath); got != beforeIdentityJSON {
		t.Fatalf("raw mutated archived identity JSON: %s", got)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") {
		t.Fatalf("raw output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestRawDataTypeStepsPrintsFixtureJSON(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	fixture := readTestFixture(t, "googlehealth_steps_list.json")
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(request rawProviderRequest) []byte {
		if request.endpointName != "dataTypes.steps.list" || request.dataType != "steps" {
			t.Fatalf("raw request = (%q, %q), want steps list", request.endpointName, request.dataType)
		}
		parsedURL, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("parse raw URL: %v", err)
		}
		if parsedURL.Path != "/v4/users/me/dataTypes/steps/dataPoints" {
			t.Fatalf("raw path = %q, want steps dataPoints path", parsedURL.Path)
		}
		query := parsedURL.Query()
		wantFilter := `steps.interval.start_time >= "2026-01-01T00:00:00Z" AND steps.interval.start_time < "2026-01-02T00:00:00Z"`
		if query.Get("filter") != wantFilter {
			t.Fatalf("filter = %q, want %q", query.Get("filter"), wantFilter)
		}
		if query.Get("pageSize") != "12" || query.Get("pageToken") != "abc123" {
			t.Fatalf("pagination query = %v, want pageSize/pageToken", query)
		}
		return fixture
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw",
		"data-type", "steps",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--page-size", "12",
		"--page-token", "abc123",
		"--config", configPath,
		"--db", archivePath,
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("raw exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if !bytes.Equal(stdout.Bytes(), fixture) {
		t.Fatalf("stdout = %q, want fixture JSON", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestDailyNamedDataTypeListRequestIsNotRollup(t *testing.T) {
	t.Parallel()
	request, err := buildGoogleHealthDataTypeListRawRequest("daily-resting-heart-rate", "2026-01-01", "2026-01-02", 0, "")
	if err != nil {
		t.Fatalf("build daily-named list request: %v", err)
	}
	if request.endpointName != "dataTypes.daily-resting-heart-rate.list" {
		t.Fatalf("endpointName = %q, want daily Data Type list", request.endpointName)
	}
	if request.method != http.MethodGet {
		t.Fatalf("method = %q, want GET", request.method)
	}
	if len(request.body) != 0 {
		t.Fatalf("request body = %s, want empty list request body", string(request.body))
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/daily-resting-heart-rate/dataPoints" {
		t.Fatalf("path = %q, want Data Points list path", parsedURL.Path)
	}
	if strings.Contains(request.endpointName+parsedURL.Path, "RollUp") || strings.Contains(parsedURL.Path, "rollUp") {
		t.Fatalf("daily Data Type request used Rollup endpoint: %s %s", request.endpointName, parsedURL.Path)
	}
	wantFilter := `daily_resting_heart_rate.date >= "2026-01-01" AND daily_resting_heart_rate.date < "2026-01-02"`
	if got := parsedURL.Query().Get("filter"); got != wantFilter {
		t.Fatalf("filter = %q, want %q", got, wantFilter)
	}
}

func TestRawProviderErrorDoesNotLeakToken(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != "connect-access-secret" {
			t.Fatalf("raw access token = %q, want stored token", accessToken)
		}
		return nil, errors.New("Google Health raw request failed with HTTP 403")
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("raw exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 403") {
		t.Fatalf("stderr = %q, want provider status", stderr.String())
	}
	if strings.Contains(stderr.String(), "connect-access-secret") || strings.Contains(stderr.String(), "connect-refresh-secret") {
		t.Fatalf("raw error leaked token material: %s", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestRawDataTypeFailsBeforeProviderWhenScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	var metadata map[string]any
	var metadataJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT token_metadata_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&metadataJSON); err != nil {
		t.Fatalf("query token metadata: %v", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["scopes"] = []string{googleHealthActivityReadonlyScope}
	updatedMetadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(updatedMetadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token scopes: %v", err)
	}
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("raw provider fetch should not run with missing scope")
		return nil, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"raw", "data-type", "heart-rate", "--from", "2026-01-01", "--config", configPath, "--db", archivePath}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("raw exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), googleHealthHealthMetricsReadonlyScope) || !strings.Contains(stderr.String(), "connect") {
		t.Fatalf("stderr = %q, want missing scope reconnect hint", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestBuildGoogleHealthRawRequestUsesProviderNamingConventions(t *testing.T) {
	t.Parallel()
	request, err := buildGoogleHealthRawRequest([]string{"endpoint", "dataTypes.heart-rate.list"}, "2026-01-01", "", 0, "")
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse raw URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/heart-rate/dataPoints" {
		t.Fatalf("path = %q, want kebab-case Data Type path", parsedURL.Path)
	}
	wantFilter := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00Z"`
	if parsedURL.Query().Get("filter") != wantFilter {
		t.Fatalf("filter = %q, want snake-case filter", parsedURL.Query().Get("filter"))
	}
}

func TestBuildGoogleHealthRawRequestRejectsNonListableDataTypes(t *testing.T) {
	t.Parallel()
	_, err := buildGoogleHealthRawRequest([]string{"data-type", "total-calories"}, "2026-01-01", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported Data Type")
	}
	if !strings.Contains(err.Error(), "not supported by dataPoints.list") {
		t.Fatalf("error = %v, want unsupported dataPoints.list", err)
	}
}

// TestBuildGoogleHealthRawRequestEndpointCatalog pins PRD #142 slice 7:
// every identity-style endpoint exposed by `raw endpoint <name>` must
// source its requiredScopes from googleHealthIdentityEndpointScopes so
// the scope contract for `raw` and the introspection commands (devices,
// settings, profile, irn-profile) can never drift apart. When slice 2
// of the PRD revises pairedDevices/getSettings scopes empirically, the
// catalog entry changes and this test follows automatically — no inline
// scope literals to update in main.go.
func TestBuildGoogleHealthRawRequestEndpointCatalog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		wantURL string
	}{
		{name: "getIdentity", wantURL: googleHealthIdentityURL},
		{name: "getProfile", wantURL: googleHealthProfileURL},
		{name: "getSettings", wantURL: googleHealthSettingsURL},
		{name: "pairedDevices", wantURL: googleHealthPairedDevicesURL},
		{name: "getIrnProfile", wantURL: googleHealthIRNProfileURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := buildGoogleHealthRawRequest([]string{"endpoint", tt.name}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", tt.name, err)
			}
			if request.endpointName != tt.name {
				t.Fatalf("endpointName = %q, want %q", request.endpointName, tt.name)
			}
			if request.url != tt.wantURL {
				t.Fatalf("url = %q, want %q", request.url, tt.wantURL)
			}
			wantScopes := googleHealthIdentityEndpointScopes[tt.name]
			if len(wantScopes) == 0 {
				t.Fatalf("catalog missing entry for %q — slice 1 contract violated", tt.name)
			}
			if len(request.requiredScopes) != len(wantScopes) {
				t.Fatalf("requiredScopes = %v, want %v (catalog entry)", request.requiredScopes, wantScopes)
			}
			for i, want := range wantScopes {
				if request.requiredScopes[i] != want {
					t.Fatalf("requiredScopes[%d] = %q, want %q (catalog entry)", i, request.requiredScopes[i], want)
				}
			}
		})
	}
}

// TestBuildGoogleHealthRawRequestUnknownEndpoint guards the
// not-found fall-through so a typo (or a renamed endpoint that
// outpaces a catalog update) still surfaces as a clear error rather
// than a nil request.
func TestBuildGoogleHealthRawRequestUnknownEndpoint(t *testing.T) {
	t.Parallel()
	_, err := buildGoogleHealthRawRequest([]string{"endpoint", "nonexistent"}, "", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported raw endpoint")
	}
	if !strings.Contains(err.Error(), `unsupported raw endpoint "nonexistent"`) {
		t.Fatalf("error = %v, want unsupported raw endpoint %q", err, "nonexistent")
	}
}

// TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog pins PRD #142
// slice 7 AC: no `[]string{googleHealthProfileReadonlyScope}` inline
// literal remains in buildGoogleHealthRawRequest. The only source of
// truth for endpoint scopes is the catalog. We verify behaviourally:
// a catalog mutation for the duration of the test flows through to the
// request's requiredScopes — proving the branch did a catalog lookup,
// not a hard-coded literal.
func TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"getIdentity", "getProfile", "getSettings", "pairedDevices", "getIrnProfile"} {
		t.Run(endpoint, func(t *testing.T) {
			original, ok := googleHealthIdentityEndpointScopes[endpoint]
			if !ok {
				t.Fatalf("catalog missing %q — slice 1 contract violated", endpoint)
			}
			sentinel := "https://example.invalid/scope/sentinel-" + endpoint
			googleHealthIdentityEndpointScopes[endpoint] = []string{sentinel}
			t.Cleanup(func() { googleHealthIdentityEndpointScopes[endpoint] = original })

			request, err := buildGoogleHealthRawRequest([]string{"endpoint", endpoint}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", endpoint, err)
			}
			if len(request.requiredScopes) != 1 || request.requiredScopes[0] != sentinel {
				t.Fatalf("requiredScopes = %v, want catalog-driven %q — branch is using an inline scope literal", request.requiredScopes, sentinel)
			}
		})
	}
}

func TestGoogleHealthRawFilterFieldsCoverFirstReleaseDataTypes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		dataType string
		from     string
		want     string
	}{
		{
			dataType: "steps",
			from:     "2026-01-01",
			want:     `steps.interval.start_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "oxygen-saturation",
			from:     "2026-01-01",
			want:     `oxygen_saturation.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "heart-rate-variability",
			from:     "2026-01-01",
			want:     `heart_rate_variability.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "daily-resting-heart-rate",
			from:     "2026-01-01",
			want:     `daily_resting_heart_rate.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-heart-rate-variability",
			from:     "2026-01-01",
			want:     `daily_heart_rate_variability.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-oxygen-saturation",
			from:     "2026-01-01",
			want:     `daily_oxygen_saturation.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-respiratory-rate",
			from:     "2026-01-01",
			want:     `daily_respiratory_rate.date >= "2026-01-01"`,
		},
		{
			dataType: "exercise",
			from:     "2026-01-01",
			want:     `exercise.interval.civil_start_time >= "2026-01-01"`,
		},
		{
			dataType: "sleep",
			from:     "2026-01-01",
			want:     `sleep.interval.civil_end_time >= "2026-01-01"`,
		},
		{
			dataType: "distance",
			from:     "2026-01-01",
			want:     `distance.interval.start_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "weight",
			from:     "2026-01-01",
			want:     `weight.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
	} {
		t.Run(test.dataType, func(t *testing.T) {
			filter, err := googleHealthDataTypeListFilter(test.dataType, test.from, "")
			if err != nil {
				t.Fatalf("filter: %v", err)
			}
			if filter != test.want {
				t.Fatalf("filter = %q, want %q", filter, test.want)
			}
		})
	}
}

func TestGoogleHealthRawFilterPreservesFractionalRFC3339Bounds(t *testing.T) {
	t.Parallel()
	filter, err := googleHealthDataTypeListFilter("heart-rate", "2026-01-01T00:00:00.500Z", "2026-01-01T01:02:03.123456789+02:00")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	want := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00.5Z" AND heart_rate.sample_time.physical_time < "2025-12-31T23:02:03.123456789Z"`
	if filter != want {
		t.Fatalf("filter = %q, want %q", filter, want)
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

func TestFetchGoogleIdentityUsesGetIdentityEndpoint(t *testing.T) {
	t.Parallel()
	var gotURL string
	doer := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotURL = request.URL.String()
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"healthUserId":"111111256096816351","legacyUserId":"A1B2C3"}`)),
		}, nil
	})}

	identity, err := fetchGoogleIdentity(providerGET{doer: doer}, "access-secret-value")
	if err != nil {
		t.Fatalf("fetch identity: %v", err)
	}
	if gotURL != googleHealthIdentityURL {
		t.Fatalf("identity URL = %q, want %q", gotURL, googleHealthIdentityURL)
	}
	if identity.healthUserID != "111111256096816351" || identity.legacyFitbitUserID != "A1B2C3" {
		t.Fatalf("identity = (%q, %q), want response identity", identity.healthUserID, identity.legacyFitbitUserID)
	}
}

func TestFetchGoogleProfileUsesProfileEndpoint(t *testing.T) {
	t.Parallel()
	var gotURL string
	doer := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotURL = request.URL.String()
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"name":"users/111111256096816351/profile","userConfiguredWalkingStrideLengthMm":720}`)),
		}, nil
	})}

	profile, err := fetchGoogleProfile(providerGET{doer: doer}, "access-secret-value")
	if err != nil {
		t.Fatalf("fetch profile: %v", err)
	}
	if gotURL != googleHealthProfileURL {
		t.Fatalf("profile URL = %q, want %q", gotURL, googleHealthProfileURL)
	}
	if profile.healthUserID != "111111256096816351" || profile.resourceName != "users/111111256096816351/profile" {
		t.Fatalf("profile = (%q, %q), want response profile", profile.healthUserID, profile.resourceName)
	}
	if !strings.Contains(profile.rawJSON, "userConfiguredWalkingStrideLengthMm") {
		t.Fatalf("profile raw JSON = %s, want profile payload", profile.rawJSON)
	}
}

func TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody(t *testing.T) {
	t.Parallel()
	doer := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != googleHealthIdentityURL {
			t.Fatalf("raw URL = %q, want identity URL", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"access-secret-value rejected"}`)),
		}, nil
	})}

	_, err := fetchGoogleHealthRaw(context.Background(), doer, rawProviderRequest{endpointName: "getIdentity", url: googleHealthIdentityURL}, "access-secret-value")
	if err == nil {
		t.Fatal("fetch raw error = nil, want HTTP failure")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("fetch raw error = %v, want status", err)
	}
	if strings.Contains(err.Error(), "access-secret-value") {
		t.Fatalf("fetch raw error leaked token/body: %v", err)
	}
}

func TestReadLimitedBodyReportsOversize(t *testing.T) {
	t.Parallel()
	body, tooLarge, err := readLimitedBody(strings.NewReader("abcdef"), 5)
	if err != nil {
		t.Fatalf("read limited body: %v", err)
	}
	if !tooLarge {
		t.Fatal("tooLarge = false, want true")
	}
	if body != nil {
		t.Fatalf("body = %q, want nil when oversized", string(body))
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

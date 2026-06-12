package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSettingsRejectsNoInputFlag pins issue #171: the dead --no-input
// flag is removed from settings' spec. The command never blocks on
// browser input, so accepting --no-input would imply a behaviour it
// does not have. Passing it now produces the Common Flag Set's
// targeted "--no-input is not supported by settings" rejection and
// exits non-zero.
func TestSettingsRejectsNoInputFlag(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "settings", "--no-input")
	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	const want = "--no-input is not supported by settings"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want substring %q", stderr.String(), want)
	}
}

// TestSettingsCommandFailsFastWhenScopeMissing pins the PRD #142 slice 4
// AC: when the stored Connection's granted scopes do not cover the
// scope the settings verb requires (whatever the catalog says today),
// the command exits non-zero, sets result.Status to
// "settings_scope_missing", names the recovery `gohealthcli connect`
// command in result.Message, and crucially does NOT issue any HTTP
// request to googleHealthSettingsURL — proving the scope pre-check
// happens before the upstream call. The test reads the required scope
// from the same googleHealthIdentityEndpointScopes catalog the
// production code uses so a future slice-2 revision of the catalog
// automatically updates what gets stripped from the stored Connection,
// keeping the test honest without manual edits.
func TestSettingsCommandFailsFastWhenScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	// Strip every scope the catalog ties to getSettings from the
	// stored Connection so AccessToken's scope pre-check fails. Using
	// the same catalog key the production code reads means this test
	// keeps pinning the right behaviour after slice 2 rewrites the
	// catalog entry.
	required := googleHealthIdentityEndpointScopes["getSettings"]
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

	// fetchSettings MUST NOT be called when the scope is missing —
	// the bare HTTP 403 PRD #142 documents only happens because the
	// pre-check is absent, so guarding the seam is what proves the
	// migration shut that path down.
	testRuntime.fetchSettings = func(string) (googleSettings, error) {
		t.Fatal("fetchSettings called despite missing scope")
		return googleSettings{}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"settings", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatalf("settings exit code = %d, want non-zero; stdout=%s", code, stdout.String())
	}
	var result settingsResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "settings_scope_missing" {
		t.Fatalf("result.Status = %q, want settings_scope_missing", result.Status)
	}
	// The recovery hint either names `--add-scopes <keyword>` (when the
	// missing scope maps to a connectAddScopeKeywords entry) or names
	// `gohealthcli connect` again (generic fallback). Either way, the
	// message must mention `connect` — that single substring covers
	// both shapes and survives a slice-2 catalog rewrite.
	if !strings.Contains(result.Message, "gohealthcli connect") {
		t.Fatalf("result.Message = %q, want it to name `gohealthcli connect` recovery", result.Message)
	}
	keywords := addScopeKeywordsForScopes(required)
	if len(keywords) == len(required) && len(keywords) > 0 {
		wantHint := "--add-scopes " + strings.Join(keywords, ",")
		if !strings.Contains(result.Message, wantHint) {
			t.Fatalf("result.Message = %q, want it to name %q", result.Message, wantHint)
		}
	}
}

// TestSettingsCommandAutoRefreshesExpiredAccessToken pins the AC for
// PRD #142 slice 4: with an expired access token but valid refresh
// token and oauthClient.kind == "file", settings refreshes
// transparently, persists the new token via UpdateConnectionTokenMetadata
// on the archive (the same handle openHealthArchiveConnectionAPI
// already returns), and exits 0 with status "settings_archived" plus
// a new identity_snapshots row whose snapshot_kind = 'settings'.
func TestSettingsCommandAutoRefreshesExpiredAccessToken(t *testing.T) {
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
	// requires for getSettings, so this test still exercises auto-refresh
	// after slice 2 (#176) revises the catalog away from the default-granted
	// scope set.
	for _, scope := range googleHealthIdentityEndpointScopes["getSettings"] {
		addStoredConnectionScope(t, archivePath, scope)
	}
	// Force the stored access-token expires_at into the past so
	// AccessToken must take the auto-refresh path.
	setConnectionTokenExpiry(t, archivePath, "2026-01-01T00:00:00Z")

	settingsNow := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	refreshedExpiresAt := settingsNow.Add(time.Hour)
	testRuntime.now = func() time.Time { return settingsNow }
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

	var calledWithToken string
	testRuntime.fetchSettings = func(accessToken string) (googleSettings, error) {
		calledWithToken = accessToken
		return googleSettings{
			rawJSON: `{"unitSystem":"METRIC"}`,
		}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"settings", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("settings exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}
	if calledWithToken != "rotated-access-secret" {
		t.Fatalf("fetchSettings access token = %q, want rotated-access-secret", calledWithToken)
	}

	var result settingsResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "settings_archived" {
		t.Fatalf("result.Status = %q, want settings_archived", result.Status)
	}

	// Refreshed expires_at must have been persisted to the archive's
	// token_metadata_json via UpdateConnectionTokenMetadata.
	gotMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if !strings.Contains(gotMetadata, refreshedExpiresAt.Format(time.RFC3339)) {
		t.Fatalf("archived token_metadata_json = %s, want refreshed expires_at %s", gotMetadata, refreshedExpiresAt.Format(time.RFC3339))
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshOAuthToken call count = %d, want 1 (no retry loop should double-rotate the stored token)", refreshCalls)
	}

	// A new identity_snapshots row with snapshot_kind = 'settings'
	// must exist so the auto-refresh path doesn't silently skip the
	// archive write the AC requires.
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var snapshotCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM identity_snapshots WHERE snapshot_kind = 'settings'`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count settings snapshots: %v", err)
	}
	if snapshotCount != 1 {
		t.Fatalf("settings snapshot count = %d, want 1", snapshotCount)
	}
}

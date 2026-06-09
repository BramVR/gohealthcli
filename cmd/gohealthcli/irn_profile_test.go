package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// addStoredConnectionScope rewrites the stored Connection's token
// metadata to append `scope` to its scopes array. Used by tests that
// need to simulate a Connection where --add-scopes has already run.
func addStoredConnectionScope(t *testing.T, archivePath, scope string) {
	t.Helper()
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var metadata string
	if err := db.QueryRow(`SELECT token_metadata_json FROM connections LIMIT 1`).Scan(&metadata); err != nil {
		t.Fatalf("read token_metadata_json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		t.Fatalf("parse token metadata: %v", err)
	}
	var scopes []string
	if v, ok := raw["scopes"]; ok {
		_ = json.Unmarshal(v, &scopes)
	}
	if !scopeListContains(scopes, scope) {
		scopes = append(scopes, scope)
	}
	encoded, _ := json.Marshal(scopes)
	raw["scopes"] = encoded
	out, _ := json.Marshal(raw)
	if _, err := db.Exec(`UPDATE connections SET token_metadata_json = ?`, string(out)); err != nil {
		t.Fatalf("write token_metadata_json: %v", err)
	}
}

// TestCurrentIRNProfileViewProjectsLatestSnapshot pins the slice C
// view: once an irn-profile snapshot is archived, current_irn_profile
// returns one row per Connection projecting the latest onboarding and
// enrollment state.
func TestCurrentIRNProfileViewProjectsLatestSnapshot(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	connection, err := archive.CurrentConnection()
	if err != nil {
		archive.Close()
		t.Fatalf("CurrentConnection: %v", err)
	}
	if _, err := archive.Insert(connection, "irn-profile", `{"onboardingState":"PENDING","enrollmentState":"NOT_ENROLLED","lastUpdateTime":"2026-05-01T00:00:00Z"}`, "2026-05-01T00:00:00Z"); err != nil {
		archive.Close()
		t.Fatalf("Insert old: %v", err)
	}
	if _, err := archive.Insert(connection, "irn-profile", `{"onboardingState":"COMPLETED","enrollmentState":"ENROLLED","lastUpdateTime":"2026-06-08T00:00:00Z"}`, "2026-06-08T00:00:00Z"); err != nil {
		archive.Close()
		t.Fatalf("Insert new: %v", err)
	}
	archive.Close()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer db.Close()
	var onboarding, enrollment, lastUpdate string
	err = db.QueryRow(`SELECT onboarding_state, enrollment_state, last_update_time FROM current_irn_profile WHERE connection_id = ?`, connection.id).Scan(&onboarding, &enrollment, &lastUpdate)
	if err != nil {
		t.Fatalf("query current_irn_profile: %v", err)
	}
	if onboarding != "COMPLETED" || enrollment != "ENROLLED" {
		t.Fatalf("onboarding=%q enrollment=%q, want COMPLETED/ENROLLED (newest snapshot)", onboarding, enrollment)
	}
	if lastUpdate != "2026-06-08T00:00:00Z" {
		t.Fatalf("last_update_time = %q, want newest snapshot value", lastUpdate)
	}
}

// TestIRNProfileCommandArchivesSnapshotWhenScopeGranted is the slice
// B tracer for #99: `gohealthcli irn-profile` fetches users.getIrnProfile
// and archives the payload through Identity Snapshot Archive with
// kind='irn-profile'.
func TestIRNProfileCommandArchivesSnapshotWhenScopeGranted(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	// Mark the IRN scope as granted on the stored Connection — slice B
	// reads from token_metadata_json's `scopes` field.
	addStoredConnectionScope(t, archivePath, connectAddScopeKeywords["irn"])

	originalFetchIRNProfile := fetchIRNProfile
	fetchIRNProfile = func(string) (googleIRNProfile, error) {
		return googleIRNProfile{
			rawJSON: `{"onboardingState":"COMPLETED","enrollmentState":"ENROLLED","lastUpdateTime":"2026-06-01T00:00:00Z"}`,
		}, nil
	}
	t.Cleanup(func() { fetchIRNProfile = originalFetchIRNProfile })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"irn-profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("irn-profile exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}

	archive, err := openIdentitySnapshotArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	latest, found, err := archive.Latest(connection, "irn-profile")
	if err != nil || !found {
		t.Fatalf("Latest(irn-profile): found=%v err=%v", found, err)
	}
	if latest.Kind != "irn-profile" {
		t.Fatalf("Kind = %q, want irn-profile", latest.Kind)
	}
}

// TestIRNProfileCommandAutoRefreshesExpiredAccessToken pins the AC for
// PRD #142 slice 1: with an expired access token but valid refresh
// token and oauthClient.kind == "file", the verb refreshes
// transparently, persists the new token via UpdateConnectionTokenMetadata
// on the archive, and exits 0 with status "irn_profile_archived".
func TestIRNProfileCommandAutoRefreshesExpiredAccessToken(t *testing.T) {
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
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	// Mark the IRN scope as granted on the stored Connection so the
	// scope pre-check passes — the verb under test exercises the
	// auto-refresh path, not the scope-missing path.
	addStoredConnectionScope(t, archivePath, connectAddScopeKeywords["irn"])
	// Force the stored access-token expires_at into the past so
	// AccessToken must take the auto-refresh path.
	setConnectionTokenExpiry(t, archivePath, "2026-01-01T00:00:00Z")

	irnNow := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	refreshedExpiresAt := irnNow.Add(time.Hour)
	testRuntime.now = func() time.Time { return irnNow }
	// Count refresh attempts so the AC's implicit "refresh once, persist
	// once" contract is guarded against a regression where retries would
	// silently double-rotate the stored token.
	refreshCalls := 0
	testRuntime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		refreshCalls++
		if refreshToken != "connect-refresh-secret" {
			t.Fatalf("refresh token = %q, want connect-refresh-secret", refreshToken)
		}
		return oauthTokenResponse{
			accessToken:  "rotated-access-secret",
			refreshToken: "connect-refresh-secret",
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    refreshedExpiresAt,
			rawTokenMaterialObject: map[string]any{
				"access_token":  "rotated-access-secret",
				"refresh_token": "connect-refresh-secret",
				"token_type":    "Bearer",
				"expires_in":    float64(3600),
			},
		}, nil
	}

	originalFetchIRNProfile := fetchIRNProfile
	var calledWithToken string
	fetchIRNProfile = func(accessToken string) (googleIRNProfile, error) {
		calledWithToken = accessToken
		return googleIRNProfile{
			rawJSON: `{"onboardingState":"COMPLETED","enrollmentState":"ENROLLED","lastUpdateTime":"2026-06-01T00:00:00Z"}`,
		}, nil
	}
	t.Cleanup(func() { fetchIRNProfile = originalFetchIRNProfile })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"irn-profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("irn-profile exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}
	if calledWithToken != "rotated-access-secret" {
		t.Fatalf("fetchIRNProfile access token = %q, want rotated-access-secret", calledWithToken)
	}

	var result irnProfileResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "irn_profile_archived" {
		t.Fatalf("result.Status = %q, want irn_profile_archived", result.Status)
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
}

// TestIRNProfileCommandFailsFastWhenScopeMissing pins the AC:
// when the .irn.readonly scope is not on the stored Connection, the
// verb errors with a clear 'run connect --add-scopes irn' instruction
// and does NOT trigger the browser flow or call the upstream API.
func TestIRNProfileCommandFailsFastWhenScopeMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d", code)
	}

	// fetchIRNProfile MUST NOT be called when the scope is missing.
	originalFetchIRNProfile := fetchIRNProfile
	fetchIRNProfile = func(string) (googleIRNProfile, error) {
		t.Fatal("fetchIRNProfile called despite missing scope")
		return googleIRNProfile{}, nil
	}
	t.Cleanup(func() { fetchIRNProfile = originalFetchIRNProfile })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"irn-profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
	if code == 0 {
		t.Fatalf("irn-profile exit code = %d, want non-zero; stdout=%s", code, stdout.String())
	}
	var result irnProfileResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "irn_profile_scope_missing" {
		t.Fatalf("result.Status = %q, want irn_profile_scope_missing", result.Status)
	}
	if !strings.Contains(result.Message, "connect --add-scopes irn") {
		t.Fatalf("result.Message = %q, want it to name `connect --add-scopes irn`", result.Message)
	}
}

// TestIRNProfileRejectsNoInputFlag pins issue #171: the dead --no-input
// flag is removed from irn-profile's spec. The command never blocks on
// browser input, so accepting --no-input would imply a behaviour it
// does not have. Passing it now produces the Common Flag Set's
// targeted "--no-input is not supported by irn-profile" rejection and
// exits non-zero.
func TestIRNProfileRejectsNoInputFlag(t *testing.T) {
	code, stdout, stderr := runCommand(t, "irn-profile", "--no-input")
	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	const want = "--no-input is not supported by irn-profile"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want substring %q", stderr.String(), want)
	}
}

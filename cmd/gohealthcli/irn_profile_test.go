package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
	code := run([]string{"irn-profile", "--config", configPath, "--db", archivePath, "--plain"}, stdout, stderr)
	if code == 0 {
		t.Fatalf("irn-profile exit code = %d, want non-zero; stdout=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "connect --add-scopes irn") {
		t.Fatalf("stdout missing reconnect hint:\n%s", stdout.String())
	}
}

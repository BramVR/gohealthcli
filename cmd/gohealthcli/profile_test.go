package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestProfileCommandFailsFastWhenScopeMissing pins the PRD #142 slice 5
// AC: when the stored Connection's granted scopes do not cover the
// scope the profile verb requires (whatever the catalog says today),
// the command exits non-zero, sets result.Status to
// "profile_scope_missing", names the recovery `gohealthcli connect`
// command in result.Message, and crucially does NOT issue any HTTP
// request to googleHealthProfileURL — proving the scope pre-check
// happens before the upstream call. The test reads the required scope
// from the same googleHealthIdentityEndpointScopes catalog the
// production code uses so a future slice-2 revision of the catalog
// automatically updates what gets stripped from the stored Connection,
// keeping the test honest without manual edits.
func TestProfileCommandFailsFastWhenScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	// Strip every scope the catalog ties to getProfile from the
	// stored Connection so AccessToken's scope pre-check fails. Using
	// the same catalog key the production code reads means this test
	// keeps pinning the right behaviour after slice 2 rewrites the
	// catalog entry.
	required := googleHealthIdentityEndpointScopes["getProfile"]
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

	// fetchProfile MUST NOT be called when the scope is missing —
	// the bare HTTP 403 PRD #142 documents only happens because the
	// pre-check is absent, so guarding the seam is what proves the
	// migration shut that path down.
	testRuntime.fetchProfile = func(string) (googleProfile, error) {
		t.Fatal("fetchProfile called despite missing scope")
		return googleProfile{}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatalf("profile exit code = %d, want non-zero; stdout=%s", code, stdout.String())
	}
	var result profileResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "profile_scope_missing" {
		t.Fatalf("result.Status = %q, want profile_scope_missing", result.Status)
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

// TestProfileCommandAutoRefreshesExpiredAccessToken pins the AC for
// PRD #142 slice 5: with an expired access token but valid refresh
// token and oauthClient.kind == "file", profile refreshes
// transparently, persists the new token via UpdateConnectionTokenMetadata
// on the archive (the same handle openHealthArchiveConnectionAPI
// already returns), and exits 0 with status "profile_archived" plus
// a new identity_snapshots row whose snapshot_kind = 'profile'.
func TestProfileCommandAutoRefreshesExpiredAccessToken(t *testing.T) {
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
	// requires for getProfile, so this test still exercises auto-refresh
	// after slice 2 (#176) revises the catalog away from the default-granted
	// scope set.
	for _, scope := range googleHealthIdentityEndpointScopes["getProfile"] {
		addStoredConnectionScope(t, archivePath, scope)
	}
	// Force the stored access-token expires_at into the past so
	// AccessToken must take the auto-refresh path.
	setConnectionTokenExpiry(t, archivePath, "2026-01-01T00:00:00Z")

	profileNow := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	refreshedExpiresAt := profileNow.Add(time.Hour)
	testRuntime.now = func() time.Time { return profileNow }
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

	// profile reaches the Provider through runtime.fetchProfile, so the
	// auto-refresh path's stub lives on the runtime adapters; without it
	// the real HTTP fetchGoogleProfile would leak into the test.
	var calledWithToken string
	testRuntime.fetchProfile = func(accessToken string) (googleProfile, error) {
		calledWithToken = accessToken
		return googleProfile{
			healthUserID: "111111256096816351",
			rawJSON:      `{"name":"users/111111256096816351/profile","profile":{"unit":"metric"}}`,
		}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"profile", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("profile exit code = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}
	if calledWithToken != "rotated-access-secret" {
		t.Fatalf("fetchProfile access token = %q, want rotated-access-secret", calledWithToken)
	}

	var result profileResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if result.Status != "profile_archived" {
		t.Fatalf("result.Status = %q, want profile_archived", result.Status)
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

	// A new identity_snapshots row with snapshot_kind = 'profile'
	// must exist so the auto-refresh path doesn't silently skip the
	// archive write the AC requires.
	db := openArchiveForTest(t, archivePath)
	var snapshotCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM identity_snapshots WHERE snapshot_kind = 'profile'`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count profile snapshots: %v", err)
	}
	if snapshotCount != 1 {
		t.Fatalf("profile snapshot count = %d, want 1", snapshotCount)
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

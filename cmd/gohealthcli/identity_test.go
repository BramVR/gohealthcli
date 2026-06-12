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

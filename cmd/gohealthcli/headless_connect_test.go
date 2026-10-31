package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type readCountingReader struct {
	reads atomic.Int32
}

func (reader *readCountingReader) Read(_ []byte) (int, error) {
	reader.reads.Add(1)
	return 0, io.EOF
}

func TestConnectHeadlessCompleteNoInputRejectsWithoutReadingStdin(t *testing.T) {
	t.Parallel()
	reader := new(readCountingReader)
	runtime := productionRuntimeAdapters()
	runtime.stdin = reader
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--complete", "--no-input", "--json"}, stdout, stderr, runtime)
	if code != 1 {
		t.Fatalf("connect --complete --no-input exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if reader.reads.Load() != 0 {
		t.Fatalf("stdin reads = %d, want 0", reader.reads.Load())
	}
	if stderr.String() != "" || !strings.Contains(stdout.String(), "cannot be combined with --no-input") {
		t.Fatalf("stdout/stderr = %q / %q, want JSON flag failure on stdout", stdout.String(), stderr.String())
	}
}

func TestConnectHeadlessStartPersistsPKCEAndPrintsAuthorizationContract(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: now, failIfCalled: true})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"connect", "--headless-start",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr, runtime)
	if code != 0 {
		t.Fatalf("connect --headless-start exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var result struct {
		Status           string `json:"status"`
		AuthorizationURL string `json:"authorization_url"`
		ExpiresAt        string `json:"expires_at"`
		Message          string `json:"message"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode stdout: %v\nstdout: %s", err, stdout.String())
	}
	if result.Status != "authorization_pending" {
		t.Fatalf("status = %q, want authorization_pending", result.Status)
	}
	if result.ExpiresAt != now.Add(10*time.Minute).Format(time.RFC3339) {
		t.Fatalf("expires_at = %q, want %q", result.ExpiresAt, now.Add(10*time.Minute).Format(time.RFC3339))
	}
	authURL, err := url.Parse(result.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := authURL.Query()
	if authURL.Scheme != "https" || authURL.Hostname() != "accounts.google.com" {
		t.Fatalf("authorization origin = %s://%s, want Google HTTPS", authURL.Scheme, authURL.Host)
	}
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("state") == "" {
		t.Fatalf("authorization URL missing PKCE S256/state contract")
	}
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect URI: %v", err)
	}
	if redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" || redirect.Path != "/oauth2callback" {
		t.Fatalf("redirect URI = %q, want dynamic IPv4 loopback callback", redirect.String())
	}

	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	if usesPOSIXPermissions() {
		info, err := os.Stat(tokenStorePath)
		if err != nil {
			t.Fatalf("stat Credential Store: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("Credential Store mode = %04o, want 0600", info.Mode().Perm())
		}
	}
	storeText := string(storeBytes)
	if !strings.Contains(storeText, `"code_verifier"`) || !strings.Contains(storeText, `"state"`) {
		t.Fatalf("Credential Store does not contain pending PKCE metadata")
	}
	if strings.Contains(stdout.String()+stderr.String(), `"code_verifier"`) {
		t.Fatal("connect --headless-start output leaked PKCE verifier")
	}
}

func TestConnectHeadlessCompleteConcurrentClaimHasOneWinner(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	baseRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:          now,
		accessToken:  "race-access-secret",
		refreshToken: "race-refresh-secret",
	})
	startStdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--headless-start", "--config", configPath, "--db", archivePath, "--json"}, startStdout, new(bytes.Buffer), baseRuntime); code != 0 {
		t.Fatalf("headless start exit = %d", code)
	}
	var start struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(startStdout.Bytes(), &start); err != nil {
		t.Fatalf("decode headless start: %v", err)
	}
	authURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	redirectURL, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	query := redirectURL.Query()
	query.Set("code", "race-code")
	query.Set("state", authURL.Query().Get("state"))
	redirectURL.RawQuery = query.Encode()

	var exchanges atomic.Int32
	baseRuntime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		exchanges.Add(1)
		body := fmt.Sprintf(`{"access_token":"race-access-secret","refresh_token":"race-refresh-secret","expires_in":3600,"scope":%q,"token_type":"Bearer"}`, authURL.Query().Get("scope"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}

	const contenders = 24
	startGate := make(chan struct{})
	codes := make(chan int, contenders)
	var group sync.WaitGroup
	for range contenders {
		group.Add(1)
		go func() {
			defer group.Done()
			<-startGate
			runtime := baseRuntime
			runtime.stdin = strings.NewReader(redirectURL.String() + "\n")
			codes <- runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, new(bytes.Buffer), new(bytes.Buffer), runtime)
		}()
	}
	close(startGate)
	group.Wait()
	close(codes)

	winners := 0
	for code := range codes {
		if code == 0 {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("completion winners = %d, want exactly 1", winners)
	}
	if got := exchanges.Load(); got != 1 {
		t.Fatalf("token exchanges = %d, want exactly 1", got)
	}
}

func TestConnectHeadlessStartSerializesWithCompletionClaim(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	lock, err := lockHeadlessClaimFile(headlessClaimLockPath(archivePath))
	if err != nil {
		t.Fatalf("hold headless claim lock: %v", err)
	}
	locked := true
	t.Cleanup(func() {
		if locked {
			_ = unlockHeadlessClaimFile(lock)
		}
	})

	done := make(chan int, 1)
	go func() {
		runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC), failIfCalled: true})
		done <- runWithRuntime([]string{"connect", "--headless-start", "--config", configPath, "--db", archivePath, "--json"}, new(bytes.Buffer), new(bytes.Buffer), runtime)
	}()

	select {
	case code := <-done:
		t.Fatalf("headless start completed with exit %d while completion claim lock was held", code)
	case <-time.After(250 * time.Millisecond):
	}
	if err := unlockHeadlessClaimFile(lock); err != nil {
		t.Fatalf("release headless claim lock: %v", err)
	}
	locked = false
	if code := <-done; code != 0 {
		t.Fatalf("headless start exit after claim lock release = %d, want 0", code)
	}
}

func TestHeadlessClaimLockPathIsScopedToArchive(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.sqlite")
	want := archivePath + ".headless-claim.lock"
	if got := headlessClaimLockPath(archivePath); got != want {
		t.Fatalf("headless claim lock path = %q, want %q", got, want)
	}
}

func TestConnectHeadlessCompleteRejectsChangedScopeBindingBeforeClaim(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC), failIfCalled: true})
	authURL, callback := startHeadlessCallbackForTest(t, configPath, archivePath, runtime)
	query := callback.Query()
	query.Set("code", "scope-binding-code")
	query.Set("state", authURL.Query().Get("state"))
	callback.RawQuery = query.Encode()

	var exchanges atomic.Int32
	runtime.stdin = strings.NewReader(callback.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return nil, fmt.Errorf("token exchange must not run")
	})}
	stdout := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--add-scopes", "settings", "--json"}, stdout, new(bytes.Buffer), runtime)
	if code != 1 {
		t.Fatalf("changed-scope completion exit = %d, want 1\nstdout: %s", code, stdout.String())
	}
	if exchanges.Load() != 0 {
		t.Fatalf("token exchanges = %d, want 0", exchanges.Load())
	}
	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	if !strings.Contains(string(storeBytes), `"headless-connect:`) {
		t.Fatal("changed-scope completion claimed pending authorization")
	}
}

func TestConnectHeadlessCompleteRejectsChangedOAuthConfigurationBeforeClaim(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC), failIfCalled: true})
	authURL, callback := startHeadlessCallbackForTest(t, configPath, archivePath, runtime)
	query := callback.Query()
	query.Set("code", "oauth-binding-code")
	query.Set("state", authURL.Query().Get("state"))
	callback.RawQuery = query.Encode()
	clientPath := filepath.Join(tempDir, "client_secret.json")
	changedClient := `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"https://accounts.google.com/o/oauth2/v2/auth","token_uri":"https://oauth2.googleapis.com/alternate-token","redirect_uris":["http://localhost"]}}`
	if err := os.WriteFile(clientPath, []byte(changedClient), 0o600); err != nil {
		t.Fatalf("change OAuth client config: %v", err)
	}

	var exchanges atomic.Int32
	runtime.stdin = strings.NewReader(callback.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return nil, fmt.Errorf("token exchange must not run")
	})}
	stdout := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), runtime)
	if code != 1 {
		t.Fatalf("changed-OAuth-config completion exit = %d, want 1\nstdout: %s", code, stdout.String())
	}
	if exchanges.Load() != 0 {
		t.Fatalf("token exchanges = %d, want 0", exchanges.Load())
	}
	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	if !strings.Contains(string(storeBytes), `"headless-connect:`) {
		t.Fatal("changed-OAuth-config completion claimed pending authorization")
	}
}

func TestConnectHeadlessPendingIdentityUsesCanonicalConfigAndArchivePaths(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	archiveDir := filepath.Dir(archivePath)
	archiveAlias := archiveDir + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(archiveDir) + string(os.PathSeparator) + filepath.Base(archivePath)
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	configWithAlias := strings.Replace(string(configBytes), tomlQuotedString(archivePath), tomlQuotedString(archiveAlias), 1)
	if configWithAlias == string(configBytes) {
		t.Fatal("test config archive path was not replaced")
	}
	if err := os.WriteFile(configPath, []byte(configWithAlias), 0o600); err != nil {
		t.Fatalf("write aliased config: %v", err)
	}
	t.Chdir(tempDir)
	relativeConfig, err := filepath.Rel(tempDir, configPath)
	if err != nil {
		t.Fatalf("relative config path: %v", err)
	}
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:          time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC),
		accessToken:  "canonical-path-access-secret",
		refreshToken: "canonical-path-refresh-secret",
	})
	startStdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--headless-start", "--config", relativeConfig, "--db", archiveAlias, "--json"}, startStdout, new(bytes.Buffer), runtime); code != 0 {
		t.Fatalf("aliased headless start exit = %d\nstdout: %s", code, startStdout.String())
	}
	var start struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(startStdout.Bytes(), &start); err != nil {
		t.Fatalf("decode headless start: %v", err)
	}
	authURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	callback, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse callback URL: %v", err)
	}
	query := callback.Query()
	query.Set("code", "canonical-path-code")
	query.Set("state", authURL.Query().Get("state"))
	callback.RawQuery = query.Encode()
	runtime.stdin = strings.NewReader(callback.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"access_token":"canonical-path-access-secret","refresh_token":"canonical-path-refresh-secret","expires_in":3600,"scope":%q,"token_type":"Bearer"}`, authURL.Query().Get("scope"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	stdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), runtime); code != 0 {
		t.Fatalf("canonical completion exit = %d, want 0\nstdout: %s", code, stdout.String())
	}
}

func TestConnectHeadlessCompleteRejectsPartialConsentBeforeIdentityOrTokenStorage(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: now, failIfCalled: true})
	startStdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--headless-start", "--config", configPath, "--db", archivePath, "--json"}, startStdout, new(bytes.Buffer), runtime); code != 0 {
		t.Fatalf("headless start exit = %d", code)
	}
	var start struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(startStdout.Bytes(), &start); err != nil {
		t.Fatalf("decode headless start: %v", err)
	}
	authURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	redirectURL, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	query := redirectURL.Query()
	query.Set("code", "partial-consent-code")
	query.Set("state", authURL.Query().Get("state"))
	redirectURL.RawQuery = query.Encode()
	runtime.stdin = strings.NewReader(redirectURL.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"access_token":"partial-access-secret","refresh_token":"partial-refresh-secret","expires_in":3600,"scope":"https://www.googleapis.com/auth/googlehealth.profile.readonly","token_type":"Bearer"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, runtime)
	if code != 1 {
		t.Fatalf("partial-consent complete exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "requested Google Health scopes were not granted") {
		t.Fatalf("stdout = %q, want partial-consent failure", stdout.String())
	}
	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	if strings.Contains(string(storeBytes), "partial-access-secret") || strings.Contains(string(storeBytes), "partial-refresh-secret") {
		t.Fatal("partial-consent token material was stored")
	}
	db := openArchiveForTest(t, archivePath)
	var connections int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM connections`).Scan(&connections); err != nil {
		t.Fatalf("count Connections: %v", err)
	}
	if connections != 0 {
		t.Fatalf("Connections = %d, want 0", connections)
	}
}

func TestConnectHeadlessCompleteRejectsChangedIdentityExpectationBeforeExchange(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: now})
	mustConnect(t, configPath, archivePath, runtime)

	startStdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--headless-start", "--config", configPath, "--db", archivePath, "--json"}, startStdout, new(bytes.Buffer), runtime); code != 0 {
		t.Fatalf("headless start exit = %d", code)
	}
	var start struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(startStdout.Bytes(), &start); err != nil {
		t.Fatalf("decode headless start: %v", err)
	}
	authURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	redirectURL, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	query := redirectURL.Query()
	query.Set("code", "identity-binding-code")
	query.Set("state", authURL.Query().Get("state"))
	redirectURL.RawQuery = query.Encode()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE connections SET google_health_user_id = ?`, "changed-identity-expectation"); err != nil {
		_ = db.Close()
		t.Fatalf("change identity expectation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	var exchanges atomic.Int32
	runtime.stdin = strings.NewReader(redirectURL.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return nil, fmt.Errorf("token exchange must not run")
	})}
	stdout := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), runtime)
	if code != 1 {
		t.Fatalf("changed-identity completion exit = %d, want 1\nstdout: %s", code, stdout.String())
	}
	if exchanges.Load() != 0 {
		t.Fatalf("token exchanges = %d, want 0", exchanges.Load())
	}
	if !strings.Contains(stdout.String(), "identity expectation") {
		t.Fatalf("stdout = %q, want identity-binding failure", stdout.String())
	}
}

func TestConnectHeadlessCompleteRejectsTamperedPendingVerifierBeforeExchange(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: now})
	startStdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--headless-start", "--config", configPath, "--db", archivePath, "--json"}, startStdout, new(bytes.Buffer), runtime); code != 0 {
		t.Fatalf("headless start exit = %d", code)
	}
	var start struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(startStdout.Bytes(), &start); err != nil {
		t.Fatalf("decode headless start: %v", err)
	}
	authURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	redirectURL, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	query := redirectURL.Query()
	query.Set("code", "tamper-code")
	query.Set("state", authURL.Query().Get("state"))
	redirectURL.RawQuery = query.Encode()

	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	var store map[string]map[string]any
	if err := json.Unmarshal(storeBytes, &store); err != nil {
		t.Fatalf("decode Credential Store: %v", err)
	}
	canonicalConfigPath, err := canonicalCredentialPath(configPath)
	if err != nil {
		t.Fatalf("canonical config path: %v", err)
	}
	canonicalArchivePath, err := canonicalCredentialPath(archivePath)
	if err != nil {
		t.Fatalf("canonical archive path: %v", err)
	}
	pending := store[headlessPendingCredentialKey(canonicalConfigPath, canonicalArchivePath)]
	if pending == nil {
		t.Fatal("Credential Store missing pending authorization")
	}
	pending["code_verifier"] = "tampered-verifier"
	storeBytes, err = json.MarshalIndent(store, "", "  ")
	if err != nil {
		t.Fatalf("encode tampered Credential Store: %v", err)
	}
	if err := os.WriteFile(tokenStorePath, append(storeBytes, '\n'), 0o600); err != nil {
		t.Fatalf("write tampered Credential Store: %v", err)
	}

	var exchanges atomic.Int32
	runtime.stdin = strings.NewReader(redirectURL.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return nil, fmt.Errorf("token exchange must not run")
	})}
	stdout := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), runtime)
	if code != 1 {
		t.Fatalf("tampered completion exit = %d, want 1\nstdout: %s", code, stdout.String())
	}
	if exchanges.Load() != 0 {
		t.Fatalf("token exchanges = %d, want 0", exchanges.Load())
	}
	if !strings.Contains(stdout.String(), "pending authorization") {
		t.Fatalf("stdout = %q, want pending-authorization tamper failure", stdout.String())
	}
}

func TestConnectHeadlessCompleteRejectsInvalidRedirectInputWithoutClaiming(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input func(*url.URL, *url.URL) string
	}{
		{name: "empty", input: func(_, _ *url.URL) string { return "" }},
		{name: "bare code", input: func(_, _ *url.URL) string { return "private-code-marker\n" }},
		{name: "malformed URL", input: func(_, _ *url.URL) string { return "%\n" }},
		{name: "multiple lines", input: func(_, callback *url.URL) string { return callback.String() + "\n" + callback.String() + "\n" }},
		{name: "oversized", input: func(_, _ *url.URL) string { return strings.Repeat("x", maxHeadlessRedirectBytes+1) }},
		{name: "wrong state", input: func(_, callback *url.URL) string {
			copy := *callback
			query := copy.Query()
			query.Set("state", "private-state-marker")
			copy.RawQuery = query.Encode()
			return copy.String() + "\n"
		}},
		{name: "wrong scheme", input: func(_, callback *url.URL) string {
			copy := *callback
			copy.Scheme = "https"
			return copy.String() + "\n"
		}},
		{name: "wrong host", input: func(_, callback *url.URL) string {
			copy := *callback
			copy.Host = "localhost:" + callback.Port()
			return copy.String() + "\n"
		}},
		{name: "wrong port", input: func(_, callback *url.URL) string {
			copy := *callback
			copy.Host = "127.0.0.1:1"
			return copy.String() + "\n"
		}},
		{name: "wrong path", input: func(_, callback *url.URL) string {
			copy := *callback
			copy.Path = "/other"
			return copy.String() + "\n"
		}},
		{name: "fragment", input: func(_, callback *url.URL) string {
			copy := *callback
			copy.Fragment = "private-fragment"
			return copy.String() + "\n"
		}},
		{name: "duplicate state", input: func(auth, callback *url.URL) string {
			copy := *callback
			query := copy.Query()
			query.Add("state", auth.Query().Get("state"))
			copy.RawQuery = query.Encode()
			return copy.String() + "\n"
		}},
		{name: "duplicate code", input: func(_, callback *url.URL) string {
			copy := *callback
			query := copy.Query()
			query.Add("code", "second-private-code")
			copy.RawQuery = query.Encode()
			return copy.String() + "\n"
		}},
		{name: "code and error", input: func(_, callback *url.URL) string {
			copy := *callback
			query := copy.Query()
			query.Set("error", "access_denied")
			copy.RawQuery = query.Encode()
			return copy.String() + "\n"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tempDir := t.TempDir()
			configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
			runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC), failIfCalled: true})
			startStdout := new(bytes.Buffer)
			if code := runWithRuntime([]string{"connect", "--headless-start", "--config", configPath, "--db", archivePath, "--json"}, startStdout, new(bytes.Buffer), runtime); code != 0 {
				t.Fatalf("headless start exit = %d", code)
			}
			var start struct {
				AuthorizationURL string `json:"authorization_url"`
			}
			if err := json.Unmarshal(startStdout.Bytes(), &start); err != nil {
				t.Fatalf("decode headless start: %v", err)
			}
			authURL, err := url.Parse(start.AuthorizationURL)
			if err != nil {
				t.Fatalf("parse authorization URL: %v", err)
			}
			callback, err := url.Parse(authURL.Query().Get("redirect_uri"))
			if err != nil {
				t.Fatalf("parse callback URL: %v", err)
			}
			query := callback.Query()
			query.Set("code", "private-code-marker")
			query.Set("state", authURL.Query().Get("state"))
			callback.RawQuery = query.Encode()
			var exchanges atomic.Int32
			runtime.stdin = strings.NewReader(test.input(authURL, callback))
			runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				exchanges.Add(1)
				return nil, fmt.Errorf("token exchange must not run")
			})}
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, runtime)
			if code != 1 {
				t.Fatalf("invalid completion exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
			}
			if exchanges.Load() != 0 {
				t.Fatalf("token exchanges = %d, want 0", exchanges.Load())
			}
			if strings.Contains(stdout.String()+stderr.String(), "private-code-marker") || strings.Contains(stdout.String()+stderr.String(), "private-state-marker") || strings.Contains(stdout.String()+stderr.String(), "private-fragment") {
				t.Fatal("invalid-input failure leaked redirected URL material")
			}
			storeBytes, err := os.ReadFile(tokenStorePath)
			if err != nil {
				t.Fatalf("read Credential Store: %v", err)
			}
			if !strings.Contains(string(storeBytes), `"headless-connect:`) {
				t.Fatal("invalid redirect claimed pending authorization")
			}
		})
	}
}

func TestConnectHeadlessCompleteConsumesRedirectFromStdinAndConnects(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:          now,
		accessToken:  "headless-access-secret",
		refreshToken: "headless-refresh-secret",
	})

	startStdout := new(bytes.Buffer)
	startStderr := new(bytes.Buffer)
	startCode := runWithRuntime([]string{
		"connect", "--headless-start",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, startStdout, startStderr, runtime)
	if startCode != 0 {
		t.Fatalf("headless start exit = %d\nstdout: %s\nstderr: %s", startCode, startStdout.String(), startStderr.String())
	}
	var start struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(startStdout.Bytes(), &start); err != nil {
		t.Fatalf("decode headless start: %v", err)
	}
	authURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	redirectURL, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	redirectQuery := redirectURL.Query()
	redirectQuery.Set("code", "synthetic-headless-code")
	redirectQuery.Set("state", authURL.Query().Get("state"))
	redirectURL.RawQuery = redirectQuery.Encode()

	runtime.stdin = strings.NewReader(redirectURL.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		form, err := url.ParseQuery(string(payload))
		if err != nil {
			return nil, err
		}
		if form.Get("code") != "synthetic-headless-code" || form.Get("code_verifier") == "" {
			return nil, fmt.Errorf("token exchange form missing code/verifier")
		}
		body := fmt.Sprintf(`{"access_token":"headless-access-secret","refresh_token":"headless-refresh-secret","expires_in":3600,"scope":%q,"token_type":"Bearer"}`, authURL.Query().Get("scope"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"connect", "--complete",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr, runtime)
	if code != 0 {
		t.Fatalf("connect --complete exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var result connectResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode complete result: %v", err)
	}
	if result.Status != "connected" || result.TokenStatus != "metadata_present" {
		t.Fatalf("complete result = %+v, want connected token metadata", result)
	}
	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	storeText := string(storeBytes)
	if strings.Contains(storeText, `"headless-connect:`) {
		t.Fatal("consumed pending authorization remains in Credential Store")
	}
	if !strings.Contains(storeText, "headless-access-secret") || !strings.Contains(storeText, "headless-refresh-secret") {
		t.Fatal("completed token material missing from Credential Store")
	}
	if strings.Contains(stdout.String()+stderr.String(), "synthetic-headless-code") || strings.Contains(stdout.String()+stderr.String(), "headless-access-secret") {
		t.Fatal("completion output leaked authorization or token material")
	}
	var replayExchanges atomic.Int32
	runtime.stdin = strings.NewReader(redirectURL.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		replayExchanges.Add(1)
		return nil, fmt.Errorf("replay must not exchange")
	})}
	if replayCode := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, new(bytes.Buffer), new(bytes.Buffer), runtime); replayCode != 1 {
		t.Fatalf("replay exit = %d, want 1", replayCode)
	}
	if replayExchanges.Load() != 0 {
		t.Fatalf("replay exchanges = %d, want 0", replayExchanges.Load())
	}
}

func TestConnectHeadlessCompleteRejectsExpiredPendingAuthorizationWithoutClaiming(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: now, failIfCalled: true})
	authURL, callback := startHeadlessCallbackForTest(t, configPath, archivePath, runtime)
	query := callback.Query()
	query.Set("code", "expired-private-code")
	query.Set("state", authURL.Query().Get("state"))
	callback.RawQuery = query.Encode()
	runtime.stdin = strings.NewReader(callback.String() + "\n")
	runtime.now = func() time.Time { return now.Add(headlessAuthorizationLifetime) }
	var exchanges atomic.Int32
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return nil, fmt.Errorf("expired authorization must not exchange")
	})}
	stdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), runtime); code != 1 {
		t.Fatalf("expired completion exit = %d, want 1\nstdout: %s", code, stdout.String())
	}
	if exchanges.Load() != 0 {
		t.Fatalf("token exchanges = %d, want 0", exchanges.Load())
	}
	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	if !strings.Contains(string(storeBytes), `"headless-connect:`) {
		t.Fatal("expired attempt claimed pending authorization")
	}
	if strings.Contains(stdout.String(), "expired-private-code") {
		t.Fatal("expiry failure leaked authorization code")
	}
}

func TestConnectHeadlessCompleteConsumesStateValidCancellationWithoutExchange(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{now: time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC), failIfCalled: true})
	authURL, callback := startHeadlessCallbackForTest(t, configPath, archivePath, runtime)
	query := callback.Query()
	query.Set("state", authURL.Query().Get("state"))
	query.Set("error", "access_denied")
	query.Set("error_description", "private-cancellation-detail")
	callback.RawQuery = query.Encode()
	runtime.stdin = strings.NewReader(callback.String() + "\n")
	var exchanges atomic.Int32
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return nil, fmt.Errorf("cancellation must not exchange")
	})}
	stdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), runtime); code != 1 {
		t.Fatalf("canceled completion exit = %d, want 1\nstdout: %s", code, stdout.String())
	}
	if exchanges.Load() != 0 {
		t.Fatalf("token exchanges = %d, want 0", exchanges.Load())
	}
	if strings.Contains(stdout.String(), "private-cancellation-detail") || !strings.Contains(stdout.String(), "authorization was canceled") {
		t.Fatalf("cancellation output = %q, want generic non-disclosing failure", stdout.String())
	}
	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	if strings.Contains(string(storeBytes), `"headless-connect:`) {
		t.Fatal("state-valid cancellation did not consume pending authorization")
	}
}

func TestConnectHeadlessCompleteReauthorizesSameIdentityAndArchivesActualScopes(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	initialRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:          time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC),
		accessToken:  "old-access-secret",
		refreshToken: "old-refresh-secret",
	})
	mustConnect(t, configPath, archivePath, initialRuntime)

	runtime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:          time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC),
		accessToken:  "replacement-access-secret",
		refreshToken: "replacement-refresh-secret",
	})
	authURL, callback := startHeadlessCallbackForTest(t, configPath, archivePath, runtime, "--add-scopes", "settings")
	query := callback.Query()
	query.Set("code", "same-identity-code")
	query.Set("state", authURL.Query().Get("state"))
	callback.RawQuery = query.Encode()
	runtime.stdin = strings.NewReader(callback.String() + "\n")
	actualScopes := authURL.Query().Get("scope") + " https://www.googleapis.com/auth/synthetic.previously_granted"
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"access_token":"replacement-access-secret","refresh_token":"replacement-refresh-secret","expires_in":3600,"scope":%q,"token_type":"Bearer"}`, actualScopes)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	stdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--complete", "--add-scopes", "settings", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), runtime); code != 0 {
		t.Fatalf("same-identity completion exit = %d\nstdout: %s", code, stdout.String())
	}
	db := openArchiveForTest(t, archivePath)
	var count int
	var metadata string
	if err := db.QueryRowContext(context.Background(), `SELECT count(*), token_metadata_json FROM connections`).Scan(&count, &metadata); err != nil {
		t.Fatalf("read Connection metadata: %v", err)
	}
	if count != 1 {
		t.Fatalf("Connections = %d, want 1", count)
	}
	for _, scope := range strings.Fields(actualScopes) {
		if !strings.Contains(metadata, scope) {
			t.Fatalf("archived token metadata missing actual granted scope %q: %s", scope, metadata)
		}
	}
	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	storeText := string(storeBytes)
	if !strings.Contains(storeText, "replacement-access-secret") || strings.Contains(storeText, "old-access-secret") {
		t.Fatal("same-identity headless completion did not replace token material")
	}
}

func TestConnectHeadlessCompleteUsesRequestedScopesWhenTokenResponseOmitsScope(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:          time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC),
		accessToken:  "omitted-scope-access-secret",
		refreshToken: "omitted-scope-refresh-secret",
	})
	authURL, callback := startHeadlessCallbackForTest(t, configPath, archivePath, runtime, "--add-scopes", "settings")
	query := callback.Query()
	query.Set("code", "omitted-scope-code")
	query.Set("state", authURL.Query().Get("state"))
	callback.RawQuery = query.Encode()
	runtime.stdin = strings.NewReader(callback.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"access_token":"omitted-scope-access-secret","refresh_token":"omitted-scope-refresh-secret","expires_in":3600,"token_type":"Bearer"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	stdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--complete", "--add-scopes", "settings", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), runtime); code != 0 {
		t.Fatalf("omitted-scope completion exit = %d, want 0\nstdout: %s", code, stdout.String())
	}
	db := openArchiveForTest(t, archivePath)
	var metadata string
	if err := db.QueryRowContext(context.Background(), `SELECT token_metadata_json FROM connections`).Scan(&metadata); err != nil {
		t.Fatalf("read Connection metadata: %v", err)
	}
	for _, scope := range strings.Fields(authURL.Query().Get("scope")) {
		if !strings.Contains(metadata, scope) {
			t.Fatalf("archived token metadata missing requested scope %q: %s", scope, metadata)
		}
	}
}

func TestConnectHeadlessCompleteRejectsDifferentIdentityBeforeReplacement(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	initialRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:          time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC),
		accessToken:  "original-access-secret",
		refreshToken: "original-refresh-secret",
		healthUserID: "111111256096816351",
	})
	mustConnect(t, configPath, archivePath, initialRuntime)

	runtime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:          time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC),
		accessToken:  "other-access-secret",
		refreshToken: "other-refresh-secret",
		healthUserID: "222222222222222222",
	})
	authURL, callback := startHeadlessCallbackForTest(t, configPath, archivePath, runtime)
	query := callback.Query()
	query.Set("code", "different-identity-code")
	query.Set("state", authURL.Query().Get("state"))
	callback.RawQuery = query.Encode()
	runtime.stdin = strings.NewReader(callback.String() + "\n")
	runtime.httpDoer = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"access_token":"other-access-secret","refresh_token":"other-refresh-secret","expires_in":3600,"scope":%q,"token_type":"Bearer"}`, authURL.Query().Get("scope"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	stdout := new(bytes.Buffer)
	if code := runWithRuntime([]string{"connect", "--complete", "--config", configPath, "--db", archivePath, "--json"}, stdout, new(bytes.Buffer), runtime); code != 1 {
		t.Fatalf("different-identity completion exit = %d, want 1\nstdout: %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "different Google Identity") {
		t.Fatalf("stdout = %q, want different-identity failure", stdout.String())
	}
	storeBytes, err := os.ReadFile(tokenStorePath)
	if err != nil {
		t.Fatalf("read Credential Store: %v", err)
	}
	storeText := string(storeBytes)
	if !strings.Contains(storeText, "original-access-secret") || strings.Contains(storeText, "other-access-secret") {
		t.Fatal("different-identity completion replaced stored token material")
	}
}

func startHeadlessCallbackForTest(t *testing.T, configPath, archivePath string, runtime runtimeAdapters, extraArgs ...string) (*url.URL, *url.URL) {
	t.Helper()
	args := []string{"connect", "--headless-start", "--config", configPath, "--db", archivePath, "--json"}
	args = append(args, extraArgs...)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	if code := runWithRuntime(args, stdout, stderr, runtime); code != 0 {
		t.Fatalf("headless start exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	var start struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &start); err != nil {
		t.Fatalf("decode headless start: %v", err)
	}
	authURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	callback, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse callback URL: %v", err)
	}
	return authURL, callback
}

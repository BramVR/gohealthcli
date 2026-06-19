package main

// harness_test.go is the shared test harness for the cmd/gohealthcli
// package (issue #286): the CLI drivers (in-process and true-binary),
// the fake runtime constructors and provider fetch fakes, fixture
// builders, and the archive assertion helpers that the per-feature
// test files compose. New tests should start from these helpers
// instead of re-pasting setup prologues.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

var testBinaryPath string

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gohealthcli-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	testBinaryPath = filepath.Join(dir, "gohealthcli")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", testBinaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build command: %v\n%s", err, string(output))
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// runCommand drives one CLI invocation in-process through run() — the
// same dispatch entry main() wires to os.Args — so the executed command
// paths count toward package coverage (issue #286). Tests that need
// true binary semantics (a private process environment, a different
// working directory, exit-status wiring) use the runBinary* drivers.
func runCommand(t *testing.T, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	ensureTestOAuthClientFiles(t, "", args)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run(args, stdout, stderr)
	return code, stdout, stderr
}

// runBinary executes the compiled gohealthcli binary built by TestMain.
// Reach for it only when binary semantics are the point of the test;
// runCommand covers everything else without the subprocess cost.
func runBinary(t *testing.T, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runBinaryInDirWithEnv(t, "", nil, args...)
}

func runBinaryInDir(t *testing.T, dir string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runBinaryInDirWithEnv(t, dir, nil, args...)
}

func runBinaryWithEnv(t *testing.T, env []string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runBinaryInDirWithEnv(t, "", env, args...)
}

func runBinaryInDirWithEnv(t *testing.T, dir string, env []string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	ensureTestOAuthClientFiles(t, dir, args)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd := exec.CommandContext(context.Background(), testBinaryPath, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), env...)

	err := cmd.Run()
	if err == nil {
		return 0, stdout, stderr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout, stderr
	}
	t.Fatalf("run command: %v\nstderr: %s", err, stderr.String())
	return 1, stdout, stderr
}

// connectedArchive runs the connect prologue shared by every
// connected-state test: init a file-credential setup in a fresh temp
// dir, run `connect` through the CLI surface with the fake OAuth
// runtime, and fail the test if connect does not exit 0. It returns
// the config path, archive path, and the connected runtime so tests
// can bind provider fetch fakes onto it. Tests that need the temp dir
// or the token-store path compose initializeFileCredentialSetup,
// newConnectFakeRuntime, and mustConnect directly instead.
func connectedArchive(t *testing.T, config fakeConnectConfig) (string, string, runtimeAdapters) {
	t.Helper()

	configPath, archivePath, _ := initializeFileCredentialSetup(t, t.TempDir())
	runtime := newConnectFakeRuntime(t, config)
	mustConnect(t, configPath, archivePath, runtime)
	return configPath, archivePath, runtime
}

// connectedArchiveViaSetup is connectedArchive's direct-call twin for
// prologues that connect through connectSetupWithRuntimeAndExtraScopes
// — the function the connect command wraps — instead of the CLI
// surface, skipping flag parsing and result rendering.
func connectedArchiveViaSetup(t *testing.T, config fakeConnectConfig) (string, string, runtimeAdapters) {
	t.Helper()

	configPath, archivePath, _ := initializeFileCredentialSetup(t, t.TempDir())
	runtime := newConnectFakeRuntime(t, config)
	mustConnectSetup(t, configPath, archivePath, runtime)
	return configPath, archivePath, runtime
}

// mustConnect runs `connect` through the CLI surface and fails the
// test on a non-zero exit.
func mustConnect(t *testing.T, configPath, archivePath string, runtime runtimeAdapters) {
	t.Helper()

	if code := runConnectCommandWithRuntime(t, configPath, archivePath, runtime); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
}

// mustConnectSetup connects through connectSetupWithRuntimeAndExtraScopes
// and fails the test on error.
func mustConnectSetup(t *testing.T, configPath, archivePath string, runtime runtimeAdapters) {
	t.Helper()

	if _, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, runtime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
}

type fakeConnectConfig struct {
	now                time.Time
	accessToken        string
	refreshToken       string
	refreshExpiresAt   *time.Time
	healthUserID       string
	legacyFitbitUserID string
	wantNoInput        *bool
	failIfCalled       bool
}

type fakeDoctorOnlineConfig struct {
	now                     time.Time
	refreshedAccessToken    string
	wantRefreshToken        string
	wantProviderAccessToken string
	healthUserID            string
	legacyFitbitUserID      string
	refreshErr              error
	providerErr             error
	failRefreshIfCalled     bool
	failProviderIfCalled    bool
}

// fakeRefreshConfig drives bindRefreshOAuthTokenFake. Empty want
// fields skip their checks; an empty refreshToken returns
// wantRefreshToken unrotated.
type fakeRefreshConfig struct {
	wantClientID     string
	wantRefreshToken string
	accessToken      string
	refreshToken     string
	expiresAt        time.Time
	calls            *int
}

// bindRefreshOAuthTokenFake replaces runtime.refreshOAuthToken with the
// standard success-shaped refresh fake: assert the stored refresh token
// (and optionally the OAuth client id), return refreshed token material
// with the canonical raw token object, and count calls when a counter
// is bound — the stanza connected-state auto-refresh tests previously
// pasted.
func bindRefreshOAuthTokenFake(t *testing.T, runtime *runtimeAdapters, config fakeRefreshConfig) {
	t.Helper()

	if config.refreshToken == "" {
		config.refreshToken = config.wantRefreshToken
	}
	runtime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		if config.calls != nil {
			*config.calls++
		}
		if config.wantClientID != "" && client.clientID != config.wantClientID {
			t.Fatalf("oauth client id = %q, want %q", client.clientID, config.wantClientID)
		}
		if config.wantRefreshToken != "" && refreshToken != config.wantRefreshToken {
			t.Fatalf("refresh token = %q, want %q", refreshToken, config.wantRefreshToken)
		}
		return oauthTokenResponse{
			accessToken:  config.accessToken,
			refreshToken: config.refreshToken,
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    config.expiresAt,
			rawTokenMaterialObject: map[string]any{
				"access_token":  config.accessToken,
				"refresh_token": config.refreshToken,
				"token_type":    "Bearer",
				"expires_in":    float64(3600),
			},
		}, nil
	}
}

func newConnectFakeRuntime(t *testing.T, config fakeConnectConfig) runtimeAdapters {
	t.Helper()

	runtime := productionRuntimeAdapters()
	if config.now.IsZero() {
		config.now = time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	}
	if config.accessToken == "" {
		config.accessToken = "access-secret-value"
	}
	if config.refreshToken == "" {
		config.refreshToken = "refresh-secret-value"
	}
	if config.healthUserID == "" {
		config.healthUserID = "111111256096816351"
	}
	runtime.runOAuthFlow = func(client oauthClientConfig, scopes []string, noInput bool) (oauthTokenResponse, error) {
		if config.failIfCalled {
			t.Fatalf("OAuth flow should not be called")
		}
		if client.clientID != "test-client" || client.clientSecret != "test-secret" {
			t.Fatalf("OAuth client = (%q, %q), want test client", client.clientID, client.clientSecret)
		}
		if len(scopes) == 0 {
			t.Fatal("OAuth scopes empty")
		}
		if config.wantNoInput != nil && noInput != *config.wantNoInput {
			t.Fatalf("noInput = %v, want %v", noInput, *config.wantNoInput)
		}
		return oauthTokenResponse{
			accessToken:           config.accessToken,
			refreshToken:          config.refreshToken,
			tokenType:             "Bearer",
			scopes:                scopes,
			expiresAt:             config.now.Add(time.Hour),
			refreshTokenExpiresAt: config.refreshExpiresAt,
			rawTokenMaterialObject: map[string]any{
				"access_token":  config.accessToken,
				"refresh_token": config.refreshToken,
				"expires_in":    float64(3600),
				"scope":         strings.Join(scopes, " "),
				"token_type":    "Bearer",
			},
		}, nil
	}
	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if config.failIfCalled {
			t.Fatalf("identity fetch should not be called")
		}
		if accessToken != config.accessToken {
			t.Fatalf("identity access token = %q, want fake token", accessToken)
		}
		return googleIdentity{
			healthUserID:       config.healthUserID,
			legacyFitbitUserID: config.legacyFitbitUserID,
			rawJSON:            fmt.Sprintf(`{"healthUserId":%q,"legacyUserId":%q}`, config.healthUserID, config.legacyFitbitUserID),
		}, nil
	}
	runtime.now = func() time.Time { return config.now }
	return runtime
}

func newDoctorOnlineFakeRuntime(t *testing.T, config fakeDoctorOnlineConfig) runtimeAdapters {
	t.Helper()

	runtime := productionRuntimeAdapters()
	if config.now.IsZero() {
		config.now = time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	}
	if config.refreshedAccessToken == "" {
		config.refreshedAccessToken = "refreshed-access-secret"
	}
	if config.wantRefreshToken == "" {
		config.wantRefreshToken = "refresh-secret-value"
	}
	if config.wantProviderAccessToken == "" {
		config.wantProviderAccessToken = config.refreshedAccessToken
	}
	if config.healthUserID == "" {
		config.healthUserID = "111111256096816351"
	}
	runtime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		if config.failRefreshIfCalled {
			t.Fatal("token refresh should not be called")
		}
		if refreshToken != config.wantRefreshToken {
			t.Fatalf("refresh token = %q, want configured refresh token", refreshToken)
		}
		if len(fallbackScopes) == 0 {
			t.Fatal("fallback scopes empty")
		}
		if config.refreshErr != nil {
			return oauthTokenResponse{}, config.refreshErr
		}
		return oauthTokenResponse{
			accessToken:  config.refreshedAccessToken,
			refreshToken: refreshToken,
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    config.now.Add(time.Hour),
			rawTokenMaterialObject: map[string]any{
				"access_token":  config.refreshedAccessToken,
				"refresh_token": refreshToken,
				"expires_in":    float64(3600),
				"scope":         strings.Join(fallbackScopes, " "),
				"token_type":    "Bearer",
			},
		}, nil
	}
	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if config.failProviderIfCalled {
			t.Fatal("provider reachability check should not be called")
		}
		if accessToken != config.wantProviderAccessToken {
			t.Fatalf("provider access token = %q, want configured token", accessToken)
		}
		if config.providerErr != nil {
			return googleIdentity{}, config.providerErr
		}
		return googleIdentity{
			healthUserID:       config.healthUserID,
			legacyFitbitUserID: config.legacyFitbitUserID,
			rawJSON:            fmt.Sprintf(`{"healthUserId":%q,"legacyUserId":%q}`, config.healthUserID, config.legacyFitbitUserID),
		}, nil
	}
	runtime.now = func() time.Time { return config.now }
	return runtime
}

func bindIdentityFetchFake(t *testing.T, runtime *runtimeAdapters, wantAccessToken string, identity googleIdentity) {
	t.Helper()

	runtime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("identity access token = %q, want stored token", accessToken)
		}
		return identity, nil
	}
}

func bindProfileFetchFake(t *testing.T, runtime *runtimeAdapters, wantAccessToken string, profile googleProfile, providerErr error) {
	t.Helper()

	runtime.fetchProfile = func(accessToken string) (googleProfile, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("profile access token = %q, want stored token", accessToken)
		}
		if providerErr != nil {
			return googleProfile{}, providerErr
		}
		return profile, nil
	}
}

func bindRawFetchFake(t *testing.T, runtime *runtimeAdapters, wantAccessToken string, response func(googlehealth.RawRequest) []byte) {
	t.Helper()

	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("raw access token = %q, want stored token", accessToken)
		}
		return response(request), nil
	}
}

func bindStepSyncFetchFake(t *testing.T, runtime *runtimeAdapters, wantAccessToken string, pages map[string]string) *[]googlehealth.RawRequest {
	t.Helper()

	bound, requests := withStepSyncFetchFake(t, *runtime, wantAccessToken, pages)
	*runtime = bound
	return requests
}

func withStepSyncFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, pages map[string]string) (runtimeAdapters, *[]googlehealth.RawRequest) {
	t.Helper()

	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("sync access token = %q, want stored token", accessToken)
		}
		if request.EndpointName != "dataTypes.steps.list" || request.DataType != "steps" {
			t.Fatalf("sync request = (%q, %q), want steps list", request.EndpointName, request.DataType)
		}
		requests = append(requests, request)
		pageToken := mustURLQuery(t, request.URL).Get("pageToken")
		body, ok := pages[pageToken]
		if !ok {
			t.Fatalf("no fake page for pageToken %q", pageToken)
		}
		return []byte(body), nil
	}
	return runtime, &requests
}

func bindStepReconcileFetchFake(t *testing.T, runtime *runtimeAdapters, wantAccessToken string, pages map[string]string) *[]googlehealth.RawRequest {
	t.Helper()

	bound, requests := withStepReconcileFetchFake(t, *runtime, wantAccessToken, pages)
	*runtime = bound
	return requests
}

func withStepReconcileFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, pages map[string]string) (runtimeAdapters, *[]googlehealth.RawRequest) {
	t.Helper()

	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("reconcile sync access token = %q, want stored token", accessToken)
		}
		if request.EndpointName != "dataTypes.steps.reconcile" || request.DataType != "steps" {
			t.Fatalf("reconcile sync request = (%q, %q), want steps reconcile", request.EndpointName, request.DataType)
		}
		if request.SourceFamilyFilter != "wearable" {
			t.Fatalf("source family filter = %q, want wearable", request.SourceFamilyFilter)
		}
		parsedURL, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse reconcile URL: %v", err)
		}
		if parsedURL.Path != "/v4/users/me/dataTypes/steps/dataPoints:reconcile" {
			t.Fatalf("reconcile path = %q, want reconcile path", parsedURL.Path)
		}
		requests = append(requests, request)
		pageToken := parsedURL.Query().Get("pageToken")
		body, ok := pages[pageToken]
		if !ok {
			t.Fatalf("no fake reconcile page for pageToken %q", pageToken)
		}
		return []byte(body), nil
	}
	return runtime, &requests
}

func bindDataPointReconcileFetchFake(t *testing.T, runtime *runtimeAdapters, wantAccessToken, dataType string, pages map[string]string) *[]googlehealth.RawRequest {
	t.Helper()

	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("reconcile sync access token = %q, want stored token", accessToken)
		}
		if request.EndpointName != "dataTypes."+dataType+".reconcile" || request.DataType != dataType {
			t.Fatalf("reconcile sync request = (%q, %q), want %s reconcile", request.EndpointName, request.DataType, dataType)
		}
		if request.SourceFamilyFilter != "wearable" {
			t.Fatalf("source family filter = %q, want wearable", request.SourceFamilyFilter)
		}
		parsedURL, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse reconcile URL: %v", err)
		}
		wantPath := "/v4/users/me/dataTypes/" + dataType + "/dataPoints:reconcile"
		if parsedURL.Path != wantPath {
			t.Fatalf("reconcile path = %q, want %q", parsedURL.Path, wantPath)
		}
		requests = append(requests, request)
		pageToken := parsedURL.Query().Get("pageToken")
		body, ok := pages[pageToken]
		if !ok {
			t.Fatalf("no fake reconcile page for pageToken %q", pageToken)
		}
		return []byte(body), nil
	}
	return &requests
}

func bindStepDailyRollupFetchFake(t *testing.T, runtime *runtimeAdapters, wantAccessToken string, pages map[string]string) *[]googlehealth.RawRequest {
	t.Helper()

	bound, requests := withStepDailyRollupFetchFake(t, *runtime, wantAccessToken, pages)
	*runtime = bound
	return requests
}

func withStepDailyRollupFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, pages map[string]string) (runtimeAdapters, *[]googlehealth.RawRequest) {
	t.Helper()

	return withDailyRollupFetchFake(t, runtime, wantAccessToken, "steps", pages)
}

func withHeartRateDailyRollupFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, pages map[string]string) (runtimeAdapters, *[]googlehealth.RawRequest) {
	t.Helper()

	return withDailyRollupFetchFake(t, runtime, wantAccessToken, "heart-rate", pages)
}

func withDailyRollupFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken, dataType string, pages map[string]string) (runtimeAdapters, *[]googlehealth.RawRequest) {
	t.Helper()

	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("rollup sync access token = %q, want stored token", accessToken)
		}
		if request.EndpointName != "dataTypes."+dataType+".dailyRollUp" || request.DataType != dataType {
			t.Fatalf("rollup sync request = (%q, %q), want %s dailyRollUp", request.EndpointName, request.DataType, dataType)
		}
		if request.Method != http.MethodPost {
			t.Fatalf("rollup method = %q, want POST", request.Method)
		}
		parsedURL, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse rollup URL: %v", err)
		}
		wantPath := "/v4/users/me/dataTypes/" + dataType + "/dataPoints:dailyRollUp"
		if parsedURL.Path != wantPath {
			t.Fatalf("rollup path = %q, want dailyRollUp path", parsedURL.Path)
		}
		var body struct {
			Range struct {
				Start struct {
					Date struct {
						Year  int `json:"year"`
						Month int `json:"month"`
						Day   int `json:"day"`
					} `json:"date"`
				} `json:"start"`
				End struct {
					Date struct {
						Year  int `json:"year"`
						Month int `json:"month"`
						Day   int `json:"day"`
					} `json:"date"`
				} `json:"end"`
			} `json:"range"`
			WindowSizeDays int    `json:"windowSizeDays"`
			PageToken      string `json:"pageToken"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatalf("rollup body is not valid JSON: %v\nbody: %s", err, string(request.Body))
		}
		if body.WindowSizeDays != 1 {
			t.Fatalf("windowSizeDays = %d, want 1", body.WindowSizeDays)
		}
		requests = append(requests, request)
		key := fmt.Sprintf("%04d-%02d-%02d/%04d-%02d-%02d/%s",
			body.Range.Start.Date.Year,
			body.Range.Start.Date.Month,
			body.Range.Start.Date.Day,
			body.Range.End.Date.Year,
			body.Range.End.Date.Month,
			body.Range.End.Date.Day,
			body.PageToken,
		)
		response, ok := pages[key]
		if !ok {
			t.Fatalf("no fake rollup page for key %q", key)
		}
		return []byte(response), nil
	}
	return runtime, &requests
}

// withHeartRateHourlyRollupFetchFake routes the runtime's fetchRawProvider
// to per-page-key canned responses for the hourly heart-rate windowed
// rollUp endpoint. The page-key shape is "<startTime>/<endTime>/<windowSize>/<pageToken>"
// where startTime/endTime are taken VERBATIM from the request body, so a
// test that passes civil dates into the gate-normalized executor proves
// the executor actually used the normalized RFC3339 form (the gate emits
// RFC3339 for hourly per PRD #141 slice 3) rather than the raw civil
// option.from.
func withHeartRateHourlyRollupFetchFake(t *testing.T, runtime runtimeAdapters, wantAccessToken string, pages map[string]string) (runtimeAdapters, *[]googlehealth.RawRequest) {
	t.Helper()

	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("rollup sync access token = %q, want stored token", accessToken)
		}
		if request.EndpointName != "dataTypes.heart-rate.rollUp" || request.DataType != "heart-rate" {
			t.Fatalf("rollup sync request = (%q, %q), want heart-rate rollUp", request.EndpointName, request.DataType)
		}
		if request.Method != http.MethodPost {
			t.Fatalf("rollup method = %q, want POST", request.Method)
		}
		parsedURL, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse rollup URL: %v", err)
		}
		if parsedURL.Path != "/v4/users/me/dataTypes/heart-rate/dataPoints:rollUp" {
			t.Fatalf("rollup path = %q, want rollUp path", parsedURL.Path)
		}
		var body struct {
			Range struct {
				StartTime string `json:"startTime"`
				EndTime   string `json:"endTime"`
			} `json:"range"`
			WindowSize string `json:"windowSize"`
			PageToken  string `json:"pageToken"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatalf("rollup body is not valid JSON: %v\nbody: %s", err, string(request.Body))
		}
		requests = append(requests, request)
		key := fmt.Sprintf("%s/%s/%s/%s",
			body.Range.StartTime,
			body.Range.EndTime,
			body.WindowSize,
			body.PageToken,
		)
		response, ok := pages[key]
		if !ok {
			t.Fatalf("no fake rollup page for key %q", key)
		}
		return []byte(response), nil
	}
	return runtime, &requests
}

func bindDataPointSyncFetchFake(t *testing.T, runtime *runtimeAdapters, wantAccessToken, dataType string, pages map[string]string) *[]googlehealth.RawRequest {
	t.Helper()

	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		if accessToken != wantAccessToken {
			t.Fatalf("sync access token = %q, want stored token", accessToken)
		}
		// The exercise sync path now also hits exportExerciseTcx after
		// each Data Point (#107 slice D, ADR-0009). The fixture archives
		// used by existing exercise sync tests do not carry TCX, so the
		// fake responds with 404 — the production code path treats 404
		// as "no TCX for this Data Point" and continues.
		if request.EndpointName == "dataTypes.exercise.exportExerciseTcx" {
			return nil, &googlehealth.HTTPError{StatusCode: 404}
		}
		if request.EndpointName != "dataTypes."+dataType+".list" || request.DataType != dataType {
			t.Fatalf("sync request = (%q, %q), want %s list", request.EndpointName, request.DataType, dataType)
		}
		parsedURL, err := url.Parse(request.URL)
		if err != nil {
			t.Fatalf("parse sync URL: %v", err)
		}
		wantPath := "/v4/users/me/dataTypes/" + dataType + "/dataPoints"
		if parsedURL.Path != wantPath {
			t.Fatalf("sync path = %q, want %q", parsedURL.Path, wantPath)
		}
		requests = append(requests, request)
		pageToken := parsedURL.Query().Get("pageToken")
		body, ok := pages[pageToken]
		if !ok {
			t.Fatalf("no fake page for pageToken %q", pageToken)
		}
		return []byte(body), nil
	}
	return &requests
}

// openArchiveForTest opens the Health Archive and registers its close
// on test cleanup — the named form of the open/fatal/defer-close
// boilerplate. Tests that must close the handle mid-test (to assert
// close ordering or reopen exclusively) keep explicit openArchive calls.
func openArchiveForTest(t *testing.T, archivePath string) *sql.DB {
	t.Helper()

	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func initializeFileCredentialSetup(t *testing.T, tempDir string) (string, string, string) {
	t.Helper()

	configPath := filepath.Join(tempDir, "config", "config.toml")
	archivePath := filepath.Join(tempDir, "data", "gohealthcli.sqlite")
	code, _, stderr := runCommand(t,
		"init",
		"--config", configPath,
		"--db", archivePath,
		"--oauth-client-file", filepath.Join(tempDir, "client_secret.json"),
	)
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	tokenStorePath := filepath.Join(tempDir, "credential-store", "tokens.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := removeCredentialStoreSection(t, string(configBytes)) + "\n[credential_store]\ntype = \"file\"\npath = \"" + tokenStorePath + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath, archivePath, tokenStorePath
}

func readTestFixture(t *testing.T, name string) []byte {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return content
}

func archivedConnectionIdentityJSON(t *testing.T, archivePath string) string {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var identityJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT google_identity_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&identityJSON); err != nil {
		t.Fatalf("query archived identity JSON: %v", err)
	}
	return identityJSON
}

func archivedConnectionTokenMetadata(t *testing.T, archivePath string) string {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var tokenMetadata string
	if err := db.QueryRowContext(context.Background(), `SELECT token_metadata_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&tokenMetadata); err != nil {
		t.Fatalf("query archived token metadata: %v", err)
	}
	return tokenMetadata
}

func setConnectionTokenExpiry(t *testing.T, archivePath, expiresAt string) {
	t.Helper()

	var metadata map[string]any
	if err := json.Unmarshal([]byte(archivedConnectionTokenMetadata(t, archivePath)), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["expires_at"] = expiresAt
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(metadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
}

func setConnectionTokenScopes(t *testing.T, archivePath string, scopes []string) {
	t.Helper()

	var metadata map[string]any
	if err := json.Unmarshal([]byte(archivedConnectionTokenMetadata(t, archivePath)), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["scopes"] = scopes
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(metadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token metadata: %v", err)
	}
}

func assertArchiveTableCount(t *testing.T, archivePath, table string, want int) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var got int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func insertStatusFixtureRows(t *testing.T, archivePath string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	if _, err := db.ExecContext(context.Background(), `INSERT INTO connections (
		id,
		provider_name,
		google_health_user_id,
		legacy_fitbit_user_id,
		token_metadata_json,
		google_identity_json,
		created_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth:111111256096816351",
		"googlehealth",
		"111111256096816351",
		"A1B2C3",
		`{"scopes":["https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly"]}`,
		`{"healthUserId":"111111256096816351","legacyUserId":"A1B2C3"}`,
		"2026-01-01T00:00:00Z",
		"2026-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("insert status fixture connection: %v", err)
	}
	dataPoints := []struct {
		dataType     string
		resourceName string
		recordKind   string
		startUTC     any
		endUTC       any
		rawJSON      string
	}{
		{"steps", "users/me/dataTypes/steps/dataPoints/a", "interval", "2026-01-01T08:00:00Z", "2026-01-01T08:15:00Z", `{"steps":{"count":"512"}}`},
		{"steps", "users/me/dataTypes/steps/dataPoints/b", "interval", "2026-01-02T08:00:00Z", "2026-01-02T08:15:00Z", `{"steps":{"count":"1024"}}`},
		{"heart-rate", "users/me/dataTypes/heart-rate/dataPoints/a", "sample", "2026-01-03T09:00:00Z", nil, `{"heartRate":{"bpm":72}}`},
	}
	for _, point := range dataPoints {
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
			point.dataType,
			point.resourceName,
			point.recordKind,
			point.startUTC,
			point.endUTC,
			"{}",
			point.rawJSON,
			"2026-01-04T00:00:00Z",
			"2026-01-04T00:00:00Z",
		); err != nil {
			t.Fatalf("insert status fixture Data Point %s: %v", point.resourceName, err)
		}
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO rollups (
		provider_name,
		connection_id,
		data_type,
		rollup_kind,
		civil_date,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		"steps",
		"dailyRollUp",
		"2026-01-04",
		`{"steps":{"countSum":"2048"}}`,
		"2026-01-05T00:00:00Z",
		"2026-01-05T00:00:00Z",
	); err != nil {
		t.Fatalf("insert status fixture Rollup: %v", err)
	}
	// Legacy archives (created via createLegacyVxArchive) still carry the
	// pre-#97 table name; the rename ALTER fires only when the migration
	// runs. Pick the right name so this helper works for both fresh-v7
	// and pre-v7 fixtures.
	snapshotTable := "identity_snapshots"
	var legacyName string
	if err := db.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type='table' AND name='profile_snapshots'`).Scan(&legacyName); err == nil {
		snapshotTable = "profile_snapshots"
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO `+snapshotTable+` (
		provider_name,
		connection_id,
		raw_json,
		fetched_at
	) VALUES (?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		`{"name":"users/111111256096816351/profile"}`,
		"2026-01-05T00:00:00Z",
	); err != nil {
		t.Fatalf("insert status fixture Profile Snapshot: %v", err)
	}
	syncRuns := []struct {
		status       string
		rangeJSON    string
		endpoint     string
		sourceFamily any
		seen         int
		newCount     int
		updated      int
		startedAt    string
		finishedAt   string
		errorSummary any
	}{
		{"sync_completed", `{"from":"2026-01-01","to":"2026-01-02T00:00:00Z"}`, "list", nil, 1, 1, 0, "2026-01-02T00:00:00Z", "2026-01-02T00:00:10Z", nil},
		{"sync_completed", `{"from":"2026-01-02","to":"2026-01-03T00:00:00Z"}`, "reconcile", "wearable", 2, 2, 0, "2026-01-03T00:00:00Z", "2026-01-03T00:00:10Z", nil},
		{"sync_failed", `{"from":"2026-01-04","to":"2026-01-05T00:00:00Z"}`, "list", nil, 0, 0, 0, "2026-01-05T00:00:00Z", "2026-01-05T00:00:05Z", "Provider timeout after 30s\nretry later"},
	}
	for _, run := range syncRuns {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO sync_runs (
			provider_name,
			connection_id,
			data_types_requested,
			range_requested_json,
			endpoint_family,
			source_family_filter,
			status,
			seen_count,
			new_count,
			updated_count,
			started_at,
			finished_at,
			error_summary
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"googlehealth",
			"googlehealth:111111256096816351",
			`["steps"]`,
			run.rangeJSON,
			run.endpoint,
			run.sourceFamily,
			run.status,
			run.seen,
			run.newCount,
			run.updated,
			run.startedAt,
			run.finishedAt,
			run.errorSummary,
		); err != nil {
			t.Fatalf("insert status fixture Sync Run: %v", err)
		}
	}
}

func createLegacyV1Archive(t *testing.T, archivePath string) {
	t.Helper()

	createLegacyArchive(t, archivePath, 1)
}

func createLegacyV3Archive(t *testing.T, archivePath string) {
	t.Helper()

	createLegacyArchive(t, archivePath, 3)
}

func createLegacyV4Archive(t *testing.T, archivePath string) {
	t.Helper()

	createLegacyArchive(t, archivePath, 4)
}

func createLegacyArchive(t *testing.T, archivePath string, schemaVersion int) {
	t.Helper()

	if err := ensureOwnerOnlyDir(filepath.Dir(archivePath)); err != nil {
		t.Fatalf("create archive parent: %v", err)
	}
	file, err := os.OpenFile(archivePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create legacy archive file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close legacy archive file: %v", err)
	}
	db := openArchiveForTest(t, archivePath)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin legacy migration: %v", err)
	}
	if err := applySchemaMigrationSteps(context.Background(), tx, 0, schemaVersion, time.Date(2026, 5, 31, 21, 0, 0, 0, time.UTC).Format(time.RFC3339)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply legacy schema migrations through version %d: %v", schemaVersion, err)
	}
	if _, err := tx.ExecContext(context.Background(), fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("set legacy user_version %d: %v", schemaVersion, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit legacy migration: %v", err)
	}
	if usesPOSIXPermissions() {
		if err := os.Chmod(archivePath, 0o600); err != nil {
			t.Fatalf("chmod legacy archive: %v", err)
		}
	}
}

func setArchiveUserVersion(t *testing.T, archivePath string, version int) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	if _, err := db.ExecContext(context.Background(), fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatalf("set archive user_version %d: %v", version, err)
	}
}

// runConnectCommandWithRuntime runs a faked `connect` end-to-end:
// fakes enter through the runtimeAdapters value (built by
// newConnectFakeRuntime) instead of patched package state (#283).
func runConnectCommandWithRuntime(t *testing.T, configPath, archivePath string, runtime runtimeAdapters) int {
	t.Helper()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"connect", "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr, runtime)
	if stdout.String() != "" {
		var got map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	return code
}

func ensureTestOAuthClientFiles(t *testing.T, dir string, args []string) {
	t.Helper()

	for index, arg := range args {
		if arg != "--oauth-client-file" || index+1 >= len(args) {
			continue
		}
		path := args[index+1]
		if path == "" {
			continue
		}
		if dir != "" && !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat OAuth client file: %v", err)
		}
		content := []byte(`{"installed":{"client_id":"test-client","client_secret":"test-secret"}}`)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write OAuth client file: %v", err)
		}
	}
}

func expectedDefaultCredentialStoreKind() string {
	return "os_native"
}

func removeCredentialStoreSection(t *testing.T, config string) string {
	t.Helper()

	start := strings.Index(config, "\n[credential_store]\n")
	if start < 0 {
		t.Fatalf("config missing credential_store section:\n%s", config)
	}
	searchFrom := start + len("\n[credential_store]\n")
	end := strings.Index(config[searchFrom:], "\n[")
	if end < 0 {
		return strings.TrimRight(config[:start], "\n") + "\n"
	}
	end += searchFrom
	return strings.TrimRight(config[:start], "\n") + "\n" + config[end+1:]
}

func assertArchiveUserVersion(t *testing.T, archivePath string, want int) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var got int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&got); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
	}
}

func assertJSONString(t *testing.T, got map[string]any, key, want string) {
	t.Helper()

	value, ok := got[key].(string)
	if !ok {
		t.Fatalf("%s = %T(%v), want string %q", key, got[key], got[key], want)
	}
	if value != want {
		t.Fatalf("%s = %q, want %q", key, value, want)
	}
}

func assertJSONNumber(t *testing.T, got map[string]any, key string, want float64) {
	t.Helper()

	value, ok := got[key].(float64)
	if !ok {
		t.Fatalf("%s = %T(%v), want number %v", key, got[key], got[key], want)
	}
	if value != want {
		t.Fatalf("%s = %v, want %v", key, value, want)
	}
}

func statusDataTypeFromJSON(t *testing.T, got map[string]any, dataType string) map[string]any {
	t.Helper()

	dataTypes, ok := got["data_types"].([]any)
	if !ok {
		t.Fatalf("data_types = %T(%v), want array", got["data_types"], got["data_types"])
	}
	for _, rawItem := range dataTypes {
		item, ok := rawItem.(map[string]any)
		if !ok {
			t.Fatalf("data type item = %T(%v), want object", rawItem, rawItem)
		}
		if item["data_type"] == dataType {
			return item
		}
	}
	t.Fatalf("data_types missing %q: %v", dataType, dataTypes)
	return nil
}

func mustURLQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL %q: %v", rawURL, err)
	}
	return parsed.Query()
}

func assertArchivedStepDataPoint(t *testing.T, archivePath string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var providerName, connectionID, dataType, resourceName, recordKind, startUTC, endUTC, startCivil, endCivil, civilDate, timezoneMetadata, dataSourceJSON, rawJSON string
	var sourceFamilyFilter sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		provider_name,
		connection_id,
		data_type,
		upstream_resource_name,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		source_family_filter,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a").Scan(
		&providerName,
		&connectionID,
		&dataType,
		&resourceName,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&sourceFamilyFilter,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived step Data Point: %v", err)
	}
	if providerName != "googlehealth" || connectionID != "googlehealth:111111256096816351" {
		t.Fatalf("Data Point connection = (%q, %q), want googlehealth connection", providerName, connectionID)
	}
	if dataType != "steps" || resourceName != "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a" || recordKind != "interval" {
		t.Fatalf("Data Point identity = (%q, %q, %q), want steps interval resource", dataType, resourceName, recordKind)
	}
	if startUTC != "2026-01-01T07:00:00Z" || endUTC != "2026-01-01T07:15:00Z" {
		t.Fatalf("physical time = (%q, %q), want UTC interval", startUTC, endUTC)
	}
	if startCivil != "2026-01-01T08:00:00" || endCivil != "2026-01-01T08:15:00" || civilDate != "2026-01-01" {
		t.Fatalf("civil time = (%q, %q, %q), want provider civil time", startCivil, endCivil, civilDate)
	}
	if timezoneMetadata != `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}` {
		t.Fatalf("timezone_metadata = %q, want offsets", timezoneMetadata)
	}
	if dataSourceJSON != `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}` {
		t.Fatalf("data_source_json = %q, want compact Data Source", dataSourceJSON)
	}
	if sourceFamilyFilter.Valid {
		t.Fatalf("source_family_filter = %q, want omitted for unfiltered sync", sourceFamilyFilter.String)
	}
	if !strings.Contains(rawJSON, `"count":"512"`) {
		t.Fatalf("raw_json = %s, want original steps count", rawJSON)
	}

	// The second fixture page row carries only the required interval
	// fields — no civil times, no offsets. Pin the optional columns NULL
	// so the parser cannot start inventing values for absent fields.
	var minimalStartUTC, minimalEndUTC, minimalRecordKind, minimalDataSourceJSON string
	var minimalStartCivil, minimalEndCivil, minimalCivilDate, minimalTimezoneMetadata sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, "users/me/dataTypes/steps/dataPoints/step-2026-01-01-b").Scan(
		&minimalRecordKind,
		&minimalStartUTC,
		&minimalEndUTC,
		&minimalStartCivil,
		&minimalEndCivil,
		&minimalCivilDate,
		&minimalTimezoneMetadata,
		&minimalDataSourceJSON,
	); err != nil {
		t.Fatalf("query minimal archived step Data Point: %v", err)
	}
	if minimalRecordKind != "interval" || minimalStartUTC != "2026-01-01T09:00:00Z" || minimalEndUTC != "2026-01-01T09:05:00Z" {
		t.Fatalf("minimal step Data Point = (%q, %q, %q), want UTC-only interval", minimalRecordKind, minimalStartUTC, minimalEndUTC)
	}
	if minimalStartCivil.Valid || minimalEndCivil.Valid || minimalCivilDate.Valid || minimalTimezoneMetadata.Valid {
		t.Fatalf("minimal step civil/timezone columns = (%v, %v, %v, %v), want all omitted", minimalStartCivil.Valid, minimalEndCivil.Valid, minimalCivilDate.Valid, minimalTimezoneMetadata.Valid)
	}
	if minimalDataSourceJSON != `{"platform":"FITBIT"}` {
		t.Fatalf("minimal step data_source_json = %q, want platform-only Data Source", minimalDataSourceJSON)
	}
}

func assertArchivedIntervalDataPoint(t *testing.T, archivePath, resourceName, wantDataType, wantStartUTC, wantEndUTC, wantStartCivil, wantEndCivil, wantCivilDate, wantTimezoneMetadata, wantDataSourceJSON, wantSourceFamily, wantRawContains string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var providerName, connectionID, dataType, recordKind, startUTC, endUTC, dataSourceJSON, rawJSON string
	var startCivil, endCivil, civilDate, timezoneMetadata, sourceFamilyFilter sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		provider_name,
		connection_id,
		data_type,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		source_family_filter,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, resourceName).Scan(
		&providerName,
		&connectionID,
		&dataType,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&sourceFamilyFilter,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived interval Data Point: %v", err)
	}
	if providerName != "googlehealth" || connectionID != "googlehealth:111111256096816351" {
		t.Fatalf("Data Point connection = (%q, %q), want googlehealth connection", providerName, connectionID)
	}
	if dataType != wantDataType || recordKind != "interval" {
		t.Fatalf("Data Point identity = (%q, %q), want (%q, interval)", dataType, recordKind, wantDataType)
	}
	if sourceFamilyFilter.String != wantSourceFamily || sourceFamilyFilter.Valid != (wantSourceFamily != "") {
		t.Fatalf("source_family_filter = %v(%q), want %q", sourceFamilyFilter.Valid, sourceFamilyFilter.String, wantSourceFamily)
	}
	if startUTC != wantStartUTC || endUTC != wantEndUTC {
		t.Fatalf("physical time = (%q, %q), want (%q, %q)", startUTC, endUTC, wantStartUTC, wantEndUTC)
	}
	if startCivil.String != wantStartCivil || !startCivil.Valid || endCivil.String != wantEndCivil || !endCivil.Valid || civilDate.String != wantCivilDate || !civilDate.Valid {
		t.Fatalf("civil time = (%v(%q), %v(%q), %v(%q)), want start %q end %q date %q", startCivil.Valid, startCivil.String, endCivil.Valid, endCivil.String, civilDate.Valid, civilDate.String, wantStartCivil, wantEndCivil, wantCivilDate)
	}
	if timezoneMetadata.String != wantTimezoneMetadata || !timezoneMetadata.Valid {
		t.Fatalf("timezone_metadata = %v(%q), want %q", timezoneMetadata.Valid, timezoneMetadata.String, wantTimezoneMetadata)
	}
	if dataSourceJSON != wantDataSourceJSON {
		t.Fatalf("data_source_json = %q, want %q", dataSourceJSON, wantDataSourceJSON)
	}
	if !strings.Contains(rawJSON, wantRawContains) {
		t.Fatalf("raw_json = %s, want %s", rawJSON, wantRawContains)
	}
}

func assertArchivedSampleDataPoint(t *testing.T, archivePath, resourceName, wantDataType, wantStartUTC, wantStartCivil, wantCivilDate, wantTimezoneMetadata, wantRawContains string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var dataType, recordKind, startUTC, dataSourceJSON, rawJSON string
	var endUTC, startCivil, endCivil, civilDate, timezoneMetadata sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		data_type,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, resourceName).Scan(
		&dataType,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived sample Data Point: %v", err)
	}
	if dataType != wantDataType || recordKind != "sample" {
		t.Fatalf("Data Point identity = (%q, %q), want (%q, sample)", dataType, recordKind, wantDataType)
	}
	if startUTC != wantStartUTC || endUTC.Valid {
		t.Fatalf("physical time = (%q, %v(%q)), want sample start %q only", startUTC, endUTC.Valid, endUTC.String, wantStartUTC)
	}
	if startCivil.String != wantStartCivil || startCivil.Valid != (wantStartCivil != "") || endCivil.Valid || civilDate.String != wantCivilDate || civilDate.Valid != (wantCivilDate != "") {
		t.Fatalf("civil time = (%v(%q), %v(%q), %v(%q)), want start %q date %q", startCivil.Valid, startCivil.String, endCivil.Valid, endCivil.String, civilDate.Valid, civilDate.String, wantStartCivil, wantCivilDate)
	}
	if timezoneMetadata.String != wantTimezoneMetadata || timezoneMetadata.Valid != (wantTimezoneMetadata != "") {
		t.Fatalf("timezone_metadata = %v(%q), want %q", timezoneMetadata.Valid, timezoneMetadata.String, wantTimezoneMetadata)
	}
	if dataSourceJSON == "" {
		t.Fatal("data_source_json is empty")
	}
	if !strings.Contains(rawJSON, wantRawContains) {
		t.Fatalf("raw_json = %s, want %s", rawJSON, wantRawContains)
	}
}

func assertArchivedDailyDataPoint(t *testing.T, archivePath, resourceName, wantDataType, wantCivilDate, wantRawContains string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var dataType, recordKind, dataSourceJSON, rawJSON string
	var startUTC, endUTC, startCivil, endCivil, civilDate, timezoneMetadata sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		data_type,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, resourceName).Scan(
		&dataType,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived daily Data Point: %v", err)
	}
	if dataType != wantDataType || recordKind != "daily" {
		t.Fatalf("Data Point identity = (%q, %q), want (%q, daily)", dataType, recordKind, wantDataType)
	}
	if startUTC.Valid || endUTC.Valid || startCivil.Valid || endCivil.Valid {
		t.Fatalf("daily physical/civil times = (%v, %v, %v, %v), want only provider civil date", startUTC.Valid, endUTC.Valid, startCivil.Valid, endCivil.Valid)
	}
	if civilDate.String != wantCivilDate || !civilDate.Valid {
		t.Fatalf("provider_civil_date = %v(%q), want %q", civilDate.Valid, civilDate.String, wantCivilDate)
	}
	if timezoneMetadata.Valid {
		t.Fatalf("timezone_metadata = %q, want omitted for date-only daily Data Point", timezoneMetadata.String)
	}
	if dataSourceJSON == "" {
		t.Fatal("data_source_json is empty")
	}
	if !strings.Contains(rawJSON, wantRawContains) {
		t.Fatalf("raw_json = %s, want %s", rawJSON, wantRawContains)
	}
}

func assertArchivedSessionDataPoint(t *testing.T, archivePath, resourceName, wantDataType, wantStartUTC, wantEndUTC, wantStartCivil, wantEndCivil, wantCivilDate, wantTimezoneMetadata, wantDataSourceJSON, wantRawContains string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var providerName, connectionID, dataType, recordKind, startUTC, endUTC, dataSourceJSON, rawJSON string
	var startCivil, endCivil, civilDate, timezoneMetadata, sourceFamilyFilter sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		provider_name,
		connection_id,
		data_type,
		record_kind,
		start_time_utc,
		end_time_utc,
		start_civil_time,
		end_civil_time,
		provider_civil_date,
		timezone_metadata,
		data_source_json,
		source_family_filter,
		raw_json
	FROM data_points
	WHERE upstream_resource_name = ?
	ORDER BY id`, resourceName).Scan(
		&providerName,
		&connectionID,
		&dataType,
		&recordKind,
		&startUTC,
		&endUTC,
		&startCivil,
		&endCivil,
		&civilDate,
		&timezoneMetadata,
		&dataSourceJSON,
		&sourceFamilyFilter,
		&rawJSON,
	); err != nil {
		t.Fatalf("query archived session Data Point: %v", err)
	}
	if providerName != "googlehealth" || connectionID != "googlehealth:111111256096816351" {
		t.Fatalf("Data Point connection = (%q, %q), want googlehealth connection", providerName, connectionID)
	}
	if dataType != wantDataType || recordKind != "session" {
		t.Fatalf("Data Point identity = (%q, %q), want (%q, session)", dataType, recordKind, wantDataType)
	}
	if sourceFamilyFilter.Valid {
		t.Fatalf("source_family_filter = %q, want omitted for session list sync", sourceFamilyFilter.String)
	}
	if startUTC != wantStartUTC || endUTC != wantEndUTC {
		t.Fatalf("physical time = (%q, %q), want (%q, %q)", startUTC, endUTC, wantStartUTC, wantEndUTC)
	}
	if startCivil.String != wantStartCivil || !startCivil.Valid || endCivil.String != wantEndCivil || !endCivil.Valid || civilDate.String != wantCivilDate || !civilDate.Valid {
		t.Fatalf("civil time = (%v(%q), %v(%q), %v(%q)), want start %q end %q date %q", startCivil.Valid, startCivil.String, endCivil.Valid, endCivil.String, civilDate.Valid, civilDate.String, wantStartCivil, wantEndCivil, wantCivilDate)
	}
	if timezoneMetadata.String != wantTimezoneMetadata || !timezoneMetadata.Valid {
		t.Fatalf("timezone_metadata = %v(%q), want %q", timezoneMetadata.Valid, timezoneMetadata.String, wantTimezoneMetadata)
	}
	if dataSourceJSON != wantDataSourceJSON {
		t.Fatalf("data_source_json = %q, want %q", dataSourceJSON, wantDataSourceJSON)
	}
	if !strings.Contains(rawJSON, wantRawContains) {
		t.Fatalf("raw_json = %s, want %s", rawJSON, wantRawContains)
	}
}

func assertSyncRun(t *testing.T, archivePath string, id int64, wantStatus string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	assertSyncRunWithEndpointFamily(t, archivePath, id, wantStatus, "list", wantSeen, wantNew, wantUpdated, wantErrorContains)
}

func assertSyncRunWithEndpointFamily(t *testing.T, archivePath string, id int64, wantStatus, wantEndpointFamily string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, id, wantStatus, wantEndpointFamily, "", wantSeen, wantNew, wantUpdated, wantErrorContains)
}

func assertSyncRunWithEndpointFamilyAndSourceFamily(t *testing.T, archivePath string, id int64, wantStatus, wantEndpointFamily, wantSourceFamily string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, id, wantStatus, "steps", wantEndpointFamily, wantSourceFamily, wantSeen, wantNew, wantUpdated, wantErrorContains)
}

func assertSyncRunForDataType(t *testing.T, archivePath string, id int64, wantStatus, wantDataType, wantEndpointFamily string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, id, wantStatus, wantDataType, wantEndpointFamily, "", wantSeen, wantNew, wantUpdated, wantErrorContains)
}

func assertSyncRunForDataTypeWithSourceFamily(t *testing.T, archivePath string, id int64, wantStatus, wantDataType, wantEndpointFamily, wantSourceFamily string, wantSeen, wantNew, wantUpdated int, wantErrorContains string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var status, dataTypesJSON, rangeJSON, endpointFamily string
	var sourceFamily sql.NullString
	var seen, newCount, updated int
	var errorSummary sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		status,
		data_types_requested,
		range_requested_json,
		endpoint_family,
		source_family_filter,
		seen_count,
		new_count,
		updated_count,
		error_summary
	FROM sync_runs WHERE id = ?`, id).Scan(
		&status,
		&dataTypesJSON,
		&rangeJSON,
		&endpointFamily,
		&sourceFamily,
		&seen,
		&newCount,
		&updated,
		&errorSummary,
	); err != nil {
		t.Fatalf("query Sync Run %d: %v", id, err)
	}
	if status != wantStatus || endpointFamily != wantEndpointFamily {
		t.Fatalf("Sync Run status/family = (%q, %q), want (%q, %s)", status, endpointFamily, wantStatus, wantEndpointFamily)
	}
	if sourceFamily.String != wantSourceFamily || sourceFamily.Valid != (wantSourceFamily != "") {
		t.Fatalf("source_family_filter = %v(%q), want %q", sourceFamily.Valid, sourceFamily.String, wantSourceFamily)
	}
	wantDataTypesJSON := fmt.Sprintf(`["%s"]`, wantDataType)
	if dataTypesJSON != wantDataTypesJSON {
		t.Fatalf("data_types_requested = %q, want %s", dataTypesJSON, wantDataTypesJSON)
	}
	if !strings.Contains(rangeJSON, `"from":"2026-01-01`) {
		t.Fatalf("range_requested_json = %q, want from", rangeJSON)
	}
	if seen != wantSeen || newCount != wantNew || updated != wantUpdated {
		t.Fatalf("Sync Run counts = (%d, %d, %d), want (%d, %d, %d)", seen, newCount, updated, wantSeen, wantNew, wantUpdated)
	}
	if wantErrorContains == "" {
		if errorSummary.Valid {
			t.Fatalf("error_summary = %q, want NULL", errorSummary.String)
		}
		return
	}
	if !errorSummary.Valid || !strings.Contains(errorSummary.String, wantErrorContains) {
		t.Fatalf("error_summary = %v(%q), want %q", errorSummary.Valid, errorSummary.String, wantErrorContains)
	}
}

func assertDataPointSourceFamilyCounts(t *testing.T, archivePath string, want map[string]int) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	rows, err := db.QueryContext(context.Background(), `SELECT IFNULL(source_family_filter, ''), count(*) FROM data_points GROUP BY IFNULL(source_family_filter, '')`)
	if err != nil {
		t.Fatalf("query Data Point source families: %v", err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var sourceFamily string
		var count int
		if err := rows.Scan(&sourceFamily, &count); err != nil {
			t.Fatalf("scan Data Point source family count: %v", err)
		}
		got[sourceFamily] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Data Point source family rows: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Data Point source family counts = %v, want %v", got, want)
	}
}

func assertArchivedStepsDailyRollup(t *testing.T, archivePath, wantCount string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var providerName, connectionID, dataType, rollupKind, civilDate, rawJSON string
	var windowStart, windowEnd, timezoneMetadata sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		provider_name,
		connection_id,
		data_type,
		rollup_kind,
		window_start_utc,
		window_end_utc,
		civil_date,
		timezone_metadata,
		raw_json
	FROM rollups`).Scan(
		&providerName,
		&connectionID,
		&dataType,
		&rollupKind,
		&windowStart,
		&windowEnd,
		&civilDate,
		&timezoneMetadata,
		&rawJSON,
	); err != nil {
		t.Fatalf("query Rollup: %v", err)
	}
	if providerName != "googlehealth" || connectionID != "googlehealth:111111256096816351" || dataType != "steps" || rollupKind != "dailyRollUp" {
		t.Fatalf("Rollup identity = (%q, %q, %q, %q), want googlehealth steps dailyRollUp", providerName, connectionID, dataType, rollupKind)
	}
	if windowStart.Valid || windowEnd.Valid {
		t.Fatalf("Rollup UTC window = (%v, %v), want NULL for civil daily Rollup", windowStart, windowEnd)
	}
	if civilDate != "2026-01-01" {
		t.Fatalf("civil_date = %q, want 2026-01-01", civilDate)
	}
	if !timezoneMetadata.Valid || !strings.Contains(timezoneMetadata.String, "civil_start_time") || !strings.Contains(timezoneMetadata.String, "civil_end_time") {
		t.Fatalf("timezone_metadata = %v(%q), want provider civil time metadata", timezoneMetadata.Valid, timezoneMetadata.String)
	}
	if !strings.Contains(rawJSON, `"countSum":"`+wantCount+`"`) {
		t.Fatalf("raw_json = %s, want countSum %s", rawJSON, wantCount)
	}
}

func assertArchivedHeartRateDailyRollup(t *testing.T, archivePath string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var providerName, connectionID, dataType, rollupKind, civilDate, rawJSON string
	var windowStart, windowEnd, timezoneMetadata sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT
		provider_name,
		connection_id,
		data_type,
		rollup_kind,
		window_start_utc,
		window_end_utc,
		civil_date,
		timezone_metadata,
		raw_json
	FROM rollups`).Scan(
		&providerName,
		&connectionID,
		&dataType,
		&rollupKind,
		&windowStart,
		&windowEnd,
		&civilDate,
		&timezoneMetadata,
		&rawJSON,
	); err != nil {
		t.Fatalf("query Rollup: %v", err)
	}
	if providerName != "googlehealth" || connectionID != "googlehealth:111111256096816351" || dataType != "heart-rate" || rollupKind != "dailyRollUp" {
		t.Fatalf("Rollup identity = (%q, %q, %q, %q), want googlehealth heart-rate dailyRollUp", providerName, connectionID, dataType, rollupKind)
	}
	if windowStart.Valid || windowEnd.Valid {
		t.Fatalf("Rollup UTC window = (%v, %v), want NULL for civil daily Rollup", windowStart, windowEnd)
	}
	if civilDate != "2026-01-01" {
		t.Fatalf("civil_date = %q, want 2026-01-01", civilDate)
	}
	if !timezoneMetadata.Valid || !strings.Contains(timezoneMetadata.String, "civil_start_time") || !strings.Contains(timezoneMetadata.String, "civil_end_time") {
		t.Fatalf("timezone_metadata = %v(%q), want provider civil time metadata", timezoneMetadata.Valid, timezoneMetadata.String)
	}
	for _, want := range []string{`"bpmAvg":68.5`, `"bpmMin":49`, `"bpmMax":122`} {
		if !strings.Contains(rawJSON, want) {
			t.Fatalf("raw_json = %s, want %s", rawJSON, want)
		}
	}
}

func assertCorrectedStepRevision(t *testing.T, archivePath string) {
	t.Helper()

	db := openArchiveForTest(t, archivePath)
	var rawJSON, startUTC, startCivil string
	if err := db.QueryRowContext(context.Background(), `SELECT raw_json, start_time_utc, start_civil_time FROM data_points WHERE upstream_resource_name = ?`, "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a").Scan(&rawJSON, &startUTC, &startCivil); err != nil {
		t.Fatalf("query corrected Data Point: %v", err)
	}
	if !strings.Contains(rawJSON, `"count":"999"`) {
		t.Fatalf("canonical raw_json = %s, want corrected count", rawJSON)
	}
	if startUTC != "2026-01-01T07:01:00Z" || startCivil != "2026-01-01T08:01:00" {
		t.Fatalf("corrected time = (%q, %q), want updated metadata", startUTC, startCivil)
	}
	var previousRawJSON, reason string
	if err := db.QueryRowContext(context.Background(), `SELECT previous_raw_json, replacement_reason FROM data_point_revisions`).Scan(&previousRawJSON, &reason); err != nil {
		t.Fatalf("query Data Point Revision: %v", err)
	}
	if !strings.Contains(previousRawJSON, `"count":"512"`) || reason != "provider_correction" {
		t.Fatalf("revision = (%s, %q), want previous count and reason", previousRawJSON, reason)
	}
}

func assertNoSecretWords(t *testing.T, text string) {
	t.Helper()
	for _, word := range []string{"access_token", "refresh_token", "client_secret", "id_token", "accessToken", "refreshToken", "clientSecret", "idToken"} {
		if strings.Contains(text, word) {
			t.Fatalf("output leaked %s: %s", word, text)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if !usesPOSIXPermissions() {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %v, want %v", path, got, want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"
)

const headlessAuthorizationLifetime = 10 * time.Minute
const maxHeadlessRedirectBytes = 16 << 10

type pendingHeadlessAuthorization struct {
	Version                    int      `json:"version"`
	State                      string   `json:"state"`
	CodeVerifier               string   `json:"code_verifier"`
	RedirectURI                string   `json:"redirect_uri"`
	RequestedScopes            []string `json:"requested_scopes"`
	CreatedAt                  string   `json:"created_at"`
	ExpiresAt                  string   `json:"expires_at"`
	Binding                    string   `json:"binding"`
	ExpectedGoogleHealthUserID string   `json:"expected_google_health_user_id,omitempty"`
}

func startHeadlessConnection(ctx context.Context, configPath, archivePath string, extraScopes []string, runtime runtimeAdapters) (connectResult, error) {
	runtime = runtime.withDefaults()
	prepared, err := prepareConnection(ctx, configPath, archivePath, extraScopes, runtime)
	result := connectResult{CredentialStore: prepared.credentialStoreKind}
	if err != nil {
		return result, err
	}
	configPath = prepared.configIdentityPath
	archivePath = prepared.archiveIdentityPath
	listener, redirectURI, err := listenForOAuthRedirect(prepared.oauthClient.redirectURIs)
	if err != nil {
		return result, err
	}
	if err := listener.Close(); err != nil {
		return result, err
	}
	state, err := randomURLToken(32)
	if err != nil {
		return result, err
	}
	verifier, err := randomURLToken(64)
	if err != nil {
		return result, err
	}
	authorizationURL, err := buildOAuthAuthURL(prepared.oauthClient, redirectURI, prepared.requestedScopes, state, pkceChallenge(verifier))
	if err != nil {
		return result, err
	}
	now := runtime.now().UTC().Truncate(time.Second)
	expiresAt := now.Add(headlessAuthorizationLifetime)
	expectedGoogleHealthUserID, err := currentExpectedGoogleHealthUserID(prepared.archivePath)
	if err != nil {
		return result, err
	}
	pending := pendingHeadlessAuthorization{
		Version:                    1,
		State:                      state,
		CodeVerifier:               verifier,
		RedirectURI:                redirectURI,
		RequestedScopes:            append([]string(nil), prepared.requestedScopes...),
		CreatedAt:                  now.Format(time.RFC3339),
		ExpiresAt:                  expiresAt.Format(time.RFC3339),
		ExpectedGoogleHealthUserID: expectedGoogleHealthUserID,
	}
	pending.Binding = headlessConnectionBinding(configPath, archivePath, prepared.oauthClient, pending)
	pendingMap, err := headlessAuthorizationMap(pending)
	if err != nil {
		return result, err
	}
	lock, err := lockHeadlessClaimFile(headlessClaimLockPath(archivePath))
	if err != nil {
		return result, fmt.Errorf("headless authorization could not be stored; run `gohealthcli connect --headless-start` again")
	}
	defer func() {
		_ = unlockHeadlessClaimFile(lock)
	}()
	if err := prepared.credentialStore.Store(headlessPendingCredentialKey(configPath, archivePath), pendingMap); err != nil {
		return result, err
	}
	return connectResult{
		Status:           "authorization_pending",
		AuthorizationURL: authorizationURL,
		ExpiresAt:        expiresAt.Format(time.RFC3339),
		CredentialStore:  prepared.credentialStoreKind,
		Message:          "Open the authorization URL, then pass the full redirected URL to `gohealthcli connect --complete` on stdin before it expires",
	}, nil
}

func completeHeadlessConnection(ctx context.Context, configPath, archivePath string, extraScopes []string, runtime runtimeAdapters) (connectResult, error) {
	runtime = runtime.withDefaults()
	prepared, err := prepareConnection(ctx, configPath, archivePath, extraScopes, runtime)
	result := connectResult{CredentialStore: prepared.credentialStoreKind}
	if err != nil {
		return result, err
	}
	configPath = prepared.configIdentityPath
	archivePath = prepared.archiveIdentityPath
	redirectedURL, err := readHeadlessRedirect(runtime.stdin)
	if err != nil {
		return result, err
	}
	key := headlessPendingCredentialKey(configPath, archivePath)
	pendingMap, err := prepared.credentialStore.Load(key)
	if err != nil {
		return result, fmt.Errorf("headless authorization is missing or already used; run `gohealthcli connect --headless-start` again")
	}
	pending, err := parsePendingHeadlessAuthorization(pendingMap)
	if err != nil {
		return result, err
	}
	_, _, err = validateHeadlessRedirect(redirectedURL, pending, configPath, archivePath, prepared, runtime.now())
	if err != nil {
		return result, err
	}
	pending, code, canceled, err := claimPendingHeadlessAuthorization(redirectedURL, configPath, archivePath, key, prepared, runtime.now())
	if err != nil {
		return result, err
	}
	if canceled {
		return result, fmt.Errorf("OAuth authorization was canceled")
	}
	token, err := exchangeOAuthCodeWithRuntime(prepared.oauthClient, pending.RedirectURI, code, pending.CodeVerifier, pending.RequestedScopes, runtime)
	if err != nil {
		return result, err
	}
	if !scopeSetContainsAll(token.scopes, pending.RequestedScopes) {
		return result, fmt.Errorf("requested Google Health scopes were not granted; run `gohealthcli connect --headless-start` again and grant every requested scope")
	}
	return finalizeConnection(ctx, prepared, token, runtime)
}

func scopeSetContainsAll(granted, required []string) bool {
	grantedSet := make(map[string]struct{}, len(granted))
	for _, scope := range granted {
		grantedSet[scope] = struct{}{}
	}
	for _, scope := range required {
		if _, ok := grantedSet[scope]; !ok {
			return false
		}
	}
	return true
}

func claimPendingHeadlessAuthorization(rawURL, configPath, archivePath, key string, prepared preparedConnection, now time.Time) (pendingHeadlessAuthorization, string, bool, error) {
	lockPath := headlessClaimLockPath(archivePath)
	lock, err := lockHeadlessClaimFile(lockPath)
	if err != nil {
		return pendingHeadlessAuthorization{}, "", false, fmt.Errorf("headless authorization could not be claimed; run `gohealthcli connect --headless-start` again")
	}
	defer func() {
		_ = unlockHeadlessClaimFile(lock)
	}()

	pendingMap, err := prepared.credentialStore.Load(key)
	if err != nil {
		return pendingHeadlessAuthorization{}, "", false, fmt.Errorf("headless authorization is missing or already used; run `gohealthcli connect --headless-start` again")
	}
	pending, err := parsePendingHeadlessAuthorization(pendingMap)
	if err != nil {
		return pendingHeadlessAuthorization{}, "", false, err
	}
	code, canceled, err := validateHeadlessRedirect(rawURL, pending, configPath, archivePath, prepared, now)
	if err != nil {
		return pendingHeadlessAuthorization{}, "", false, err
	}
	if err := prepared.credentialStore.Delete(key); err != nil {
		return pendingHeadlessAuthorization{}, "", false, fmt.Errorf("headless authorization could not be claimed; run `gohealthcli connect --headless-start` again")
	}
	return pending, code, canceled, nil
}

func headlessClaimLockPath(archivePath string) string {
	return archivePath + ".headless-claim.lock"
}

func readHeadlessRedirect(reader io.Reader) (string, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maxHeadlessRedirectBytes+1))
	if err != nil {
		return "", fmt.Errorf("read redirected URL from stdin")
	}
	if len(content) > maxHeadlessRedirectBytes {
		return "", fmt.Errorf("redirected URL from stdin is too large")
	}
	input := strings.TrimSuffix(string(content), "\n")
	input = strings.TrimSuffix(input, "\r")
	if input == "" {
		return "", fmt.Errorf("connect --complete requires one full redirected URL on stdin")
	}
	if strings.ContainsAny(input, "\r\n") {
		return "", fmt.Errorf("connect --complete accepts exactly one redirected URL on stdin")
	}
	return input, nil
}

func validateHeadlessRedirect(rawURL string, pending pendingHeadlessAuthorization, configPath, archivePath string, prepared preparedConnection, now time.Time) (string, bool, error) {
	redirected, err := url.Parse(rawURL)
	if err != nil || !redirected.IsAbs() || redirected.User != nil || redirected.Fragment != "" {
		return "", false, fmt.Errorf("redirected URL is invalid")
	}
	want, err := url.Parse(pending.RedirectURI)
	if err != nil || redirected.Scheme != want.Scheme || redirected.Host != want.Host || redirected.EscapedPath() != want.EscapedPath() {
		return "", false, fmt.Errorf("redirected URL does not match the pending loopback redirect")
	}
	currentExpectedIdentity, err := currentExpectedGoogleHealthUserID(prepared.archivePath)
	if err != nil {
		return "", false, fmt.Errorf("read current Google Identity expectation")
	}
	if currentExpectedIdentity != pending.ExpectedGoogleHealthUserID {
		return "", false, fmt.Errorf("pending authorization identity expectation no longer matches the Health Archive")
	}
	if !slices.Equal(pending.RequestedScopes, prepared.requestedScopes) {
		return "", false, fmt.Errorf("pending authorization does not match the current config, archive, OAuth client, or scopes")
	}
	if pending.Binding != headlessConnectionBinding(configPath, archivePath, prepared.oauthClient, pending) {
		return "", false, fmt.Errorf("pending authorization does not match the current config, archive, OAuth client, or scopes")
	}
	expiresAt, err := time.Parse(time.RFC3339, pending.ExpiresAt)
	if err != nil || !now.UTC().Before(expiresAt) {
		return "", false, fmt.Errorf("pending authorization expired; run `gohealthcli connect --headless-start` again")
	}
	query := redirected.Query()
	states := query["state"]
	if len(states) != 1 || subtle.ConstantTimeCompare([]byte(states[0]), []byte(pending.State)) != 1 {
		return "", false, fmt.Errorf("OAuth state mismatch")
	}
	codes := query["code"]
	oauthErrors := query["error"]
	if len(oauthErrors) == 1 && oauthErrors[0] != "" && len(codes) == 0 {
		return "", true, nil
	}
	if len(codes) != 1 || codes[0] == "" || len(oauthErrors) != 0 {
		return "", false, fmt.Errorf("redirected URL must contain exactly one authorization code")
	}
	return codes[0], false, nil
}

func headlessAuthorizationMap(pending pendingHeadlessAuthorization) (map[string]any, error) {
	content, err := json.Marshal(pending)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(content, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func parsePendingHeadlessAuthorization(value map[string]any) (pendingHeadlessAuthorization, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return pendingHeadlessAuthorization{}, invalidPendingHeadlessAuthorizationError()
	}
	var pending pendingHeadlessAuthorization
	if err := json.Unmarshal(content, &pending); err != nil || pending.Version != 1 || pending.State == "" || pending.CodeVerifier == "" || pending.RedirectURI == "" || len(pending.RequestedScopes) == 0 || pending.ExpiresAt == "" || pending.Binding == "" {
		return pendingHeadlessAuthorization{}, invalidPendingHeadlessAuthorizationError()
	}
	return pending, nil
}

func invalidPendingHeadlessAuthorizationError() error {
	return fmt.Errorf("pending authorization in Credential Store is invalid; run `gohealthcli connect --headless-start` again")
}

func headlessPendingCredentialKey(configPath, archivePath string) string {
	sum := sha256.Sum256([]byte(configPath + "\x00" + archivePath))
	return "headless-connect:" + hex.EncodeToString(sum[:])
}

func headlessConnectionBinding(configPath, archivePath string, client oauthClientConfig, pending pendingHeadlessAuthorization) string {
	material := strings.Join([]string{
		configPath,
		archivePath,
		client.kind,
		client.clientID,
		client.clientSecret,
		client.authURI,
		client.tokenURI,
		strings.Join(client.redirectURIs, "\x00"),
		fmt.Sprintf("%d", pending.Version),
		pending.State,
		pending.CodeVerifier,
		pending.RedirectURI,
		strings.Join(pending.RequestedScopes, "\x00"),
		pending.CreatedAt,
		pending.ExpiresAt,
		pending.ExpectedGoogleHealthUserID,
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func currentExpectedGoogleHealthUserID(archivePath string) (string, error) {
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	connection, err := archive.CurrentConnection()
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return connection.GoogleHealthUserID, nil
}

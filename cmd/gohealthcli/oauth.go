package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type oauthClientConfig struct {
	kind         string
	clientID     string
	clientSecret string
	authURI      string
	tokenURI     string
	redirectURIs []string
}

type oauthTokenResponse struct {
	accessToken            string
	refreshToken           string
	tokenType              string
	scopes                 []string
	expiresAt              time.Time
	refreshTokenExpiresAt  *time.Time
	rawTokenMaterialObject map[string]any
}

func validateOAuthClientConfig(source oauthClientSource) error {
	switch source.kind {
	case "file":
		if source.path == "" {
			return errors.New("missing OAuth client file path")
		}
		if err := validateOAuthClientFile(source.path); err != nil {
			return err
		}
	case "secret_provider":
		if source.provider == "" || source.item == "" {
			return errors.New("missing Secret Provider reference")
		}
	case "":
		return errors.New("missing OAuth client source")
	default:
		return errors.New("unsupported OAuth client source")
	}
	return nil
}

// readOwnerOnlyOAuthClientFile enforces the owner-only invariant on the OAuth
// client file before reading it: it rejects a path that is missing, a
// directory, or any other non-regular file (FIFO, socket, device), and on
// POSIX platforms a regular file with a mode other than 0600. Sharing this
// between validateOAuthClientFile and loadOAuthClientConfig keeps the
// connect/init/doctor validation path and the sync auto-refresh path from
// drifting on what counts as an acceptable client file.
func readOwnerOnlyOAuthClientFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("OAuth client file is missing")
		}
		return nil, errors.New("OAuth client file cannot be checked")
	}
	if info.IsDir() {
		return nil, errors.New("OAuth client file path is a directory")
	}
	// Reject FIFOs, sockets, and devices before os.ReadFile, which could hang
	// or behave unexpectedly on them; only a regular file is a valid client.
	if !info.Mode().IsRegular() {
		return nil, errors.New("OAuth client file is not a regular file")
	}
	if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("OAuth client file is not owner-only: mode %04o, want 0600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("OAuth client file cannot be read")
	}
	return content, nil
}

func validateOAuthClientFile(path string) error {
	content, err := readOwnerOnlyOAuthClientFile(path)
	if err != nil {
		return err
	}
	if _, err := parseOAuthClientConfigContent(content); err != nil {
		return err
	}
	return nil
}

func loadOAuthClientConfig(path string) (oauthClientConfig, error) {
	content, err := readOwnerOnlyOAuthClientFile(path)
	if err != nil {
		return oauthClientConfig{}, err
	}
	return parseOAuthClientConfigContent(content)
}

func parseOAuthClientConfigContent(content []byte) (oauthClientConfig, error) {
	var raw map[string]json.RawMessage
	// A JSON "null" unmarshals into a nil map without error, so it is
	// rejected here together with non-object input.
	if err := json.Unmarshal(content, &raw); err != nil || raw == nil {
		return oauthClientConfig{}, errors.New("OAuth client file must contain a JSON object")
	}
	var client struct {
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		AuthURI      string   `json:"auth_uri"`
		TokenURI     string   `json:"token_uri"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	clientKind := ""
	for _, key := range []string{"installed", "web"} {
		if nested, ok := raw[key]; ok {
			if err := json.Unmarshal(nested, &client); err != nil {
				return oauthClientConfig{}, errors.New("OAuth client file has malformed client details")
			}
			clientKind = key
			break
		}
	}
	if clientKind == "" {
		return oauthClientConfig{}, errors.New(`OAuth client file is missing the "installed" object (Google Desktop client JSON shape: {"installed": {"client_id": "...", ...}} with the client ID and client secret)`)
	}
	if clientKind == "web" {
		return oauthClientConfig{}, errors.New("OAuth client file must be an installed desktop client, not a web client")
	}
	if client.ClientID == "" || client.ClientSecret == "" {
		return oauthClientConfig{}, errors.New("OAuth client file is missing client_id or client_secret")
	}
	if client.AuthURI == "" {
		client.AuthURI = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	if client.TokenURI == "" {
		client.TokenURI = "https://oauth2.googleapis.com/token"
	}
	// Pin auth_uri/token_uri to https and a Google OAuth host so the
	// client secret, authorization code, and refresh token can never be
	// POSTed (or the browser opened) on an attacker-named or cleartext
	// endpoint, even when the OAuth client file is attacker-influenced
	// (see docs/security.md). This fails closed on both the connect
	// exchange path and every auto-refresh path.
	if err := requireGoogleOAuthHTTPS(client.AuthURI, "accounts.google.com"); err != nil {
		return oauthClientConfig{}, err
	}
	if err := requireGoogleOAuthHTTPS(client.TokenURI, "oauth2.googleapis.com"); err != nil {
		return oauthClientConfig{}, err
	}
	return oauthClientConfig{
		kind:         clientKind,
		clientID:     client.ClientID,
		clientSecret: client.ClientSecret,
		authURI:      client.AuthURI,
		tokenURI:     client.TokenURI,
		redirectURIs: client.RedirectURIs,
	}, nil
}

// requireGoogleOAuthHTTPS enforces that an OAuth endpoint URI uses the
// https scheme and a Google OAuth host, mirroring the http+loopback
// enforcement in listenForOAuthRedirect for the credential-bearing
// auth_uri/token_uri endpoints.
func requireGoogleOAuthHTTPS(rawURI, host string) error {
	parsed, err := url.Parse(rawURI)
	// URL schemes and DNS hostnames are case-insensitive, so compare them
	// case-insensitively while still pinning to the exact Google host — a
	// valid config like "HTTPS://Accounts.Google.Com/..." must be accepted.
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), host) {
		return errors.New("OAuth client auth_uri/token_uri must use https and a Google OAuth host")
	}
	return nil
}

func oauthScopesForDataTypes(dataTypes []string) []string {
	needed := make(map[string]struct{})
	needed[googlehealth.ScopeProfileReadonly] = struct{}{}
	for _, dataType := range dataTypes {
		for _, scope := range googlehealth.ScopesForDataType(dataType) {
			needed[scope] = struct{}{}
		}
	}
	if len(needed) == 0 {
		needed[googlehealth.ScopeActivityReadonly] = struct{}{}
	}
	ordered := []string{
		googlehealth.ScopeActivityReadonly,
		googlehealth.ScopeHealthMetricsReadonly,
		googlehealth.ScopeSleepReadonly,
		googlehealth.ScopeNutritionReadonly,
		googlehealth.ScopeProfileReadonly,
	}
	scopes := make([]string, 0, len(needed))
	for _, scope := range ordered {
		if _, ok := needed[scope]; ok {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

func runBrowserOAuthFlow(client oauthClientConfig, scopes []string, noInput bool) (oauthTokenResponse, error) {
	return runBrowserOAuthFlowWithRuntime(client, scopes, noInput, runtimeAdapters{openBrowser: openBrowser})
}

func runBrowserOAuthFlowWithRuntime(client oauthClientConfig, scopes []string, noInput bool, runtime runtimeAdapters) (oauthTokenResponse, error) {
	if runtime.openBrowser == nil {
		runtime.openBrowser = openBrowser
	}
	if runtime.now == nil {
		runtime.now = productionNow
	}
	if noInput {
		return oauthTokenResponse{}, errors.New("connect requires browser OAuth; rerun without --no-input")
	}
	listener, redirectURI, err := listenForOAuthRedirect(client.redirectURIs)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	defer listener.Close()

	state, err := randomURLToken(32)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	verifier, err := randomURLToken(64)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	challenge := pkceChallenge(verifier)
	authURL, err := buildOAuthAuthURL(client, redirectURI, scopes, state, challenge)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	// context.Background(): the browser-OAuth flow is interactive and
	// blocks on the user's redirect with no cancellation path today (its
	// token POST rides context.Background() the same way, #284); the
	// context keeps the subprocess spawn on the Context API (#305).
	if err := runtime.openBrowser(context.Background(), authURL); err != nil {
		return oauthTokenResponse{}, fmt.Errorf("open browser: %w", err)
	}
	code, err := waitForOAuthCode(listener, state)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	return exchangeOAuthCodeWithRuntime(client, redirectURI, code, verifier, runtime)
}

func listenForOAuthRedirect(redirectURIs []string) (net.Listener, string, error) {
	redirectPath := "/oauth2callback"
	for _, candidate := range redirectURIs {
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Scheme != "http" {
			continue
		}
		host := parsed.Hostname()
		if host != "127.0.0.1" && host != "localhost" {
			continue
		}
		redirectPath = parsed.EscapedPath()
		break
	}
	// Background-scoped (#284): the loopback redirect listener for the
	// interactive OAuth flow has no cancellation instrumentation — the
	// flow is bounded by the user closing the browser or the process
	// exiting, not by a context.
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return listener, fmt.Sprintf("http://127.0.0.1:%d%s", port, redirectPath), nil
}

func buildOAuthAuthURL(client oauthClientConfig, redirectURI string, scopes []string, state, challenge string) (string, error) {
	authURL, err := url.Parse(client.authURI)
	if err != nil {
		return "", errors.New("OAuth auth_uri is invalid")
	}
	query := authURL.Query()
	query.Set("client_id", client.clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", strings.Join(scopes, " "))
	query.Set("access_type", "offline")
	// include_granted_scopes=true tells Google to grant the union of the
	// requested scopes and any scopes the user has previously consented
	// to under this client, so `connect --add-scopes irn` extends an
	// existing grant rather than replacing it.
	query.Set("include_granted_scopes", "true")
	query.Set("prompt", "consent")
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	authURL.RawQuery = query.Encode()
	return authURL.String(), nil
}

func waitForOAuthCode(listener net.Listener, wantState string) (string, error) {
	result := make(chan struct {
		code string
		err  error
	}, 1)
	server := &http.Server{}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("state") != wantState {
			http.Error(w, "invalid OAuth state", http.StatusBadRequest)
			result <- struct {
				code string
				err  error
			}{err: errors.New("OAuth state mismatch")}
			return
		}
		if errText := query.Get("error"); errText != "" {
			http.Error(w, "OAuth failed", http.StatusBadRequest)
			result <- struct {
				code string
				err  error
			}{err: fmt.Errorf("OAuth failed: %s", errText)}
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(w, "missing OAuth code", http.StatusBadRequest)
			result <- struct {
				code string
				err  error
			}{err: errors.New("OAuth redirect missing code")}
			return
		}
		fmt.Fprintln(w, "gohealthcli connected. You can close this tab.")
		result <- struct {
			code string
			err  error
		}{code: code}
	})
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			result <- struct {
				code string
				err  error
			}{err: err}
		}
	}()
	outcome := <-result
	_ = server.Close()
	return outcome.code, outcome.err
}

// postOAuthForm posts url-encoded OAuth form values through the
// runtime's HTTP doer, mirroring (*http.Client).PostForm. A nil doer
// (callers that only set now, mirroring the nil-now fallback above)
// falls back to the shared timeout client — never the process-wide
// default client, which carries no deadline (#281). The request is
// Background-scoped (#284): OAuth token exchange/refresh runs from the
// interactive connect flow and the mid-run refresh hook, neither of
// which carries SIGINT instrumentation, and the doer's timeout still
// bounds the call. Threading the Sync Run context through the refresh
// hook would change its func() shape and is out of #284's
// in-place-conversion scope.
func postOAuthForm(runtime runtimeAdapters, tokenURI string, values url.Values) (*http.Response, error) {
	doer := runtime.httpDoer
	if doer == nil {
		doer = googlehealth.HTTPClient
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, tokenURI, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doer.Do(request)
}

func exchangeOAuthCodeWithRuntime(client oauthClientConfig, redirectURI, code, verifier string, runtime runtimeAdapters) (oauthTokenResponse, error) {
	if runtime.now == nil {
		runtime.now = productionNow
	}
	values := url.Values{}
	values.Set("client_id", client.clientID)
	values.Set("client_secret", client.clientSecret)
	values.Set("code", code)
	values.Set("code_verifier", verifier)
	values.Set("grant_type", "authorization_code")
	values.Set("redirect_uri", redirectURI)
	response, err := postOAuthForm(runtime, client.tokenURI, values)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return oauthTokenResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return oauthTokenResponse{}, fmt.Errorf("OAuth token exchange failed with HTTP %d", response.StatusCode)
	}
	return parseOAuthTokenResponse(body, runtime.now())
}

func refreshGoogleOAuthToken(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
	return refreshGoogleOAuthTokenWithRuntime(client, refreshToken, fallbackScopes, runtimeAdapters{now: productionNow})
}

func refreshGoogleOAuthTokenWithRuntime(client oauthClientConfig, refreshToken string, fallbackScopes []string, runtime runtimeAdapters) (oauthTokenResponse, error) {
	if runtime.now == nil {
		runtime.now = productionNow
	}
	values := url.Values{}
	values.Set("client_id", client.clientID)
	values.Set("client_secret", client.clientSecret)
	values.Set("refresh_token", refreshToken)
	values.Set("grant_type", "refresh_token")
	response, err := postOAuthForm(runtime, client.tokenURI, values)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return oauthTokenResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return oauthTokenResponse{}, fmt.Errorf("OAuth token refresh failed with HTTP %d", response.StatusCode)
	}
	return parseOAuthRefreshTokenResponse(body, runtime.now(), refreshToken, fallbackScopes)
}

func parseOAuthTokenResponse(body []byte, now time.Time) (oauthTokenResponse, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return oauthTokenResponse{}, errors.New("OAuth token response is not valid JSON")
	}
	accessToken, _ := raw["access_token"].(string)
	if accessToken == "" {
		return oauthTokenResponse{}, errors.New("OAuth token response missing access token")
	}
	refreshToken, _ := raw["refresh_token"].(string)
	if refreshToken == "" {
		return oauthTokenResponse{}, errors.New("OAuth token response missing refresh token; rerun connect and grant offline access")
	}
	tokenType, _ := raw["token_type"].(string)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	expiresIn, _ := raw["expires_in"].(float64)
	if expiresIn <= 0 {
		return oauthTokenResponse{}, errors.New("OAuth token response missing expiry")
	}
	scopeText, _ := raw["scope"].(string)
	scopes := strings.Fields(scopeText)
	if len(scopes) == 0 {
		return oauthTokenResponse{}, errors.New("OAuth token response missing scopes")
	}
	var refreshExpiresAt *time.Time
	if refreshExpiresIn, ok := raw["refresh_token_expires_in"].(float64); ok && refreshExpiresIn > 0 {
		value := now.Add(time.Duration(refreshExpiresIn) * time.Second).UTC()
		refreshExpiresAt = &value
	}
	return oauthTokenResponse{
		accessToken:            accessToken,
		refreshToken:           refreshToken,
		tokenType:              tokenType,
		scopes:                 scopes,
		expiresAt:              now.Add(time.Duration(expiresIn) * time.Second).UTC(),
		refreshTokenExpiresAt:  refreshExpiresAt,
		rawTokenMaterialObject: raw,
	}, nil
}

func parseOAuthRefreshTokenResponse(body []byte, now time.Time, fallbackRefreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return oauthTokenResponse{}, errors.New("OAuth token response is not valid JSON")
	}
	accessToken, _ := raw["access_token"].(string)
	if accessToken == "" {
		return oauthTokenResponse{}, errors.New("OAuth token response missing access token")
	}
	refreshToken, _ := raw["refresh_token"].(string)
	if refreshToken == "" {
		refreshToken = fallbackRefreshToken
		raw["refresh_token"] = fallbackRefreshToken
	}
	if refreshToken == "" {
		return oauthTokenResponse{}, errors.New("OAuth token response missing refresh token; run `gohealthcli connect` again")
	}
	tokenType, _ := raw["token_type"].(string)
	if tokenType == "" {
		tokenType = "Bearer"
		raw["token_type"] = tokenType
	}
	expiresIn, _ := raw["expires_in"].(float64)
	if expiresIn <= 0 {
		return oauthTokenResponse{}, errors.New("OAuth token response missing expiry")
	}
	scopeText, _ := raw["scope"].(string)
	scopes := strings.Fields(scopeText)
	if len(scopes) == 0 {
		scopes = fallbackScopes
		raw["scope"] = strings.Join(scopes, " ")
	}
	if len(scopes) == 0 {
		return oauthTokenResponse{}, errors.New("OAuth token response missing scopes")
	}
	var refreshExpiresAt *time.Time
	if refreshExpiresIn, ok := raw["refresh_token_expires_in"].(float64); ok && refreshExpiresIn > 0 {
		value := now.Add(time.Duration(refreshExpiresIn) * time.Second).UTC()
		refreshExpiresAt = &value
	}
	return oauthTokenResponse{
		accessToken:            accessToken,
		refreshToken:           refreshToken,
		tokenType:              tokenType,
		scopes:                 scopes,
		expiresAt:              now.Add(time.Duration(expiresIn) * time.Second).UTC(),
		refreshTokenExpiresAt:  refreshExpiresAt,
		rawTokenMaterialObject: raw,
	}, nil
}

func randomURLToken(byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func openBrowser(ctx context.Context, target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "open", target).Start()
	case "windows":
		return exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target).Start()
	default:
		return exec.CommandContext(ctx, "xdg-open", target).Start()
	}
}

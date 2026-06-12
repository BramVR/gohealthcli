package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/archived"
	"strings"
	"time"
)

var (
	errCurrentConnectionIdentityMismatch     = errors.New("Provider returned a different Google Identity; use a new archive path")
	errCurrentConnectionProviderUnauthorized = errors.New("Google Health rejected stored Connection token; run `gohealthcli connect` again")
	errCurrentConnectionMissingAccessToken   = errors.New("Credential Store token material is missing access token; run `gohealthcli connect` again")
	errCurrentConnectionMissingRefreshToken  = errors.New("Credential Store token material is missing refresh token; run `gohealthcli connect` again")
	errCurrentConnectionTokenExpired         = errors.New("Connection token has expired; run `gohealthcli connect` again")
	// errCurrentConnectionScopeMissing is the sentinel callers switch
	// on when the stored Connection's granted scopes do not cover the
	// scopes required for an upstream call. The wrapping error still
	// carries the precise `connect --add-scopes <keyword>` recovery
	// message that requireConnectionScopes already builds (below);
	// the sentinel only adds a typed test surface so each command can
	// set its own "<command>_scope_missing" status without duplicating
	// the pre-check logic. The sentinel's own Error() text matches the
	// phrase requireConnectionScopes's wrapped message starts with, so
	// errors.Is matchers and substring matchers see consistent prose.
	errCurrentConnectionScopeMissing = errors.New("Connection token is missing required Google Health scope")
)

type currentConnectionAccess struct {
	credentialStore credentialStoreConfig
	connection      archived.Connection
	protectedPaths  []string
	runtime         runtimeAdapters
	// autoRefresh, when set, lets AccessToken transparently refresh and
	// persist an expired access token instead of erroring. Zero value
	// preserves the historical behavior (fail-on-expired) so callers that
	// have not opted in are unaffected.
	autoRefresh *autoRefreshConfig
}

type autoRefreshConfig struct {
	oauthClient oauthClientSource
	archive     connectionTokenWriter
}

// connectionTokenWriter is the narrow archive seam the auto-refresh path
// needs: it must persist the refreshed token's metadata so a subsequent
// `status --plain` reports the new expires_at. Both healthArchiveWriter
// (used by sync) and healthArchiveConnectionAPI (used by doctor) satisfy
// it, so the auto-refresh path does not force callers to open a second
// archive handle.
type connectionTokenWriter interface {
	UpdateConnectionTokenMetadata(connectionID string, token oauthTokenResponse, now time.Time) error
}

type doctorOnlineTokenCheck struct {
	accessToken           string
	refreshedToken        *oauthTokenResponse
	previousTokenMaterial map[string]any
}

func newCurrentConnectionAccessWithRuntime(credentialStore credentialStoreConfig, connection archived.Connection, protectedPaths []string, runtime runtimeAdapters) currentConnectionAccess {
	return currentConnectionAccess{
		credentialStore: credentialStore,
		connection:      connection,
		protectedPaths:  append([]string(nil), protectedPaths...),
		runtime:         runtime.withDefaults(),
	}
}

func (access currentConnectionAccess) WithAutoRefresh(oauthClient oauthClientSource, archive connectionTokenWriter) currentConnectionAccess {
	access.autoRefresh = &autoRefreshConfig{oauthClient: oauthClient, archive: archive}
	return access
}

func (access currentConnectionAccess) AccessToken(requiredScopes []string) (string, error) {
	if err := requireUsableConnectionAccessToken(access.connection.TokenMetadataJSON, access.runtime.now()); err != nil {
		if access.autoRefresh == nil || !errors.Is(err, errCurrentConnectionTokenExpired) {
			return "", err
		}
		if err := requireConnectionScopes(access.connection.TokenMetadataJSON, requiredScopes); err != nil {
			return "", err
		}
		return access.refreshAndPersistAccessToken()
	}
	if err := requireConnectionScopes(access.connection.TokenMetadataJSON, requiredScopes); err != nil {
		return "", err
	}
	tokenMaterial, err := access.loadTokenMaterial()
	if err != nil {
		return "", err
	}
	return accessTokenFromTokenMaterial(tokenMaterial)
}

func (access currentConnectionAccess) refreshAndPersistAccessToken() (string, error) {
	// RefreshableAccessToken always performs an OAuth refresh on success
	// and returns the new token in refreshedToken, so there is no
	// "refresh-not-needed" branch to handle here.
	check, err := access.RefreshableAccessToken(access.autoRefresh.oauthClient)
	if err != nil {
		return "", wrapAutoRefreshFailure(err)
	}
	if err := persistDoctorOnlineRefreshedTokenWithRuntime(
		access.autoRefresh.archive,
		access.credentialStore,
		access.connection.ID,
		*check.refreshedToken,
		check.previousTokenMaterial,
		access.runtime,
	); err != nil {
		return "", wrapAutoRefreshFailure(err)
	}
	return check.accessToken, nil
}

// MidRunTokenRefresher exposes the refresh-and-persist hook to callers
// whose access token can outlive its ~1h validity while they hold it —
// today that is sync ingestion, whose pagination loops can run longer
// than one access token (googleHealthIngestionRequest.refreshAccessToken).
// Returns nil when auto-refresh was not opted into via WithAutoRefresh,
// so callers can assign the result unconditionally and keep the
// fail-on-401 behavior for Connections that cannot refresh.
func (access currentConnectionAccess) MidRunTokenRefresher() func() (string, error) {
	if access.autoRefresh == nil {
		return nil
	}
	return access.refreshAndPersistAccessToken
}

func wrapAutoRefreshFailure(err error) error {
	return fmt.Errorf("auto-refresh of Connection access token failed: %w; run `gohealthcli doctor --online` to diagnose or `gohealthcli connect` to re-link", err)
}

func (access currentConnectionAccess) FetchVerifiedIdentity(accessToken string) (googleIdentity, error) {
	identity, err := access.runtime.fetchIdentity(accessToken)
	if err != nil {
		return googleIdentity{}, normalizeProviderError(err)
	}
	if err := access.RequireMatchingHealthUserID(identity.healthUserID); err != nil {
		return googleIdentity{}, err
	}
	return identity, nil
}

func (access currentConnectionAccess) RequireMatchingHealthUserID(healthUserID string) error {
	if healthUserID != access.connection.GoogleHealthUserID {
		return errCurrentConnectionIdentityMismatch
	}
	return nil
}

func (access currentConnectionAccess) RefreshableAccessToken(oauthClient oauthClientSource) (doctorOnlineTokenCheck, error) {
	tokenMaterial, err := access.loadTokenMaterial()
	if err != nil {
		return doctorOnlineTokenCheck{}, err
	}
	if _, err := accessTokenFromTokenMaterial(tokenMaterial); err != nil {
		return doctorOnlineTokenCheck{}, err
	}
	refreshToken, ok := tokenMaterial["refresh_token"].(string)
	if !ok || refreshToken == "" {
		return doctorOnlineTokenCheck{}, errCurrentConnectionMissingRefreshToken
	}
	_, scopes, err := connectionTokenExpiryAndScopes(access.connection.TokenMetadataJSON)
	if err != nil {
		return doctorOnlineTokenCheck{}, err
	}
	if oauthClient.kind != "file" {
		return doctorOnlineTokenCheck{}, errors.New("token refresh requires an OAuth client file source; run `gohealthcli connect` again")
	}
	client, err := loadOAuthClientConfig(oauthClient.path)
	if err != nil {
		return doctorOnlineTokenCheck{}, err
	}
	token, err := access.runtime.refreshOAuthToken(client, refreshToken, scopes)
	if err != nil {
		return doctorOnlineTokenCheck{}, err
	}
	return doctorOnlineTokenCheck{accessToken: token.accessToken, refreshedToken: &token, previousTokenMaterial: tokenMaterial}, nil
}

func (access currentConnectionAccess) loadTokenMaterial() (map[string]any, error) {
	if err := validateCredentialStoreRuntimeWithRuntime(access.credentialStore, access.protectedPaths, access.runtime); err != nil {
		return nil, err
	}
	store, err := newCredentialStoreWithRuntime(access.credentialStore, access.runtime)
	if err != nil {
		return nil, err
	}
	return store.Load(access.connection.ID)
}

func accessTokenFromTokenMaterial(tokenMaterial map[string]any) (string, error) {
	accessToken, ok := tokenMaterial["access_token"].(string)
	if !ok || accessToken == "" {
		return "", errCurrentConnectionMissingAccessToken
	}
	return accessToken, nil
}

func isCurrentConnectionIdentityMismatch(err error) bool {
	return errors.Is(err, errCurrentConnectionIdentityMismatch)
}

func isCurrentConnectionTokenMissing(err error) bool {
	return errors.Is(err, errCurrentConnectionMissingAccessToken) ||
		errors.Is(err, errCurrentConnectionMissingRefreshToken) ||
		errors.Is(err, errCredentialStoreTokenMaterialNotFound)
}

func requireConnectionScopes(metadata string, requiredScopes []string) error {
	if len(requiredScopes) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		return errors.New("Connection token metadata is not valid JSON; run `gohealthcli connect` again")
	}
	value, ok := raw["scopes"]
	if !ok {
		return errors.New("Connection token metadata is missing scopes; run `gohealthcli connect` again")
	}
	var grantedScopes []string
	if err := json.Unmarshal(value, &grantedScopes); err != nil {
		return errors.New("Connection token metadata scopes are invalid; run `gohealthcli connect` again")
	}
	granted := make(map[string]struct{}, len(grantedScopes))
	for _, scope := range grantedScopes {
		granted[scope] = struct{}{}
	}
	// Collect every required scope that is not granted so the hint
	// path below can decide whether all missing scopes are
	// `--add-scopes` keywords (and therefore worth combining into a
	// single `ecg,irn`-style recovery hint). The error message itself
	// still names the first missing scope; the keyword join is what
	// changes between single-scope and multi-scope misses.
	var missing []string
	for _, requiredScope := range requiredScopes {
		if _, ok := granted[requiredScope]; !ok {
			missing = append(missing, requiredScope)
		}
	}
	if len(missing) > 0 {
		// Wrap the typed sentinel so callers can switch on
		// errors.Is(err, errCurrentConnectionScopeMissing) to set
		// per-command "<command>_scope_missing" status without
		// duplicating this pre-check. The user-facing message keeps
		// naming the precise `--add-scopes <keyword>` recovery (or the
		// generic `connect` fallback for non-keyword scopes) — only the
		// error type changes.
		if keywords := addScopeKeywordsForScopes(missing); len(keywords) == len(missing) {
			// Every missing scope is an opt-in Tier 2 scope — point
			// the user at the lightweight `connect --add-scopes` flow
			// rather than re-running the full base-set connect.
			return fmt.Errorf("%w %s; run `gohealthcli connect --add-scopes %s`", errCurrentConnectionScopeMissing, missing[0], strings.Join(keywords, ","))
		}
		return fmt.Errorf("%w %s; run `gohealthcli connect` again", errCurrentConnectionScopeMissing, missing[0])
	}
	return nil
}

func validateTokenMetadata(metadata string) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		return errors.New("token metadata is not valid JSON")
	}
	if len(raw) == 0 {
		return errors.New("missing token metadata")
	}
	if metadataContainsSecretKeys(raw) {
		return errors.New("token metadata contains forbidden secret material")
	}
	if _, err := requireJSONString(raw, "credential_store_key"); err != nil {
		return err
	}
	expiresAt, err := requireJSONString(raw, "expires_at")
	if err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
		return errors.New("token metadata expiry is not RFC3339")
	}
	if err := requireJSONStringArray(raw, "scopes"); err != nil {
		return err
	}
	return nil
}

func requireUsableConnectionAccessToken(metadata string, now time.Time) error {
	expiresAt, _, err := connectionTokenExpiryAndScopes(metadata)
	if err != nil {
		return err
	}
	if !expiresAt.After(now.UTC()) {
		return errCurrentConnectionTokenExpired
	}
	return nil
}

func connectionTokenExpiryAndScopes(metadata string) (time.Time, []string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &raw); err != nil {
		return time.Time{}, nil, errors.New("Connection token metadata is not valid JSON; run `gohealthcli connect` again")
	}
	expiresAtText, err := requireJSONString(raw, "expires_at")
	if err != nil {
		return time.Time{}, nil, errors.New("Connection token metadata is incomplete; run `gohealthcli connect` again")
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtText)
	if err != nil {
		return time.Time{}, nil, errors.New("Connection token expiry is invalid; run `gohealthcli connect` again")
	}
	value, ok := raw["scopes"]
	if !ok {
		return time.Time{}, nil, errors.New("Connection token metadata is missing scopes; run `gohealthcli connect` again")
	}
	var scopes []string
	if err := json.Unmarshal(value, &scopes); err != nil || len(scopes) == 0 {
		return time.Time{}, nil, errors.New("Connection token metadata scopes are invalid; run `gohealthcli connect` again")
	}
	for _, scope := range scopes {
		if strings.TrimSpace(scope) == "" {
			return time.Time{}, nil, errors.New("Connection token metadata scopes are invalid; run `gohealthcli connect` again")
		}
	}
	return expiresAt, scopes, nil
}

func metadataContainsSecretKeys(value any) bool {
	switch typed := value.(type) {
	case map[string]json.RawMessage:
		for key, nested := range typed {
			if secretMetadataKey(key) {
				return true
			}
			var decoded any
			if err := json.Unmarshal(nested, &decoded); err == nil && metadataContainsSecretKeys(decoded) {
				return true
			}
		}
	case map[string]any:
		for key, nested := range typed {
			if secretMetadataKey(key) || metadataContainsSecretKeys(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if metadataContainsSecretKeys(nested) {
				return true
			}
		}
	}
	return false
}

func secretMetadataKey(key string) bool {
	lower := strings.ToLower(key)
	normalized := strings.NewReplacer("_", "", "-", "").Replace(lower)
	return strings.Contains(normalized, "accesstoken") ||
		strings.Contains(normalized, "refreshtoken") ||
		strings.Contains(normalized, "clientsecret") ||
		strings.Contains(normalized, "idtoken")
}

func requireJSONString(raw map[string]json.RawMessage, key string) (string, error) {
	value, ok := raw[key]
	if !ok {
		return "", fmt.Errorf("missing token metadata %s", key)
	}
	var parsed string
	if err := json.Unmarshal(value, &parsed); err != nil || parsed == "" {
		return "", fmt.Errorf("token metadata %s must be a non-empty string", key)
	}
	return parsed, nil
}

func requireJSONStringArray(raw map[string]json.RawMessage, key string) error {
	value, ok := raw[key]
	if !ok {
		return fmt.Errorf("missing token metadata %s", key)
	}
	var parsed []string
	if err := json.Unmarshal(value, &parsed); err != nil || len(parsed) == 0 {
		return fmt.Errorf("token metadata %s must be a non-empty string array", key)
	}
	for _, item := range parsed {
		if strings.TrimSpace(item) == "" {
			return fmt.Errorf("token metadata %s must not contain empty strings", key)
		}
	}
	return nil
}

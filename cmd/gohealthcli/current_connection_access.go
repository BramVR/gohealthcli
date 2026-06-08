package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	errCurrentConnectionIdentityMismatch     = errors.New("Provider returned a different Google Identity; use a new archive path")
	errCurrentConnectionProviderUnauthorized = errors.New("Google Health rejected stored Connection token; run `gohealthcli connect` again")
	errCurrentConnectionMissingAccessToken   = errors.New("Credential Store token material is missing access token; run `gohealthcli connect` again")
	errCurrentConnectionMissingRefreshToken  = errors.New("Credential Store token material is missing refresh token; run `gohealthcli connect` again")
	errCurrentConnectionTokenExpired         = errors.New("Connection token has expired; run `gohealthcli connect` again")
)

type currentConnectionAccess struct {
	credentialStore credentialStoreConfig
	connection      archivedConnection
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

func newCurrentConnectionAccess(credentialStore credentialStoreConfig, connection archivedConnection, protectedPaths []string) currentConnectionAccess {
	return newCurrentConnectionAccessWithRuntime(credentialStore, connection, protectedPaths, productionRuntimeAdapters())
}

func newCurrentConnectionAccessWithRuntime(credentialStore credentialStoreConfig, connection archivedConnection, protectedPaths []string, runtime runtimeAdapters) currentConnectionAccess {
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
	if err := requireUsableConnectionAccessToken(access.connection.tokenMetadataJSON, access.runtime.now()); err != nil {
		if access.autoRefresh == nil || !errors.Is(err, errCurrentConnectionTokenExpired) {
			return "", err
		}
		if err := requireConnectionScopes(access.connection.tokenMetadataJSON, requiredScopes); err != nil {
			return "", err
		}
		return access.refreshAndPersistAccessToken()
	}
	if err := requireConnectionScopes(access.connection.tokenMetadataJSON, requiredScopes); err != nil {
		return "", err
	}
	tokenMaterial, err := access.loadTokenMaterial()
	if err != nil {
		return "", err
	}
	return accessTokenFromTokenMaterial(tokenMaterial)
}

func (access currentConnectionAccess) refreshAndPersistAccessToken() (string, error) {
	check, err := access.RefreshableAccessToken(access.autoRefresh.oauthClient)
	if err != nil {
		return "", wrapAutoRefreshFailure(err)
	}
	if check.refreshedToken == nil {
		return check.accessToken, nil
	}
	if err := persistDoctorOnlineRefreshedTokenWithRuntime(
		access.autoRefresh.archive,
		access.credentialStore,
		access.connection.id,
		*check.refreshedToken,
		check.previousTokenMaterial,
		access.runtime,
	); err != nil {
		return "", wrapAutoRefreshFailure(err)
	}
	return check.accessToken, nil
}

func wrapAutoRefreshFailure(err error) error {
	return fmt.Errorf("auto-refresh of Connection access token failed: %w; run `gohealthcli doctor --online` to diagnose or `gohealthcli connect` to re-link", err)
}

func (access currentConnectionAccess) FetchVerifiedIdentity(accessToken string) (googleIdentity, error) {
	identity, err := access.runtime.fetchIdentity(accessToken)
	if err != nil {
		return googleIdentity{}, currentConnectionProviderError(err)
	}
	if err := access.RequireMatchingHealthUserID(identity.healthUserID); err != nil {
		return googleIdentity{}, err
	}
	return identity, nil
}

func (access currentConnectionAccess) RequireMatchingHealthUserID(healthUserID string) error {
	if healthUserID != access.connection.googleHealthUserID {
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
	_, scopes, err := connectionTokenExpiryAndScopes(access.connection.tokenMetadataJSON)
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
	return store.Load(access.connection.id)
}

func accessTokenFromTokenMaterial(tokenMaterial map[string]any) (string, error) {
	accessToken, ok := tokenMaterial["access_token"].(string)
	if !ok || accessToken == "" {
		return "", errCurrentConnectionMissingAccessToken
	}
	return accessToken, nil
}

func currentConnectionProviderError(err error) error {
	if strings.Contains(err.Error(), "HTTP 401") {
		return errCurrentConnectionProviderUnauthorized
	}
	return err
}

func isCurrentConnectionIdentityMismatch(err error) bool {
	return errors.Is(err, errCurrentConnectionIdentityMismatch)
}

func isCurrentConnectionTokenMissing(err error) bool {
	return errors.Is(err, errCurrentConnectionMissingAccessToken) ||
		errors.Is(err, errCurrentConnectionMissingRefreshToken) ||
		strings.Contains(err.Error(), "token material not found")
}

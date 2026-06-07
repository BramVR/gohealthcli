package main

import (
	"errors"
	"strings"
	"time"
)

var (
	errCurrentConnectionIdentityMismatch     = errors.New("Provider returned a different Google Identity; use a new archive path")
	errCurrentConnectionProviderUnauthorized = errors.New("Google Health rejected stored Connection token; run `gohealthcli connect` again")
	errCurrentConnectionMissingAccessToken   = errors.New("Credential Store token material is missing access token; run `gohealthcli connect` again")
	errCurrentConnectionMissingRefreshToken  = errors.New("Credential Store token material is missing refresh token; run `gohealthcli connect` again")
)

type currentConnectionAccess struct {
	credentialStore credentialStoreConfig
	connection      archivedConnection
	protectedPaths  []string
	now             func() time.Time
}

type doctorOnlineTokenCheck struct {
	accessToken           string
	refreshedToken        *oauthTokenResponse
	previousTokenMaterial map[string]any
}

func newCurrentConnectionAccess(credentialStore credentialStoreConfig, connection archivedConnection, protectedPaths []string) currentConnectionAccess {
	return currentConnectionAccess{
		credentialStore: credentialStore,
		connection:      connection,
		protectedPaths:  append([]string(nil), protectedPaths...),
		now:             currentTime,
	}
}

func (access currentConnectionAccess) AccessToken(requiredScopes []string) (string, error) {
	if err := requireUsableConnectionAccessToken(access.connection.tokenMetadataJSON, access.now()); err != nil {
		return "", err
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

func (access currentConnectionAccess) FetchVerifiedIdentity(accessToken string) (googleIdentity, error) {
	identity, err := fetchIdentity(accessToken)
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
		return doctorOnlineTokenCheck{}, errors.New("doctor --online requires an OAuth client file source to refresh tokens; run `gohealthcli connect` again")
	}
	client, err := loadOAuthClientConfig(oauthClient.path)
	if err != nil {
		return doctorOnlineTokenCheck{}, err
	}
	token, err := refreshOAuthToken(client, refreshToken, scopes)
	if err != nil {
		return doctorOnlineTokenCheck{}, err
	}
	return doctorOnlineTokenCheck{accessToken: token.accessToken, refreshedToken: &token, previousTokenMaterial: tokenMaterial}, nil
}

func (access currentConnectionAccess) loadTokenMaterial() (map[string]any, error) {
	if err := validateCredentialStoreRuntime(access.credentialStore, access.protectedPaths); err != nil {
		return nil, err
	}
	store, err := newCredentialStore(access.credentialStore)
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

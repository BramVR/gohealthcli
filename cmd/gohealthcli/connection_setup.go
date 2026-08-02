package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

// preparedConnection is the reusable local half of Connection setup. It owns
// every check that must succeed before any OAuth flow starts or token material
// is accepted from a completion path.
type preparedConnection struct {
	archivePath         string
	credentialStore     credentialStore
	credentialStoreKind string
	oauthClient         oauthClientConfig
	requestedScopes     []string
}

func prepareConnection(ctx context.Context, configPath, archivePath string, extraScopes []string, runtime runtimeAdapters) (preparedConnection, error) {
	runtime = runtime.withDefaults()
	config, err := inspectFullConfig(configPath, archivePath)
	if err != nil {
		return preparedConnection{}, setupFailureRemediation(err, fmt.Sprintf("config check failed: %v", err))
	}
	prepared := preparedConnection{
		archivePath:         archivePath,
		credentialStoreKind: config.credentialStore.kind,
	}
	if config.oauthClient.kind != "file" {
		return prepared, errors.New("connect requires an OAuth client file source; Secret Provider references are setup-only")
	}
	if _, err := (healthArchiveLifecycle{path: archivePath}).MigrateAndInspect(ctx, false); err != nil {
		var checkErr healthArchiveOpenError
		if errors.As(err, &checkErr) {
			return preparedConnection{}, err
		}
		return prepared, err
	}
	prepared.credentialStore, err = newCredentialStoreWithRuntime(config.credentialStore, runtime)
	if err != nil {
		return prepared, err
	}
	if err := validateCredentialStoreRuntimeWithRuntime(config.credentialStore, []string{configPath, archivePath, config.oauthClient.path}, runtime); err != nil {
		return prepared, err
	}
	prepared.oauthClient, err = loadOAuthClientConfig(config.oauthClient.path)
	if err != nil {
		return prepared, err
	}
	prepared.requestedScopes = unionScopes(oauthScopesForDataTypes(config.defaultDataTypes), extraScopes)
	return prepared, nil
}

// finalizeConnection validates Provider identity before token material can
// reach the Credential Store, then records the same Connection metadata that
// interactive connect has always produced. A canceled caller can stop before
// persistence starts; once the token write begins, the synchronous persistence
// pair finishes on a non-cancelable context to avoid creating a cancellation
// window between the Credential Store and Health Archive writes.
func finalizeConnection(ctx context.Context, prepared preparedConnection, token oauthTokenResponse, runtime runtimeAdapters) (connectResult, error) {
	result := connectResult{CredentialStore: prepared.credentialStoreKind}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	runtime = runtime.withDefaults()
	identity, err := runtime.fetchIdentity(token.accessToken)
	if err != nil {
		return result, connectionFailureRemediation(googlehealth.NormalizeError(err))
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	connectionID := "googlehealth:" + identity.healthUserID
	archive, err := openHealthArchiveConnectionAPI(prepared.archivePath)
	if err != nil {
		return result, err
	}
	defer archive.Close()
	if err := archive.EnsureSameGoogleIdentity(ctx, identity.healthUserID); err != nil {
		return result, connectionFailureRemediation(err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := prepared.credentialStore.Store(connectionID, token.rawTokenMaterialObject); err != nil {
		return result, err
	}
	if err := archive.UpsertConnection(context.WithoutCancel(ctx), connectionID, identity, token, runtime.now()); err != nil {
		return result, err
	}
	return connectResult{
		Status:             "connected",
		ConnectionID:       connectionID,
		ProviderName:       "googlehealth",
		GoogleHealthUserID: identity.healthUserID,
		LegacyFitbitUserID: identity.legacyFitbitUserID,
		CredentialStore:    prepared.credentialStoreKind,
		TokenStatus:        "metadata_present",
		Message:            "Google Identity connected",
	}, nil
}

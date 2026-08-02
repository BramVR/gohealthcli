package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestPrepareConnectionDoesNotStartOAuth(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{failIfCalled: true})

	setup, err := prepareConnection(context.Background(), configPath, archivePath, nil, runtime)
	if err != nil {
		t.Fatalf("prepare Connection: %v", err)
	}
	if setup.archivePath != archivePath {
		t.Fatalf("archive path = %q, want %q", setup.archivePath, archivePath)
	}
	if setup.credentialStoreKind != "file" {
		t.Fatalf("Credential Store = %q, want file", setup.credentialStoreKind)
	}
	if setup.oauthClient.clientID != "test-client" {
		t.Fatalf("OAuth client ID = %q, want test-client", setup.oauthClient.clientID)
	}
	if len(setup.requestedScopes) == 0 {
		t.Fatal("requested scopes empty")
	}
}

func TestFinalizeConnectionStopsBeforeIdentityAndStorageWhenCanceled(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{failIfCalled: true})
	setup, err := prepareConnection(context.Background(), configPath, archivePath, nil, runtime)
	if err != nil {
		t.Fatalf("prepare Connection: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	token := oauthTokenResponse{
		accessToken:  "canceled-access-secret",
		refreshToken: "canceled-refresh-secret",
		tokenType:    "Bearer",
		scopes:       setup.requestedScopes,
		expiresAt:    time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC),
		rawTokenMaterialObject: map[string]any{
			"access_token":  "canceled-access-secret",
			"refresh_token": "canceled-refresh-secret",
		},
	}

	if _, err := finalizeConnection(ctx, setup, token, runtime); !errors.Is(err, context.Canceled) {
		t.Fatalf("finalize canceled Connection: %v, want context.Canceled", err)
	}
	if _, err := os.Stat(tokenStorePath); !os.IsNotExist(err) {
		t.Fatalf("Credential Store stat error = %v, want not found", err)
	}
}

func TestFinalizeConnectionIdentityFailureDoesNotStoreToken(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath, archivePath, tokenStorePath := initializeFileCredentialSetup(t, tempDir)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{})
	setup, err := prepareConnection(context.Background(), configPath, archivePath, nil, runtime)
	if err != nil {
		t.Fatalf("prepare Connection: %v", err)
	}
	runtime.fetchIdentity = func(string) (googleIdentity, error) {
		return googleIdentity{}, errors.New("identity lookup unavailable")
	}
	token := oauthTokenResponse{
		accessToken:  "unvalidated-access-secret",
		refreshToken: "unvalidated-refresh-secret",
		tokenType:    "Bearer",
		scopes:       setup.requestedScopes,
		expiresAt:    time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC),
		rawTokenMaterialObject: map[string]any{
			"access_token":  "unvalidated-access-secret",
			"refresh_token": "unvalidated-refresh-secret",
		},
	}

	if _, err := finalizeConnection(context.Background(), setup, token, runtime); err == nil {
		t.Fatal("finalize Connection error = nil, want identity failure")
	}
	if _, err := os.Stat(tokenStorePath); !os.IsNotExist(err) {
		t.Fatalf("Credential Store stat error = %v, want not found", err)
	}
}

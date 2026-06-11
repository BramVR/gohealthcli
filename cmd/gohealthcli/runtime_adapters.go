package main

import (
	"time"
)

type runtimeAdapters struct {
	// httpDoer is the transport seam every Provider HTTP request rides
	// (#281): the production adapter binds the shared timeout client
	// (#271), tests bind a fake. Code paths that build requests receive
	// this doer (directly or via providerGET()) instead of reading a
	// package-level client.
	httpDoer                       httpDoer
	runOAuthFlow                   func(oauthClientConfig, []string, bool) (oauthTokenResponse, error)
	refreshOAuthToken              func(oauthClientConfig, string, []string) (oauthTokenResponse, error)
	openBrowser                    func(string) error
	fetchIdentity                  func(string) (googleIdentity, error)
	fetchProfile                   func(string) (googleProfile, error)
	fetchRawProvider               func(rawProviderRequest, string) ([]byte, error)
	now                            func() time.Time
	currentOS                      string
	findExecutable                 func(string) (string, error)
	runSecurityAddGenericPassword  func(string, string, []byte) error
	runSecurityFindGenericPassword func(string, string) ([]byte, error)
	runSecretToolStore             func(string, string, []byte) error
	runSecretToolLookup            func(string, string) ([]byte, error)
	runWindowsCredentialWrite      func(string, string, []byte) error
	runWindowsCredentialRead       func(string, string) ([]byte, error)
}

func productionRuntimeAdapters() runtimeAdapters {
	return runtimeAdapters{
		httpDoer:                       providerHTTPClient,
		runOAuthFlow:                   runOAuthFlow,
		refreshOAuthToken:              refreshOAuthToken,
		openBrowser:                    openBrowser,
		fetchIdentity:                  fetchIdentity,
		fetchProfile:                   fetchProfile,
		fetchRawProvider:               fetchRawProvider,
		now:                            currentTime,
		currentOS:                      currentOS,
		findExecutable:                 findExecutable,
		runSecurityAddGenericPassword:  runSecurityAddGenericPassword,
		runSecurityFindGenericPassword: runSecurityFindGenericPassword,
		runSecretToolStore:             runSecretToolStore,
		runSecretToolLookup:            runSecretToolLookup,
		runWindowsCredentialWrite:      runWindowsCredentialWrite,
		runWindowsCredentialRead:       runWindowsCredentialRead,
	}
}

func (adapters runtimeAdapters) withDefaults() runtimeAdapters {
	production := productionRuntimeAdapters()
	// The doer resolves first: the closures bound below capture the
	// adapters value and must see the injected (or defaulted) doer.
	if adapters.httpDoer == nil {
		adapters.httpDoer = production.httpDoer
	}
	if adapters.openBrowser == nil {
		adapters.openBrowser = production.openBrowser
	}
	if adapters.now == nil {
		adapters.now = production.now
	}
	if adapters.runOAuthFlow == nil {
		adapters.runOAuthFlow = func(client oauthClientConfig, scopes []string, noInput bool) (oauthTokenResponse, error) {
			return runBrowserOAuthFlowWithRuntime(client, scopes, noInput, adapters)
		}
	}
	if adapters.refreshOAuthToken == nil {
		adapters.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
			return refreshGoogleOAuthTokenWithRuntime(client, refreshToken, fallbackScopes, adapters)
		}
	}
	if adapters.fetchIdentity == nil {
		adapters.fetchIdentity = production.fetchIdentity
	}
	if adapters.fetchProfile == nil {
		adapters.fetchProfile = production.fetchProfile
	}
	if adapters.fetchRawProvider == nil {
		adapters.fetchRawProvider = production.fetchRawProvider
	}
	if adapters.currentOS == "" {
		adapters.currentOS = production.currentOS
	}
	if adapters.findExecutable == nil {
		adapters.findExecutable = production.findExecutable
	}
	if adapters.runSecurityAddGenericPassword == nil {
		adapters.runSecurityAddGenericPassword = production.runSecurityAddGenericPassword
	}
	if adapters.runSecurityFindGenericPassword == nil {
		adapters.runSecurityFindGenericPassword = production.runSecurityFindGenericPassword
	}
	if adapters.runSecretToolStore == nil {
		adapters.runSecretToolStore = production.runSecretToolStore
	}
	if adapters.runSecretToolLookup == nil {
		adapters.runSecretToolLookup = production.runSecretToolLookup
	}
	if adapters.runWindowsCredentialWrite == nil {
		adapters.runWindowsCredentialWrite = production.runWindowsCredentialWrite
	}
	if adapters.runWindowsCredentialRead == nil {
		adapters.runWindowsCredentialRead = production.runWindowsCredentialRead
	}
	return adapters
}

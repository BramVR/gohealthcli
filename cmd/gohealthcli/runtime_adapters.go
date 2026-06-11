package main

import (
	"os/exec"
	goruntime "runtime"
	"time"
)

type runtimeAdapters struct {
	// httpDoer is the transport seam every Provider HTTP request rides
	// (#281): the production adapter binds the shared timeout client
	// (#271), tests bind a fake. Code paths that build requests receive
	// this doer (directly or via providerGET()) instead of reading a
	// package-level client.
	httpDoer           httpDoer
	runOAuthFlow       func(oauthClientConfig, []string, bool) (oauthTokenResponse, error)
	refreshOAuthToken  func(oauthClientConfig, string, []string) (oauthTokenResponse, error)
	openBrowser        func(string) error
	fetchIdentity      func(string) (googleIdentity, error)
	fetchProfile       func(string) (googleProfile, error)
	fetchPairedDevices func(string) (googlePairedDevices, error)
	fetchSettings      func(string) (googleSettings, error)
	fetchIRNProfile    func(string) (googleIRNProfile, error)
	fetchRawProvider   func(rawProviderRequest, string) ([]byte, error)
	// openHealthArchiveWriter opens the Health Archive write handle the
	// Sync Run path uses (gate connection lookup + lifecycle). Tests
	// wrap it to inject failing writers; production binds the real
	// opener.
	openHealthArchiveWriter        func(string) (healthArchiveWriter, error)
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

// productionFetchPairedDevices, productionFetchSettings, and
// productionFetchIRNProfile bind the real Identity Snapshot fetchers
// over the production Provider GET module (shared timeout client as
// the HTTP doer, #281). They are plain functions, not package vars:
// tests fake these dependencies by setting the corresponding
// runtimeAdapters fields, never by mutating package state (#283).
func productionFetchPairedDevices(accessToken string) (googlePairedDevices, error) {
	return fetchGooglePairedDevices(productionProviderGET(), accessToken)
}

func productionFetchSettings(accessToken string) (googleSettings, error) {
	return fetchGoogleSettings(productionProviderGET(), accessToken)
}

func productionFetchIRNProfile(accessToken string) (googleIRNProfile, error) {
	return fetchGoogleIRNProfile(productionProviderGET(), accessToken)
}

// productionNow is the production clock: the current UTC time. It is a
// plain function, not a package var — tests inject fixed clocks through
// runtimeAdapters.now or the healthArchiveLifecycle.now field (#283).
func productionNow() time.Time {
	return time.Now().UTC()
}

func productionRuntimeAdapters() runtimeAdapters {
	return runtimeAdapters{
		httpDoer:                       providerHTTPClient,
		runOAuthFlow:                   runBrowserOAuthFlow,
		refreshOAuthToken:              refreshGoogleOAuthToken,
		openBrowser:                    openBrowser,
		fetchIdentity:                  productionFetchIdentity,
		fetchProfile:                   productionFetchProfile,
		fetchPairedDevices:             productionFetchPairedDevices,
		fetchSettings:                  productionFetchSettings,
		fetchIRNProfile:                productionFetchIRNProfile,
		fetchRawProvider:               productionFetchRawProvider,
		openHealthArchiveWriter:        openHealthArchiveWriter,
		now:                            productionNow,
		currentOS:                      goruntime.GOOS,
		findExecutable:                 exec.LookPath,
		runSecurityAddGenericPassword:  runSecurityAddGenericPasswordCommand,
		runSecurityFindGenericPassword: runSecurityFindGenericPasswordCommand,
		runSecretToolStore:             runSecretToolStoreCommand,
		runSecretToolLookup:            runSecretToolLookupCommand,
		runWindowsCredentialWrite:      runWindowsCredentialWriteCommand,
		runWindowsCredentialRead:       runWindowsCredentialReadCommand,
	}
}

// providerGET derives the shared Provider GET module from the adapters'
// HTTP doer, so Identity Snapshot fetches ride whatever transport the
// adapters carry (production: the shared timeout client; tests: a fake
// doer). Retry seams stay nil — fetchWithRetry falls back to real
// backoff sleeps; tests that need virtual sleeps construct the module
// value directly.
func (adapters runtimeAdapters) providerGET() providerGET {
	return providerGET{doer: adapters.httpDoer}
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
	// Nil fetchers default to the real fetcher bodies routed through the
	// adapters' (possibly injected) doer, so runtimeAdapters{httpDoer:
	// fake} exercises production URL building, headers, and status
	// mapping against the fake transport. The production dispatch path
	// never reaches these branches: productionRuntimeAdapters binds the
	// package-level seams explicitly.
	if adapters.fetchIdentity == nil {
		adapters.fetchIdentity = func(accessToken string) (googleIdentity, error) {
			return fetchGoogleIdentity(adapters.providerGET(), accessToken)
		}
	}
	if adapters.fetchProfile == nil {
		adapters.fetchProfile = func(accessToken string) (googleProfile, error) {
			return fetchGoogleProfile(adapters.providerGET(), accessToken)
		}
	}
	if adapters.fetchPairedDevices == nil {
		adapters.fetchPairedDevices = func(accessToken string) (googlePairedDevices, error) {
			return fetchGooglePairedDevices(adapters.providerGET(), accessToken)
		}
	}
	if adapters.fetchSettings == nil {
		adapters.fetchSettings = func(accessToken string) (googleSettings, error) {
			return fetchGoogleSettings(adapters.providerGET(), accessToken)
		}
	}
	if adapters.fetchIRNProfile == nil {
		adapters.fetchIRNProfile = func(accessToken string) (googleIRNProfile, error) {
			return fetchGoogleIRNProfile(adapters.providerGET(), accessToken)
		}
	}
	if adapters.fetchRawProvider == nil {
		adapters.fetchRawProvider = func(request rawProviderRequest, accessToken string) ([]byte, error) {
			return fetchGoogleHealthRaw(adapters.httpDoer, request, accessToken)
		}
	}
	if adapters.openHealthArchiveWriter == nil {
		adapters.openHealthArchiveWriter = openHealthArchiveWriter
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

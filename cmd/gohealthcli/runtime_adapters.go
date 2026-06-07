package main

import (
	"time"
)

type runtimeAdapters struct {
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
	if adapters.runOAuthFlow == nil {
		adapters.runOAuthFlow = production.runOAuthFlow
	}
	if adapters.refreshOAuthToken == nil {
		adapters.refreshOAuthToken = production.refreshOAuthToken
	}
	if adapters.openBrowser == nil {
		adapters.openBrowser = production.openBrowser
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
	if adapters.now == nil {
		adapters.now = production.now
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

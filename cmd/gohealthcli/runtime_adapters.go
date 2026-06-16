package main

import (
	"context"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
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
	httpDoer           googlehealth.Doer
	runOAuthFlow       func(oauthClientConfig, []string, bool) (oauthTokenResponse, error)
	refreshOAuthToken  func(oauthClientConfig, string, []string) (oauthTokenResponse, error)
	openBrowser        func(context.Context, string) error
	fetchIdentity      func(string) (googleIdentity, error)
	fetchProfile       func(string) (googleProfile, error)
	fetchPairedDevices func(string) (googlePairedDevices, error)
	fetchSettings      func(string) (googleSettings, error)
	fetchIRNProfile    func(string) (googleIRNProfile, error)
	fetchRawProvider   func(context.Context, googlehealth.RawRequest, string) ([]byte, error)
	retrySleeper       googlehealth.RetrySleeper
	// openHealthArchiveWriter opens the Health Archive write handle the
	// Sync Run path uses (gate connection lookup + lifecycle). Tests
	// wrap it to inject failing writers; production binds the real
	// opener.
	openHealthArchiveWriter func(string) (healthArchiveWriter, error)
	now                     func() time.Time
	// sleep is the blocking-wait seam the Sync Run finalize retry loop
	// rides between SQLITE_BUSY attempts. Production binds time.Sleep;
	// tests bind a no-op so retry scenarios stay instant.
	sleep                          func(time.Duration)
	currentOS                      string
	findExecutable                 func(string) (string, error)
	runSecurityAddGenericPassword  func(context.Context, string, string, []byte) error
	runSecurityFindGenericPassword func(context.Context, string, string) ([]byte, error)
	runSecretToolStore             func(context.Context, string, string, []byte) error
	runSecretToolLookup            func(context.Context, string, string) ([]byte, error)
	runWindowsCredentialWrite      func(context.Context, string, string, []byte) error
	runWindowsCredentialRead       func(context.Context, string, string) ([]byte, error)
	// observeSubcommandFlagSet is the issue #76 schema-drift test hook
	// (see flagSetObserver in common_flags.go). Production leaves it
	// nil — notifySubcommandFlagSetObserver treats nil as a no-op.
	observeSubcommandFlagSet flagSetObserver
}

// productionFetchPairedDevices, productionFetchSettings, and
// productionFetchIRNProfile bind the real Identity Snapshot fetchers
// over the production Provider GET module (shared timeout client as
// the HTTP doer, #281). They are plain functions, not package vars:
// tests fake these dependencies by setting the corresponding
// runtimeAdapters fields, never by mutating package state (#283).
func productionFetchPairedDevices(accessToken string) (googlePairedDevices, error) {
	return fetchGooglePairedDevices(googlehealth.ProductionGET(), accessToken)
}

func productionFetchSettings(accessToken string) (googleSettings, error) {
	return fetchGoogleSettings(googlehealth.ProductionGET(), accessToken)
}

func productionFetchIRNProfile(accessToken string) (googleIRNProfile, error) {
	return fetchGoogleIRNProfile(googlehealth.ProductionGET(), accessToken)
}

// productionNow is the production clock: the current UTC time. It is a
// plain function, not a package var — tests inject fixed clocks through
// runtimeAdapters.now or the healthArchiveLifecycle.now field (#283).
func productionNow() time.Time {
	return time.Now().UTC()
}

func productionRuntimeAdapters() runtimeAdapters {
	return runtimeAdapters{
		httpDoer:                       googlehealth.HTTPClient,
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
		sleep:                          time.Sleep,
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
// doer). Retry seams stay nil — the module falls back to real backoff
// sleeps; package-internal googlehealth tests that need virtual sleeps
// construct the module value directly.
func (adapters runtimeAdapters) providerGET() googlehealth.GET {
	return googlehealth.NewGET(adapters.httpDoer)
}

// newGoogleHealthIngestionWithRuntime binds the Provider ingestion to
// the runtime adapters seam: the adapters' single-attempt raw fetch
// (production: googlehealth.FetchRaw over the shared timeout client;
// tests: a fake) plus the adapters' clock. The googlehealth package
// wraps the fetch in its bounded retry middleware.
func newGoogleHealthIngestionWithRuntime(runtime runtimeAdapters) googlehealth.Ingestion {
	runtime = runtime.withDefaults()
	return googlehealth.NewIngestionWithRetrySeams(runtime.fetchRawProvider, runtime.now, runtime.retrySleeper, nil)
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
	if adapters.sleep == nil {
		adapters.sleep = production.sleep
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
	// never reaches these branches: productionRuntimeAdapters binds
	// every field to a concrete production function explicitly.
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
		adapters.fetchRawProvider = func(ctx context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
			return googlehealth.FetchRaw(ctx, adapters.httpDoer, request, accessToken)
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

// productionFetchIdentity and productionFetchProfile bind the real
// fetchers over the production Provider GET module (shared timeout
// client as the HTTP doer, #281). Plain functions, not package vars:
// tests fake these dependencies through runtimeAdapters fields (#283).
func productionFetchIdentity(accessToken string) (googleIdentity, error) {
	return fetchGoogleIdentity(googlehealth.ProductionGET(), accessToken)
}

func productionFetchProfile(accessToken string) (googleProfile, error) {
	return fetchGoogleProfile(googlehealth.ProductionGET(), accessToken)
}

// productionFetchRawProvider binds the real raw Provider fetch over the
// shared timeout client. ctx scopes the HTTP request so a canceled Sync
// Run aborts the in-flight call (#284).
func productionFetchRawProvider(ctx context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
	return googlehealth.FetchRaw(ctx, googlehealth.HTTPClient, request, accessToken)
}

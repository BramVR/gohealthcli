package googlehealth

// OAuth scope URLs for the Google Health API. The Data Type catalog
// (catalog.go) references these per entry; main's OAuth flow composes
// its requested-scope set from them via ScopesForDataType and the
// `connect --add-scopes` keyword map.
const (
	ScopeActivityReadonly      = "https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly"
	ScopeHealthMetricsReadonly = "https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly"
	ScopeSleepReadonly         = "https://www.googleapis.com/auth/googlehealth.sleep.readonly"
	ScopeNutritionReadonly     = "https://www.googleapis.com/auth/googlehealth.nutrition.readonly"
	ScopeProfileReadonly       = "https://www.googleapis.com/auth/googlehealth.profile.readonly"
)

// Tier 2 opt-in scopes (#104, #176). Users grant these via
// `gohealthcli connect --add-scopes ecg,irn,settings`. The CLI-side
// keyword→scope mapping lives in main (connect_add_scopes.go); this
// file owns the constants the catalog references. `settings.readonly`
// (#176) is what Google's `users.getSettings` and
// `users.pairedDevices.list` actually require — `profile.readonly`
// alone returns HTTP 403 for those.
const (
	ScopeEcgReadonly      = "https://www.googleapis.com/auth/googlehealth.electrocardiogram.readonly"
	ScopeIrnReadonly      = "https://www.googleapis.com/auth/googlehealth.irn.readonly"
	ScopeSettingsReadonly = "https://www.googleapis.com/auth/googlehealth.settings.readonly"
)

// ScopeLocationReadonly is the Tier 2 optional scope from #140:
// `googlehealth.location.readonly` is the scope Google requires (on
// top of `activity_and_fitness.readonly`) to authorise
// `users.dataTypes.dataPoints.exportExerciseTcx`. Users opt in via
// `gohealthcli connect --add-scopes tcx`; the exercise sync then
// archives TCX route bytes as a `tcx`-kind Attachment per ADR-0009.
// Without it, exercise sync skips the TCX hook cleanly (no 403
// round-trip) — see attachExerciseTcxIfAvailable.
const ScopeLocationReadonly = "https://www.googleapis.com/auth/googlehealth.location.readonly"

// Identity-endpoint URLs. Each is the upstream Google Health URL one
// Identity Snapshot fetcher in main GETs through the shared Provider
// GET module; `raw endpoint <name>` dispatches to the same URLs via
// the identityEndpointURLs catalog below.
const (
	IdentityURL      = "https://health.googleapis.com/v4/users/me/identity"
	ProfileURL       = "https://health.googleapis.com/v4/users/me/profile"
	SettingsURL      = "https://health.googleapis.com/v4/users/me/settings"
	PairedDevicesURL = "https://health.googleapis.com/v4/users/me/pairedDevices"
	IRNProfileURL    = "https://health.googleapis.com/v4/users/me/irnProfile"
)

// identityEndpointScopes is the declarative scope catalog keyed by
// Google Health identity-endpoint identifier. Each entry pins the
// OAuth scope URL(s) the upstream call requires, so the Identity
// Snapshot commands and the `raw` endpoint dispatcher converge on one
// source of truth — adding a new endpoint or revising a scope is a
// one-row change here.
//
// Values track Google's per-method documentation: getProfile and
// getIdentity require `profile.readonly`, getSettings and pairedDevices
// require `settings.readonly` (PRD #142 slice 2 / #176 confirmed
// empirically — `profile.readonly` alone returns HTTP 403 for those
// two), and getIrnProfile requires the IRN scope. References:
//   - https://developers.google.com/health/api/reference/rest/v4/users/getProfile
//   - https://developers.google.com/health/api/reference/rest/v4/users/getSettings
//   - https://developers.google.com/health/api/reference/rest/v4/users.pairedDevices/list
//   - https://developers.google.com/health/api/reference/rest/v4/users/getIrnProfile
//   - https://developers.google.com/health/api/reference/rest/v4/users/getIdentity
//
// TestGoogleHealthIdentityEndpointScopesCatalog pins the per-endpoint
// values so any future revision is a one-row change here plus a
// matching test-value flip.
var identityEndpointScopes = map[string][]string{
	"getProfile":    {ScopeProfileReadonly},
	"getSettings":   {ScopeSettingsReadonly},
	"pairedDevices": {ScopeSettingsReadonly},
	"getIrnProfile": {ScopeIrnReadonly},
	"getIdentity":   {ScopeProfileReadonly},
}

// identityEndpointURLs pairs each catalog entry with its upstream
// Google Health URL constant, so `raw endpoint <name>` can dispatch
// through a single lookup without re-listing the endpoint names.
var identityEndpointURLs = map[string]string{
	"getIdentity":   IdentityURL,
	"getProfile":    ProfileURL,
	"getSettings":   SettingsURL,
	"pairedDevices": PairedDevicesURL,
	"getIrnProfile": IRNProfileURL,
}

// IdentityEndpointScopes returns the OAuth scopes the named identity
// endpoint requires, or nil for an unknown endpoint. The Identity
// Snapshot command engine in main uses this for its pre-call scope
// check so the per-command scope literals cannot drift from the `raw
// endpoint` dispatcher's catalog (PRD #142).
func IdentityEndpointScopes(endpoint string) []string {
	return append([]string(nil), identityEndpointScopes[endpoint]...)
}

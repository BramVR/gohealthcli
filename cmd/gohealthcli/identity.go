package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/archived"
	"io"
)

const googleHealthIdentityURL = "https://health.googleapis.com/v4/users/me/identity"

type identityResult struct {
	Status             string `json:"status"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	LegacyFitbitUserID string `json:"legacy_fitbit_user_id,omitempty"`
	Message            string `json:"message"`
}

type googleIdentity struct {
	healthUserID       string
	legacyFitbitUserID string
	rawJSON            string
}

// identityCommand is identity's Identity Snapshot engine spec (issue
// #282). identity joined the engine via the #273 parity decision: it
// shares the whole pipeline through Connection access (auto-refresh
// for file-based OAuth Connections, the getIdentity scope pre-check —
// the same catalog entry `raw endpoint getIdentity` consumes). Its
// genuinely-unique decoration is the act override: instead of
// archiving a snapshot, identity re-fetches the verified Google
// Identity and refreshes the metadata stored alongside the Connection
// — which is also why its spec name carries no "Snapshot" and it sets
// no snapshotKind.
var identityCommand = identitySnapshotCommandSpec[identityResult, googleIdentity]{
	command:            "identity",
	commonFlags:        AllCommonFlagsSpec,
	statusFailed:       "identity_failed",
	statusUnavailable:  "identity_unavailable",
	statusScopeMissing: "identity_scope_missing",
	scopeEndpointKey:   "getIdentity",
	seedResult: func(connection archived.Connection) identityResult {
		return identityResult{
			ConnectionID:       connection.ID,
			ProviderName:       connection.ProviderName,
			GoogleHealthUserID: connection.GoogleHealthUserID,
			LegacyFitbitUserID: connection.LegacyFitbitUserID,
		}
	},
	status:      func(result *identityResult) string { return result.Status },
	setStatus:   func(result *identityResult, status string) { result.Status = status },
	setMessage:  func(result *identityResult, message string) { result.Message = message },
	writeResult: writeIdentityResult,
	act: func(engine identitySnapshotCommandContext, result *identityResult) error {
		identity, err := engine.connectionAccess.FetchVerifiedIdentity(engine.accessToken)
		if err != nil {
			if isCurrentConnectionIdentityMismatch(err) {
				result.Status = "identity_mismatch"
			} else if isProviderUnreachableError(err) {
				// Provider outage (non-auth HTTP failure or network error)
				// gets its own documented JSON failure status so automation
				// can tell it apart from local misconfiguration (issue #272).
				result.Status = "provider_unreachable"
			}
			return err
		}
		if err := engine.archive.RefreshConnectionIdentity(context.Background(), engine.connection, identity, engine.runtime.now()); err != nil {
			return err
		}
		result.Status = "identity_refreshed"
		result.GoogleHealthUserID = identity.healthUserID
		if identity.legacyFitbitUserID != "" {
			result.LegacyFitbitUserID = identity.legacyFitbitUserID
		}
		result.Message = "Google Identity refreshed"
		return nil
	},
}

// fetchGoogleIdentity is a thin call site over the shared Provider GET
// module (provider_get.go, issue #280), which owns the transport
// behavior: bearer auth, size limit, timeout, typed labeled status
// errors, JSON validity, and retry/Retry-After. The module value
// carries the HTTP doer (#281). Identity-shape validation stays in
// parseGoogleIdentity.
func fetchGoogleIdentity(get providerGET, accessToken string) (googleIdentity, error) {
	body, err := fetchProviderJSON(context.Background(), get, googleHealthIdentityURL, "identity", accessToken)
	if err != nil {
		return googleIdentity{}, err
	}
	return parseGoogleIdentity(body)
}

func parseGoogleIdentity(body []byte) (googleIdentity, error) {
	var raw struct {
		HealthUserID string `json:"healthUserId"`
		LegacyUserID string `json:"legacyUserId"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return googleIdentity{}, errors.New("Google Health identity response is not valid JSON")
	}
	if raw.HealthUserID == "" {
		return googleIdentity{}, errors.New("Google Health identity response missing healthUserId")
	}
	return googleIdentity{
		healthUserID:       raw.HealthUserID,
		legacyFitbitUserID: raw.LegacyUserID,
		rawJSON:            string(body),
	}, nil
}

func writeIdentityResult(result identityResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if result.ConnectionID != "" {
			if _, err := fmt.Fprintf(stdout, "connection_id: %s\n", result.ConnectionID); err != nil {
				return err
			}
		}
		if result.ProviderName != "" {
			if _, err := fmt.Fprintf(stdout, "provider_name: %s\n", result.ProviderName); err != nil {
				return err
			}
		}
		if result.GoogleHealthUserID != "" {
			if _, err := fmt.Fprintf(stdout, "google_health_user_id: %s\n", result.GoogleHealthUserID); err != nil {
				return err
			}
		}
		if result.LegacyFitbitUserID != "" {
			if _, err := fmt.Fprintf(stdout, "legacy_fitbit_user_id: %s\n", result.LegacyFitbitUserID); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}
	switch result.Status {
	case "identity_refreshed":
		if _, err := fmt.Fprintln(stdout, "Google Identity refreshed"); err != nil {
			return err
		}
	case "identity_mismatch":
		if _, err := fmt.Fprintln(stdout, "Google Identity mismatch"); err != nil {
			return err
		}
	case "identity_unavailable":
		if _, err := fmt.Fprintln(stdout, "Google Identity unavailable"); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintln(stdout, "Google Identity failed"); err != nil {
			return err
		}
	}
	if result.ConnectionID != "" {
		if _, err := fmt.Fprintf(stdout, "Connection: %s\n", result.ConnectionID); err != nil {
			return err
		}
	}
	if result.GoogleHealthUserID != "" {
		if _, err := fmt.Fprintf(stdout, "Google Health user ID: %s\n", result.GoogleHealthUserID); err != nil {
			return err
		}
	}
	if result.LegacyFitbitUserID != "" {
		if _, err := fmt.Fprintf(stdout, "Legacy Fitbit user ID: %s\n", result.LegacyFitbitUserID); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
}

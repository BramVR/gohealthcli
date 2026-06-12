package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BramVR/gohealthcli/internal/archived"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
	"io"
	"strings"
)

type profileResult struct {
	Status             string `json:"status"`
	SnapshotID         int64  `json:"snapshot_id,omitempty"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	LegacyFitbitUserID string `json:"legacy_fitbit_user_id,omitempty"`
	FetchedAt          string `json:"fetched_at,omitempty"`
	Message            string `json:"message"`
}

type googleProfile struct {
	healthUserID string
	resourceName string
	rawJSON      string
}

// profileSnapshotCommand is profile's Identity Snapshot engine spec
// (issue #282). Its genuinely-unique decoration is the profile ID
// verification: verifyPayload confirms the fetched payload belongs to
// the archived Google Identity — falling back to the verified-identity
// endpoint when the profile payload carries no user ID — before any
// snapshot is archived, mapping a mismatch to "profile_mismatch".
// fetchPayload rides the runtime.fetchProfile adapter, the same seam
// tests already fake.
var profileSnapshotCommand = identitySnapshotCommandSpec[profileResult, googleProfile]{
	command:            "profile",
	commonFlags:        AllCommonFlagsSpec,
	statusFailed:       "profile_failed",
	statusUnavailable:  "profile_unavailable",
	statusScopeMissing: "profile_scope_missing",
	scopeEndpointKey:   "getProfile",
	seedResult: func(connection archived.Connection) profileResult {
		return profileResult{
			ConnectionID:       connection.ID,
			ProviderName:       connection.ProviderName,
			GoogleHealthUserID: connection.GoogleHealthUserID,
			LegacyFitbitUserID: connection.LegacyFitbitUserID,
		}
	},
	status:       func(result *profileResult) string { return result.Status },
	setStatus:    func(result *profileResult, status string) { result.Status = status },
	setMessage:   func(result *profileResult, message string) { result.Message = message },
	writeResult:  writeProfileResult,
	snapshotKind: snapshotKindProfile,
	fetchPayload: func(runtime runtimeAdapters, accessToken string) (googleProfile, error) {
		return runtime.fetchProfile(accessToken)
	},
	payloadRawJSON: func(payload googleProfile) string { return payload.rawJSON },
	verifyPayload: func(engine identitySnapshotCommandContext, result *profileResult, payload googleProfile) error {
		profileHealthUserID := payload.healthUserID
		if profileHealthUserID == "" {
			identity, err := engine.connectionAccess.FetchVerifiedIdentity(engine.accessToken)
			if err != nil {
				if isCurrentConnectionIdentityMismatch(err) {
					result.Status = "profile_mismatch"
				} else if googlehealth.IsUnreachableError(err) {
					result.Status = "provider_unreachable"
				}
				return err
			}
			profileHealthUserID = identity.healthUserID
		}
		if err := engine.connectionAccess.RequireMatchingHealthUserID(profileHealthUserID); err != nil {
			result.Status = "profile_mismatch"
			return err
		}
		return nil
	},
	finishArchived: func(result *profileResult, snapshotID int64, fetchedAt string) {
		result.Status = "profile_archived"
		result.SnapshotID = snapshotID
		result.FetchedAt = fetchedAt
		result.Message = "Profile Snapshot archived"
	},
}

// fetchGoogleProfile is a thin call site over the shared Provider GET
// module (internal/googlehealth, issue #280), which owns the transport
// behavior: bearer auth, size limit, timeout, typed labeled status
// errors, JSON validity, and retry/Retry-After. The module value
// carries the HTTP doer (#281). Profile-shape validation stays in
// parseGoogleProfile.
func fetchGoogleProfile(get googlehealth.GET, accessToken string) (googleProfile, error) {
	body, err := get.FetchJSON(context.Background(), googlehealth.ProfileURL, "profile", accessToken)
	if err != nil {
		return googleProfile{}, err
	}
	return parseGoogleProfile(body)
}

func parseGoogleProfile(body []byte) (googleProfile, error) {
	var raw struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return googleProfile{}, errors.New("Google Health profile response is not valid JSON")
	}
	if raw.Name == "" {
		return googleProfile{}, errors.New("Google Health profile response missing name")
	}
	return googleProfile{
		healthUserID: googleHealthUserIDFromProfileName(raw.Name),
		resourceName: raw.Name,
		rawJSON:      string(body),
	}, nil
}

func googleHealthUserIDFromProfileName(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) != 3 || parts[0] != "users" || parts[2] != "profile" || parts[1] == "me" {
		return ""
	}
	return parts[1]
}

func writeProfileResult(result profileResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if result.SnapshotID != 0 {
			if _, err := fmt.Fprintf(stdout, "snapshot_id: %d\n", result.SnapshotID); err != nil {
				return err
			}
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
		if result.FetchedAt != "" {
			if _, err := fmt.Fprintf(stdout, "fetched_at: %s\n", result.FetchedAt); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}
	switch result.Status {
	case "profile_archived":
		if _, err := fmt.Fprintln(stdout, "Profile Snapshot archived"); err != nil {
			return err
		}
	case "profile_mismatch":
		if _, err := fmt.Fprintln(stdout, "Profile Snapshot mismatch"); err != nil {
			return err
		}
	case "profile_unavailable":
		if _, err := fmt.Fprintln(stdout, "Profile Snapshot unavailable"); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintln(stdout, "Profile Snapshot failed"); err != nil {
			return err
		}
	}
	if result.SnapshotID != 0 {
		if _, err := fmt.Fprintf(stdout, "Snapshot: %d\n", result.SnapshotID); err != nil {
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
	if result.FetchedAt != "" {
		if _, err := fmt.Fprintf(stdout, "Fetched at: %s\n", result.FetchedAt); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
}

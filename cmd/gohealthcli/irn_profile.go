package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

const googleHealthIRNProfileURL = "https://health.googleapis.com/v4/users/me/irnProfile"

// googleIRNProfile is the raw response from users.getIrnProfile. Slice
// C will project this through current_irn_profile.
type googleIRNProfile struct {
	rawJSON string
}

// fetchIRNProfile is the seam tests stub. Production binds the real
// fetchGoogleIRNProfile over the production Provider GET module
// (shared timeout client as the HTTP doer, #281).
var fetchIRNProfile = func(accessToken string) (googleIRNProfile, error) {
	return fetchGoogleIRNProfile(productionProviderGET(), accessToken)
}

type irnProfileResult struct {
	Status             string `json:"status"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	SnapshotID         int64  `json:"snapshot_id,omitempty"`
	FetchedAt          string `json:"fetched_at,omitempty"`
	Message            string `json:"message"`
}

// irnProfileSnapshotCommand is irn-profile's Identity Snapshot engine
// spec (issue #282): the command is the spec — irn-profile has no
// decoration beyond the shared fetch → handoff → render pipeline. The
// fetchPayload closure reads the fetchIRNProfile seam at invocation
// time so tests keep stubbing the same package var.
//
// irn-profile does no prompting and never blocks on browser input, so
// --no-input would imply a behaviour the command does not have. The
// Common Flag Set's pre-Parse scan turns a stray --no-input into a
// targeted "--no-input is not supported by irn-profile" message
// (issue #171), so the help block and the runtime spec agree. The
// accepted-flag list is sourced from the same identitySnapshotCommon-
// FlagNames helper the registry uses, so runtime parsing and the
// published schema cannot drift apart.
var irnProfileSnapshotCommand = identitySnapshotCommandSpec[irnProfileResult, googleIRNProfile]{
	command: "irn-profile",
	commonFlags: func() CommonFlagSpec {
		return CommonFlagSpec{Accepted: identitySnapshotCommonFlagNames()}
	},
	statusFailed:       "irn_profile_failed",
	statusUnavailable:  "irn_profile_unavailable",
	statusScopeMissing: "irn_profile_scope_missing",
	scopeEndpointKey:   "getIrnProfile",
	seedResult: func(connection archivedConnection) irnProfileResult {
		return irnProfileResult{
			ConnectionID:       connection.id,
			ProviderName:       connection.providerName,
			GoogleHealthUserID: connection.googleHealthUserID,
		}
	},
	status:       func(result *irnProfileResult) string { return result.Status },
	setStatus:    func(result *irnProfileResult, status string) { result.Status = status },
	setMessage:   func(result *irnProfileResult, message string) { result.Message = message },
	writeResult:  writeIRNProfileResult,
	snapshotKind: snapshotKindIRNProfile,
	fetchPayload: func(_ runtimeAdapters, accessToken string) (googleIRNProfile, error) {
		return fetchIRNProfile(accessToken)
	},
	payloadRawJSON: func(payload googleIRNProfile) string { return payload.rawJSON },
	finishArchived: func(result *irnProfileResult, snapshotID int64, fetchedAt string) {
		result.Status = "irn_profile_archived"
		result.SnapshotID = snapshotID
		result.FetchedAt = fetchedAt
		result.Message = "IRN Profile Snapshot archived"
	},
}

func scopeListContains(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

// fetchGoogleIRNProfile is a thin call site over the shared Provider
// GET module (provider_get.go, issue #280), which owns the transport
// behavior: bearer auth, size limit, timeout, typed labeled status
// errors, JSON validity, and retry/Retry-After. The module value
// carries the HTTP doer (#281).
func fetchGoogleIRNProfile(get providerGET, accessToken string) (googleIRNProfile, error) {
	body, err := fetchProviderJSON(context.Background(), get, googleHealthIRNProfileURL, "irnProfile", accessToken)
	if err != nil {
		return googleIRNProfile{}, err
	}
	return googleIRNProfile{rawJSON: string(body)}, nil
}

func writeIRNProfileResult(result irnProfileResult, mode outputMode, stdout io.Writer) error {
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
		if result.FetchedAt != "" {
			if _, err := fmt.Fprintf(stdout, "fetched_at: %s\n", result.FetchedAt); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}
	if _, err := fmt.Fprintf(stdout, "IRN Profile: %s\n", result.Status); err != nil {
		return err
	}
	if result.FetchedAt != "" {
		if _, err := fmt.Fprintf(stdout, "Fetched at: %s\n", result.FetchedAt); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "%s\n", result.Message)
	return err
}

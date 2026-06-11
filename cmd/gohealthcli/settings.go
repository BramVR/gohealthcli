package main

import (
	"encoding/json"
	"fmt"
	"io"
)

const googleHealthSettingsURL = "https://health.googleapis.com/v4/users/me/settings"

// googleSettings is the raw payload returned by users.getSettings. We
// keep the raw JSON so a Normalized View can project new fields without
// a parser change; consumers that need a typed view read current_settings.
type googleSettings struct {
	rawJSON string
}

type settingsResult struct {
	Status             string `json:"status"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	SnapshotID         int64  `json:"snapshot_id,omitempty"`
	FetchedAt          string `json:"fetched_at,omitempty"`
	Message            string `json:"message"`
}

// settingsSnapshotCommand is settings' Identity Snapshot engine spec
// (issue #282): the command is the spec — settings has no decoration
// beyond the shared fetch → handoff → render pipeline. The fetchPayload
// closure rides the runtime adapters' fetchSettings seam, so tests
// inject fakes through the adapters value (#283).
//
// settings does no prompting and never blocks on browser input, so
// --no-input would imply a behaviour the command does not have. The
// Common Flag Set's pre-Parse scan turns a stray --no-input into a
// targeted "--no-input is not supported by settings" message (issue
// #171), so the help block and the runtime spec agree. The
// accepted-flag list is sourced from the same identitySnapshotCommon-
// FlagNames helper the registry uses, so runtime parsing and the
// published schema cannot drift apart.
var settingsSnapshotCommand = identitySnapshotCommandSpec[settingsResult, googleSettings]{
	command: "settings",
	commonFlags: func() CommonFlagSpec {
		return CommonFlagSpec{Accepted: identitySnapshotCommonFlagNames()}
	},
	statusFailed:       "settings_failed",
	statusUnavailable:  "settings_unavailable",
	statusScopeMissing: "settings_scope_missing",
	scopeEndpointKey:   "getSettings",
	seedResult: func(connection archivedConnection) settingsResult {
		return settingsResult{
			ConnectionID:       connection.id,
			ProviderName:       connection.providerName,
			GoogleHealthUserID: connection.googleHealthUserID,
		}
	},
	status:       func(result *settingsResult) string { return result.Status },
	setStatus:    func(result *settingsResult, status string) { result.Status = status },
	setMessage:   func(result *settingsResult, message string) { result.Message = message },
	writeResult:  writeSettingsResult,
	snapshotKind: snapshotKindSettings,
	fetchPayload: func(runtime runtimeAdapters, accessToken string) (googleSettings, error) {
		return runtime.fetchSettings(accessToken)
	},
	payloadRawJSON: func(payload googleSettings) string { return payload.rawJSON },
	finishArchived: func(result *settingsResult, snapshotID int64, fetchedAt string) {
		result.Status = "settings_archived"
		result.SnapshotID = snapshotID
		result.FetchedAt = fetchedAt
		result.Message = "Settings Snapshot archived"
	},
}

// fetchGoogleSettings is a thin call site over the shared Provider GET
// module (provider_get.go, issue #280), which owns the transport
// behavior: bearer auth, size limit, timeout, typed labeled status
// errors, JSON validity, and retry/Retry-After. The module value
// carries the HTTP doer (#281).
func fetchGoogleSettings(get providerGET, accessToken string) (googleSettings, error) {
	body, err := fetchProviderJSON(get, googleHealthSettingsURL, "settings", accessToken)
	if err != nil {
		return googleSettings{}, err
	}
	return googleSettings{rawJSON: string(body)}, nil
}

func writeSettingsResult(result settingsResult, mode outputMode, stdout io.Writer) error {
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
	if _, err := fmt.Fprintf(stdout, "Settings: %s\n", result.Status); err != nil {
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

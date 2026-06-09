package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"
)

const googleHealthSettingsURL = "https://health.googleapis.com/v4/users/me/settings"

// googleSettings is the raw payload returned by users.getSettings. We
// keep the raw JSON so a Normalized View can project new fields without
// a parser change; consumers that need a typed view read current_settings.
type googleSettings struct {
	rawJSON string
}

// fetchSettings is the seam tests stub. Production calls
// fetchGoogleSettings which hits the real Google Health API.
var fetchSettings = fetchGoogleSettings

type settingsResult struct {
	Status             string `json:"status"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	SnapshotID         int64  `json:"snapshot_id,omitempty"`
	FetchedAt          string `json:"fetched_at,omitempty"`
	Message            string `json:"message"`
}

func runSettingsWithRuntime(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("settings", flag.ContinueOnError)
	flags.SetOutput(stderr)

	// settings does no prompting and never blocks on browser input, so
	// --no-input would imply a behaviour the command does not have.
	// The Common Flag Set's pre-Parse scan turns a stray --no-input
	// into a targeted "--no-input is not supported by settings" message
	// (issue #171), so the help block and the runtime spec agree. The
	// accepted-flag list is sourced from the same identitySnapshotCommon-
	// FlagNames helper the registry uses, so runtime parsing and the
	// published schema cannot drift apart.
	common := RegisterCommon(flags, CommonFlagSpec{Accepted: identitySnapshotCommonFlagNames()}, CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		JSONOutput:  mode.json,
		PlainOutput: mode.plain,
	})

	if err := ParseCommon(flags, common, args); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "settings",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected settings argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	result, err := settingsSetupWithRuntime(common.ConfigPath, common.ArchivePath, runtime)
	if err != nil {
		if result.Status == "" {
			result.Status = "settings_failed"
		}
		result.Message = err.Error()
		if writeErr := writeSettingsResult(result, mode, stdout); writeErr != nil {
			return ReportFailure(FailureReport{
				Command: "settings",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	if err := writeSettingsResult(result, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "settings",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func settingsSetupWithRuntime(configPath, archivePath string, runtime runtimeAdapters) (settingsResult, error) {
	runtime = runtime.withDefaults()
	config, err := inspectIdentityConfig(configPath, archivePath)
	if err != nil {
		return settingsResult{}, fmt.Errorf("config check failed: %w", err)
	}
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return settingsResult{}, err
	}
	// archive is closed either by writeIdentitySnapshotHandoff (success
	// path) or by this deferred guard (any error before handoff).
	archiveClosed := false
	defer func() {
		if !archiveClosed {
			_ = archive.Close()
		}
	}()
	connection, err := archive.CurrentConnection()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return settingsResult{Status: "settings_unavailable"}, errors.New("no Connection found; run `gohealthcli connect` first")
		}
		return settingsResult{}, err
	}
	result := settingsResult{
		ConnectionID:       connection.id,
		ProviderName:       connection.providerName,
		GoogleHealthUserID: connection.googleHealthUserID,
	}
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{configPath, archivePath}, runtime)
	// users.getSettings is identity-level metadata, covered by the
	// existing profile.readonly scope; no separate settings.readonly
	// scope exists in the Google Health API today.
	accessToken, err := connectionAccess.AccessToken([]string{googleHealthProfileReadonlyScope})
	if err != nil {
		return result, err
	}
	settings, err := fetchSettings(accessToken)
	if err != nil {
		return result, currentConnectionProviderError(err)
	}
	fetchedAt := runtime.now().UTC().Format(time.RFC3339)
	snapshotID, err := writeIdentitySnapshotHandoff(archive, archivePath, connection, "settings", settings.rawJSON, fetchedAt)
	archiveClosed = true // handoff owns archive's lifecycle now
	if err != nil {
		return result, err
	}
	result.Status = "settings_archived"
	result.SnapshotID = snapshotID
	result.FetchedAt = fetchedAt
	result.Message = "Settings Snapshot archived"
	return result, nil
}

func fetchGoogleSettings(accessToken string) (googleSettings, error) {
	request, err := http.NewRequest(http.MethodGet, googleHealthSettingsURL, nil)
	if err != nil {
		return googleSettings{}, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return googleSettings{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return googleSettings{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return googleSettings{}, fmt.Errorf("Google Health settings request failed with HTTP %d", response.StatusCode)
	}
	if !json.Valid(body) {
		return googleSettings{}, errors.New("Google Health settings response is not valid JSON")
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

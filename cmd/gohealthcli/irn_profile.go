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

const googleHealthIRNProfileURL = "https://health.googleapis.com/v4/users/me/irnProfile"

// googleIRNProfile is the raw response from users.getIrnProfile. Slice
// C will project this through current_irn_profile.
type googleIRNProfile struct {
	rawJSON string
}

// fetchIRNProfile is the test seam.
var fetchIRNProfile = fetchGoogleIRNProfile

type irnProfileResult struct {
	Status             string `json:"status"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	SnapshotID         int64  `json:"snapshot_id,omitempty"`
	FetchedAt          string `json:"fetched_at,omitempty"`
	Message            string `json:"message"`
}

func runIRNProfileWithRuntime(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("irn-profile", flag.ContinueOnError)
	flags.SetOutput(stderr)

	irnConfigPath := flags.String("config", configPath, "config file path")
	irnArchivePath := flags.String("db", archivePath, "SQLite Health Archive path")
	irnJSONOutput := flags.Bool("json", mode.json, "write stable JSON to stdout")
	irnPlainOutput := flags.Bool("plain", mode.plain, "write plain key/value output to stdout")
	flags.Bool("no-input", false, "never prompt, never wait for browser input")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected irn-profile argument: %s\n", flags.Arg(0))
		return 1
	}

	mode = outputMode{json: *irnJSONOutput, plain: *irnPlainOutput}
	result, err := irnProfileSetupWithRuntime(*irnConfigPath, *irnArchivePath, runtime)
	if err != nil {
		if result.Status == "" {
			result.Status = "irn_profile_failed"
		}
		result.Message = err.Error()
		if writeErr := writeIRNProfileResult(result, mode, stdout); writeErr != nil {
			fmt.Fprintf(stderr, "write output: %v\n", writeErr)
			return 1
		}
		return 1
	}
	if err := writeIRNProfileResult(result, mode, stdout); err != nil {
		fmt.Fprintf(stderr, "write output: %v\n", err)
		return 1
	}
	return 0
}

func irnProfileSetupWithRuntime(configPath, archivePath string, runtime runtimeAdapters) (irnProfileResult, error) {
	runtime = runtime.withDefaults()
	config, err := inspectIdentityConfig(configPath, archivePath)
	if err != nil {
		return irnProfileResult{}, fmt.Errorf("config check failed: %w", err)
	}
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return irnProfileResult{}, err
	}
	archiveClosed := false
	defer func() {
		if !archiveClosed {
			_ = archive.Close()
		}
	}()
	connection, err := archive.CurrentConnection()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return irnProfileResult{Status: "irn_profile_unavailable"}, errors.New("no Connection found; run `gohealthcli connect` first")
		}
		return irnProfileResult{}, err
	}
	result := irnProfileResult{
		ConnectionID:       connection.id,
		ProviderName:       connection.providerName,
		GoogleHealthUserID: connection.googleHealthUserID,
	}
	// Fail fast if the IRN scope isn't on the stored Connection — calling
	// the API would 403 and the user would not know why. AC requires
	// this error to be the only thing the verb does in that case (no
	// browser flow, no upstream call).
	_, scopes, err := connectionTokenExpiryAndScopes(connection.tokenMetadataJSON)
	if err != nil {
		return result, err
	}
	if !scopeListContains(scopes, connectAddScopeKeywords["irn"]) {
		result.Status = "irn_profile_scope_missing"
		return result, errors.New("irn-profile requires the IRN OAuth scope; run `gohealthcli connect --add-scopes irn` to grant it")
	}
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{configPath, archivePath}, runtime)
	accessToken, err := connectionAccess.AccessToken([]string{connectAddScopeKeywords["irn"]})
	if err != nil {
		return result, err
	}
	irn, err := fetchIRNProfile(accessToken)
	if err != nil {
		return result, currentConnectionProviderError(err)
	}
	fetchedAt := runtime.now().UTC().Format(time.RFC3339)
	snapshotID, err := writeIdentitySnapshotHandoff(archive, archivePath, connection, "irn-profile", irn.rawJSON, fetchedAt)
	archiveClosed = true
	if err != nil {
		return result, err
	}
	result.Status = "irn_profile_archived"
	result.SnapshotID = snapshotID
	result.FetchedAt = fetchedAt
	result.Message = "IRN Profile Snapshot archived"
	return result, nil
}

func scopeListContains(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func fetchGoogleIRNProfile(accessToken string) (googleIRNProfile, error) {
	request, err := http.NewRequest(http.MethodGet, googleHealthIRNProfileURL, nil)
	if err != nil {
		return googleIRNProfile{}, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return googleIRNProfile{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return googleIRNProfile{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return googleIRNProfile{}, fmt.Errorf("Google Health irnProfile request failed with HTTP %d", response.StatusCode)
	}
	if !json.Valid(body) {
		return googleIRNProfile{}, errors.New("Google Health irnProfile response is not valid JSON")
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

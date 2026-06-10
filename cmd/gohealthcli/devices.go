package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const googleHealthPairedDevicesURL = "https://health.googleapis.com/v4/users/me/pairedDevices"

// googlePairedDevices is the raw response from users.pairedDevices.list.
// Keep the JSON body verbatim so the paired_devices Normalized View can
// project new fields without a parser change.
type googlePairedDevices struct {
	rawJSON string
}

// fetchPairedDevices is the seam tests stub. Production uses
// fetchGooglePairedDevices.
var fetchPairedDevices = fetchGooglePairedDevices

type devicesResult struct {
	Status             string                `json:"status"`
	ConnectionID       string                `json:"connection_id,omitempty"`
	ProviderName       string                `json:"provider_name,omitempty"`
	GoogleHealthUserID string                `json:"google_health_user_id,omitempty"`
	SnapshotID         int64                 `json:"snapshot_id,omitempty"`
	DeviceCount        int                   `json:"device_count"`
	Devices            []devicesResultDevice `json:"devices,omitempty"`
	FetchedAt          string                `json:"fetched_at,omitempty"`
	Message            string                `json:"message"`
}

// devicesResultDevice mirrors the columns the paired_devices Normalized
// View exposes; downstream tooling can read either surface and get the
// same fields without crossing layers.
type devicesResultDevice struct {
	DeviceType        string   `json:"device_type"`
	Model             string   `json:"model"`
	Manufacturer      string   `json:"manufacturer"`
	BatteryPercentage *int     `json:"battery_percentage,omitempty"`
	LastSyncTime      string   `json:"last_sync_time,omitempty"`
	Features          []string `json:"features,omitempty"`
}

func runDevicesWithRuntime(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("devices", flag.ContinueOnError)
	flags.SetOutput(stderr)

	// devices does no prompting and never blocks on browser input, so
	// --no-input would imply a behaviour the command does not have.
	// The Common Flag Set's pre-Parse scan turns a stray --no-input
	// into a targeted "--no-input is not supported by devices" message
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
			Command: "devices",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected devices argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	result, err := devicesSetupWithRuntime(common.ConfigPath, common.ArchivePath, runtime)
	if err != nil {
		if result.Status == "" {
			result.Status = "devices_failed"
		}
		result.Message = err.Error()
		if writeErr := writeDevicesResult(result, mode, stdout); writeErr != nil {
			return ReportFailure(FailureReport{
				Command: "devices",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", writeErr),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 1
	}
	if err := writeDevicesResult(result, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "devices",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func devicesSetupWithRuntime(configPath, archivePath string, runtime runtimeAdapters) (devicesResult, error) {
	runtime = runtime.withDefaults()
	config, err := inspectIdentityConfig(configPath, archivePath)
	if err != nil {
		return devicesResult{}, fmt.Errorf("config check failed: %w", err)
	}
	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return devicesResult{}, err
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
			return devicesResult{Status: "devices_unavailable"}, errors.New("no Connection found; run `gohealthcli connect` first")
		}
		return devicesResult{}, err
	}
	result := devicesResult{
		ConnectionID:       connection.id,
		ProviderName:       connection.providerName,
		GoogleHealthUserID: connection.googleHealthUserID,
	}
	// The deepened currentConnectionAccess pattern (PRD #142): wire
	// WithAutoRefresh when the OAuth client is a file source — the
	// archive handle openHealthArchiveConnectionAPI returned already
	// satisfies connectionTokenWriter — so an expired access token
	// refreshes and persists transparently, the way
	// sync_run_lifecycle.go already does. The required scope comes
	// from googleHealthIdentityEndpointScopes["pairedDevices"] so a
	// slice-2 revision of the catalog (PRD #142 #176) flows into
	// devices automatically. The scope pre-check happens inside
	// AccessToken via the errCurrentConnectionScopeMissing sentinel,
	// so we set the per-command status without re-implementing the
	// scope-list comparison locally.
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, []string{configPath, archivePath}, runtime)
	if config.oauthClient.kind == "file" {
		connectionAccess = connectionAccess.WithAutoRefresh(config.oauthClient, archive)
	}
	accessToken, err := connectionAccess.AccessToken(googleHealthIdentityEndpointScopes["pairedDevices"])
	if err != nil {
		if errors.Is(err, errCurrentConnectionScopeMissing) {
			result.Status = "devices_scope_missing"
		}
		return result, err
	}
	devices, err := fetchPairedDevices(accessToken)
	if err != nil {
		return result, currentConnectionProviderError(err)
	}
	result.Devices = parsePairedDeviceSummaries(devices.rawJSON)
	result.DeviceCount = len(result.Devices)
	fetchedAt := runtime.now().UTC().Format(time.RFC3339)
	snapshotID, err := writeIdentitySnapshotHandoff(archive, archivePath, connection, "paired-devices", devices.rawJSON, fetchedAt)
	archiveClosed = true
	if err != nil {
		return result, err
	}
	result.Status = "devices_archived"
	result.SnapshotID = snapshotID
	result.FetchedAt = fetchedAt
	result.Message = fmt.Sprintf("Paired Devices Snapshot archived (%d device(s))", result.DeviceCount)
	return result, nil
}

func fetchGooglePairedDevices(accessToken string) (googlePairedDevices, error) {
	request, err := http.NewRequest(http.MethodGet, googleHealthPairedDevicesURL, nil)
	if err != nil {
		return googlePairedDevices{}, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return googlePairedDevices{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return googlePairedDevices{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return googlePairedDevices{}, fmt.Errorf("Google Health pairedDevices request failed with HTTP %d", response.StatusCode)
	}
	if !json.Valid(body) {
		return googlePairedDevices{}, errors.New("Google Health pairedDevices response is not valid JSON")
	}
	return googlePairedDevices{rawJSON: string(body)}, nil
}

// parsePairedDeviceSummaries projects the raw users.pairedDevices.list
// payload into the same columns the paired_devices view exposes, so
// CLI output and SQL queries answer the same questions. The raw JSON
// stays the source of truth in identity_snapshots; this is purely
// for the result's user-facing rendering.
func parsePairedDeviceSummaries(rawJSON string) []devicesResultDevice {
	var envelope struct {
		Devices []struct {
			DeviceType        string   `json:"deviceType"`
			Model             string   `json:"model"`
			Manufacturer      string   `json:"manufacturer"`
			BatteryPercentage *int     `json:"batteryPercentage,omitempty"`
			LastSyncTime      string   `json:"lastSyncTime,omitempty"`
			Features          []string `json:"features,omitempty"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &envelope); err != nil {
		return nil
	}
	if len(envelope.Devices) == 0 {
		return nil
	}
	result := make([]devicesResultDevice, 0, len(envelope.Devices))
	for _, device := range envelope.Devices {
		result = append(result, devicesResultDevice{
			DeviceType:        device.DeviceType,
			Model:             device.Model,
			Manufacturer:      device.Manufacturer,
			BatteryPercentage: device.BatteryPercentage,
			LastSyncTime:      device.LastSyncTime,
			Features:          device.Features,
		})
	}
	return result
}

func writeDevicesResult(result devicesResult, mode outputMode, stdout io.Writer) error {
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
		if _, err := fmt.Fprintf(stdout, "device_count: %d\n", result.DeviceCount); err != nil {
			return err
		}
		for index, device := range result.Devices {
			prefix := fmt.Sprintf("devices.%d.", index)
			if _, err := fmt.Fprintf(stdout, "%sdevice_type: %s\n", prefix, device.DeviceType); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(stdout, "%smodel: %s\n", prefix, device.Model); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(stdout, "%smanufacturer: %s\n", prefix, device.Manufacturer); err != nil {
				return err
			}
			if device.BatteryPercentage != nil {
				if _, err := fmt.Fprintf(stdout, "%sbattery_percentage: %d\n", prefix, *device.BatteryPercentage); err != nil {
					return err
				}
			}
			if device.LastSyncTime != "" {
				if _, err := fmt.Fprintf(stdout, "%slast_sync_time: %s\n", prefix, device.LastSyncTime); err != nil {
					return err
				}
			}
			if len(device.Features) > 0 {
				if _, err := fmt.Fprintf(stdout, "%sfeatures: %s\n", prefix, strings.Join(device.Features, ",")); err != nil {
					return err
				}
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
	if _, err := fmt.Fprintf(stdout, "Paired Devices: %s\n", result.Status); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "Device count: %d\n", result.DeviceCount); err != nil {
		return err
	}
	for _, device := range result.Devices {
		battery := "?"
		if device.BatteryPercentage != nil {
			battery = fmt.Sprintf("%d%%", *device.BatteryPercentage)
		}
		lastSync := device.LastSyncTime
		if lastSync == "" {
			lastSync = "?"
		}
		if _, err := fmt.Fprintf(stdout, "- %s %s (%s) — battery %s, last sync %s\n", device.Manufacturer, device.Model, device.DeviceType, battery, lastSync); err != nil {
			return err
		}
	}
	if result.FetchedAt != "" {
		if _, err := fmt.Fprintf(stdout, "Fetched at: %s\n", result.FetchedAt); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "%s\n", result.Message)
	return err
}

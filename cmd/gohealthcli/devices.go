package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

const googleHealthPairedDevicesURL = "https://health.googleapis.com/v4/users/me/pairedDevices"

// googlePairedDevices is the raw response from users.pairedDevices.list.
// Keep the JSON body verbatim so the paired_devices Normalized View can
// project new fields without a parser change.
type googlePairedDevices struct {
	rawJSON string
}

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
// same fields without crossing layers. Field names follow the real
// users.pairedDevices.list payload verified against a live archive on
// 2026-06-11 (#298) — the API wraps the list in `pairedDevices` and
// emits name/deviceType/batteryStatus/batteryLevel/deviceVersion, not
// the model/manufacturer/batteryPercentage shape #98 assumed.
type devicesResultDevice struct {
	Name          string `json:"name"`
	DeviceType    string `json:"device_type"`
	DeviceVersion string `json:"device_version"`
	BatteryStatus string `json:"battery_status,omitempty"`
	BatteryLevel  *int   `json:"battery_level,omitempty"`
}

// devicesSnapshotCommand is devices' Identity Snapshot engine spec
// (issue #282). Its genuinely-unique decoration is the per-device
// summary parsing: decorate projects the raw users.pairedDevices.list
// payload into the result's Devices/DeviceCount before the snapshot
// handoff, so a handoff failure still reports what was fetched. The
// fetchPayload closure rides the runtime adapters' fetchPairedDevices
// seam, so tests inject fakes through the adapters value (#283).
//
// devices does no prompting and never blocks on browser input, so
// --no-input would imply a behaviour the command does not have. The
// Common Flag Set's pre-Parse scan turns a stray --no-input into a
// targeted "--no-input is not supported by devices" message (issue
// #171), so the help block and the runtime spec agree. The
// accepted-flag list is sourced from the same identitySnapshotCommon-
// FlagNames helper the registry uses, so runtime parsing and the
// published schema cannot drift apart.
var devicesSnapshotCommand = identitySnapshotCommandSpec[devicesResult, googlePairedDevices]{
	command: "devices",
	commonFlags: func() CommonFlagSpec {
		return CommonFlagSpec{Accepted: identitySnapshotCommonFlagNames()}
	},
	statusFailed:       "devices_failed",
	statusUnavailable:  "devices_unavailable",
	statusScopeMissing: "devices_scope_missing",
	scopeEndpointKey:   "pairedDevices",
	seedResult: func(connection archivedConnection) devicesResult {
		return devicesResult{
			ConnectionID:       connection.id,
			ProviderName:       connection.providerName,
			GoogleHealthUserID: connection.googleHealthUserID,
		}
	},
	status:       func(result *devicesResult) string { return result.Status },
	setStatus:    func(result *devicesResult, status string) { result.Status = status },
	setMessage:   func(result *devicesResult, message string) { result.Message = message },
	writeResult:  writeDevicesResult,
	snapshotKind: snapshotKindPairedDevices,
	fetchPayload: func(runtime runtimeAdapters, accessToken string) (googlePairedDevices, error) {
		return runtime.fetchPairedDevices(accessToken)
	},
	payloadRawJSON: func(payload googlePairedDevices) string { return payload.rawJSON },
	decorate: func(result *devicesResult, payload googlePairedDevices) {
		result.Devices = parsePairedDeviceSummaries(payload.rawJSON)
		result.DeviceCount = len(result.Devices)
	},
	finishArchived: func(result *devicesResult, snapshotID int64, fetchedAt string) {
		result.Status = "devices_archived"
		result.SnapshotID = snapshotID
		result.FetchedAt = fetchedAt
		result.Message = fmt.Sprintf("Paired Devices Snapshot archived (%d device(s))", result.DeviceCount)
	},
}

// fetchGooglePairedDevices is a thin call site over the shared
// Provider GET module (provider_get.go, issue #280), which owns the
// transport behavior: bearer auth, size limit, timeout, typed labeled
// status errors, JSON validity, and retry/Retry-After. The module
// value carries the HTTP doer (#281).
func fetchGooglePairedDevices(get providerGET, accessToken string) (googlePairedDevices, error) {
	body, err := fetchProviderJSON(context.Background(), get, googleHealthPairedDevicesURL, "pairedDevices", accessToken)
	if err != nil {
		return googlePairedDevices{}, err
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
		PairedDevices []struct {
			Name          string `json:"name"`
			DeviceType    string `json:"deviceType"`
			DeviceVersion string `json:"deviceVersion"`
			BatteryStatus string `json:"batteryStatus,omitempty"`
			BatteryLevel  *int   `json:"batteryLevel,omitempty"`
		} `json:"pairedDevices"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &envelope); err != nil {
		return nil
	}
	if len(envelope.PairedDevices) == 0 {
		return nil
	}
	result := make([]devicesResultDevice, 0, len(envelope.PairedDevices))
	for _, device := range envelope.PairedDevices {
		result = append(result, devicesResultDevice{
			Name:          device.Name,
			DeviceType:    device.DeviceType,
			DeviceVersion: device.DeviceVersion,
			BatteryStatus: device.BatteryStatus,
			BatteryLevel:  device.BatteryLevel,
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
			if _, err := fmt.Fprintf(stdout, "%sname: %s\n", prefix, device.Name); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(stdout, "%sdevice_type: %s\n", prefix, device.DeviceType); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(stdout, "%sdevice_version: %s\n", prefix, device.DeviceVersion); err != nil {
				return err
			}
			if device.BatteryStatus != "" {
				if _, err := fmt.Fprintf(stdout, "%sbattery_status: %s\n", prefix, device.BatteryStatus); err != nil {
					return err
				}
			}
			if device.BatteryLevel != nil {
				if _, err := fmt.Fprintf(stdout, "%sbattery_level: %d\n", prefix, *device.BatteryLevel); err != nil {
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
		switch {
		case device.BatteryStatus != "" && device.BatteryLevel != nil:
			battery = fmt.Sprintf("%s (%d%%)", device.BatteryStatus, *device.BatteryLevel)
		case device.BatteryLevel != nil:
			battery = fmt.Sprintf("%d%%", *device.BatteryLevel)
		case device.BatteryStatus != "":
			battery = device.BatteryStatus
		}
		if _, err := fmt.Fprintf(stdout, "- %s (%s) — battery %s\n", device.DeviceVersion, device.DeviceType, battery); err != nil {
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

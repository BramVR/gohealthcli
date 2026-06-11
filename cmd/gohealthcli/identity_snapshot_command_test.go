package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// identitySnapshotCommandCases enumerates the five Identity Snapshot
// family commands (issue #282) with the per-command status strings the
// engine extraction must preserve bit-for-bit. The table is the pin:
// every status here is asserted through the public run() seam, so a
// unification that collapses "irn_profile_unavailable" into a derived
// "irn-profile_unavailable" (or similar drift) fails loudly.
var identitySnapshotCommandCases = []struct {
	command           string
	statusUnavailable string
	statusFailed      string
}{
	{command: "devices", statusUnavailable: "devices_unavailable", statusFailed: "devices_failed"},
	{command: "settings", statusUnavailable: "settings_unavailable", statusFailed: "settings_failed"},
	{command: "irn-profile", statusUnavailable: "irn_profile_unavailable", statusFailed: "irn_profile_failed"},
	{command: "profile", statusUnavailable: "profile_unavailable", statusFailed: "profile_failed"},
	{command: "identity", statusUnavailable: "identity_unavailable", statusFailed: "identity_failed"},
}

// TestIdentitySnapshotCommandsReportUnavailableWithoutConnection pins
// the no-Connection path for every Identity Snapshot family command:
// exit 1, the per-command `<cmd>_unavailable` status, the shared
// "no Connection found; run `gohealthcli connect` first" message, and
// no connection_id leak in the JSON envelope. Prior to issue #282 only
// identity had this pin; the engine extraction must keep all five.
func TestIdentitySnapshotCommandsReportUnavailableWithoutConnection(t *testing.T) {
	for _, tc := range identitySnapshotCommandCases {
		t.Run(tc.command, func(t *testing.T) {
			tempDir := t.TempDir()
			configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
			installConnectFakes(t, fakeConnectConfig{failIfCalled: true})

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run([]string{tc.command, "--config", configPath, "--db", archivePath, "--json"}, stdout, stderr)
			if code != 1 {
				t.Fatalf("%s exit code = %d, want 1\nstdout: %s", tc.command, code, stdout.String())
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty in --json mode", stderr.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			assertJSONString(t, got, "status", tc.statusUnavailable)
			assertJSONString(t, got, "message", "no Connection found; run `gohealthcli connect` first")
			if _, ok := got["connection_id"]; ok {
				t.Fatalf("connection_id = %v, want omitted when no Connection exists", got["connection_id"])
			}
		})
	}
}

// TestIdentitySnapshotCommandsRejectUnexpectedArgument pins the
// Failure Reporter path for stray positionals on every Identity
// Snapshot family command: the unexpected_argument envelope with the
// per-command "unexpected <cmd> argument: <arg>" wording. devices
// already had this pin (failure_reporter_integration_test.go); the
// other four gain it here so the engine extraction cannot reword one
// sibling silently.
func TestIdentitySnapshotCommandsRejectUnexpectedArgument(t *testing.T) {
	for _, tc := range identitySnapshotCommandCases {
		t.Run(tc.command, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, "--json", tc.command, "unexpected-positional")
			if code == 0 {
				t.Fatalf("exit code = 0, want failure")
			}
			var envelope map[string]string
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("stdout is not a JSON envelope: %v\nstdout: %s", err, stdout.String())
			}
			if envelope["status"] != "unexpected_argument" {
				t.Errorf("status = %q, want unexpected_argument", envelope["status"])
			}
			wantMessage := "unexpected " + tc.command + " argument: unexpected-positional"
			if envelope["message"] != wantMessage {
				t.Errorf("message = %q, want %q", envelope["message"], wantMessage)
			}
			if stderr.String() != "" {
				t.Errorf("stderr = %q, want empty in --json failure mode", stderr.String())
			}
		})
	}
}

// TestIdentitySnapshotCommandsReportFailedStatusOnConfigError pins the
// statusless-error fallback for every Identity Snapshot family
// command: an error with no more-specific status (here: the config
// check failing on a missing config file) surfaces under the
// per-command `<cmd>_failed` status with the "config check failed: "
// wrapping preserved, exit 1.
func TestIdentitySnapshotCommandsReportFailedStatusOnConfigError(t *testing.T) {
	for _, tc := range identitySnapshotCommandCases {
		t.Run(tc.command, func(t *testing.T) {
			tempDir := t.TempDir()
			missingConfigPath := filepath.Join(tempDir, "config.toml")
			archivePath := filepath.Join(tempDir, "gohealthcli.sqlite")

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run([]string{tc.command, "--config", missingConfigPath, "--db", archivePath, "--json"}, stdout, stderr)
			if code != 1 {
				t.Fatalf("%s exit code = %d, want 1\nstdout: %s", tc.command, code, stdout.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			assertJSONString(t, got, "status", tc.statusFailed)
			message, ok := got["message"].(string)
			if !ok || !strings.HasPrefix(message, "config check failed: ") {
				t.Fatalf("message = %T(%v), want \"config check failed: \" prefix", got["message"], got["message"])
			}
		})
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestResultSpecificRemediationJSONPlainAndHumanParity(t *testing.T) {
	t.Parallel()
	actions := []string{"gohealthcli doctor", "gohealthcli connect"}
	tests := []struct {
		name  string
		write func(remediation []string, mode outputMode, stdout io.Writer) error
	}{
		{name: "connect", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeConnectResult(connectResult{Status: "connect_failed", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
		{name: "doctor", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeDoctorResult(doctorResult{Status: "connection_unhealthy", TokenStatus: "token_missing", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
		{name: "identity", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeIdentityResult(identityResult{Status: "identity_unavailable", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
		{name: "profile", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeProfileResult(profileResult{Status: "profile_unavailable", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
		{name: "settings", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeSettingsResult(settingsResult{Status: "settings_unavailable", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
		{name: "devices", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeDevicesResult(devicesResult{Status: "devices_unavailable", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
		{name: "irn-profile", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeIRNProfileResult(irnProfileResult{Status: "irn_profile_unavailable", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var jsonOutput bytes.Buffer
			if err := test.write(actions, outputMode{json: true}, &jsonOutput); err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Remediation []string `json:"remediation"`
			}
			if err := json.Unmarshal(jsonOutput.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if len(envelope.Remediation) != 2 || envelope.Remediation[0] != actions[0] || envelope.Remediation[1] != actions[1] {
				t.Fatalf("JSON remediation = %#v", envelope.Remediation)
			}
			var emptyJSON bytes.Buffer
			if err := test.write(nil, outputMode{json: true}, &emptyJSON); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(emptyJSON.String(), "remediation") {
				t.Fatalf("empty JSON remediation was not omitted: %s", emptyJSON.String())
			}

			var plain bytes.Buffer
			if err := test.write(actions, outputMode{plain: true}, &plain); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plain.String(), "remediation.0: gohealthcli doctor\n") || !strings.Contains(plain.String(), "remediation.1: gohealthcli connect\n") {
				t.Fatalf("plain remediation missing:\n%s", plain.String())
			}

			var humanBefore, humanAfter bytes.Buffer
			if err := test.write(nil, outputMode{}, &humanBefore); err != nil {
				t.Fatal(err)
			}
			if err := test.write(actions, outputMode{}, &humanAfter); err != nil {
				t.Fatal(err)
			}
			if humanAfter.String() != humanBefore.String() {
				t.Fatalf("human output changed: before=%q after=%q", humanBefore.String(), humanAfter.String())
			}
		})
	}
}

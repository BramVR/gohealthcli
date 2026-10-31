package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
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
		{name: "sync", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeSyncResult(syncResult{Status: "sync_failed", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
		{name: "query", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeQueryResult(queryResult{Status: "query_failed", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
		{name: "status", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeStatusResult(statusResult{Status: "status_failed", Message: "safe", Remediation: remediation}, mode, stdout)
		}},
		{name: "sync-status", write: func(remediation []string, mode outputMode, stdout io.Writer) error {
			return writeSyncStatusResult(syncStatusResult{Status: "sync_status_failed", Message: "safe", Remediation: remediation}, mode, time.Time{}, stdout)
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

func TestSyncFanOutRemediationStaysOnAffectedChild(t *testing.T) {
	t.Parallel()
	results := []syncResult{
		{Status: "sync_completed", DataTypes: []string{"steps"}, Message: "safe success"},
		{Status: "sync_failed", DataTypes: []string{"heart-rate"}, Message: "safe failure", Remediation: []string{"gohealthcli doctor", "gohealthcli connect"}},
	}

	var jsonOutput bytes.Buffer
	if err := writeSyncFanOutResult(results, syncCommandOptions{}, outputMode{json: true}, &jsonOutput); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Remediation []string `json:"remediation"`
		Results     []struct {
			Remediation []string `json:"remediation"`
		} `json:"results"`
	}
	if err := json.Unmarshal(jsonOutput.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Remediation) != 0 {
		t.Fatalf("wrapper remediation = %#v, want absent", envelope.Remediation)
	}
	if len(envelope.Results) != 2 || len(envelope.Results[0].Remediation) != 0 || len(envelope.Results[1].Remediation) != 2 {
		t.Fatalf("child remediation = %#v, want only results[1]", envelope.Results)
	}

	var plain bytes.Buffer
	if err := writeSyncFanOutResult(results, syncCommandOptions{}, outputMode{plain: true}, &plain); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "results.0.remediation") || !strings.Contains(plain.String(), "results.1.remediation.0: gohealthcli doctor\n") || !strings.Contains(plain.String(), "results.1.remediation.1: gohealthcli connect\n") {
		t.Fatalf("plain remediation leaked or missing:\n%s", plain.String())
	}

	var humanBefore, humanAfter bytes.Buffer
	without := append([]syncResult(nil), results...)
	without[1].Remediation = nil
	if err := writeSyncFanOutResult(without, syncCommandOptions{}, outputMode{}, &humanBefore); err != nil {
		t.Fatal(err)
	}
	if err := writeSyncFanOutResult(results, syncCommandOptions{}, outputMode{}, &humanAfter); err != nil {
		t.Fatal(err)
	}
	if humanAfter.String() != humanBefore.String() {
		t.Fatalf("human fan-out changed: before=%q after=%q", humanBefore.String(), humanAfter.String())
	}
}

func TestSyncAndArchiveFailureRemediationUsesTypedReviewedCausesOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{name: "missing archive", err: os.ErrNotExist, want: []string{"gohealthcli doctor", "gohealthcli init"}},
		{name: "rejected token", err: googlehealth.ErrUnauthorized, want: []string{"gohealthcli doctor --online", "gohealthcli connect"}},
		{name: "identity mismatch", err: errHealthArchiveIdentityMismatch, want: []string{"gohealthcli doctor --online", "gohealthcli init --help"}},
		{name: "canceled", err: googlehealth.ErrSyncCanceled},
		{name: "corrupt archive", err: errors.New("database disk image is malformed")},
		{name: "unknown provider", err: errors.New("upstream unavailable")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wrapped := syncFailureRemediation(test.err)
			if !errors.Is(wrapped, test.err) {
				t.Fatalf("classified error lost cause: %v", wrapped)
			}
			got := remediationFromError(wrapped)
			if len(got) != len(test.want) {
				t.Fatalf("remediation = %#v, want %#v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("remediation = %#v, want %#v", got, test.want)
				}
			}
		})
	}
}

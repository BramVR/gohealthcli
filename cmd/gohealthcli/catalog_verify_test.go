package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogVerifyOfflineFixtureJSON(t *testing.T) {
	t.Parallel()

	fixture := filepath.Join("..", "..", "internal", "googlehealth", "testdata", "google-health-discovery-v4.json")
	code, stdout, stderr := runCommand(t, "catalog", "verify", "--discovery", fixture, "--json")
	if code != 0 {
		t.Fatalf("catalog verify exit code = %d, want 0; stderr=%q; stdout=%q", code, stderr.String(), stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got struct {
		Status            string `json:"status"`
		Source            string `json:"source"`
		DiscoveryRevision string `json:"discovery_revision"`
		KnownGaps         []struct {
			Kind      string   `json:"kind"`
			DataTypes []string `json:"data_types"`
		} `json:"known_gaps"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON output: %v\noutput: %s", err, stdout.String())
	}
	if got.Status != "verified_with_known_gaps" {
		t.Errorf("status = %q, want verified_with_known_gaps", got.Status)
	}
	if got.Source != "file" {
		t.Errorf("source = %q, want file", got.Source)
	}
	if got.DiscoveryRevision != "20260817" {
		t.Errorf("discovery_revision = %q, want 20260817", got.DiscoveryRevision)
	}
	wantGaps := []struct {
		Kind      string   `json:"kind"`
		DataTypes []string `json:"data_types"`
	}{
		{Kind: "local_rollup_only", DataTypes: []string{"calories-in-heart-rate-zone", "total-calories"}},
		{Kind: "upstream_raw_only", DataTypes: []string{"food", "food-measurement-unit"}},
		{Kind: "upstream_write_only", DataTypes: []string{"menstrual-period", "moods", "ovulation-test", "symptoms"}},
	}
	if !reflect.DeepEqual(got.KnownGaps, wantGaps) {
		t.Errorf("known_gaps = %#v, want %#v", got.KnownGaps, wantGaps)
	}
}

func TestCatalogVerifyOutputModes(t *testing.T) {
	t.Parallel()

	fixture, err := filepath.Abs(filepath.Join("..", "..", "internal", "googlehealth", "testdata", "google-health-discovery-v4.json"))
	if err != nil {
		t.Fatalf("absolute fixture path: %v", err)
	}

	code, humanStdout, humanStderr := runCommand(t, "catalog", "verify", "--discovery", fixture)
	if code != 0 || humanStderr.Len() != 0 {
		t.Fatalf("human exit=%d stderr=%q", code, humanStderr.String())
	}
	wantHuman := "Google Health catalog verified with known gaps.\n" +
		"Discovery source: file (revision 20260817)\n" +
		"Known gap local_rollup_only: calories-in-heart-rate-zone, total-calories\n" +
		"Known gap upstream_raw_only: food, food-measurement-unit\n" +
		"Known gap upstream_write_only: menstrual-period, moods, ovulation-test, symptoms\n" +
		"Unverifiable filter_fields: the discovery document describes shared filters but not each Data Type's accepted filter field\n" +
		"Unverifiable operation_support: the discovery document lists shared methods but not exact per-Data-Type operation support\n"
	if humanStdout.String() != wantHuman {
		t.Errorf("human stdout = %q, want %q", humanStdout.String(), wantHuman)
	}

	code, plainStdout, plainStderr := runCommand(t, "catalog", "verify", "--discovery", fixture, "--plain")
	if code != 0 || plainStderr.Len() != 0 {
		t.Fatalf("plain exit=%d stderr=%q", code, plainStderr.String())
	}
	wantPlain := "status: verified_with_known_gaps\n" +
		"source: file\n" +
		"discovery_revision: 20260817\n" +
		"known_gap.0.kind: local_rollup_only\n" +
		"known_gap.0.data_types: calories-in-heart-rate-zone,total-calories\n" +
		"known_gap.1.kind: upstream_raw_only\n" +
		"known_gap.1.data_types: food,food-measurement-unit\n" +
		"known_gap.2.kind: upstream_write_only\n" +
		"known_gap.2.data_types: menstrual-period,moods,ovulation-test,symptoms\n" +
		"unverifiable.0.fact: filter_fields\n" +
		"unverifiable.0.reason: the discovery document describes shared filters but not each Data Type's accepted filter field\n" +
		"unverifiable.1.fact: operation_support\n" +
		"unverifiable.1.reason: the discovery document lists shared methods but not exact per-Data-Type operation support\n"
	if plainStdout.String() != wantPlain {
		t.Errorf("plain stdout = %q, want %q", plainStdout.String(), wantPlain)
	}
}

func TestCatalogVerifyTimeoutIsStableNonzeroDrift(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", request.Method)
		}
		if request.URL.String() != catalogDiscoveryURL {
			t.Errorf("URL = %q, want %q", request.URL.String(), catalogDiscoveryURL)
		}
		if request.Header.Get("Authorization") != "" {
			t.Errorf("Authorization header was set")
		}
		return nil, context.DeadlineExceeded
	})}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"catalog", "verify", "--json"}, stdout, stderr, runtimeAdapters{httpDoer: client})
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	want := "{\"status\":\"drift_detected\",\"source\":\"live\",\"drift\":[{\"kind\":\"discovery_unavailable\"}]}\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestCatalogVerifyMalformedAndDriftExitNonzero(t *testing.T) {
	t.Parallel()

	malformed := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(malformed, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatalf("write malformed fixture: %v", err)
	}
	code, stdout, stderr := runCommand(t, "catalog", "verify", "--discovery", malformed, "--json")
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status":"drift_detected"`) || !strings.Contains(stdout.String(), `"kind":"discovery_invalid"`) {
		t.Errorf("stdout = %q, want stable malformed drift", stdout.String())
	}
}

func TestCatalogVerifyDoesNotRequireHomeConfigOrArchive(t *testing.T) {
	t.Parallel()

	fixture, err := filepath.Abs(filepath.Join("..", "..", "internal", "googlehealth", "testdata", "google-health-discovery-v4.json"))
	if err != nil {
		t.Fatalf("absolute fixture path: %v", err)
	}
	code, stdout, stderr := runBinaryWithEnv(t, []string{"HOME=", "XDG_CONFIG_HOME=", "XDG_DATA_HOME="}, "catalog", "verify", "--discovery", fixture, "--json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"status":"verified_with_known_gaps"`) {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestCatalogVerifyRejectsJSONAndPlainTogether(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCommand(t, "catalog", "verify", "--json", "--plain")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--plain and --json are mutually exclusive") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestCatalogMissingActionPreservesGlobalJSONMode(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "--json", "catalog")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	want := "{\"status\":\"unexpected_argument\",\"message\":\"expected action: list, scopes, verify, or describe\"}\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestCatalogVerifyAcceptsFlagsBeforeAction(t *testing.T) {
	t.Parallel()

	fixture := filepath.Join("..", "..", "internal", "googlehealth", "testdata", "google-health-discovery-v4.json")
	code, stdout, stderr := runCommand(t, "catalog", "--json", "--discovery", fixture, "verify")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status":"verified_with_known_gaps"`) {
		t.Errorf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestCatalogFlagArgsPreservesDiscoveryValuesNamedLikeActions(t *testing.T) {
	t.Parallel()

	for _, flagName := range []string{"-discovery", "--discovery"} {
		for _, path := range []string{"list", "scopes", "describe"} {
			gotArgs, gotAction := catalogFlagArgs([]string{flagName, path, "verify"})
			if gotAction != "verify" {
				t.Errorf("catalogFlagArgs(%q, %q, verify) action = %q, want verify", flagName, path, gotAction)
			}
			if len(gotArgs) != 2 || gotArgs[0] != flagName || gotArgs[1] != path {
				t.Errorf("catalogFlagArgs(%q, %q, verify) args = %v, want [%s %s]", flagName, path, gotArgs, flagName, path)
			}
		}
	}
}

func TestCatalogMissingActionPreservesCatalogJSONMode(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "catalog", "--json")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	want := "{\"status\":\"unexpected_argument\",\"message\":\"expected action: list, scopes, verify, or describe\"}\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestCatalogListJSONContract(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "catalog", "list", "--json")
	if code != 0 {
		t.Fatalf("catalog list exit code = %d, want 0; stderr=%q; stdout=%q", code, stderr.String(), stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got struct {
		DataTypes []struct {
			DataType       string   `json:"data_type"`
			Selection      string   `json:"selection"`
			RawDataPoints  string   `json:"raw_data_points"`
			RequiredScopes []string `json:"required_scopes"`
		} `json:"data_types"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON output: %v\noutput: %s", err, stdout.String())
	}
	if len(got.DataTypes) == 0 {
		t.Fatal("data_types is empty")
	}
	first := got.DataTypes[0]
	if first.DataType != "steps" {
		t.Errorf("data_types[0].data_type = %q, want steps", first.DataType)
	}
	if first.Selection != "default" {
		t.Errorf("data_types[0].selection = %q, want default", first.Selection)
	}
	if first.RawDataPoints != "supported" {
		t.Errorf("data_types[0].raw_data_points = %q, want supported", first.RawDataPoints)
	}
	if len(first.RequiredScopes) != 1 || first.RequiredScopes[0] == "" {
		t.Errorf("data_types[0].required_scopes = %v, want one exact scope", first.RequiredScopes)
	}
	wantStates := map[string][2]string{
		"total-calories":              {"default", "unsupported"},
		"calories-in-heart-rate-zone": {"opt_in", "unsupported"},
	}
	for _, dataType := range got.DataTypes {
		want, ok := wantStates[dataType.DataType]
		if !ok {
			continue
		}
		if dataType.Selection != want[0] || dataType.RawDataPoints != want[1] {
			t.Errorf("%s selection/raw_data_points = %s/%s, want %s/%s", dataType.DataType, dataType.Selection, dataType.RawDataPoints, want[0], want[1])
		}
		delete(wantStates, dataType.DataType)
	}
	if len(wantStates) != 0 {
		t.Errorf("JSON output missing Data Types: %v", wantStates)
	}
}

func TestCatalogScopesJSONContractWithoutProviderAccess(t *testing.T) {
	t.Parallel()

	providerCalled := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("Provider access is forbidden")
	})}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"catalog", "scopes", "--json"}, stdout, stderr, runtimeAdapters{httpDoer: client})
	if code != 0 {
		t.Fatalf("catalog scopes exit code = %d, want 0; stderr=%q; stdout=%q", code, stderr.String(), stdout.String())
	}
	if providerCalled {
		t.Fatal("catalog scopes accessed the Provider")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got struct {
		Scopes []struct {
			Scope     string   `json:"scope"`
			DataTypes []string `json:"data_types"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON output: %v\noutput: %s", err, stdout.String())
	}
	if len(got.Scopes) == 0 {
		t.Fatal("scopes is empty")
	}
	first := got.Scopes[0]
	if first.Scope != "https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly" {
		t.Errorf("scopes[0].scope = %q, want activity scope", first.Scope)
	}
	if len(first.DataTypes) == 0 || first.DataTypes[0] != "steps" {
		t.Errorf("scopes[0].data_types = %v, want catalog-ordered membership starting with steps", first.DataTypes)
	}
	if strings.Contains(stdout.String(), "granted") {
		t.Errorf("stdout claims a grant state: %s", stdout.String())
	}
}

func TestCatalogListHumanAndPlainContracts(t *testing.T) {
	t.Parallel()

	code, humanStdout, humanStderr := runCommand(t, "catalog", "list")
	if code != 0 || humanStderr.Len() != 0 {
		t.Fatalf("human exit=%d stderr=%q stdout=%q", code, humanStderr.String(), humanStdout.String())
	}
	if !strings.HasPrefix(humanStdout.String(), "Google Health Data Types (") {
		t.Errorf("human stdout missing heading: %q", humanStdout.String())
	}
	for _, want := range []string{
		"- steps: default; raw Data Points supported; scope https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly\n",
		"- total-calories: default; raw Data Points unsupported; scope https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly\n",
		"- calories-in-heart-rate-zone: opt-in; raw Data Points unsupported; scope https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly\n",
	} {
		if !strings.Contains(humanStdout.String(), want) {
			t.Errorf("human stdout missing %q\nstdout=%s", want, humanStdout.String())
		}
	}

	code, plainStdout, plainStderr := runCommand(t, "catalog", "list", "--plain")
	if code != 0 || plainStderr.Len() != 0 {
		t.Fatalf("plain exit=%d stderr=%q stdout=%q", code, plainStderr.String(), plainStdout.String())
	}
	wantPlainPrefix := "data_type.0.data_type: steps\n" +
		"data_type.0.selection: default\n" +
		"data_type.0.raw_data_points: supported\n" +
		"data_type.0.required_scopes: https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly\n"
	if !strings.HasPrefix(plainStdout.String(), wantPlainPrefix) {
		t.Errorf("plain stdout prefix = %q, want %q", plainStdout.String(), wantPlainPrefix)
	}
}

func TestCatalogScopesHumanAndPlainContracts(t *testing.T) {
	t.Parallel()

	code, humanStdout, humanStderr := runCommand(t, "catalog", "scopes")
	if code != 0 || humanStderr.Len() != 0 {
		t.Fatalf("human exit=%d stderr=%q stdout=%q", code, humanStderr.String(), humanStdout.String())
	}
	wantHumanPrefix := "Google Health OAuth Scopes (6)\n" +
		"- https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly\n" +
		"  Data Types: steps, exercise, distance, total-calories"
	if !strings.HasPrefix(humanStdout.String(), wantHumanPrefix) {
		t.Errorf("human stdout prefix = %q, want %q", humanStdout.String(), wantHumanPrefix)
	}
	if strings.Contains(humanStdout.String(), "granted") {
		t.Errorf("human stdout claims a grant state: %s", humanStdout.String())
	}

	code, plainStdout, plainStderr := runCommand(t, "catalog", "scopes", "--plain")
	if code != 0 || plainStderr.Len() != 0 {
		t.Fatalf("plain exit=%d stderr=%q stdout=%q", code, plainStderr.String(), plainStdout.String())
	}
	wantPlainPrefix := "scope.0.scope: https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly\n" +
		"scope.0.data_types: steps,exercise,distance,total-calories"
	if !strings.HasPrefix(plainStdout.String(), wantPlainPrefix) {
		t.Errorf("plain stdout prefix = %q, want %q", plainStdout.String(), wantPlainPrefix)
	}
}

func TestCatalogRegistryDocumentsBrowseActions(t *testing.T) {
	t.Parallel()

	definition, ok := lookupCommand("catalog")
	if !ok {
		t.Fatal("catalog command missing from registry")
	}
	if definition.Short != "Browse and verify the compiled Provider catalog." {
		t.Errorf("catalog short = %q", definition.Short)
	}
	if definition.PositionalArgs != "<list|scopes|verify>" {
		t.Errorf("catalog positional_args = %q, want <list|scopes|verify>", definition.PositionalArgs)
	}
	for _, phrase := range []string{
		"`catalog list`",
		"`catalog scopes`",
		"`catalog verify`",
		"without config, a Health Archive, Connection, credentials, or network access",
	} {
		if !strings.Contains(definition.Long, phrase) {
			t.Errorf("catalog long help missing %q", phrase)
		}
	}

	code, _, stderr := runCommand(t, "help", "catalog")
	if code != 0 {
		t.Fatalf("help catalog exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), definition.Long) {
		t.Errorf("help catalog does not render registry long description: %q", stderr.String())
	}
}

func TestCatalogBrowseActionsUseNoRuntimeOrLocalState(t *testing.T) {
	t.Parallel()

	var accessed []string
	forbidden := func(name string) error {
		accessed = append(accessed, name)
		return errors.New(name + " access is forbidden")
	}
	runtime := runtimeAdapters{
		httpDoer: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, forbidden("network")
		})},
		runOAuthFlow: func(oauthClientConfig, []string, bool) (oauthTokenResponse, error) {
			return oauthTokenResponse{}, forbidden("OAuth")
		},
		refreshOAuthToken: func(oauthClientConfig, string, []string) (oauthTokenResponse, error) {
			return oauthTokenResponse{}, forbidden("token refresh")
		},
		openBrowser: func(context.Context, string) error { return forbidden("browser") },
		fetchIdentity: func(string) (googleIdentity, error) {
			return googleIdentity{}, forbidden("Connection identity")
		},
		fetchProfile: func(string) (googleProfile, error) {
			return googleProfile{}, forbidden("profile")
		},
		fetchPairedDevices: func(string) (googlePairedDevices, error) {
			return googlePairedDevices{}, forbidden("paired devices")
		},
		fetchSettings: func(string) (googleSettings, error) {
			return googleSettings{}, forbidden("settings")
		},
		fetchIRNProfile: func(string) (googleIRNProfile, error) {
			return googleIRNProfile{}, forbidden("IRN profile")
		},
		fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
			return nil, forbidden("Provider")
		},
		openHealthArchiveWriter: func(string) (healthArchiveWriter, error) {
			return nil, forbidden("Health Archive writer")
		},
		openSyncPlanningArchive: func(context.Context, string) (syncPlanningArchive, error) {
			return nil, forbidden("Health Archive reader")
		},
		runSecurityFindGenericPassword: func(context.Context, string, string) ([]byte, error) {
			return nil, forbidden("macOS Credential Store")
		},
		runSecretToolLookup: func(context.Context, string, string) ([]byte, error) {
			return nil, forbidden("Linux Credential Store")
		},
		runWindowsCredentialRead: func(context.Context, string, string) ([]byte, error) {
			return nil, forbidden("Windows Credential Store")
		},
	}
	missingRoot := filepath.Join(t.TempDir(), "must-not-exist")
	for _, action := range []string{"list", "scopes"} {
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		args := []string{
			"--config", filepath.Join(missingRoot, "config.toml"),
			"--db", filepath.Join(missingRoot, "archive.sqlite"),
			"--json", "catalog", action,
		}
		if code := runWithRuntime(args, stdout, stderr, runtime); code != 0 {
			t.Fatalf("catalog %s exit code = %d; stderr=%q; stdout=%q", action, code, stderr.String(), stdout.String())
		}
	}
	if len(accessed) != 0 {
		t.Fatalf("catalog browse actions accessed forbidden runtime adapters: %v", accessed)
	}
}

func TestCatalogBrowseActionsPropagateWriteErrors(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"list", "scopes"} {
		for _, modeArg := range []string{"", "--plain", "--json"} {
			name := action + "/human"
			args := []string{action}
			if modeArg != "" {
				name = action + "/" + strings.TrimPrefix(modeArg, "--")
				args = append(args, modeArg)
			}
			t.Run(name, func(t *testing.T) {
				stderr := new(bytes.Buffer)
				if code := runCatalogWithRuntime(args, CommonFlagValues{}, failingWriter{}, stderr, runtimeAdapters{}); code == 0 {
					t.Fatalf("catalog %v exit code = 0, want write failure", args)
				}
			})
		}
	}
}

func TestCatalogBrowseBinaryNeedsNoHomeConfigOrArchive(t *testing.T) {
	t.Parallel()

	env := []string{"HOME=", "XDG_CONFIG_HOME=", "XDG_DATA_HOME="}
	for _, action := range []string{"list", "scopes"} {
		code, stdout, stderr := runBinaryWithEnv(t, env, "catalog", action, "--json")
		if code != 0 {
			t.Fatalf("catalog %s exit code = %d; stderr=%q; stdout=%q", action, code, stderr.String(), stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("catalog %s stderr = %q, want empty", action, stderr.String())
		}
		if !json.Valid(stdout.Bytes()) {
			t.Fatalf("catalog %s stdout is not JSON: %q", action, stdout.String())
		}
	}
}

func TestCatalogBrowseActionsAcceptFlagsBeforeAction(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"list", "scopes"} {
		code, stdout, stderr := runCommand(t, "catalog", "--json", action)
		if code != 0 {
			t.Fatalf("catalog --json %s exit code = %d; stderr=%q; stdout=%q", action, code, stderr.String(), stdout.String())
		}
		if stderr.Len() != 0 || !json.Valid(stdout.Bytes()) {
			t.Fatalf("catalog --json %s stderr=%q stdout=%q", action, stderr.String(), stdout.String())
		}
	}
}

func TestCatalogBrowseActionsRejectVerifyOnlyDiscoveryFlag(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"list", "scopes"} {
		code, stdout, stderr := runCommand(t, "catalog", action, "--discovery", "unused.json", "--json")
		if code != 1 {
			t.Fatalf("catalog %s --discovery exit code = %d, want 1", action, code)
		}
		want := "{\"status\":\"flag_invalid\",\"message\":\"--discovery is supported only by catalog verify\"}\n"
		if stdout.String() != want || stderr.Len() != 0 {
			t.Errorf("catalog %s --discovery stdout=%q stderr=%q, want stdout=%q and empty stderr", action, stdout.String(), stderr.String(), want)
		}
	}
}

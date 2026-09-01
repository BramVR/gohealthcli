package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestRawPlanJSONUsesProductionRequestFactsWithoutEffects(t *testing.T) {
	t.Parallel()
	runtime := forbiddenRawPlanRuntime(t)
	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "heart-rate",
		"--from", "yesterday",
		"--to", "today",
		"--timezone", "Europe/Brussels",
		"--page-size", "12",
		"--page-token", "synthetic-page-secret",
		"--plan", "--json",
		"--config", "/synthetic/missing-config",
		"--db", "/synthetic/missing-archive",
	}, &stdout, &stderr, runtime)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if strings.Contains(stdout.String(), "synthetic-page-secret") {
		t.Fatal("raw plan exposed the page token")
	}

	var got struct {
		Status string `json:"status"`
		Target struct {
			Kind         string `json:"kind"`
			EndpointName string `json:"endpoint_name"`
			DataType     string `json:"data_type"`
		} `json:"target"`
		Request struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"request"`
		RequiredScopes []string `json:"required_scopes"`
		Range          struct {
			From       string `json:"from"`
			To         string `json:"to"`
			Timezone   string `json:"timezone"`
			ResolvedAt string `json:"resolved_at"`
		} `json:"range"`
		Paging struct {
			PageSize          int64 `json:"page_size"`
			PageTokenProvided bool  `json:"page_token_provided"`
		} `json:"paging"`
		PlanningEffects map[string]bool `json:"planning_effects"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if got.Status != "plan_ready" || got.Target.Kind != "data-type" || got.Target.EndpointName != "dataTypes.heart-rate.list" || got.Target.DataType != "heart-rate" {
		t.Fatalf("target = %+v, status = %q", got.Target, got.Status)
	}
	if got.Request.Method != "GET" || got.Request.Headers["Accept"] != "application/json" {
		t.Fatalf("request = %+v, want GET with JSON Accept", got.Request)
	}
	if strings.Contains(got.Request.URL, "synthetic-page-secret") || !strings.Contains(got.Request.URL, "REDACTED") {
		t.Fatalf("planned URL is not sanitized: %s", got.Request.URL)
	}
	if got.Range.From != "2026-03-28T23:00:00Z" || got.Range.To != "2026-03-29T22:00:00Z" || got.Range.Timezone != "Europe/Brussels" || got.Range.ResolvedAt == "" {
		t.Fatalf("range = %+v", got.Range)
	}
	if got.Paging.PageSize != 12 || !got.Paging.PageTokenProvided {
		t.Fatalf("paging = %+v", got.Paging)
	}
	if len(got.RequiredScopes) == 0 {
		t.Fatal("required_scopes is empty")
	}
	wantEffects := []string{
		"provider_request",
		"credential_store_read",
		"token_load",
		"token_refresh",
		"health_archive_open",
		"health_archive_write",
		"migration",
		"cursor_change",
		"sidecar_creation",
	}
	for _, name := range wantEffects {
		value, ok := got.PlanningEffects[name]
		if !ok || value {
			t.Errorf("planning_effects.%s = %t, present %t; want false", name, value, ok)
		}
	}
}

func TestRawPlanSupportsEveryCanonicalTarget(t *testing.T) {
	t.Parallel()
	runtime := forbiddenRawPlanRuntime(t)
	targets := make([][]string, 0, len(googlehealth.RawEndpointNames())+len(googlehealth.ListableDataTypes()))
	for _, endpoint := range googlehealth.RawEndpointNames() {
		targets = append(targets, []string{"endpoint", endpoint})
	}
	for _, dataType := range googlehealth.ListableDataTypes() {
		targets = append(targets, []string{"data-type", dataType})
	}
	for _, target := range targets {
		target := target
		t.Run(strings.Join(target, "/"), func(t *testing.T) {
			args := append([]string{"raw"}, target...)
			if target[0] == "data-type" || strings.HasPrefix(target[1], "dataTypes.") {
				args = append(args, "--from", "2026-01-01", "--timezone", "UTC")
			}
			args = append(args, "--plan", "--json", "--config", "/synthetic/missing-config", "--db", "/synthetic/missing-archive")
			var stdout, stderr bytes.Buffer
			if code := runWithRuntime(args, &stdout, &stderr, runtime); code != 0 {
				t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
			}
			if !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
				t.Fatalf("invalid output\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRawPlanHumanPlainAndJSONModes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		flag string
		want string
	}{
		{name: "human", want: "Raw request plan: plan_ready\n"},
		{name: "plain", flag: "--plain", want: "planning_effects.health_archive_open: false\n"},
		{name: "json", flag: "--json", want: `"health_archive_open": false`},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"raw", "endpoint", "getIdentity", "--plan"}
			if test.flag != "" {
				args = append(args, test.flag)
			}
			var stdout, stderr bytes.Buffer
			if code := runWithRuntime(args, &stdout, &stderr, forbiddenRawPlanRuntime(t)); code != 0 {
				t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) || stderr.Len() != 0 {
				t.Fatalf("stdout = %q, want %q; stderr = %q", stdout.String(), test.want, stderr.String())
			}
		})
	}
}

func TestRawPlanReadsOnlyConfiguredTimezoneWhenNeeded(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatalf("secure synthetic config directory: %v", err)
	}
	configPath := filepath.Join(directory, "config.toml")
	content := "archive_path = \"/synthetic/archive\"\ntimezone = \"America/New_York\"\n[oauth_client]\nsource = \"ignored\"\n[credential_store]\ntype = \"ignored\"\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write synthetic config: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "steps", "--from", "yesterday", "--to", "today",
		"--plan", "--json", "--config", configPath, "--db", "/synthetic/missing-archive",
	}, &stdout, &stderr, forbiddenRawPlanRuntime(t))
	if code != 0 {
		t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"timezone": "America/New_York"`) {
		t.Fatalf("stdout = %s, want configured timezone", stdout.String())
	}
}

func TestRawPlanLeavesArchiveAndSidecarPathsUntouched(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "health.sqlite")
	archiveBytes := []byte("synthetic archive sentinel")
	if err := os.WriteFile(archivePath, archiveBytes, 0o600); err != nil {
		t.Fatalf("write archive sentinel: %v", err)
	}
	sidecarPath := attachmentRootDirForArchive(archivePath)
	if err := os.Mkdir(sidecarPath, 0o700); err != nil {
		t.Fatalf("create sidecar sentinel directory: %v", err)
	}
	sidecarSentinel := filepath.Join(sidecarPath, "sentinel")
	if err := os.WriteFile(sidecarSentinel, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("write sidecar sentinel: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "steps", "--from", "2026-01-01", "--timezone", "UTC",
		"--plan", "--json", "--config", filepath.Join(directory, "missing-config"), "--db", archivePath,
	}, &stdout, &stderr, forbiddenRawPlanRuntime(t))
	if code != 0 {
		t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	gotArchive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive sentinel: %v", err)
	}
	gotSidecar, err := os.ReadFile(sidecarSentinel)
	if err != nil {
		t.Fatalf("read sidecar sentinel: %v", err)
	}
	if !bytes.Equal(gotArchive, archiveBytes) || string(gotSidecar) != "unchanged" {
		t.Fatalf("planning changed archive or sidecar sentinel")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(archivePath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("planning created %s", archivePath+suffix)
		}
	}
}

func TestRawPlanInvalidTargetsAndConflictsUseFailureReporterBeforeAccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown target", args: []string{"raw", "unknown", "target", "--plan", "--json"}, want: `"status":"flag_invalid"`},
		{name: "identity page size", args: []string{"raw", "endpoint", "getIdentity", "--page-size", "1", "--plan", "--json"}, want: `"status":"flag_invalid"`},
		{name: "normal JSON", args: []string{"raw", "endpoint", "getIdentity", "--json"}, want: "--json is not supported by raw without --plan"},
		{name: "normal plain", args: []string{"raw", "endpoint", "getIdentity", "--plain"}, want: "--plain is not supported by raw without --plan"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runWithRuntime(test.args, &stdout, &stderr, forbiddenRawPlanRuntime(t)); code == 0 {
				t.Fatalf("exit = 0, want failure\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), test.want) {
				t.Fatalf("output = %q, want %q", stdout.String()+stderr.String(), test.want)
			}
		})
	}
}

func TestRawPlanReportsStickyWriteErrors(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	code := runWithRuntime([]string{"raw", "endpoint", "getIdentity", "--plan", "--plain"}, failingWriter{}, &stderr, forbiddenRawPlanRuntime(t))
	if code == 0 {
		t.Fatal("exit = 0, want write failure")
	}
	if got, want := stderr.String(), "raw: write output: write failed\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRawPlanIdentityNeedsNoConfigOrExternalAccess(t *testing.T) {
	t.Parallel()
	runtime := forbiddenRawPlanRuntime(t)
	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "endpoint", "getIdentity", "--plan", "--json",
		"--config", "/synthetic/missing-config",
		"--db", "/synthetic/missing-archive",
	}, &stdout, &stderr, runtime)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got struct {
		Request struct {
			Method string `json:"method"`
			URL    string `json:"url"`
		} `json:"request"`
		Range any `json:"range"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if got.Request.Method != "GET" || got.Request.URL != googlehealth.IdentityURL {
		t.Fatalf("request = %+v", got.Request)
	}
	if got.Range != nil {
		t.Fatalf("identity range = %#v, want omitted", got.Range)
	}
}

func forbiddenRawPlanRuntime(t *testing.T) runtimeAdapters {
	t.Helper()
	forbidden := func(name string) {
		t.Fatalf("raw --plan invoked forbidden adapter: %s", name)
	}
	return runtimeAdapters{
		now: func() time.Time {
			return time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC)
		},
		fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
			forbidden("Provider request")
			return nil, errors.New("unreachable")
		},
		refreshOAuthToken: func(oauthClientConfig, string, []string) (oauthTokenResponse, error) {
			forbidden("token refresh")
			return oauthTokenResponse{}, errors.New("unreachable")
		},
		openHealthArchiveWriter: func(string) (healthArchiveWriter, error) {
			forbidden("Health Archive writer")
			return nil, errors.New("unreachable")
		},
		openSyncPlanningArchive: func(context.Context, string) (syncPlanningArchive, error) {
			forbidden("Health Archive planning reader")
			return nil, errors.New("unreachable")
		},
		currentOS: "darwin",
		runSecurityFindGenericPassword: func(context.Context, string, string) ([]byte, error) {
			forbidden("Credential Store read")
			return nil, errors.New("unreachable")
		},
		runSecretToolLookup: func(context.Context, string, string) ([]byte, error) {
			forbidden("Credential Store read")
			return nil, errors.New("unreachable")
		},
		runWindowsCredentialRead: func(context.Context, string, string) ([]byte, error) {
			forbidden("Credential Store read")
			return nil, errors.New("unreachable")
		},
	}
}

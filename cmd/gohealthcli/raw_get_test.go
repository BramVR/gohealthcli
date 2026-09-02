package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestRawDataPointGetPrintsExactProviderBytesWithoutArchiving(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	payload := []byte("{\n  \"name\": \"synthetic-data-point\"\n}\n")
	requestCount := 0
	testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		requestCount++
		if accessToken != "connect-access-secret" {
			t.Fatalf("access token = %q", accessToken)
		}
		if request.EndpointName != "dataTypes.weight.get" || request.DataType != "weight" {
			t.Fatalf("request = %+v", request)
		}
		wantURL := "https://health.googleapis.com/v4/users/me/dataTypes/weight/dataPoints/synthetic%2Fid%3F%23%25%20value"
		if request.URL != wantURL {
			t.Fatalf("URL = %q, want %q", request.URL, wantURL)
		}
		return payload, nil
	}

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "weight", "get",
		"--id", "synthetic/id?#% value",
		"--config", configPath,
		"--db", archivePath,
	}, &stdout, &stderr, testRuntime)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), payload) {
		t.Fatalf("stdout = %q, want exact Provider bytes %q", stdout.Bytes(), payload)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if requestCount != 1 {
		t.Fatalf("Provider requests = %d, want exactly 1", requestCount)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertArchiveTableCount(t, archivePath, "sync_cursors", 0)
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") {
		t.Fatal("raw get output leaked token material")
	}
}

func TestRawDataPointGetWritesExactProviderBytesToSafeOutput(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	payload := []byte("{\"name\":\"synthetic-data-point\"}\n")
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(request googlehealth.RawRequest) []byte {
		if request.EndpointName != "dataTypes.exercise.get" {
			t.Fatalf("EndpointName = %q", request.EndpointName)
		}
		return payload
	})
	outputPath := filepath.Join(t.TempDir(), "raw-get.json")

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "exercise", "get", "--id", "synthetic-id",
		"--output", outputPath,
		"--config", configPath,
		"--db", archivePath,
	}, &stdout, &stderr, testRuntime)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("output = %q, want exact Provider bytes %q", got, payload)
	}
	if !strings.Contains(stderr.String(), fmt.Sprintf("raw: wrote %d bytes", len(payload))) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRawDataPointGetUsesSharedProviderErrorPath(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	requestCount := 0
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		requestCount++
		return nil, &googlehealth.HTTPError{StatusCode: http.StatusServiceUnavailable}
	}

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "weight", "get", "--id", "synthetic-id",
		"--config", configPath,
		"--db", archivePath,
	}, &stdout, &stderr, testRuntime)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "HTTP 503") {
		t.Fatalf("stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
	if requestCount != 1 {
		t.Fatalf("Provider requests = %d, want 1", requestCount)
	}
}

func TestRawDataPointGetPlanHasNoEffects(t *testing.T) {
	t.Parallel()
	runtime := forbiddenRawPlanRuntime(t)
	runtime.prepareRawOutput = func(string) (preparedRawOutput, error) {
		t.Fatal("raw get --plan prepared output")
		return nil, nil
	}

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "sleep", "get",
		"--id", "synthetic/id",
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
	var result rawPlanResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if result.Target.EndpointName != "dataTypes.sleep.get" || result.Target.DataType != "sleep" {
		t.Fatalf("target = %+v", result.Target)
	}
	if result.Request.Method != "GET" || result.Request.URL != "https://health.googleapis.com/v4/users/me/dataTypes/sleep/dataPoints/REDACTED" {
		t.Fatalf("request = %+v", result.Request)
	}
	if strings.Contains(stdout.String(), "synthetic") {
		t.Fatalf("plan exposes Provider ID: %s", stdout.String())
	}
	if result.Range != nil || result.Paging.PageSize != 0 || result.Paging.PageTokenProvided {
		t.Fatalf("range/paging = %+v %+v", result.Range, result.Paging)
	}
	if result.PlanningEffects != (rawPlanEffects{}) {
		t.Fatalf("planning effects = %+v, want all false", result.PlanningEffects)
	}
}

func TestRawDataPointGetRejectsIncompatibleInputsBeforeEffects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing ID", args: []string{"data-type", "weight", "get"}, want: "requires --id"},
		{name: "empty ID", args: []string{"data-type", "weight", "get", "--id="}, want: "--id requires a non-empty Provider ID"},
		{name: "unsupported Data Type", args: []string{"data-type", "steps", "get", "--id", "synthetic-id"}, want: `raw Data Type "steps" is not supported by dataPoints.get`},
		{name: "from", args: []string{"data-type", "weight", "get", "--id", "synthetic-id", "--from", "2026-01-01"}, want: "does not support --from"},
		{name: "to", args: []string{"data-type", "weight", "get", "--id", "synthetic-id", "--to", "2026-01-02"}, want: "does not support --to"},
		{name: "timezone", args: []string{"data-type", "weight", "get", "--id", "synthetic-id", "--timezone", "UTC"}, want: "does not support --timezone"},
		{name: "page size", args: []string{"data-type", "weight", "get", "--id", "synthetic-id", "--page-size", "1"}, want: "does not support --page-size"},
		{name: "page token", args: []string{"data-type", "weight", "get", "--id", "synthetic-id", "--page-token", "synthetic-page"}, want: "does not support --page-token"},
		{name: "source family", args: []string{"data-type", "weight", "get", "--id", "synthetic-id", "--source-family", "wearable"}, want: "flag provided but not defined: -source-family"},
		{name: "ID on list", args: []string{"data-type", "weight", "--id", "synthetic-id", "--from", "2026-01-01"}, want: "--id is supported only by raw data-type <data-type> get"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtime := runtimeAdapters{
				fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
					t.Fatal("rejected raw get contacted Provider")
					return nil, nil
				},
				prepareRawOutput: func(string) (preparedRawOutput, error) {
					t.Fatal("rejected raw get prepared output")
					return nil, nil
				},
			}
			args := append([]string{"raw"}, test.args...)
			args = append(args, "--config", "/synthetic/missing-config", "--db", "/synthetic/missing-archive")
			var stdout, stderr bytes.Buffer
			code := runWithRuntime(args, &stdout, &stderr, runtime)
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
			if strings.Contains(stderr.String(), "config check failed") {
				t.Fatalf("validation reached config: %s", stderr.String())
			}
		})
	}
}

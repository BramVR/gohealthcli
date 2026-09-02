package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestRawRollupPrintsOneExactProviderResponseWithoutArchiving(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         []string
		endpointName string
		windowSize   string
	}{
		{
			name: "daily",
			args: []string{"data-type", "steps", "daily-rollup",
				"--from", "2026-01-01", "--to", "2026-01-02",
				"--page-size", "17", "--page-token", "synthetic-page-token"},
			endpointName: "dataTypes.steps.dailyRollUp",
		},
		{
			name: "physical window",
			args: []string{"data-type", "total-calories", "rollup",
				"--from", "2026-01-01T00:00:00Z", "--to", "2026-01-02T00:00:00Z",
				"--window", "1h", "--page-size", "17", "--page-token", "synthetic-page-token"},
			endpointName: "dataTypes.total-calories.rollUp",
			windowSize:   "3600s",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
				accessToken:        "connect-access-secret",
				refreshToken:       "connect-refresh-secret",
				healthUserID:       "111111256096816351",
				legacyFitbitUserID: "A1B2C3",
			})
			beforeTokenMetadata := archivedConnectionTokenMetadata(t, archivePath)
			beforeIdentityJSON := archivedConnectionIdentityJSON(t, archivePath)
			payload := []byte("{\n  \"rollupDataPoints\": [],\n  \"nextPageToken\": \"synthetic-next-page\"\n}\n")
			requestCount := 0
			testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
				requestCount++
				if accessToken != "connect-access-secret" || request.EndpointName != test.endpointName || request.Method != http.MethodPost {
					t.Fatalf("access token/request = %q / %+v", accessToken, request)
				}
				var body struct {
					WindowSize string `json:"windowSize"`
					PageSize   int64  `json:"pageSize"`
					PageToken  string `json:"pageToken"`
				}
				if err := json.Unmarshal(request.Body, &body); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				if body.WindowSize != test.windowSize || body.PageSize != 17 || body.PageToken != "synthetic-page-token" {
					t.Fatalf("request body = %+v", body)
				}
				return payload, nil
			}

			args := append([]string{"raw"}, test.args...)
			args = append(args, "--config", configPath, "--db", archivePath)
			var stdout, stderr bytes.Buffer
			code := runWithRuntime(args, &stdout, &stderr, testRuntime)
			if code != 0 || !bytes.Equal(stdout.Bytes(), payload) || stderr.Len() != 0 {
				t.Fatalf("exit/stdout/stderr = %d / %q / %q", code, stdout.Bytes(), stderr.String())
			}
			if requestCount != 1 {
				t.Fatalf("Provider requests = %d, want 1", requestCount)
			}
			assertRawRollupArchiveUntouched(t, archivePath)
			if got := archivedConnectionTokenMetadata(t, archivePath); got != beforeTokenMetadata {
				t.Fatalf("raw Rollup mutated token metadata without refresh: %s", got)
			}
			if got := archivedConnectionIdentityJSON(t, archivePath); got != beforeIdentityJSON {
				t.Fatalf("raw Rollup mutated archived identity: %s", got)
			}
		})
	}
}

func TestRawRollupPlanSanitizesPostBodyWithoutEffects(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "steps", "rollup",
		"--from", "yesterday", "--to", "today", "--timezone", "Europe/Brussels",
		"--window", "1h", "--page-size", "17", "--page-token", "synthetic-page-secret",
		"--plan", "--json", "--config", "/synthetic/missing-config", "--db", "/synthetic/missing-archive",
	}, &stdout, &stderr, forbiddenRawPlanRuntime(t))
	if code != 0 || stderr.Len() != 0 || strings.Contains(stdout.String(), "synthetic-page-secret") {
		t.Fatalf("exit/stdout/stderr = %d / %q / %q", code, stdout.String(), stderr.String())
	}
	var result rawPlanResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if result.Target.EndpointName != "dataTypes.steps.rollUp" || result.Request.Method != http.MethodPost {
		t.Fatalf("target/request = %+v / %+v", result.Target, result.Request)
	}
	var body struct {
		WindowSize string `json:"windowSize"`
		PageToken  string `json:"pageToken"`
	}
	if err := json.Unmarshal(result.Request.Body, &body); err != nil || body.WindowSize != "3600s" || body.PageToken != "REDACTED" {
		t.Fatalf("request body = %s, %v", result.Request.Body, err)
	}
	if result.Range == nil || result.Range.From != "2026-03-28T23:00:00Z" || result.Range.To != "2026-03-29T22:00:00Z" {
		t.Fatalf("range = %+v", result.Range)
	}
	if result.Paging.PageSize != 17 || !result.Paging.PageTokenProvided || result.PlanningEffects != (rawPlanEffects{}) {
		t.Fatalf("paging/effects = %+v / %+v", result.Paging, result.PlanningEffects)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"rollup_write": false`)) || !bytes.Contains(stdout.Bytes(), []byte(`"sync_run_change": false`)) {
		t.Fatalf("plan omitted explicit Rollup or Sync Run effect: %s", stdout.String())
	}
}

func TestRawRollupPlanHumanAndPlainDescribePostRequest(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"", "--plain"} {
		args := []string{
			"raw", "data-type", "steps", "daily-rollup",
			"--from", "2026-01-01", "--to", "2026-01-02", "--timezone", "UTC", "--plan",
			"--config", "/synthetic/missing-config", "--db", "/synthetic/missing-archive",
		}
		if mode != "" {
			args = append(args, mode)
		}
		var stdout, stderr bytes.Buffer
		code := runWithRuntime(args, &stdout, &stderr, forbiddenRawPlanRuntime(t))
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("mode %q exit/stderr = %d / %q", mode, code, stderr.String())
		}
		got := stdout.String()
		if !strings.Contains(got, "Content-Type=application/json") && !strings.Contains(got, "request.headers.Content-Type: application/json") {
			t.Fatalf("mode %q omitted Content-Type: %s", mode, got)
		}
		if !strings.Contains(got, "windowSizeDays") {
			t.Fatalf("mode %q omitted POST body: %s", mode, got)
		}
	}
}

func TestRawRollupWritesExactProviderResponseToSafeOutput(t *testing.T) {
	t.Parallel()

	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken: "connect-access-secret", refreshToken: "connect-refresh-secret",
		healthUserID: "111111256096816351", legacyFitbitUserID: "A1B2C3",
	})
	payload := []byte("{\"rollupDataPoints\":[],\"nextPageToken\":\"synthetic-next\"}\n")
	requestCount := 0
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		requestCount++
		return payload, nil
	}
	outputPath := filepath.Join(t.TempDir(), "raw-rollup.json")

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "steps", "daily-rollup",
		"--from", "2026-01-01", "--to", "2026-01-02", "--output", outputPath,
		"--config", configPath, "--db", archivePath,
	}, &stdout, &stderr, testRuntime)
	if code != 0 || requestCount != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), string(payload)) {
		t.Fatalf("exit/requests/stdout/stderr = %d / %d / %q / %q", code, requestCount, stdout.String(), stderr.String())
	}
	got, err := os.ReadFile(outputPath)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("output = %q, %v", got, err)
	}
	assertRawRollupArchiveUntouched(t, archivePath)
}

func TestRawRollupProviderErrorUsesRawFailurePath(t *testing.T) {
	t.Parallel()

	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken: "connect-access-secret", refreshToken: "connect-refresh-secret",
		healthUserID: "111111256096816351", legacyFitbitUserID: "A1B2C3",
	})
	requestCount := 0
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		requestCount++
		return nil, &googlehealth.HTTPError{StatusCode: http.StatusServiceUnavailable}
	}
	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "steps", "daily-rollup", "--from", "2026-01-01", "--to", "2026-01-02",
		"--config", configPath, "--db", archivePath,
	}, &stdout, &stderr, testRuntime)
	if code != 1 || requestCount != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "HTTP 503") {
		t.Fatalf("exit/requests/stdout/stderr = %d / %d / %q / %q", code, requestCount, stdout.String(), stderr.String())
	}
	assertRawRollupArchiveUntouched(t, archivePath)
}

func TestRawRollupRejectsInvalidInputsBeforeExternalAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unsupported Data Type", args: []string{"data-type", "sleep", "daily-rollup", "--from", "2026-01-01", "--to", "2026-01-02"}, want: "not supported by dataPoints.dailyRollUp"},
		{name: "missing from", args: []string{"data-type", "steps", "daily-rollup", "--to", "2026-01-02"}, want: "requires --from and --to"},
		{name: "missing to", args: []string{"data-type", "steps", "daily-rollup", "--from", "2026-01-01"}, want: "requires --from and --to"},
		{name: "missing window", args: []string{"data-type", "steps", "rollup", "--from", "2026-01-01", "--to", "2026-01-02"}, want: "requires --window"},
		{name: "unsupported window", args: []string{"data-type", "steps", "rollup", "--from", "2026-01-01", "--to", "2026-01-02", "--window", "6h"}, want: "supported window granularities: 1h, 1d, 7d"},
		{name: "window on daily", args: []string{"data-type", "steps", "daily-rollup", "--from", "2026-01-01", "--to", "2026-01-02", "--window", "1h"}, want: "--window is supported only"},
		{name: "daily range needs chunking", args: []string{"data-type", "heart-rate", "daily-rollup", "--from", "2026-01-01", "--to", "2026-01-16"}, want: "narrow --from/--to to one Provider request"},
		{name: "physical range needs chunking", args: []string{"data-type", "total-calories", "rollup", "--from", "2026-01-01", "--to", "2026-01-16", "--window", "1h"}, want: "narrow --from/--to to one Provider request"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := forbiddenRawPlanRuntime(t)
			runtime.prepareRawOutput = func(string) (preparedRawOutput, error) {
				t.Fatal("invalid raw Rollup prepared output")
				return nil, errors.New("unreachable")
			}
			args := append([]string{"raw"}, test.args...)
			args = append(args, "--timezone", "UTC", "--plan", "--json", "--config", "/synthetic/missing-config", "--db", "/synthetic/missing-archive")
			var stdout, stderr bytes.Buffer
			code := runWithRuntime(args, &stdout, &stderr, runtime)
			if code != 1 || !strings.Contains(stdout.String()+stderr.String(), test.want) {
				t.Fatalf("exit = %d, want 1 and %q\nstdout: %s\nstderr: %s", code, test.want, stdout.String(), stderr.String())
			}
		})
	}
}

func assertRawRollupArchiveUntouched(t *testing.T, archivePath string) {
	t.Helper()
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertArchiveTableCount(t, archivePath, "sync_cursors", 0)
	entries, err := os.ReadDir(attachmentRootDirForArchive(archivePath))
	if err != nil {
		t.Fatalf("read sidecar path: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("raw Rollup created sidecar entries: %v", entries)
	}
}

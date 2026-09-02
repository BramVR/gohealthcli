package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestRawReconcilePrintsOneExactProviderPageWithoutArchiving(t *testing.T) {
	t.Parallel()

	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	payload := []byte("{\n  \"dataPoints\": [],\n  \"nextPageToken\": \"synthetic-next-page\"\n}\n")
	requestCount := 0
	testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		requestCount++
		if accessToken != "connect-access-secret" {
			t.Fatalf("access token = %q", accessToken)
		}
		if request.EndpointName != "dataTypes.steps.reconcile" || request.SourceFamilyFilter != "wearable" {
			t.Fatalf("request = %+v", request)
		}
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Path != "/v4/users/me/dataTypes/steps/dataPoints:reconcile" || parsed.Query().Get("pageSize") != "10000" {
			t.Fatalf("request URL = %q", request.URL)
		}
		return payload, nil
	}

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "steps", "reconcile",
		"--source-family", "wearable",
		"--from", "2026-01-01T00:00:00Z",
		"--to", "2026-01-02T00:00:00Z",
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
		t.Fatalf("Provider requests = %d, want 1", requestCount)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertArchiveTableCount(t, archivePath, "sync_cursors", 0)
	entries, err := os.ReadDir(attachmentRootDirForArchive(archivePath))
	if err != nil {
		t.Fatalf("read sidecar path: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("raw reconcile created sidecar entries: %v", entries)
	}
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") {
		t.Fatal("raw reconcile output leaked token material")
	}
}

func TestRawReconcilePlanIsSanitizedAndHasNoEffects(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "daily-resting-heart-rate", "reconcile",
		"--source-family", "wearable",
		"--from", "yesterday",
		"--to", "today",
		"--timezone", "Europe/Brussels",
		"--page-token", "synthetic-page-secret",
		"--plan", "--json",
		"--config", "/synthetic/missing-config",
		"--db", "/synthetic/missing-archive",
	}, &stdout, &stderr, forbiddenRawPlanRuntime(t))
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 || strings.Contains(stdout.String(), "synthetic-page-secret") {
		t.Fatalf("stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
	var result rawPlanResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if result.Target.EndpointName != "dataTypes.daily-resting-heart-rate.reconcile" || result.Target.SourceFamily != "wearable" {
		t.Fatalf("target = %+v", result.Target)
	}
	if result.Range == nil || result.Range.From != "2026-03-29" || result.Range.To != "2026-03-30" || result.Range.Timezone != "Europe/Brussels" {
		t.Fatalf("range = %+v", result.Range)
	}
	if result.Paging.PageSize != 10000 || !result.Paging.PageTokenProvided {
		t.Fatalf("paging = %+v", result.Paging)
	}
	if !strings.Contains(result.Request.URL, "dataSourceFamily=users%2Fme%2FdataSourceFamilies%2Fgoogle-wearables") || !strings.Contains(result.Request.URL, "pageToken=REDACTED") {
		t.Fatalf("request URL = %q", result.Request.URL)
	}
	if result.PlanningEffects != (rawPlanEffects{}) {
		t.Fatalf("planning effects = %+v, want all false", result.PlanningEffects)
	}
}

func TestRawReconcileWritesExactProviderPageToSafeOutput(t *testing.T) {
	t.Parallel()

	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	payload := []byte("{\"dataPoints\":[],\"nextPageToken\":\"synthetic-next\"}\n")
	requestCount := 0
	testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		requestCount++
		parsed, err := url.Parse(request.URL)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Query().Get("pageSize") != "25" || parsed.Query().Get("pageToken") != "synthetic-current" {
			t.Fatalf("request URL = %q", request.URL)
		}
		return payload, nil
	}
	outputPath := filepath.Join(t.TempDir(), "reconcile-page.json")

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"raw", "data-type", "steps", "reconcile",
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--page-size", "25",
		"--page-token", "synthetic-current",
		"--output", outputPath,
		"--config", configPath,
		"--db", archivePath,
	}, &stdout, &stderr, testRuntime)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, payload) || stdout.Len() != 0 || strings.Contains(stderr.String(), string(payload)) {
		t.Fatalf("output/stdout/stderr = %q / %q / %q", got, stdout.String(), stderr.String())
	}
	if requestCount != 1 || !strings.Contains(stderr.String(), fmt.Sprintf("raw: wrote %d bytes", len(payload))) {
		t.Fatalf("requests/stderr = %d / %q", requestCount, stderr.String())
	}
}

func TestRawReconcileProviderErrorUsesRawFailurePath(t *testing.T) {
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
		"raw", "data-type", "steps", "reconcile",
		"--source-family", "wearable", "--from", "2026-01-01",
		"--config", configPath, "--db", archivePath,
	}, &stdout, &stderr, testRuntime)
	if code != 1 || requestCount != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "HTTP 503") {
		t.Fatalf("exit/requests/stdout/stderr = %d / %d / %q / %q", code, requestCount, stdout.String(), stderr.String())
	}
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
}

func TestRawReconcileRejectsInvalidInputsBeforeExternalAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing source family", args: []string{"data-type", "steps", "reconcile", "--from", "2026-01-01"}, want: "requires --source-family"},
		{name: "empty source family", args: []string{"data-type", "steps", "reconcile", "--source-family=", "--from", "2026-01-01"}, want: "--source-family requires a non-empty source family"},
		{name: "unknown source family", args: []string{"data-type", "steps", "reconcile", "--source-family", "synthetic-family", "--from", "2026-01-01"}, want: "currently supports only wearable"},
		{name: "unsupported Data Type", args: []string{"data-type", "electrocardiogram", "reconcile", "--source-family", "wearable", "--from", "2026-01-01"}, want: "not supported by dataPoints.reconcile"},
		{name: "missing from", args: []string{"data-type", "steps", "reconcile", "--source-family", "wearable"}, want: "reconcile requires --from"},
		{name: "incompatible range shape", args: []string{"data-type", "daily-resting-heart-rate", "reconcile", "--source-family", "wearable", "--from", "2026-01-01T00:00:00Z", "--timezone", "UTC", "--plan"}, want: "expected YYYY-MM-DD"},
		{name: "source family on list", args: []string{"data-type", "steps", "--source-family", "wearable", "--from", "2026-01-01"}, want: "supported only by raw data-type <data-type> reconcile"},
		{name: "zero page size", args: []string{"data-type", "steps", "reconcile", "--source-family", "wearable", "--from", "2026-01-01", "--page-size", "0"}, want: "--page-size must be a positive integer"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := forbiddenRawPlanRuntime(t)
			runtime.prepareRawOutput = func(string) (preparedRawOutput, error) {
				t.Fatal("invalid raw reconcile prepared output")
				return nil, errors.New("unreachable")
			}
			args := append([]string{"raw"}, test.args...)
			args = append(args, "--config", "/synthetic/missing-config", "--db", "/synthetic/missing-archive")
			var stdout, stderr bytes.Buffer
			code := runWithRuntime(args, &stdout, &stderr, runtime)
			if code != 1 || !strings.Contains(stdout.String()+stderr.String(), test.want) {
				t.Fatalf("exit = %d, want 1 and %q\nstdout: %s\nstderr: %s", code, test.want, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), "config check failed") {
				t.Fatalf("validation reached config: %s%s", stdout.String(), stderr.String())
			}
		})
	}
}

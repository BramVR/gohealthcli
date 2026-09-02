package main

import (
	"bytes"
	"context"
	"net/url"
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
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") {
		t.Fatal("raw reconcile output leaked token material")
	}
}

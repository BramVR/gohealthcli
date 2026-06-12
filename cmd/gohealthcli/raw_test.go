package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRawEndpointIdentityPrintsProviderJSONWithoutArchiving(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	beforeIdentityJSON := archivedConnectionIdentityJSON(t, archivePath)
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(request rawProviderRequest) []byte {
		if request.url != googleHealthIdentityURL {
			t.Fatalf("raw URL = %q, want identity URL", request.url)
		}
		return []byte(`{"healthUserId":"999999999999999999","legacyUserId":"RAW"}`)
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("raw exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stdout.String() != `{"healthUserId":"999999999999999999","legacyUserId":"RAW"}` {
		t.Fatalf("stdout = %q, want raw provider JSON", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := archivedConnectionIdentityJSON(t, archivePath); got != beforeIdentityJSON {
		t.Fatalf("raw mutated archived identity JSON: %s", got)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
	if strings.Contains(stdout.String()+stderr.String(), "connect-access-secret") {
		t.Fatalf("raw output leaked token material:\nstdout:%s\nstderr:%s", stdout.String(), stderr.String())
	}
}

func TestRawDataTypeStepsPrintsFixtureJSON(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	fixture := readTestFixture(t, "googlehealth_steps_list.json")
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(request rawProviderRequest) []byte {
		if request.endpointName != "dataTypes.steps.list" || request.dataType != "steps" {
			t.Fatalf("raw request = (%q, %q), want steps list", request.endpointName, request.dataType)
		}
		parsedURL, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("parse raw URL: %v", err)
		}
		if parsedURL.Path != "/v4/users/me/dataTypes/steps/dataPoints" {
			t.Fatalf("raw path = %q, want steps dataPoints path", parsedURL.Path)
		}
		query := parsedURL.Query()
		wantFilter := `steps.interval.start_time >= "2026-01-01T00:00:00Z" AND steps.interval.start_time < "2026-01-02T00:00:00Z"`
		if query.Get("filter") != wantFilter {
			t.Fatalf("filter = %q, want %q", query.Get("filter"), wantFilter)
		}
		if query.Get("pageSize") != "12" || query.Get("pageToken") != "abc123" {
			t.Fatalf("pagination query = %v, want pageSize/pageToken", query)
		}
		return fixture
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw",
		"data-type", "steps",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--page-size", "12",
		"--page-token", "abc123",
		"--config", configPath,
		"--db", archivePath,
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("raw exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if !bytes.Equal(stdout.Bytes(), fixture) {
		t.Fatalf("stdout = %q, want fixture JSON", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestRawProviderErrorDoesNotLeakToken(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != "connect-access-secret" {
			t.Fatalf("raw access token = %q, want stored token", accessToken)
		}
		return nil, errors.New("Google Health raw request failed with HTTP 403")
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("raw exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 403") {
		t.Fatalf("stderr = %q, want provider status", stderr.String())
	}
	if strings.Contains(stderr.String(), "connect-access-secret") || strings.Contains(stderr.String(), "connect-refresh-secret") {
		t.Fatalf("raw error leaked token material: %s", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestRawDataTypeFailsBeforeProviderWhenScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	db, err := openArchive(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	var metadata map[string]any
	var metadataJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT token_metadata_json FROM connections WHERE id = ?`, "googlehealth:111111256096816351").Scan(&metadataJSON); err != nil {
		t.Fatalf("query token metadata: %v", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		t.Fatalf("unmarshal token metadata: %v", err)
	}
	metadata["scopes"] = []string{googleHealthActivityReadonlyScope}
	updatedMetadataJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal token metadata: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `UPDATE connections SET token_metadata_json = ? WHERE id = ?`, string(updatedMetadataJSON), "googlehealth:111111256096816351")
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("update token scopes: %v", err)
	}
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("raw provider fetch should not run with missing scope")
		return nil, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"raw", "data-type", "heart-rate", "--from", "2026-01-01", "--config", configPath, "--db", archivePath}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("raw exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), googleHealthHealthMetricsReadonlyScope) || !strings.Contains(stderr.String(), "connect") {
		t.Fatalf("stderr = %q, want missing scope reconnect hint", stderr.String())
	}
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

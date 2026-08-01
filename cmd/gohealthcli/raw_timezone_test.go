package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestRawDataTypeNamedRangeUsesCanonicalPhysicalResolution(t *testing.T) {
	t.Parallel()
	configPath, archivePath, runtime := connectedArchiveViaSetup(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	resolvedAt := time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC)
	runtime.now = func() time.Time { return resolvedAt }
	beforeTokenMetadata := archivedConnectionTokenMetadata(t, archivePath)
	beforeIdentityJSON := archivedConnectionIdentityJSON(t, archivePath)
	var request googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, got googlehealth.RawRequest, accessToken string) ([]byte, error) {
		if accessToken != "connect-access-secret" {
			t.Fatalf("access token = %q", accessToken)
		}
		request = got
		return []byte(`{"dataPoints":[]}`), nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw",
		"data-type", "steps",
		"--from", "yesterday",
		"--to", "today",
		"--timezone", "Europe/Brussels",
		"--config", configPath,
		"--db", archivePath,
	}, stdout, stderr, runtime)
	if code != 0 {
		t.Fatalf("raw exit code = %d\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := `steps.interval.start_time >= "2026-03-28T23:00:00Z" AND steps.interval.start_time < "2026-03-29T22:00:00Z"`
	if got := mustURLQuery(t, request.URL).Get("filter"); got != want {
		t.Fatalf("filter = %q, want %q", got, want)
	}
	if got := archivedConnectionTokenMetadata(t, archivePath); got != beforeTokenMetadata {
		t.Fatalf("raw range read mutated token metadata without refresh: %s", got)
	}
	if got := archivedConnectionIdentityJSON(t, archivePath); got != beforeIdentityJSON {
		t.Fatalf("raw range read mutated archived identity: %s", got)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
}

func TestRawDataTypeAndListEndpointAliasesResolveIdentically(t *testing.T) {
	t.Parallel()
	configPath, archivePath, runtime := connectedArchiveViaSetup(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	runtime.now = func() time.Time { return time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC) }
	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		requests = append(requests, request)
		return []byte(`{"dataPoints":[]}`), nil
	}

	for _, target := range [][]string{
		{"data-type", "sleep"},
		{"endpoint", "dataTypes.sleep.list"},
	} {
		args := append([]string{"raw"}, target...)
		args = append(args,
			"--from", "yesterday",
			"--to", "today",
			"--timezone", "Europe/Brussels",
			"--page-size", "7",
			"--page-token", "next-page",
			"--config", configPath,
			"--db", archivePath,
		)
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		if code := runWithRuntime(args, stdout, stderr, runtime); code != 0 {
			t.Fatalf("raw %v exit code = %d\nstderr: %s\nstdout: %s", target, code, stderr.String(), stdout.String())
		}
	}
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(requests))
	}
	if requests[0].EndpointName != requests[1].EndpointName || requests[0].URL != requests[1].URL {
		t.Fatalf("alias requests differ:\ndata-type: %#v\nendpoint:  %#v", requests[0], requests[1])
	}
}

func TestRawAndSyncNamedRangesProduceEquivalentProviderFilters(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		dataType string
	}{
		{name: "physical", dataType: "steps"},
		{name: "civil", dataType: "sleep"},
		{name: "daily", dataType: "daily-resting-heart-rate"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, runtime := connectedArchiveViaSetup(t, fakeConnectConfig{
				accessToken:        "connect-access-secret",
				refreshToken:       "connect-refresh-secret",
				healthUserID:       "111111256096816351",
				legacyFitbitUserID: "A1B2C3",
			})
			runtime.now = func() time.Time { return time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC) }
			var requests []googlehealth.RawRequest
			runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
				requests = append(requests, request)
				return []byte(`{"dataPoints":[]}`), nil
			}

			commands := [][]string{
				{
					"raw", "data-type", test.dataType,
					"--from", "yesterday", "--to", "today", "--timezone", "Europe/Brussels",
					"--config", configPath, "--db", archivePath,
				},
				{
					"sync", "--types", test.dataType,
					"--from", "yesterday", "--to", "today", "--timezone", "Europe/Brussels",
					"--config", configPath, "--db", archivePath, "--json",
				},
			}
			for _, args := range commands {
				stdout := new(bytes.Buffer)
				stderr := new(bytes.Buffer)
				if code := runWithRuntime(args, stdout, stderr, runtime); code != 0 {
					t.Fatalf("%s exit code = %d\nstderr: %s\nstdout: %s", args[0], code, stderr.String(), stdout.String())
				}
			}
			if len(requests) != 2 {
				t.Fatalf("provider requests = %d, want raw + sync", len(requests))
			}
			rawFilter := mustURLQuery(t, requests[0].URL).Get("filter")
			syncFilter := mustURLQuery(t, requests[1].URL).Get("filter")
			if rawFilter != syncFilter {
				t.Fatalf("raw filter = %q, sync filter = %q", rawFilter, syncFilter)
			}
		})
	}
}

func TestRawTimezoneUsesConfigUnlessFlagOverridesIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		flag       string
		wantFilter string
	}{
		{
			name:       "configured timezone",
			wantFilter: `steps.interval.start_time >= "2026-03-28T23:00:00Z" AND steps.interval.start_time < "2026-03-29T22:00:00Z"`,
		},
		{
			name:       "flag override",
			flag:       "America/New_York",
			wantFilter: `steps.interval.start_time >= "2026-03-29T04:00:00Z" AND steps.interval.start_time < "2026-03-30T04:00:00Z"`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			configPath, archivePath, runtime := connectedArchiveViaSetup(t, fakeConnectConfig{
				accessToken:        "connect-access-secret",
				refreshToken:       "connect-refresh-secret",
				healthUserID:       "111111256096816351",
				legacyFitbitUserID: "A1B2C3",
			})
			configBytes, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			config := strings.Replace(string(configBytes), `timezone = "UTC"`, `timezone = "Europe/Brussels"`, 1)
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			runtime.now = func() time.Time { return time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC) }
			var request googlehealth.RawRequest
			runtime.fetchRawProvider = func(_ context.Context, got googlehealth.RawRequest, _ string) ([]byte, error) {
				request = got
				return []byte(`{"dataPoints":[]}`), nil
			}
			args := []string{
				"raw", "data-type", "steps", "--from", "yesterday", "--to", "today",
				"--config", configPath, "--db", archivePath,
			}
			if test.flag != "" {
				args = append(args, "--timezone", test.flag)
			}
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			if code := runWithRuntime(args, stdout, stderr, runtime); code != 0 {
				t.Fatalf("raw exit code = %d\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
			}
			if got := mustURLQuery(t, request.URL).Get("filter"); got != test.wantFilter {
				t.Fatalf("filter = %q, want %q", got, test.wantFilter)
			}
		})
	}
}

func TestRawRangeCapturesClockBeforeProviderSetup(t *testing.T) {
	t.Parallel()
	configPath, archivePath, runtime := connectedArchiveViaSetup(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	resolvedAt := time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC)
	clockCalls := 0
	runtime.now = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return resolvedAt
		}
		return resolvedAt.AddDate(0, 0, 10)
	}
	var request googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, got googlehealth.RawRequest, _ string) ([]byte, error) {
		request = got
		return []byte(`{"dataPoints":[]}`), nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw", "data-type", "steps",
		"--from", "today", "--to", "now", "--timezone", "Europe/Brussels",
		"--config", configPath, "--db", archivePath,
	}, stdout, stderr, runtime)
	if code != 0 {
		t.Fatalf("raw exit code = %d\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	want := `steps.interval.start_time >= "2026-03-29T22:00:00Z" AND steps.interval.start_time < "2026-03-30T10:15:30Z"`
	if got := mustURLQuery(t, request.URL).Get("filter"); got != want {
		t.Fatalf("filter = %q, want first clock instant %q", got, want)
	}
	if clockCalls < 2 {
		t.Fatalf("clock calls = %d, want later provider setup clock use", clockCalls)
	}
}

func TestRawIdentityRejectsRangeFlagsBeforeConfigArchiveOrProviderIO(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		flag string
	}{
		{name: "from", args: []string{"--from", "today"}, flag: "--from"},
		{name: "to", args: []string{"--to", "now"}, flag: "--to"},
		{name: "timezone", args: []string{"--timezone", "Europe/Brussels"}, flag: "--timezone"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			configPath := filepath.Join(root, "missing", "config.toml")
			archivePath := filepath.Join(root, "missing", "health.db")
			runtime := runtimeAdapters{
				now: func() time.Time {
					t.Fatal("clock must not run for rejected identity range flags")
					return time.Time{}
				},
				fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
					t.Fatal("provider must not run for rejected identity range flags")
					return nil, nil
				},
			}
			args := []string{"raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}
			args = append(args, test.args...)
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			if code := runWithRuntime(args, stdout, stderr, runtime); code != 1 {
				t.Fatalf("raw exit code = %d, want 1\nstderr: %s", code, stderr.String())
			}
			want := "raw endpoint getIdentity does not support " + test.flag
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), want)
			}
			if _, err := os.Stat(filepath.Dir(configPath)); !os.IsNotExist(err) {
				t.Fatalf("rejected identity flags touched setup path: %v", err)
			}
		})
	}
}

func TestRawRejectsEmptyTimezoneBeforeSetup(t *testing.T) {
	t.Parallel()
	runtime := runtimeAdapters{
		now: func() time.Time {
			t.Fatal("clock must not run for an empty timezone flag")
			return time.Time{}
		},
		fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
			t.Fatal("provider must not run for an empty timezone flag")
			return nil, nil
		},
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"raw", "data-type", "steps", "--from", "today", "--timezone", ""}, stdout, stderr, runtime)
	if code != 1 {
		t.Fatalf("raw exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--timezone requires a non-empty IANA timezone") {
		t.Fatalf("stderr = %q, want empty timezone validation", stderr.String())
	}
}

func TestRawRejectsNonListableDataTypesBeforeConfigIO(t *testing.T) {
	t.Parallel()
	for _, target := range [][]string{
		{"data-type", "total-calories"},
		{"endpoint", "dataTypes.total-calories.list"},
	} {
		target := target
		t.Run(strings.Join(target, "_"), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			configPath := filepath.Join(root, "missing", "config.toml")
			archivePath := filepath.Join(root, "missing", "health.db")
			runtime := runtimeAdapters{
				now: func() time.Time {
					t.Fatal("clock must not run for a non-listable Data Type")
					return time.Time{}
				},
				fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
					t.Fatal("provider must not run for a non-listable Data Type")
					return nil, nil
				},
			}
			args := append([]string{"raw"}, target...)
			args = append(args, "--from", "today", "--config", configPath, "--db", archivePath)
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			if code := runWithRuntime(args, stdout, stderr, runtime); code != 1 {
				t.Fatalf("raw exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), "not supported by dataPoints.list") {
				t.Fatalf("stderr = %q, want canonical listability error", stderr.String())
			}
			if _, err := os.Stat(filepath.Dir(configPath)); !os.IsNotExist(err) {
				t.Fatalf("invalid Data Type touched setup path: %v", err)
			}
		})
	}
}

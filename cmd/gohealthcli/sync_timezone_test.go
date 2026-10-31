package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/archived"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestSyncTimezoneNamedRangesReachTargetAwareProviderFilters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		dataType   string
		wantFrom   string
		wantTo     string
		wantFilter string
	}{
		{
			name:       "physical",
			dataType:   "steps",
			wantFrom:   "2026-03-28T23:00:00Z",
			wantTo:     "2026-03-29T22:00:00Z",
			wantFilter: `steps.interval.start_time >= "2026-03-28T23:00:00Z" AND steps.interval.start_time < "2026-03-29T22:00:00Z"`,
		},
		{
			name:       "civil",
			dataType:   "sleep",
			wantFrom:   "2026-03-29T00:00:00",
			wantTo:     "2026-03-30T00:00:00",
			wantFilter: `sleep.interval.civil_end_time >= "2026-03-29T00:00:00" AND sleep.interval.civil_end_time < "2026-03-30T00:00:00"`,
		},
		{
			name:       "daily",
			dataType:   "daily-resting-heart-rate",
			wantFrom:   "2026-03-29",
			wantTo:     "2026-03-30",
			wantFilter: `daily_resting_heart_rate.date >= "2026-03-29" AND daily_resting_heart_rate.date < "2026-03-30"`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
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
				// Lifecycle bookkeeping gets a deliberately different
				// calendar day. The provider range must still consume the
				// preflight plan resolved from the first captured instant.
				return resolvedAt.AddDate(0, 0, 10)
			}
			var requests []googlehealth.RawRequest
			runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
				if accessToken != "connect-access-secret" {
					t.Fatalf("access token = %q", accessToken)
				}
				requests = append(requests, request)
				return []byte(`{"dataPoints":[]}`), nil
			}

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := runWithRuntime([]string{
				"sync",
				"--config", configPath,
				"--db", archivePath,
				"--types", test.dataType,
				"--from", "yesterday",
				"--to", "today",
				"--timezone", "Europe/Brussels",
				"--json",
			}, stdout, stderr, runtime)
			if code != 0 {
				t.Fatalf("sync exit code = %d\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
			}
			if len(requests) != 1 {
				t.Fatalf("provider requests = %d, want 1", len(requests))
			}
			if got := mustURLQuery(t, requests[0].URL).Get("filter"); got != test.wantFilter {
				t.Fatalf("filter = %q, want %q", got, test.wantFilter)
			}
			var result syncResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("decode result: %v\n%s", err, stdout.String())
			}
			if result.From != test.wantFrom || result.To != test.wantTo {
				t.Errorf("result range = %q..%q, want %q..%q", result.From, result.To, test.wantFrom, test.wantTo)
			}
			if clockCalls < 2 {
				t.Fatalf("clock calls = %d, want lifecycle bookkeeping after range capture", clockCalls)
			}
			db := openArchiveForTest(t, archivePath)
			var cursorTime string
			if err := db.QueryRowContext(context.Background(), `
				SELECT cursor_time
				FROM sync_cursors
				WHERE connection_id = ? AND data_type = ? AND source_family_filter = '' AND rollup_kind = 'none'
			`, "googlehealth:111111256096816351", test.dataType).Scan(&cursorTime); err != nil {
				t.Fatalf("read Sync Cursor: %v", err)
			}
			if cursorTime != test.wantTo {
				t.Errorf("cursor_time = %q, want exact resolved --to %q", cursorTime, test.wantTo)
			}
		})
	}
}

// TestSyncStatusExposesPersistedResolvedRangeMetadata is the #405 tracer
// bullet: status must report the range-resolution facts archived when the Sync
// Run started, rather than attempting to resolve relative inputs again later.
func TestSyncStatusExposesPersistedResolvedRangeMetadata(t *testing.T) {
	t.Parallel()
	configPath, archivePath, runtime := connectedArchiveViaSetup(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	resolvedAt := time.Date(2026, 3, 30, 10, 15, 30, 123456789, time.UTC)
	runtime.now = func() time.Time { return resolvedAt }
	runtime.fetchRawProvider = func(_ context.Context, _ googlehealth.RawRequest, _ string) ([]byte, error) {
		return []byte(`{"dataPoints":[]}`), nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	if code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--from", "yesterday",
		"--to", "today",
		"--timezone", "Europe/Brussels",
		"--json",
	}, stdout, stderr, runtime); code != 0 {
		t.Fatalf("sync exit code = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runWithRuntime([]string{
		"sync",
		"--status",
		"--config", configPath,
		"--db", archivePath,
		"--json",
	}, stdout, stderr, runtime); code != 0 {
		t.Fatalf("sync --status exit code = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	var result struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode sync --status: %v\n%s", err, stdout.String())
	}
	if len(result.Runs) != 1 {
		t.Fatalf("runs = %d, want 1\n%s", len(result.Runs), stdout.String())
	}
	want := map[string]string{
		"from":        "2026-03-28T23:00:00Z",
		"to":          "2026-03-29T22:00:00Z",
		"timezone":    "Europe/Brussels",
		"resolved_at": "2026-03-30T10:15:30.123456789Z",
		"from_input":  "yesterday",
		"to_input":    "today",
	}
	for key, value := range want {
		if got := result.Runs[0][key]; got != value {
			t.Errorf("runs[0].%s = %#v, want %q", key, got, value)
		}
	}
}

func TestSyncAuditPreservesExplicitRangeWithoutRelativeInputEcho(t *testing.T) {
	t.Parallel()
	resolvedAt := time.Date(2026, 6, 10, 9, 0, 0, 987654321, time.UTC)
	configPath, archivePath, runtime := connectedArchiveViaSetup(t, fakeConnectConfig{
		now:                resolvedAt,
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	runtime.now = func() time.Time { return resolvedAt }
	runtime.fetchRawProvider = func(_ context.Context, _ googlehealth.RawRequest, _ string) ([]byte, error) {
		return []byte(`{"dataPoints":[]}`), nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	if code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--from", "2026-06-08T00:00:00Z",
		"--to", "2026-06-09T00:00:00Z",
		"--json",
	}, stdout, stderr, runtime); code != 0 {
		t.Fatalf("sync exit code = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	db := openArchiveForTest(t, archivePath)
	var rangeJSON string
	if err := db.QueryRowContext(context.Background(), `SELECT range_requested_json FROM sync_runs`).Scan(&rangeJSON); err != nil {
		t.Fatalf("read Sync Run audit: %v", err)
	}
	want := `{"from":"2026-06-08T00:00:00Z","to":"2026-06-09T00:00:00Z","timezone":"UTC","resolved_at":"2026-06-10T09:00:00.987654321Z"}`
	if rangeJSON != want {
		t.Fatalf("range_requested_json = %s, want %s", rangeJSON, want)
	}
}

func TestSyncTimezonePreservesExplicitRFC3339(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	gate := syncPreflightGate{ctx: fakeSyncPreflightContext(now, archivedConnectionForTimezoneTest())}
	const from = "2026-06-07T03:30:00+05:30"
	const to = "2026-06-07T04:30:00+05:30"

	plan, err := gate.Validate(syncCommandOptions{
		dataTypes: []string{"steps"},
		from:      from,
		to:        to,
		timezone:  "America/New_York",
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if plan.from != from || plan.to != to {
		t.Fatalf("plan range = %q..%q, want explicit RFC3339 unchanged", plan.from, plan.to)
	}
}

func TestSyncTimezoneUsesConfigWhenFlagIsAbsent(t *testing.T) {
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
	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		requests = append(requests, request)
		return []byte(`{"dataPoints":[]}`), nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--from", "yesterday",
		"--to", "today",
		"--json",
	}, stdout, stderr, runtime)
	if code != 0 {
		t.Fatalf("sync exit code = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	want := `steps.interval.start_time >= "2026-03-28T23:00:00Z" AND steps.interval.start_time < "2026-03-29T22:00:00Z"`
	if got := mustURLQuery(t, requests[0].URL).Get("filter"); got != want {
		t.Fatalf("filter = %q, want configured timezone filter %q", got, want)
	}
}

func TestSyncTimezoneFlagOverridesConfig(t *testing.T) {
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
	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		requests = append(requests, request)
		return []byte(`{"dataPoints":[]}`), nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--from", "yesterday",
		"--to", "today",
		"--timezone", "America/New_York",
		"--json",
	}, stdout, stderr, runtime)
	if code != 0 {
		t.Fatalf("sync exit code = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	want := `steps.interval.start_time >= "2026-03-29T04:00:00Z" AND steps.interval.start_time < "2026-03-30T04:00:00Z"`
	if got := mustURLQuery(t, requests[0].URL).Get("filter"); got != want {
		t.Fatalf("filter = %q, want flag-overridden filter %q", got, want)
	}
}

func TestSyncLegacyConfigUsesUTCWithoutMachineTimezoneInference(t *testing.T) {
	t.Setenv("TZ", "Pacific/Kiritimati")
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
	legacyConfig := strings.Replace(string(configBytes), `timezone = "UTC"`+"\n", "", 1)
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	runtime.now = func() time.Time { return time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC) }
	var requests []googlehealth.RawRequest
	runtime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, _ string) ([]byte, error) {
		requests = append(requests, request)
		return []byte(`{"dataPoints":[]}`), nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--from", "today",
		"--to", "now",
		"--json",
	}, stdout, stderr, runtime)
	if code != 0 {
		t.Fatalf("sync exit code = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	want := `steps.interval.start_time >= "2026-03-30T00:00:00Z" AND steps.interval.start_time < "2026-03-30T10:15:30Z"`
	if got := mustURLQuery(t, requests[0].URL).Get("filter"); got != want {
		t.Fatalf("filter = %q, want UTC filter %q", got, want)
	}
}

func TestSyncRejectsInvalidConfigTimezoneBeforeProviderOrArchiveWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		timezone string
	}{
		{name: "empty", timezone: ""},
		{name: "invalid", timezone: "Mars/Olympus_Mons"},
	}
	for _, test := range tests {
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
			config := strings.Replace(string(configBytes), `timezone = "UTC"`, `timezone = "`+test.timezone+`"`, 1)
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			providerCalls := 0
			runtime.fetchRawProvider = func(_ context.Context, _ googlehealth.RawRequest, _ string) ([]byte, error) {
				providerCalls++
				return []byte(`{"dataPoints":[]}`), nil
			}

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := runWithRuntime([]string{
				"sync",
				"--config", configPath,
				"--db", archivePath,
				"--types", "steps",
				"--from", "today",
				"--to", "now",
				"--json",
			}, stdout, stderr, runtime)
			if code == 0 {
				t.Fatalf("sync exit code = 0, want config timezone failure\nstdout: %s", stdout.String())
			}
			if providerCalls != 0 {
				t.Fatalf("provider calls = %d, want 0", providerCalls)
			}
			db := openArchiveForTest(t, archivePath)
			var runCount int
			if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sync_runs`).Scan(&runCount); err != nil {
				t.Fatalf("count Sync Runs: %v", err)
			}
			if runCount != 0 {
				t.Fatalf("Sync Run rows = %d, want 0", runCount)
			}
		})
	}
}

func TestSyncRejectsEmptyExplicitTimezone(t *testing.T) {
	t.Parallel()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := run([]string{"sync", "--timezone", ""}, stdout, stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--timezone requires a non-empty IANA timezone") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func archivedConnectionForTimezoneTest() archived.Connection {
	return archived.Connection{ID: "googlehealth:111", ProviderName: "googlehealth"}
}

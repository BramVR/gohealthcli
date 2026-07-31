package main

import (
	"bytes"
	"context"
	"encoding/json"
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

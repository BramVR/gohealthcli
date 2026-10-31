package main

import (
	"bytes"
	"context"
	"github.com/BramVR/gohealthcli/internal/googlehealth"
	"testing"
	"time"
)

// TestSyncWritesHeartbeatAfterEachPage is the issue #236 slice 1
// tracer bullet: while a Sync Run paginates, each archived page must
// be visible to a CONCURRENT reader as fresh counts plus a
// last_progress_at heartbeat on the in-flight sync_running row.
// Before #236 the counts were written only inside the finalize
// transaction, so a 20-minute run polled as 0/0/0 the whole way —
// liveness without progress.
func TestSyncWritesHeartbeatAfterEachPage(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	firstPage := `{
		"dataPoints": [{
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a",
			"dataSource": {"platform": "FITBIT"},
			"steps": {
				"interval": {
					"startTime": "2026-01-01T08:00:00Z",
					"endTime": "2026-01-01T08:15:00Z"
				},
				"count": "512"
			}
		}],
		"nextPageToken": "page-2"
	}`
	secondPage := `{
		"dataPoints": [{
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-b",
			"dataSource": {"platform": "FITBIT"},
			"steps": {
				"interval": {
					"startTime": "2026-01-01T09:00:00Z",
					"endTime": "2026-01-01T09:05:00Z"
				},
				"count": "200"
			}
		}]
	}`

	// The fake provider doubles as the concurrent poller: when the sync
	// asks for page-2, page 1 has been fully archived, so this is
	// exactly the moment a `sync --status` reader in another process
	// would observe the in-flight row.
	var midRun *probedSyncRunRow
	testRuntime.fetchRawProvider = func(_ context.Context, request googlehealth.RawRequest, accessToken string) ([]byte, error) {
		pageToken := mustURLQuery(t, request.URL).Get("pageToken")
		switch pageToken {
		case "":
			return []byte(firstPage), nil
		case "page-2":
			observed := probeSyncRunRow(t, archivePath, 1)
			midRun = &observed
			return []byte(secondPage), nil
		default:
			t.Fatalf("no fake page for pageToken %q", pageToken)
			return nil, nil
		}
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}

	if midRun == nil {
		t.Fatal("fake provider never served page-2; pagination did not happen")
	}
	if midRun.status != "sync_running" {
		t.Fatalf("mid-run status = %q, want sync_running", midRun.status)
	}
	if midRun.finishedAt.Valid {
		t.Fatalf("mid-run finished_at = %q, want NULL while paginating", midRun.finishedAt.String)
	}
	if midRun.seenCount != 1 || midRun.newCount != 1 || midRun.updatedCount != 0 {
		t.Fatalf("mid-run counts = %d/%d/%d, want 1/1/0 after the first page", midRun.seenCount, midRun.newCount, midRun.updatedCount)
	}
	if !midRun.lastProgressAt.Valid || midRun.lastProgressAt.String != "2026-01-02T00:00:00Z" {
		t.Fatalf("mid-run last_progress_at = %+v, want 2026-01-02T00:00:00Z", midRun.lastProgressAt)
	}

	// The terminal write stays authoritative: the finalize transaction
	// still owns the final counts and status.
	assertSyncRun(t, archivePath, 1, "sync_completed", 2, 2, 0, "")
}

func TestSyncStatusDoesNotFenceLiveRetryStorm(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	testRuntime.now = func() time.Time { return now }
	attempts := 0
	testRuntime.fetchRawProvider = func(_ context.Context, _ googlehealth.RawRequest, _ string) ([]byte, error) {
		attempts++
		if attempts < 3 {
			return nil, &googlehealth.HTTPError{StatusCode: 429, RetryAfter: 6 * time.Minute, Endpoint: "steps"}
		}
		return []byte(`{"dataPoints":[]}`), nil
	}
	polledMidRetry := false
	testRuntime.retrySleeper = func(_ context.Context, d time.Duration) bool {
		now = now.Add(d)
		if !polledMidRetry && !now.Before(time.Date(2026, 1, 2, 12, 6, 0, 0, time.UTC)) {
			polledMidRetry = true
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := runWithRuntime([]string{
				"sync",
				"--status",
				"--config", configPath,
				"--db", archivePath,
				"--json",
			}, stdout, stderr, testRuntime)
			if code != 0 {
				t.Fatalf("sync --status mid retry exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
			}
			midRetry := probeSyncRunRow(t, archivePath, 1)
			if midRetry.status != "sync_running" {
				t.Fatalf("mid-retry status = %q, want sync_running; row was fenced", midRetry.status)
			}
		}
		return false
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-02",
		"--to", "2026-01-03T00:00:00Z",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if !polledMidRetry {
		t.Fatal("sync --status was never invoked during retry backoff")
	}
	if attempts != 3 {
		t.Fatalf("fetch attempts = %d, want 3", attempts)
	}
	finished := probeSyncRunRow(t, archivePath, 1)
	if finished.status != "sync_completed" || finished.seenCount != 0 || finished.newCount != 0 || finished.updatedCount != 0 {
		t.Fatalf("finished row = status %q counts %d/%d/%d, want sync_completed 0/0/0", finished.status, finished.seenCount, finished.newCount, finished.updatedCount)
	}
}

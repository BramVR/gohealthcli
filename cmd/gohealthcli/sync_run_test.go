package main

import (
	"strings"
	"testing"
	"time"
)

func TestSyncRunExecutorArchivesDataPointList(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	requests := installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-executor",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:15:00Z"
					},
					"count": "512"
				}
			}]
		}`,
	})

	result, err := (syncRunExecutor{}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("execute Sync Run: %v", err)
	}

	if result.Status != "sync_completed" || result.EndpointFamily != "list" {
		t.Fatalf("Sync Run result = (%q, %q), want completed list", result.Status, result.EndpointFamily)
	}
	if result.DataPointsSeen != 1 || result.DataPointsNew != 1 || result.DataPointsUpdated != 0 {
		t.Fatalf("Data Point counts = (%d, %d, %d), want (1, 1, 0)", result.DataPointsSeen, result.DataPointsNew, result.DataPointsUpdated)
	}
	if len(*requests) != 1 || (*requests)[0].endpointName != "dataTypes.steps.list" {
		t.Fatalf("requests = %#v, want one steps list request", *requests)
	}
	if strings.Contains((*requests)[0].url, "dataSourceFamily") {
		t.Fatalf("list request unexpectedly used source-family filtering: %s", (*requests)[0].url)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "steps", "list", 1, 1, 0, "")
}

func TestSyncRunExecutorArchivesDataPointReconcileForSourceFamily(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	requests := installStepReconcileFetchFake(t, "connect-access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-wearable",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:15:00Z"
					},
					"count": "512"
				}
			}]
		}`,
	})

	result, err := (syncRunExecutor{}).Execute(syncCommandOptions{
		configPath:   configPath,
		archivePath:  archivePath,
		dataTypes:    []string{"steps"},
		sourceFamily: "wearable",
		from:         "2026-01-01",
		to:           "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("execute Sync Run: %v", err)
	}

	if result.Status != "sync_completed" || result.EndpointFamily != "reconcile" || result.SourceFamily != "wearable" {
		t.Fatalf("Sync Run result = (%q, %q, %q), want completed reconcile wearable", result.Status, result.EndpointFamily, result.SourceFamily)
	}
	if len(*requests) != 1 || (*requests)[0].endpointName != "dataTypes.steps.reconcile" {
		t.Fatalf("requests = %#v, want one steps reconcile request", *requests)
	}
	if gotFamily := mustURLQuery(t, (*requests)[0].url).Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"wearable": 1})
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 1, "sync_completed", "reconcile", "wearable", 1, 1, 0, "")
}

func TestSyncRunExecutorArchivesDailyRollups(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	requests := installStepDailyRollupFetchFake(t, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": `{
			"rollupDataPoints": [{
				"steps": {"countSum": "1234"},
				"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
				"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
			}]
		}`,
	})

	result, err := (syncRunExecutor{}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		rollup:      "daily",
		from:        "2026-01-01",
		to:          "2026-01-02",
	})
	if err != nil {
		t.Fatalf("execute Sync Run: %v", err)
	}

	if result.Status != "sync_completed" || result.EndpointFamily != "dailyRollUp" {
		t.Fatalf("Sync Run result = (%q, %q), want completed dailyRollUp", result.Status, result.EndpointFamily)
	}
	if result.DataPointsSeen != 0 || result.RollupsSeen != 1 || result.RollupsNew != 1 || result.RollupsUpdated != 0 {
		t.Fatalf("counts = dataPointsSeen:%d rollups:(%d, %d, %d), want dataPointsSeen:0 rollups:(1, 1, 0)", result.DataPointsSeen, result.RollupsSeen, result.RollupsNew, result.RollupsUpdated)
	}
	if len(*requests) != 1 || (*requests)[0].endpointName != "dataTypes.steps.dailyRollUp" {
		t.Fatalf("requests = %#v, want one steps dailyRollUp request", *requests)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertArchivedStepsDailyRollup(t, archivePath, "1234")
	assertSyncRunWithEndpointFamily(t, archivePath, 1, "sync_completed", "dailyRollUp", 1, 1, 0, "")
}

func TestSyncRunExecutorRecordsFailedListRunForRepeatedPageToken(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	installConnectFakes(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if code := runConnectCommand(t, configPath, archivePath); code != 0 {
		t.Fatalf("connect exit code = %d, want 0", code)
	}
	originalCurrentTime := currentTime
	currentTime = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { currentTime = originalCurrentTime })

	installStepSyncFetchFake(t, "connect-access-secret", map[string]string{
		"":           `{"dataPoints":[],"nextPageToken":"same-token"}`,
		"same-token": `{"dataPoints":[],"nextPageToken":"same-token"}`,
	})

	result, err := (syncRunExecutor{}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err == nil {
		t.Fatal("execute Sync Run error = nil, want repeated page token failure")
	}
	if result.Status != "sync_failed" || !strings.Contains(result.Message, "repeated page token") {
		t.Fatalf("Sync Run result = (%q, %q), want repeated-token failure", result.Status, result.Message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_failed", "steps", "list", 0, 0, 0, "repeated page token")
}

package main

import (
	"strings"
	"testing"
	"time"
)

func TestSyncRunExecutorArchivesDataPointList(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	testRuntime, requests := withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
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

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
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
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	testRuntime, requests := withStepReconcileFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
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

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
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
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC) }

	testRuntime, requests := withStepDailyRollupFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": `{
			"rollupDataPoints": [{
				"steps": {"countSum": "1234"},
				"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
				"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
			}]
		}`,
	})

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
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
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"":           `{"dataPoints":[],"nextPageToken":"same-token"}`,
		"same-token": `{"dataPoints":[],"nextPageToken":"same-token"}`,
	})

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
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

func TestSyncRunExecutorRecordsPartialCountsWhenLaterPageFails(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "connect-access-secret", map[string]string{
		"": `{
			"dataPoints": [{
				"name": "users/me/dataTypes/steps/dataPoints/partial-before-failure",
				"dataSource": {"platform": "FITBIT"},
				"steps": {
					"interval": {
						"startTime": "2026-01-01T08:00:00Z",
						"endTime": "2026-01-01T08:15:00Z"
					}
				}
			}],
			"nextPageToken": "bad-page"
		}`,
		"bad-page": `{`,
	})

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err == nil {
		t.Fatal("execute Sync Run error = nil, want parse failure")
	}
	if result.Status != "sync_failed" || !strings.Contains(result.Message, "not valid JSON") {
		t.Fatalf("Sync Run result = (%q, %q), want JSON failure", result.Status, result.Message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunForDataType(t, archivePath, 1, "sync_failed", "steps", "list", 1, 1, 0, "not valid JSON")
}

func TestSyncRunExecutorAutoRefreshesExpiredAccessTokenAndPersists(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	connectAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                connectAt,
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntime(configPath, archivePath, false, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	// Force the stored access-token expires_at into the past so AccessToken
	// must take the auto-refresh path.
	setConnectionTokenExpiry(t, archivePath, "2026-01-01T00:00:00Z")

	syncNow := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	refreshedExpiresAt := syncNow.Add(time.Hour)
	testRuntime.now = func() time.Time { return syncNow }
	testRuntime.refreshOAuthToken = func(client oauthClientConfig, refreshToken string, fallbackScopes []string) (oauthTokenResponse, error) {
		if refreshToken != "connect-refresh-secret" {
			t.Fatalf("refresh token = %q, want connect-refresh-secret", refreshToken)
		}
		return oauthTokenResponse{
			accessToken:  "rotated-access-secret",
			refreshToken: "connect-refresh-secret",
			tokenType:    "Bearer",
			scopes:       fallbackScopes,
			expiresAt:    refreshedExpiresAt,
			rawTokenMaterialObject: map[string]any{
				"access_token":  "rotated-access-secret",
				"refresh_token": "connect-refresh-secret",
				"token_type":    "Bearer",
				"expires_in":    float64(3600),
			},
		}, nil
	}
	testRuntime.fetchIdentity = func(accessToken string) (googleIdentity, error) {
		if accessToken != "rotated-access-secret" {
			t.Fatalf("identity access token = %q, want rotated token", accessToken)
		}
		return googleIdentity{healthUserID: "111111256096816351", legacyFitbitUserID: "A1B2C3"}, nil
	}

	testRuntime, _ = withStepSyncFetchFake(t, testRuntime, "rotated-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})

	result, err := (syncRunExecutor{runtime: testRuntime}).Execute(syncCommandOptions{
		configPath:  configPath,
		archivePath: archivePath,
		dataTypes:   []string{"steps"},
		from:        "2026-01-01",
		to:          "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("execute Sync Run: %v", err)
	}
	if result.Status != "sync_completed" {
		t.Fatalf("Sync Run status = %q, want sync_completed", result.Status)
	}

	gotMetadata := archivedConnectionTokenMetadata(t, archivePath)
	if !strings.Contains(gotMetadata, refreshedExpiresAt.Format(time.RFC3339)) {
		t.Fatalf("token metadata after sync = %s, want refreshed expires_at %s", gotMetadata, refreshedExpiresAt.Format(time.RFC3339))
	}
}

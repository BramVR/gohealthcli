package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSyncRejectsInvalidSourceFamilyOptionsBeforeSetup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		args        []string
		wantMessage string
	}{
		{
			name:        "unsupported source family",
			args:        []string{"sync", "--source-family", "phone", "--from", "2026-01-01", "--json"},
			wantMessage: "supports only wearable",
		},
		{
			name:        "source family with rollup",
			args:        []string{"sync", "--source-family", "wearable", "--rollup", "daily", "--from", "2026-01-01", "--json"},
			wantMessage: "cannot be combined with --rollup",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := run(tc.args, stdout, stderr)
			if code != 1 {
				t.Fatalf("sync exit code = %d, want 1", code)
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
			}
			assertJSONString(t, got, "status", "sync_failed")
			if !strings.Contains(got["message"].(string), tc.wantMessage) {
				t.Fatalf("message = %q, want %q", got["message"], tc.wantMessage)
			}
			if _, ok := got["sync_run_id"]; ok {
				t.Fatalf("sync_run_id = %v, want omitted before setup", got["sync_run_id"])
			}
		})
	}
}

func TestSyncArchivesStepsIdempotentlyAndTracksRevisions(t *testing.T) {
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
			"dataSource": {"platform": "FITBIT", "device": {"manufacturer": "Google", "model": "Pixel Watch"}},
			"steps": {
				"interval": {
					"startTime": "2026-01-01T08:00:00+01:00",
					"startUtcOffset": "3600s",
					"endTime": "2026-01-01T08:15:00+01:00",
					"endUtcOffset": "3600s",
					"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}},
					"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 15}}
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
	requests := bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"":       firstPage,
		"page-2": secondPage,
	})

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
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "connection_id", "googlehealth:111111256096816351")
	assertJSONString(t, got, "provider_name", "googlehealth")
	assertJSONString(t, got, "from", "2026-01-01")
	assertJSONString(t, got, "to", "2026-01-02T00:00:00Z")
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 2)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertJSONNumber(t, got, "rollups_seen", 0)
	assertJSONNumber(t, got, "rollups_new", 0)
	assertJSONNumber(t, got, "rollups_updated", 0)
	if len(*requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(*requests))
	}
	if (*requests)[0].endpointName != "dataTypes.steps.list" || (*requests)[0].dataType != "steps" {
		t.Fatalf("request target = (%q, %q), want steps list", (*requests)[0].endpointName, (*requests)[0].dataType)
	}
	if strings.Contains((*requests)[0].url, "source") {
		t.Fatalf("sync URL unexpectedly includes source filtering: %s", (*requests)[0].url)
	}
	if pageToken := mustURLQuery(t, (*requests)[1].url).Get("pageToken"); pageToken != "page-2" {
		t.Fatalf("second pageToken = %q, want page-2", pageToken)
	}
	assertArchivedStepDataPoint(t, archivePath)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 1, "sync_completed", 2, 2, 0, "")

	requests = bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"":       firstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--plain",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("second sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	wantPlain := "status: sync_completed\nsync_run_id: 2\nconnection_id: googlehealth:111111256096816351\nprovider_name: googlehealth\ndata_types: steps\nfrom: 2026-01-01\nto: 2026-01-02T00:00:00Z\nendpoint_family: list\ndata_points_seen: 2\ndata_points_new: 0\ndata_points_updated: 0\nrollups_seen: 0\nrollups_new: 0\nrollups_updated: 0\nmessage: Sync Run archived steps Data Points\n"
	if stdout.String() != wantPlain {
		t.Fatalf("stdout = %q, want %q", stdout.String(), wantPlain)
	}
	if len(*requests) != 2 {
		t.Fatalf("second request count = %d, want 2", len(*requests))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 2, "sync_completed", 2, 0, 0, "")

	semanticallySameFirstPage := `{
		"nextPageToken": "page-2",
		"dataPoints": [{
			"steps": {
				"count": "512",
				"interval": {
					"civilEndTime": {"time": {"minutes": 15, "hours": 8}, "date": {"day": 1, "month": 1, "year": 2026}},
					"civilStartTime": {"time": {"hours": 8}, "date": {"day": 1, "month": 1, "year": 2026}},
					"endUtcOffset": "3600s",
					"endTime": "2026-01-01T08:15:00+01:00",
					"startUtcOffset": "3600s",
					"startTime": "2026-01-01T08:00:00+01:00"
				}
			},
			"dataSource": {"device": {"model": "Pixel Watch", "manufacturer": "Google"}, "platform": "FITBIT"},
			"name": "users/me/dataTypes/steps/dataPoints/step-2026-01-01-a"
		}]
	}`
	bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"":       semanticallySameFirstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("semantic sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("semantic stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRun(t, archivePath, 3, "sync_completed", 2, 0, 0, "")

	correctedFirstPage := strings.Replace(firstPage, `"count": "512"`, `"count": "999"`, 1)
	correctedFirstPage = strings.Replace(correctedFirstPage, `"startTime": "2026-01-01T08:00:00+01:00"`, `"startTime": "2026-01-01T08:01:00+01:00"`, 1)
	correctedFirstPage = strings.Replace(correctedFirstPage, `"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}}`, `"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 1}}`, 1)
	bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"":       correctedFirstPage,
		"page-2": secondPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 2)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertSyncRun(t, archivePath, 4, "sync_completed", 2, 0, 1, "")
	assertCorrectedStepRevision(t, archivePath)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesSampleDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	heartRatePage := string(readTestFixture(t, "googlehealth_heart_rate_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "heart-rate", map[string]string{"": heartRatePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00Z" AND heart_rate.sample_time.physical_time < "2026-01-02T00:00:00Z"` {
		t.Fatalf("heart-rate filter = %q", gotFilter)
	}
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a", "heart-rate", "2026-01-01T07:30:00Z", "2026-01-01T08:30:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"beatsPerMinute":"72"`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "heart-rate", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "heart-rate", map[string]string{"": heartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "heart-rate", "list", 1, 0, 0, "")

	correctedHeartRatePage := strings.Replace(heartRatePage, `"beatsPerMinute": "72"`, `"beatsPerMinute": "75"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "heart-rate", map[string]string{"": correctedHeartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/heart-rate/dataPoints/hr-2026-01-01-a", "heart-rate", "2026-01-01T07:30:00Z", "2026-01-01T08:30:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"beatsPerMinute":"75"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "heart-rate", "list", 1, 0, 1, "")

	oxygenPage := string(readTestFixture(t, "googlehealth_oxygen_saturation_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "oxygen-saturation", map[string]string{"": oxygenPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "oxygen-saturation",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("oxygen sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("oxygen stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/oxygen-saturation/dataPoints/spo2-2026-01-01-a", "oxygen-saturation", "2026-01-01T22:10:00Z", "2026-01-01T22:10:00", "2026-01-01", "", `"percentage":"97.5"`)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertSyncRunForDataType(t, archivePath, 4, "sync_completed", "oxygen-saturation", "list", 1, 1, 0, "")

	heartRateVariabilityPage := string(readTestFixture(t, "googlehealth_heart_rate_variability_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "heart-rate-variability", map[string]string{"": heartRateVariabilityPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate-variability",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("heart-rate variability sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("heart-rate variability stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/heart-rate-variability/dataPoints/hrv-2026-01-01-a", "heart-rate-variability", "2026-01-01T05:20:00Z", "2026-01-01T05:20:00", "2026-01-01", "", `"rootMeanSquareOfSuccessiveDifferencesMilliseconds":42.125`)
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertSyncRunForDataType(t, archivePath, 5, "sync_completed", "heart-rate-variability", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesWeightDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	weightPage := string(readTestFixture(t, "googlehealth_weight_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "weight", map[string]string{"": weightPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `weight.sample_time.physical_time >= "2026-01-01T00:00:00Z" AND weight.sample_time.physical_time < "2026-01-02T00:00:00Z"` {
		t.Fatalf("weight filter = %q", gotFilter)
	}
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/weight/dataPoints/weight-2026-01-01", "weight", "2026-01-01T05:45:00Z", "2026-01-01T06:45:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"weightGrams":71234.5`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "weight", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "weight", map[string]string{"": weightPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "weight", "list", 1, 0, 0, "")

	correctedWeightPage := strings.Replace(weightPage, `"weightGrams": 71234.5`, `"weightGrams": 71235.25`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "weight", map[string]string{"": correctedWeightPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/weight/dataPoints/weight-2026-01-01", "weight", "2026-01-01T05:45:00Z", "2026-01-01T06:45:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"weightGrams":71235.25`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "weight", "list", 1, 0, 1, "")

	reconciledWeightPage := `{"dataPoints": [{
		"dataPointName": "users/me/dataTypes/weight/dataPoints/weight-2026-01-01-wearable",
		"weight": {
			"sampleTime": {
				"physicalTime": "2026-01-01T06:45:00+01:00",
				"utcOffset": "3600s",
				"civilTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 6, "minutes": 45}}
			},
			"weightGrams": 71234.5
		}
	}]}`
	reconcileRequests := bindDataPointReconcileFetchFake(t, &testRuntime, "connect-access-secret", "weight", map[string]string{"": reconciledWeightPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "weight",
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("wearable weight sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("wearable weight stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if gotFamily := mustURLQuery(t, (*reconcileRequests)[0].url).Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("weight dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertArchivedSampleDataPoint(t, archivePath, "users/me/dataTypes/weight/dataPoints/weight-2026-01-01-wearable", "weight", "2026-01-01T05:45:00Z", "2026-01-01T06:45:00", "2026-01-01", `{"utc_offset":"3600s"}`, `"weightGrams":71234.5`)
	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, 4, "sync_completed", "weight", "reconcile", "wearable", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesDistanceDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	distancePage := string(readTestFixture(t, "googlehealth_distance_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "distance", map[string]string{"": distancePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `distance.interval.start_time >= "2026-01-01T00:00:00Z" AND distance.interval.start_time < "2026-01-02T00:00:00Z"` {
		t.Fatalf("distance filter = %q", gotFilter)
	}
	assertArchivedIntervalDataPoint(t, archivePath, "users/me/dataTypes/distance/dataPoints/distance-2026-01-01", "distance", "2026-01-01T07:00:00Z", "2026-01-01T07:30:00Z", "2026-01-01T08:00:00", "2026-01-01T08:30:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, "", `"millimeters":"2450"`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "distance", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "distance", map[string]string{"": distancePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "distance", "list", 1, 0, 0, "")

	correctedDistancePage := strings.Replace(distancePage, `"millimeters": "2450"`, `"millimeters": "2500"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "distance", map[string]string{"": correctedDistancePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedIntervalDataPoint(t, archivePath, "users/me/dataTypes/distance/dataPoints/distance-2026-01-01", "distance", "2026-01-01T07:00:00Z", "2026-01-01T07:30:00Z", "2026-01-01T08:00:00", "2026-01-01T08:30:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, "", `"millimeters":"2500"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "distance", "list", 1, 0, 1, "")

	reconciledDistancePage := `{"dataPoints": [{
		"dataPointName": "users/me/dataTypes/distance/dataPoints/distance-2026-01-01-wearable",
		"distance": {
			"interval": {
				"startTime": "2026-01-01T08:00:00+01:00",
				"startUtcOffset": "3600s",
				"endTime": "2026-01-01T08:30:00+01:00",
				"endUtcOffset": "3600s",
				"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8}},
				"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 1}, "time": {"hours": 8, "minutes": 30}}
			},
			"millimeters": "2450"
		}
	}]}`
	reconcileRequests := bindDataPointReconcileFetchFake(t, &testRuntime, "connect-access-secret", "distance", map[string]string{"": reconciledDistancePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "distance",
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("wearable distance sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("wearable distance stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if gotFamily := mustURLQuery(t, (*reconcileRequests)[0].url).Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("distance dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertArchivedIntervalDataPoint(t, archivePath, "users/me/dataTypes/distance/dataPoints/distance-2026-01-01-wearable", "distance", "2026-01-01T07:00:00Z", "2026-01-01T07:30:00Z", "2026-01-01T08:00:00", "2026-01-01T08:30:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, "{}", "wearable", `"millimeters":"2450"`)
	assertSyncRunForDataTypeWithSourceFamily(t, archivePath, 4, "sync_completed", "distance", "reconcile", "wearable", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesDailyDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	restingHeartRatePage := string(readTestFixture(t, "googlehealth_daily_resting_heart_rate_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-resting-heart-rate", map[string]string{"": restingHeartRatePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-resting-heart-rate",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("daily resting heart-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily resting heart-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONString(t, got, "to", "2026-01-02")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if (*requests)[0].endpointName != "dataTypes.daily-resting-heart-rate.list" {
		t.Fatalf("endpoint = %q, want daily Data Type list", (*requests)[0].endpointName)
	}
	if strings.Contains((*requests)[0].url, "dailyRollUp") {
		t.Fatalf("daily Data Point sync used Rollup URL: %s", (*requests)[0].url)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `daily_resting_heart_rate.date >= "2026-01-01" AND daily_resting_heart_rate.date < "2026-01-02"` {
		t.Fatalf("daily resting heart-rate filter = %q", gotFilter)
	}
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-resting-heart-rate/dataPoints/rhr-2026-01-01", "daily-resting-heart-rate", "2026-01-01", `"beatsPerMinute":"61"`)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "daily-resting-heart-rate", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-resting-heart-rate", map[string]string{"": restingHeartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-resting-heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent daily sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent daily stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "daily-resting-heart-rate", "list", 1, 0, 0, "")

	correctedRestingHeartRatePage := strings.Replace(restingHeartRatePage, `"beatsPerMinute": "61"`, `"beatsPerMinute": "63"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-resting-heart-rate", map[string]string{"": correctedRestingHeartRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-resting-heart-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected daily sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected daily stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-resting-heart-rate/dataPoints/rhr-2026-01-01", "daily-resting-heart-rate", "2026-01-01", `"beatsPerMinute":"63"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "daily-resting-heart-rate", "list", 1, 0, 1, "")

	dailyOxygenPage := string(readTestFixture(t, "googlehealth_daily_oxygen_saturation_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-oxygen-saturation", map[string]string{"": dailyOxygenPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-oxygen-saturation",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("daily oxygen sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily oxygen stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-oxygen-saturation/dataPoints/spo2-daily-2026-01-01", "daily-oxygen-saturation", "2026-01-01", `"averagePercentage":96.8`)
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 4, "sync_completed", "daily-oxygen-saturation", "list", 1, 1, 0, "")

	dailyHeartRateVariabilityPage := string(readTestFixture(t, "googlehealth_daily_heart_rate_variability_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-heart-rate-variability", map[string]string{"": dailyHeartRateVariabilityPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-heart-rate-variability",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("daily heart-rate variability sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily heart-rate variability stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-heart-rate-variability/dataPoints/hrv-daily-2026-01-01", "daily-heart-rate-variability", "2026-01-01", `"averageHeartRateVariabilityMilliseconds":45.7`)
	assertArchiveTableCount(t, archivePath, "data_points", 3)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 5, "sync_completed", "daily-heart-rate-variability", "list", 1, 1, 0, "")

	dailyRespiratoryRatePage := string(readTestFixture(t, "googlehealth_daily_respiratory_rate_list.json"))
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "daily-respiratory-rate", map[string]string{"": dailyRespiratoryRatePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "daily-respiratory-rate",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("daily respiratory-rate sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("daily respiratory-rate stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertArchivedDailyDataPoint(t, archivePath, "users/me/dataTypes/daily-respiratory-rate/dataPoints/resp-daily-2026-01-01", "daily-respiratory-rate", "2026-01-01", `"breathsPerMinute":14.2`)
	assertArchiveTableCount(t, archivePath, "data_points", 4)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 6, "sync_completed", "daily-respiratory-rate", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesSleepSessionDataPoints(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC) }

	sleepPage := string(readTestFixture(t, "googlehealth_sleep_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "sleep", map[string]string{"": sleepPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "sleep",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("sleep sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("sleep stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONString(t, got, "to", "2026-01-03")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if (*requests)[0].endpointName != "dataTypes.sleep.list" {
		t.Fatalf("endpoint = %q, want sleep Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `sleep.interval.civil_end_time >= "2026-01-01" AND sleep.interval.civil_end_time < "2026-01-03"` {
		t.Fatalf("sleep filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/sleep/dataPoints/sleep-2026-01-01", "sleep", "2026-01-01T21:30:00Z", "2026-01-02T05:45:00Z", "2026-01-01T22:30:00", "2026-01-02T06:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"type":"LIGHT"`)
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "sleep", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "sleep", map[string]string{"": sleepPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "sleep",
		"--from", "2026-01-01",
		"--to", "2026-01-03",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent sleep sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent sleep stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "sleep", "list", 1, 0, 0, "")

	correctedSleepPage := strings.Replace(sleepPage, `"type": "LIGHT"`, `"type": "REM"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "sleep", map[string]string{"": correctedSleepPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "sleep",
		"--from", "2026-01-01",
		"--to", "2026-01-03",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected sleep sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected sleep stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/sleep/dataPoints/sleep-2026-01-01", "sleep", "2026-01-01T21:30:00Z", "2026-01-02T05:45:00Z", "2026-01-01T22:30:00", "2026-01-02T06:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"type":"REM"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "sleep", "list", 1, 0, 1, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesExerciseSessionDataPointsIdempotentlyAndTracksRevisions(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	exercisePage := string(readTestFixture(t, "googlehealth_exercise_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "exercise", map[string]string{"": exercisePage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "exercise",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("exercise sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("exercise stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertJSONString(t, got, "to", "2026-01-02")
	if (*requests)[0].endpointName != "dataTypes.exercise.list" {
		t.Fatalf("endpoint = %q, want exercise Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `exercise.interval.civil_start_time >= "2026-01-01" AND exercise.interval.civil_start_time < "2026-01-02"` {
		t.Fatalf("exercise filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/exercise/dataPoints/exercise-2026-01-01", "exercise", "2026-01-01T16:15:00Z", "2026-01-01T16:45:00Z", "2026-01-01T17:15:00", "2026-01-01T17:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"exerciseType":"RUNNING"`)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "exercise", "list", 1, 1, 0, "")

	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "exercise", map[string]string{"": exercisePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "exercise",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent exercise sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent exercise stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunForDataType(t, archivePath, 2, "sync_completed", "exercise", "list", 1, 0, 0, "")

	correctedExercisePage := strings.Replace(exercisePage, `"activeDuration": "1800s"`, `"activeDuration": "2100s"`, 1)
	bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "exercise", map[string]string{"": correctedExercisePage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "exercise",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected exercise sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected exercise stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/exercise/dataPoints/exercise-2026-01-01", "exercise", "2026-01-01T16:15:00Z", "2026-01-01T16:45:00Z", "2026-01-01T17:15:00", "2026-01-01T17:45:00", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"activeDuration":"2100s"`)
	assertSyncRunForDataType(t, archivePath, 3, "sync_completed", "exercise", "list", 1, 0, 1, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestSyncArchivesElectrocardiogramSessionDataPoints pins the
// session-parser contract for the Tier 2 ECG Data Type (#104).
// addStoredConnectionScope simulates the user having run
// `connect --add-scopes ecg`; without that the AccessToken call
// would short-circuit on the missing-scope error. The fixture mirrors
// the sleep/exercise session shape because the live probe is deferred
// until the user grants the scope against the live OAuth client.
func TestSyncArchivesElectrocardiogramSessionDataPoints(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	addStoredConnectionScope(t, archivePath, googleHealthEcgReadonlyScope)
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	ecgPage := string(readTestFixture(t, "googlehealth_electrocardiogram_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "electrocardiogram", map[string]string{"": ecgPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "electrocardiogram",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("electrocardiogram sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("electrocardiogram stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if (*requests)[0].endpointName != "dataTypes.electrocardiogram.list" {
		t.Fatalf("endpoint = %q, want electrocardiogram Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `electrocardiogram.interval.civil_start_time >= "2026-01-01" AND electrocardiogram.interval.civil_start_time < "2026-01-02"` {
		t.Fatalf("electrocardiogram filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/electrocardiogram/dataPoints/ecg-2026-01-01", "electrocardiogram", "2026-01-01T09:30:00Z", "2026-01-01T09:30:30Z", "2026-01-01T10:30:00", "2026-01-01T10:30:30", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"classification":"SINUS_RHYTHM"`)
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "electrocardiogram", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

// TestSyncArchivesIrregularRhythmNotificationSessionDataPoints pins
// the session-parser contract for the Tier 2 IRN Data Type (#104).
// Same harness as the ECG test, with the IRN-specific scope and
// fixture payload.
func TestSyncArchivesIrregularRhythmNotificationSessionDataPoints(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	addStoredConnectionScope(t, archivePath, googleHealthIrnReadonlyScope)
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	irnPage := string(readTestFixture(t, "googlehealth_irregular_rhythm_notification_list.json"))
	requests := bindDataPointSyncFetchFake(t, &testRuntime, "connect-access-secret", "irregular-rhythm-notification", map[string]string{"": irnPage})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "irregular-rhythm-notification",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("irregular-rhythm-notification sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("irregular-rhythm-notification stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_completed")
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	if (*requests)[0].endpointName != "dataTypes.irregular-rhythm-notification.list" {
		t.Fatalf("endpoint = %q, want irregular-rhythm-notification Data Type list", (*requests)[0].endpointName)
	}
	if gotFilter := mustURLQuery(t, (*requests)[0].url).Get("filter"); gotFilter != `irregular_rhythm_notification.interval.civil_start_time >= "2026-01-01" AND irregular_rhythm_notification.interval.civil_start_time < "2026-01-02"` {
		t.Fatalf("irregular-rhythm-notification filter = %q", gotFilter)
	}
	assertArchivedSessionDataPoint(t, archivePath, "users/me/dataTypes/irregular-rhythm-notification/dataPoints/irn-2026-01-01", "irregular-rhythm-notification", "2026-01-01T08:00:00Z", "2026-01-01T08:00:30Z", "2026-01-01T09:00:00", "2026-01-01T09:00:30", "2026-01-01", `{"end_utc_offset":"3600s","start_utc_offset":"3600s"}`, `{"platform":"FITBIT","device":{"manufacturer":"Google","model":"Pixel Watch"}}`, `"classification":"ATRIAL_FIBRILLATION_SUGGESTED"`)
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunForDataType(t, archivePath, 1, "sync_completed", "irregular-rhythm-notification", "list", 1, 1, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesWearableStepsViaReconcile(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }

	defaultPage := `{"dataPoints": [{
		"name": "users/me/dataTypes/steps/dataPoints/shared-step",
		"dataSource": {"platform": "FITBIT", "device": {"manufacturer": "Google", "model": "Pixel Watch"}},
		"steps": {"interval": {"startTime": "2026-01-01T08:00:00Z", "endTime": "2026-01-01T08:15:00Z"}, "count": "512"}
	}]}`
	listRequests := bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{"": defaultPage})
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
		t.Fatalf("default sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if len(*listRequests) != 1 {
		t.Fatalf("default request count = %d, want 1", len(*listRequests))
	}
	if query := mustURLQuery(t, (*listRequests)[0].url); query.Get("dataSourceFamily") != "" {
		t.Fatalf("default sync dataSourceFamily = %q, want empty", query.Get("dataSourceFamily"))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 1, "sync_completed", "list", "", 1, 1, 0, "")

	reconciledPage := `{"dataPoints": [{
		"dataPointName": "users/me/dataTypes/steps/dataPoints/shared-step",
		"steps": {"interval": {"startTime": "2026-01-01T08:00:00Z", "endTime": "2026-01-01T08:15:00Z"}, "count": "512"}
	}]}`
	reconcileRequests := bindStepReconcileFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{"": reconciledPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("wearable sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("wearable stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "reconcile")
	assertJSONString(t, got, "source_family", "wearable")
	assertJSONNumber(t, got, "data_points_seen", 1)
	assertJSONNumber(t, got, "data_points_new", 1)
	assertJSONNumber(t, got, "data_points_updated", 0)
	if len(*reconcileRequests) != 1 {
		t.Fatalf("reconcile request count = %d, want 1", len(*reconcileRequests))
	}
	query := mustURLQuery(t, (*reconcileRequests)[0].url)
	if gotFamily := query.Get("dataSourceFamily"); gotFamily != "users/me/dataSourceFamilies/google-wearables" {
		t.Fatalf("dataSourceFamily = %q, want google-wearables", gotFamily)
	}
	wantFilter := `steps.interval.start_time >= "2026-01-01T00:00:00Z" AND steps.interval.start_time < "2026-01-02T00:00:00Z"`
	if gotFilter := query.Get("filter"); gotFilter != wantFilter {
		t.Fatalf("filter = %q, want %q", gotFilter, wantFilter)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 2, "sync_completed", "reconcile", "wearable", 1, 1, 0, "")

	bindStepReconcileFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{"": reconciledPage})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--source-family", "wearable",
		"--from", "2026-01-01",
		"--plain",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent wearable sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "source_family: wearable\n") {
		t.Fatalf("plain stdout = %q, want source family", stdout.String())
	}
	if !strings.Contains(stdout.String(), "data_points_new: 0\n") {
		t.Fatalf("plain stdout = %q, want idempotent count", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 2)
	assertDataPointSourceFamilyCounts(t, archivePath, map[string]int{"": 1, "wearable": 1})
	assertSyncRunWithEndpointFamilyAndSourceFamily(t, archivePath, 3, "sync_completed", "reconcile", "wearable", 1, 0, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncArchivesStepsDailyRollupsOnlyWhenRequested(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC) }

	listRequests := bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"": `{"dataPoints":[]}`,
	})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("default sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("default stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "list")
	assertJSONNumber(t, got, "data_points_seen", 0)
	assertJSONNumber(t, got, "rollups_seen", 0)
	if len(*listRequests) != 1 {
		t.Fatalf("default request count = %d, want 1", len(*listRequests))
	}
	if (*listRequests)[0].endpointName != "dataTypes.steps.list" {
		t.Fatalf("default endpoint = %q, want list", (*listRequests)[0].endpointName)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 0)
	assertSyncRunWithEndpointFamily(t, archivePath, 1, "sync_completed", "list", 0, 0, 0, "")

	firstRollupPage := `{
		"rollupDataPoints": [{
			"steps": {"countSum": "1234"},
			"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}},
			"civilEndTime": {"date": {"year": 2026, "month": 1, "day": 2}}
		}]
	}`
	rollupRequests := bindStepDailyRollupFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": firstRollupPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "endpoint_family", "dailyRollUp")
	assertJSONNumber(t, got, "data_points_seen", 0)
	assertJSONNumber(t, got, "data_points_new", 0)
	assertJSONNumber(t, got, "data_points_updated", 0)
	assertJSONNumber(t, got, "rollups_seen", 1)
	assertJSONNumber(t, got, "rollups_new", 1)
	assertJSONNumber(t, got, "rollups_updated", 0)
	if len(*rollupRequests) != 1 {
		t.Fatalf("rollup request count = %d, want 1", len(*rollupRequests))
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertArchivedStepsDailyRollup(t, archivePath, "1234")
	assertSyncRunWithEndpointFamily(t, archivePath, 2, "sync_completed", "dailyRollUp", 1, 1, 0, "")

	bindStepDailyRollupFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": firstRollupPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("idempotent rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("idempotent rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "rollups_seen", 1)
	assertJSONNumber(t, got, "rollups_new", 0)
	assertJSONNumber(t, got, "rollups_updated", 0)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertSyncRunWithEndpointFamily(t, archivePath, 3, "sync_completed", "dailyRollUp", 1, 0, 0, "")

	correctedRollupPage := strings.Replace(firstRollupPage, `"countSum": "1234"`, `"countSum": "4321"`, 1)
	bindStepDailyRollupFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"2026-01-01/2026-01-02/": correctedRollupPage,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("corrected rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("corrected rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "rollups_seen", 1)
	assertJSONNumber(t, got, "rollups_new", 0)
	assertJSONNumber(t, got, "rollups_updated", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertArchivedStepsDailyRollup(t, archivePath, "4321")
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 0)
	assertSyncRunWithEndpointFamily(t, archivePath, 4, "sync_completed", "dailyRollUp", 1, 0, 1, "")

	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01T12:00:00",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("timed rollup sync exit code = %d, want 1\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	// The gate's preflight message names both supported shapes and the
	// rollup kind so an operator hears exactly what the rollup will
	// accept; this replaces the slice-2 planner-stage "expected
	// YYYY-MM-DD" error with a richer local rejection.
	if !strings.Contains(stdout.String(), "expected YYYY-MM-DD or RFC3339") {
		t.Fatalf("timed rollup stdout = %q, want supported-shapes error", stdout.String())
	}
	if !strings.Contains(stdout.String(), "daily") {
		t.Fatalf("timed rollup stdout = %q, want rollup kind named", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	// PRD #141 slice 3: civil-vs-RFC3339 input-shape errors are caught at
	// the preflight gate before any sync_run row is written. Previously
	// this scenario produced an audit row from the planner-stage parse
	// error; the gate now owns the contract so the archive must show
	// only the 4 rows from the earlier successful invocations.
	assertArchiveTableCount(t, archivePath, "sync_runs", 4)

	longRangeRequests := bindStepDailyRollupFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"2026-01-01/2026-04-01/": `{"rollupDataPoints": [{
			"steps": {"countSum": "9000"},
			"civilStartTime": {"date": {"year": 2026, "month": 4, "day": 1}},
			"civilEndTime": {"date": {"year": 2026, "month": 4, "day": 2}}
		}]}`,
		"2026-04-01/2026-04-15/": `{"rollupDataPoints": [{
			"steps": {"countSum": "1400"},
			"civilStartTime": {"date": {"year": 2026, "month": 4, "day": 2}},
			"civilEndTime": {"date": {"year": 2026, "month": 4, "day": 3}}
		}]}`,
	})
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	code = runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--rollup", "daily",
		"--from", "2026-01-01",
		"--to", "2026-04-15",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("long rollup sync exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if len(*longRangeRequests) != 2 {
		t.Fatalf("long rollup request count = %d, want 2", len(*longRangeRequests))
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("long rollup stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONNumber(t, got, "rollups_seen", 2)
	assertJSONNumber(t, got, "rollups_new", 2)
	assertArchiveTableCount(t, archivePath, "rollups", 3)
	// Sync Run id is 5 (not 6) because the preceding civil-shape failure
	// is now caught at the gate and does not write a sync_run row.
	assertSyncRunWithEndpointFamily(t, archivePath, 5, "sync_completed", "dailyRollUp", 2, 2, 0, "")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncProviderFailureRecordsFailedRun(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		if accessToken != "connect-access-secret" {
			t.Fatalf("sync access token = %q, want stored token", accessToken)
		}
		return nil, errors.New("Google Health raw request failed with HTTP 503")
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "steps",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	message := got["message"].(string)
	if !strings.Contains(message, "HTTP 503") {
		t.Fatalf("message = %q, want provider status", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, "HTTP 503")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncRefusesDifferentProviderIdentityBeforeArchiving(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindIdentityFetchFake(t, &testRuntime, "connect-access-secret", googleIdentity{
		healthUserID:       "222222222222222222",
		legacyFitbitUserID: "DIFFERENT",
		rawJSON:            `{"healthUserId":"222222222222222222","legacyUserId":"DIFFERENT"}`,
	})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run after identity mismatch")
		return nil, nil
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
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	if message := got["message"].(string); !strings.Contains(message, "different Google Identity") {
		t.Fatalf("message = %q, want identity mismatch", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, "different Google Identity")
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncReportsFailedWhenCompletionRecordFails(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindStepSyncFetchFake(t, &testRuntime, "connect-access-secret", map[string]string{
		"": `{
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
			}]
		}`,
	})
	// Wrap the writer so FinalizeSyncRun (the atomic sync_run+cursor write)
	// fails when called for a sync_completed outcome. This exercises the
	// CLI's "atomic finalize failed → recover-as-sync_failed" path without
	// reaching past the adapters seam the executor routes every archive
	// open through.
	testRuntime.openHealthArchiveWriter = func(path string) (healthArchiveWriter, error) {
		inner, err := openHealthArchiveWriter(path)
		if err != nil {
			return nil, err
		}
		return fakeFinalizeWriter{healthArchiveWriter: inner, failOn: failOnCompletedOutcome(errSimulatedFinalizeCompletedFailure)}, nil
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
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	assertJSONString(t, got, "status", "sync_failed")
	assertJSONNumber(t, got, "sync_run_id", 1)
	assertJSONNumber(t, got, "data_points_seen", 1)
	if message := got["message"].(string); !strings.Contains(message, "archive finalization failed") {
		t.Fatalf("message = %q, want finalization error", message)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncFailsBeforeProviderWhenScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googleHealthProfileReadonlyScope})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run with missing scope")
		return nil, nil
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
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), googleHealthActivityReadonlyScope) || !strings.Contains(stdout.String(), "connect") {
		t.Fatalf("stdout = %q, want missing scope reconnect hint", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRun(t, archivePath, 1, "sync_failed", 0, 0, 0, googleHealthActivityReadonlyScope)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

func TestSyncSampleDataTypeFailsBeforeProviderWhenHealthMetricsScopeMissing(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	setConnectionTokenScopes(t, archivePath, []string{googleHealthProfileReadonlyScope, googleHealthActivityReadonlyScope})
	testRuntime.fetchRawProvider = func(_ context.Context, request rawProviderRequest, accessToken string) ([]byte, error) {
		t.Fatal("sync provider fetch should not run with missing health metrics scope")
		return nil, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"sync",
		"--config", configPath,
		"--db", archivePath,
		"--types", "heart-rate",
		"--from", "2026-01-01",
		"--json",
	}, stdout, stderr, testRuntime)
	if code != 1 {
		t.Fatalf("sync exit code = %d, want 1", code)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), googleHealthHealthMetricsReadonlyScope) || !strings.Contains(stdout.String(), "connect") {
		t.Fatalf("stdout = %q, want missing health metrics scope reconnect hint", stdout.String())
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertSyncRunForDataType(t, archivePath, 1, "sync_failed", "heart-rate", "list", 0, 0, 0, googleHealthHealthMetricsReadonlyScope)
	assertNoSecretWords(t, stdout.String()+stderr.String())
}

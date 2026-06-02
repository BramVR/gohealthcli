package main

import (
	"strings"
	"testing"
)

func TestHealthArchiveWriterRequiresCurrentConnection(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	_, err = archive.CurrentConnection()
	if err == nil || !strings.Contains(err.Error(), "no Connection found") {
		t.Fatalf("CurrentConnection error = %v, want no Connection found", err)
	}
}

func TestHealthArchiveWriterRecordsDataPointRevisionsRollupsAndSyncRun(t *testing.T) {
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

	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatalf("open Health Archive writer: %v", err)
	}
	defer archive.Close()

	connection, err := archive.CurrentConnection()
	if err != nil {
		t.Fatalf("CurrentConnection: %v", err)
	}
	startedAt := "2026-01-01T00:00:00Z"
	syncRunID, err := archive.StartSyncRun(connection, []string{"steps"}, "2026-01-01", "2026-01-02", "list", "", startedAt)
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}

	point := archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "steps",
		upstreamResourceName: "users/me/dataTypes/steps/dataPoints/archive-writer",
		recordKind:           "interval",
		startTimeUTC:         "2026-01-01T08:00:00Z",
		endTimeUTC:           "2026-01-01T08:15:00Z",
		dataSourceJSON:       `{"platform":"FITBIT"}`,
		rawJSON:              `{"steps":{"count":"123"}}`,
	}
	if status, err := archive.UpsertDataPoint(point, "2026-01-01T00:00:01Z"); err != nil || status != "new" {
		t.Fatalf("first UpsertDataPoint = (%q, %v), want new", status, err)
	}
	if status, err := archive.UpsertDataPoint(point, "2026-01-01T00:00:02Z"); err != nil || status != "unchanged" {
		t.Fatalf("same UpsertDataPoint = (%q, %v), want unchanged", status, err)
	}
	point.rawJSON = `{"steps":{"count":"456"}}`
	if status, err := archive.UpsertDataPoint(point, "2026-01-01T00:00:03Z"); err != nil || status != "updated" {
		t.Fatalf("corrected UpsertDataPoint = (%q, %v), want updated", status, err)
	}

	rollup := archivedRollup{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             "steps",
		rollupKind:           "dailyRollUp",
		civilDate:            "2026-01-01",
		timezoneMetadataJSON: "{}",
		rawJSON:              `{"steps":{"countSum":"123"}}`,
	}
	if status, err := archive.UpsertRollup(rollup, "2026-01-01T00:00:04Z"); err != nil || status != "new" {
		t.Fatalf("first UpsertRollup = (%q, %v), want new", status, err)
	}
	rollup.rawJSON = `{"steps":{"countSum":"456"}}`
	if status, err := archive.UpsertRollup(rollup, "2026-01-01T00:00:05Z"); err != nil || status != "updated" {
		t.Fatalf("corrected UpsertRollup = (%q, %v), want updated", status, err)
	}

	if err := archive.FinishSyncRun(syncRunID, "sync_completed", 4, 2, 2, "2026-01-01T00:00:06Z", ""); err != nil {
		t.Fatalf("FinishSyncRun: %v", err)
	}
	assertArchiveTableCount(t, archivePath, "data_points", 1)
	assertArchiveTableCount(t, archivePath, "data_point_revisions", 1)
	assertArchiveTableCount(t, archivePath, "rollups", 1)
	assertSyncRunForDataType(t, archivePath, syncRunID, "sync_completed", "steps", "list", 4, 2, 2, "")
}

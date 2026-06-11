package main

import "testing"

func TestHealthArchiveReaderSummarizesQueriesAndExportsReadOnly(t *testing.T) {
	tempDir := t.TempDir()
	_, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	insertStatusFixtureRows(t, archivePath)

	reader, err := openHealthArchiveReader(archivePath)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()

	status, err := reader.StatusSummary()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Status != "ok" {
		t.Fatalf("status = %q, want ok", status.Status)
	}
	if status.DataPointCount != 3 || status.RollupCount != 1 || status.ProfileSnapshotCount != 1 || status.SyncRunCount != 3 {
		t.Fatalf("counts = data_points:%d rollups:%d profiles:%d sync_runs:%d", status.DataPointCount, status.RollupCount, status.ProfileSnapshotCount, status.SyncRunCount)
	}
	if len(status.DataTypes) != 2 {
		t.Fatalf("data type count = %d, want 2: %+v", len(status.DataTypes), status.DataTypes)
	}
	if status.LatestSuccessfulRun == nil || status.LatestSuccessfulRun.Status != "sync_completed" {
		t.Fatalf("latest successful run = %+v, want sync_completed", status.LatestSuccessfulRun)
	}

	encoder := newPlainModeEncoder()
	query, err := reader.Query(`SELECT count(*) AS data_point_count FROM data_points`, encoder)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if query.Status != "query_completed" || query.RowCount != 1 || len(query.Rows) != 1 {
		t.Fatalf("query result = %+v, want one completed row", query)
	}
	if got, ok := query.Rows[0][0].(int64); !ok || got != 3 {
		t.Fatalf("query row value = %T(%v), want int64(3)", query.Rows[0][0], query.Rows[0][0])
	}

	if _, err := reader.Query(`DELETE FROM data_points`, encoder); err == nil {
		t.Fatal("mutating query error = nil, want rejected")
	}

	rows, err := reader.ExportRows(exportDatasetSpecs["daily-steps"])
	if err != nil {
		t.Fatalf("daily steps rows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("daily steps row count = %d, want 3: %+v", len(rows), rows)
	}
	assertDailyStepsRow(t, rows[0], "2026-01-01", 512, "dataPoints", "", 1)
}

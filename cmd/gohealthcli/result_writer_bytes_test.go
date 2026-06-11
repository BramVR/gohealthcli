package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the EXACT output bytes of the result writer families
// the sticky-error writer conversion touches (#274): status, doctor,
// sync, and sync fan-out, each across json, plain, and human modes.
// The fixtures exercise every conditional line so a conversion that
// drops, reorders, or reformats a single field fails here byte-first.

func statusWriterFixtureRich() statusResult {
	return statusResult{
		Status:                "ok",
		ArchivePath:           "/home/bram/.local/share/gohealthcli/gohealthcli.sqlite",
		SchemaVersion:         21,
		DataPointCount:        165432,
		RollupCount:           87,
		ProfileSnapshotCount:  3,
		IdentitySnapshotCount: 9,
		SyncRunCount:          42,
		KnownDataTypes:        []string{"heart-rate", "steps"},
		DataTypes: []statusDataType{
			{
				DataType:                 "heart-rate",
				DataPointCount:           160000,
				RollupCount:              0,
				NewestDataPointTimestamp: "2026-06-10T07:55:00Z",
			},
			{
				DataType:                 "steps",
				DataPointCount:           5432,
				RollupCount:              87,
				NewestDataPointTimestamp: "2026-06-10T08:15:00Z",
				NewestRollupTimestamp:    "2026-06-09",
				SyncCursors: []statusSyncCursor{
					{RollupKind: "none", CursorTime: "2026-06-10T00:00:00Z", AdvancedAt: "2026-06-10T08:20:11Z"},
					{SourceFamilyFilter: "wearable", RollupKind: "daily", CursorTime: "2026-06-09T00:00:00Z", AdvancedAt: "2026-06-09T21:04:33Z"},
				},
			},
		},
		PairedDeviceCount: 2,
		IdentitySnapshotsFreshness: &statusSnapshotFreshness{
			PairedDeviceCount: 2,
			LatestFetchedAt: map[string]string{
				"profile":        "2026-06-08T06:00:00Z",
				"settings":       "2026-06-08T06:00:05Z",
				"paired-devices": "2026-06-08T06:00:10Z",
				"irn-profile":    "2026-06-08T06:00:15Z",
			},
		},
		Tier2: &statusTier2{
			ElectrocardiogramEventCount:             4,
			ElectrocardiogramScopeGranted:           true,
			IrregularRhythmNotificationCount:        1,
			IrregularRhythmNotificationScopeGranted: true,
		},
		LatestSuccessfulRun: &statusSyncRun{
			ID:                 41,
			Status:             "sync_completed",
			DataTypes:          []string{"steps"},
			From:               "2026-06-09",
			To:                 "2026-06-10T00:00:00Z",
			EndpointFamily:     "reconcile",
			SourceFamilyFilter: "wearable",
			SeenCount:          120,
			NewCount:           110,
			UpdatedCount:       10,
			StartedAt:          "2026-06-10T08:19:02Z",
			FinishedAt:         "2026-06-10T08:20:11Z",
		},
		LatestFailedRun: &statusSyncRun{
			ID:           42,
			Status:       "sync_failed",
			DataTypes:    []string{"heart-rate"},
			StartedAt:    "2026-06-10T09:00:00Z",
			FinishedAt:   "2026-06-10T09:00:30Z",
			ErrorSummary: "Provider timeout after 30s",
		},
		Message: "Health Archive status summarized",
	}
}

func statusWriterFixtureMinimal() statusResult {
	return statusResult{
		Status:  "status_failed",
		Message: "open archive: no such file",
	}
}

const statusWriterRichJSON = `{
  "status": "ok",
  "archive_path": "/home/bram/.local/share/gohealthcli/gohealthcli.sqlite",
  "schema_version": 21,
  "data_point_count": 165432,
  "rollup_count": 87,
  "profile_snapshot_count": 3,
  "identity_snapshot_count": 9,
  "sync_run_count": 42,
  "known_data_types": [
    "heart-rate",
    "steps"
  ],
  "data_types": [
    {
      "data_type": "heart-rate",
      "data_point_count": 160000,
      "rollup_count": 0,
      "newest_data_point_timestamp": "2026-06-10T07:55:00Z"
    },
    {
      "data_type": "steps",
      "data_point_count": 5432,
      "rollup_count": 87,
      "newest_data_point_timestamp": "2026-06-10T08:15:00Z",
      "newest_rollup_timestamp": "2026-06-09",
      "sync_cursors": [
        {
          "rollup_kind": "none",
          "cursor_time": "2026-06-10T00:00:00Z",
          "advanced_at": "2026-06-10T08:20:11Z"
        },
        {
          "source_family_filter": "wearable",
          "rollup_kind": "daily",
          "cursor_time": "2026-06-09T00:00:00Z",
          "advanced_at": "2026-06-09T21:04:33Z"
        }
      ]
    }
  ],
  "paired_device_count": 2,
  "identity_snapshots_freshness": {
    "paired_device_count": 2,
    "latest_fetched_at": {
      "irn-profile": "2026-06-08T06:00:15Z",
      "paired-devices": "2026-06-08T06:00:10Z",
      "profile": "2026-06-08T06:00:00Z",
      "settings": "2026-06-08T06:00:05Z"
    }
  },
  "tier_2": {
    "electrocardiogram_event_count": 4,
    "electrocardiogram_scope_granted": true,
    "irregular_rhythm_notification_count": 1,
    "irregular_rhythm_notification_scope_granted": true
  },
  "latest_successful_sync_run": {
    "id": 41,
    "status": "sync_completed",
    "data_types": [
      "steps"
    ],
    "from": "2026-06-09",
    "to": "2026-06-10T00:00:00Z",
    "endpoint_family": "reconcile",
    "source_family_filter": "wearable",
    "seen_count": 120,
    "new_count": 110,
    "updated_count": 10,
    "started_at": "2026-06-10T08:19:02Z",
    "finished_at": "2026-06-10T08:20:11Z"
  },
  "latest_failed_sync_run": {
    "id": 42,
    "status": "sync_failed",
    "data_types": [
      "heart-rate"
    ],
    "seen_count": 0,
    "new_count": 0,
    "updated_count": 0,
    "started_at": "2026-06-10T09:00:00Z",
    "finished_at": "2026-06-10T09:00:30Z",
    "error_summary": "Provider timeout after 30s"
  },
  "message": "Health Archive status summarized"
}
`

const statusWriterRichPlain = `status: ok
archive_path: /home/bram/.local/share/gohealthcli/gohealthcli.sqlite
schema_version: 21
data_point_count: 165432
rollup_count: 87
profile_snapshot_count: 3
identity_snapshot_count: 9
sync_run_count: 42
known_data_types: heart-rate,steps
data_type.heart-rate.data_point_count: 160000
data_type.heart-rate.rollup_count: 0
data_type.heart-rate.newest_data_point_timestamp: 2026-06-10T07:55:00Z
data_type.steps.data_point_count: 5432
data_type.steps.rollup_count: 87
data_type.steps.newest_data_point_timestamp: 2026-06-10T08:15:00Z
data_type.steps.newest_rollup_timestamp: 2026-06-09
data_type.steps.sync_cursor.0.rollup_kind: none
data_type.steps.sync_cursor.0.cursor_time: 2026-06-10T00:00:00Z
data_type.steps.sync_cursor.0.advanced_at: 2026-06-10T08:20:11Z
data_type.steps.sync_cursor.1.rollup_kind: daily
data_type.steps.sync_cursor.1.source_family_filter: wearable
data_type.steps.sync_cursor.1.cursor_time: 2026-06-09T00:00:00Z
data_type.steps.sync_cursor.1.advanced_at: 2026-06-09T21:04:33Z
paired_device_count: 2
identity_snapshot.profile.fetched_at: 2026-06-08T06:00:00Z
identity_snapshot.settings.fetched_at: 2026-06-08T06:00:05Z
identity_snapshot.paired-devices.fetched_at: 2026-06-08T06:00:10Z
identity_snapshot.irn-profile.fetched_at: 2026-06-08T06:00:15Z
electrocardiogram_event_count: 4
irregular_rhythm_notification_count: 1
latest_successful_sync_run_id: 41
latest_successful_sync_run_status: sync_completed
latest_successful_sync_run_data_types: steps
latest_successful_sync_run_from: 2026-06-09
latest_successful_sync_run_to: 2026-06-10T00:00:00Z
latest_successful_sync_run_endpoint_family: reconcile
latest_successful_sync_run_source_family_filter: wearable
latest_successful_sync_run_seen_count: 120
latest_successful_sync_run_new_count: 110
latest_successful_sync_run_updated_count: 10
latest_successful_sync_run_started_at: 2026-06-10T08:19:02Z
latest_successful_sync_run_finished_at: 2026-06-10T08:20:11Z
latest_failed_sync_run_id: 42
latest_failed_sync_run_status: sync_failed
latest_failed_sync_run_data_types: heart-rate
latest_failed_sync_run_seen_count: 0
latest_failed_sync_run_new_count: 0
latest_failed_sync_run_updated_count: 0
latest_failed_sync_run_started_at: 2026-06-10T09:00:00Z
latest_failed_sync_run_finished_at: 2026-06-10T09:00:30Z
latest_failed_sync_run_error_summary: Provider timeout after 30s
message: Health Archive status summarized
`

const statusWriterRichHuman = `Health Archive status
Health Archive: /home/bram/.local/share/gohealthcli/gohealthcli.sqlite
Schema version: 21
Counts: 165432 Data Points, 87 Rollups, 9 Identity Snapshots (3 Profile), 42 Sync Runs
Known Data Types: heart-rate, steps
- heart-rate: 160000 Data Points, 0 Rollups, newest Data Point 2026-06-10T07:55:00Z
- steps: 5432 Data Points, 87 Rollups, newest Data Point 2026-06-10T08:15:00Z, newest Rollup 2026-06-09, Sync Cursor (none) 2026-06-10T00:00:00Z, Sync Cursor (daily/wearable) 2026-06-09T00:00:00Z
Latest successful Sync Run: 41 (2026-06-09 to 2026-06-10T00:00:00Z)
Latest failed Sync Run: 42 (Provider timeout after 30s)
Message: Health Archive status summarized
`

const statusWriterMinimalJSON = `{
  "status": "status_failed",
  "archive_path": "",
  "data_point_count": 0,
  "rollup_count": 0,
  "profile_snapshot_count": 0,
  "identity_snapshot_count": 0,
  "sync_run_count": 0,
  "message": "open archive: no such file"
}
`

const statusWriterMinimalPlain = `status: status_failed
data_point_count: 0
rollup_count: 0
profile_snapshot_count: 0
identity_snapshot_count: 0
sync_run_count: 0
message: open archive: no such file
`

const statusWriterMinimalHuman = `Health Archive status failed
Counts: 0 Data Points, 0 Rollups, 0 Identity Snapshots (0 Profile), 0 Sync Runs
Message: open archive: no such file
`

func TestStatusWriterEmitsByteIdenticalOutputAcrossModes(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result statusResult
		mode   outputMode
		want   string
	}{
		{"rich json", statusWriterFixtureRich(), outputMode{json: true}, statusWriterRichJSON},
		{"rich plain", statusWriterFixtureRich(), outputMode{plain: true}, statusWriterRichPlain},
		{"rich human", statusWriterFixtureRich(), outputMode{}, statusWriterRichHuman},
		{"minimal json", statusWriterFixtureMinimal(), outputMode{json: true}, statusWriterMinimalJSON},
		{"minimal plain", statusWriterFixtureMinimal(), outputMode{plain: true}, statusWriterMinimalPlain},
		{"minimal human", statusWriterFixtureMinimal(), outputMode{}, statusWriterMinimalHuman},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			buffer := new(bytes.Buffer)
			if err := writeStatusResult(testCase.result, testCase.mode, buffer); err != nil {
				t.Fatalf("writeStatusResult: %v", err)
			}
			if got := buffer.String(); got != testCase.want {
				t.Fatalf("status %s output drifted:\ngot:\n%q\nwant:\n%q", testCase.name, got, testCase.want)
			}
		})
	}
}

// TestStatusReportsFirstWriteErrorOnce pins the write-output failure
// contract the sticky-error writer must preserve (#274): when stdout
// rejects the very first write, status exits 1 and the operator sees
// exactly one `status: write output: ...` line on stderr.
func TestStatusReportsFirstWriteErrorOnce(t *testing.T) {
	stderr := new(bytes.Buffer)

	code := runStatus(
		[]string{"--db", filepath.Join(t.TempDir(), "missing.sqlite")},
		defaultConfigPath(),
		defaultArchivePath(),
		false,
		false,
		outputMode{},
		failingWriter{},
		stderr,
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got, want := stderr.String(), "status: write output: write failed\n"; got != want {
		t.Fatalf("stderr = %q, want exactly %q", got, want)
	}
	if count := strings.Count(stderr.String(), "write output"); count != 1 {
		t.Fatalf("write failure reported %d times, want once", count)
	}
}

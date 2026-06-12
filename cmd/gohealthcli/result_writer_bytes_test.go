package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	t.Parallel()
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

func doctorWriterFixtureRich() doctorResult {
	schemaVersion := 21
	connectionCount := 1
	return doctorResult{
		Status:             "ok",
		ConfigPath:         "/home/bram/.config/gohealthcli/config.toml",
		ArchivePath:        "/home/bram/.local/share/gohealthcli/gohealthcli.sqlite",
		OAuthClientSource:  "file:client_secret.json",
		CredentialStore:    "os_native",
		SchemaVersion:      &schemaVersion,
		ConnectionCount:    &connectionCount,
		TokenStatus:        "ok",
		AttachmentRootPath: "/home/bram/.local/share/gohealthcli/attachments",
		AttachmentRootMode: "0700",
		Attachments: &doctorAttachmentReport{
			OrphanRows:  []doctorOrphanRow{{SHA256: "abc", PathRelative: "ab/abc", DataPointID: 7}},
			OrphanFiles: []doctorOrphanFile{{AbsolutePath: "/tmp/orphan"}},
		},
		Message: "local gohealthcli setup is initialized",
	}
}

func doctorWriterFixtureMinimal() doctorResult {
	return doctorResult{
		Status:      "setup_missing",
		ConfigPath:  "/home/bram/.config/gohealthcli/config.toml",
		ArchivePath: "/home/bram/.local/share/gohealthcli/gohealthcli.sqlite",
		TokenStatus: "unknown",
		Message:     "local gohealthcli setup not found",
	}
}

const doctorWriterRichJSON = `{
  "status": "ok",
  "config_path": "/home/bram/.config/gohealthcli/config.toml",
  "archive_path": "/home/bram/.local/share/gohealthcli/gohealthcli.sqlite",
  "oauth_client_source": "file:client_secret.json",
  "credential_store": "os_native",
  "schema_version": 21,
  "connection_count": 1,
  "token_status": "ok",
  "attachment_root_path": "/home/bram/.local/share/gohealthcli/attachments",
  "attachment_root_mode": "0700",
  "attachments": {
    "orphan_rows": [
      {
        "sha256": "abc",
        "path_relative": "ab/abc",
        "data_point_id": 7
      }
    ],
    "orphan_files": [
      {
        "absolute_path": "/tmp/orphan"
      }
    ]
  },
  "message": "local gohealthcli setup is initialized"
}
`

const doctorWriterRichPlain = `status: ok
config_path: /home/bram/.config/gohealthcli/config.toml
archive_path: /home/bram/.local/share/gohealthcli/gohealthcli.sqlite
oauth_client_source: file:client_secret.json
credential_store: os_native
schema_version: 21
connection_count: 1
token_status: ok
attachment_root_path: /home/bram/.local/share/gohealthcli/attachments
attachment_root_mode: 0700
attachments_orphan_files: 1
attachments_orphan_rows: 1
message: local gohealthcli setup is initialized
`

const doctorWriterRichHuman = `Setup ok
Config: /home/bram/.config/gohealthcli/config.toml
Health Archive: /home/bram/.local/share/gohealthcli/gohealthcli.sqlite
OAuth client source: file:client_secret.json
Credential Store: os_native
Schema version: 21
Connections: 1
Token status: ok
Message: local gohealthcli setup is initialized
`

const doctorWriterMinimalJSON = `{
  "status": "setup_missing",
  "config_path": "/home/bram/.config/gohealthcli/config.toml",
  "archive_path": "/home/bram/.local/share/gohealthcli/gohealthcli.sqlite",
  "oauth_client_source": "",
  "credential_store": "",
  "schema_version": null,
  "connection_count": null,
  "token_status": "unknown",
  "message": "local gohealthcli setup not found"
}
`

const doctorWriterMinimalPlain = `status: setup_missing
config_path: /home/bram/.config/gohealthcli/config.toml
archive_path: /home/bram/.local/share/gohealthcli/gohealthcli.sqlite
token_status: unknown
message: local gohealthcli setup not found
`

const doctorWriterMinimalHuman = `Setup missing
Config: /home/bram/.config/gohealthcli/config.toml
Health Archive: /home/bram/.local/share/gohealthcli/gohealthcli.sqlite
Token status: unknown
Message: local gohealthcli setup not found
`

func TestDoctorWriterEmitsByteIdenticalOutputAcrossModes(t *testing.T) {
	t.Parallel()
	unhealthy := doctorResult{Status: "connection_unhealthy", ConfigPath: "/c", ArchivePath: "/a", Message: "refresh failed"}
	invalid := doctorResult{Status: "setup_invalid", ConfigPath: "/c", ArchivePath: "/a", Message: "config check failed"}
	for _, testCase := range []struct {
		name   string
		result doctorResult
		mode   outputMode
		want   string
	}{
		{"rich json", doctorWriterFixtureRich(), outputMode{json: true}, doctorWriterRichJSON},
		{"rich plain", doctorWriterFixtureRich(), outputMode{plain: true}, doctorWriterRichPlain},
		{"rich human", doctorWriterFixtureRich(), outputMode{}, doctorWriterRichHuman},
		{"minimal json", doctorWriterFixtureMinimal(), outputMode{json: true}, doctorWriterMinimalJSON},
		{"minimal plain", doctorWriterFixtureMinimal(), outputMode{plain: true}, doctorWriterMinimalPlain},
		{"minimal human", doctorWriterFixtureMinimal(), outputMode{}, doctorWriterMinimalHuman},
		{"unhealthy human header", unhealthy, outputMode{}, "Connection unhealthy\nConfig: /c\nHealth Archive: /a\nMessage: refresh failed\n"},
		{"invalid human header", invalid, outputMode{}, "Setup invalid\nConfig: /c\nHealth Archive: /a\nMessage: config check failed\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			buffer := new(bytes.Buffer)
			if err := writeDoctorResult(testCase.result, testCase.mode, buffer); err != nil {
				t.Fatalf("writeDoctorResult: %v", err)
			}
			if got := buffer.String(); got != testCase.want {
				t.Fatalf("doctor %s output drifted:\ngot:\n%q\nwant:\n%q", testCase.name, got, testCase.want)
			}
		})
	}
}

// TestDoctorReportsFirstWriteErrorOnce pins the doctor side of the
// write-output failure contract (#274): a stdout that rejects the very
// first write must surface exactly one `doctor: write output: ...`
// stderr line with exit code 1 — not the setup_missing exit code 2,
// because the failure is the broken stdout, not the missing setup.
func TestDoctorReportsFirstWriteErrorOnce(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	stderr := new(bytes.Buffer)

	code := runDoctorWithRuntime(
		[]string{"--config", filepath.Join(tempDir, "config.toml"), "--db", filepath.Join(tempDir, "missing.sqlite")},
		CommonFlagValues{ConfigPath: defaultConfigPath(), ArchivePath: defaultArchivePath()},
		failingWriter{},
		stderr,
		productionRuntimeAdapters(),
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got, want := stderr.String(), "doctor: write output: write failed\n"; got != want {
		t.Fatalf("stderr = %q, want exactly %q", got, want)
	}
	if count := strings.Count(stderr.String(), "write output"); count != 1 {
		t.Fatalf("write failure reported %d times, want once", count)
	}
}

func syncWriterFixtureRich() syncResult {
	return syncResult{
		Status:            "sync_completed",
		SyncRunID:         57,
		ConnectionID:      "google_health:118236",
		ProviderName:      "google_health",
		DataTypes:         []string{"steps"},
		From:              "2026-06-09",
		ResumedFromCursor: true,
		To:                "2026-06-10T00:00:00Z",
		EndpointFamily:    "reconcile",
		SourceFamily:      "wearable",
		DataPointsSeen:    120,
		DataPointsNew:     110,
		DataPointsUpdated: 10,
		RollupsSeen:       2,
		RollupsNew:        1,
		RollupsUpdated:    1,
		Message:           "Sync Run completed",
	}
}

func syncWriterFixtureMinimal() syncResult {
	return syncResult{
		Status:    "sync_failed",
		DataTypes: []string{"steps"},
		Message:   "provider unreachable",
	}
}

// syncWriterFixtureCanceled mirrors a SIGINT mid-run: the audit row
// exists (SyncRunID set), counts are partial, and the executor's
// errSyncCanceled message landed on the result.
func syncWriterFixtureCanceled() syncResult {
	return syncResult{
		Status:         "sync_canceled",
		SyncRunID:      59,
		DataTypes:      []string{"steps"},
		DataPointsSeen: 40,
		DataPointsNew:  40,
		Message:        "Sync Run canceled",
	}
}

const syncWriterRichJSON = `{
  "status": "sync_completed",
  "sync_run_id": 57,
  "connection_id": "google_health:118236",
  "provider_name": "google_health",
  "data_types": [
    "steps"
  ],
  "from": "2026-06-09",
  "resumed_from_cursor": true,
  "to": "2026-06-10T00:00:00Z",
  "endpoint_family": "reconcile",
  "source_family": "wearable",
  "data_points_seen": 120,
  "data_points_new": 110,
  "data_points_updated": 10,
  "rollups_seen": 2,
  "rollups_new": 1,
  "rollups_updated": 1,
  "message": "Sync Run completed"
}
`

const syncWriterRichPlain = `status: sync_completed
sync_run_id: 57
connection_id: google_health:118236
provider_name: google_health
data_types: steps
from: 2026-06-09
resumed_from_cursor: true
to: 2026-06-10T00:00:00Z
endpoint_family: reconcile
source_family: wearable
data_points_seen: 120
data_points_new: 110
data_points_updated: 10
rollups_seen: 2
rollups_new: 1
rollups_updated: 1
message: Sync Run completed
`

const syncWriterRichHuman = `Sync Run completed
Sync Run: 57
Connection: google_health:118236
Data Types: steps
Range: 2026-06-09 to 2026-06-10T00:00:00Z
Resumed from Sync Cursor
Source family: wearable
Data Points: seen 120, new 110, updated 10
Rollups: seen 2, new 1, updated 1
Message: Sync Run completed
`

const syncWriterMinimalJSON = `{
  "status": "sync_failed",
  "data_types": [
    "steps"
  ],
  "data_points_seen": 0,
  "data_points_new": 0,
  "data_points_updated": 0,
  "rollups_seen": 0,
  "rollups_new": 0,
  "rollups_updated": 0,
  "message": "provider unreachable"
}
`

const syncWriterMinimalPlain = `status: sync_failed
data_types: steps
data_points_seen: 0
data_points_new: 0
data_points_updated: 0
rollups_seen: 0
rollups_new: 0
rollups_updated: 0
message: provider unreachable
`

const syncWriterMinimalHuman = `Sync Run failed
Data Types: steps
Data Points: seen 0, new 0, updated 0
Rollups: seen 0, new 0, updated 0
Message: provider unreachable
`

// A canceled single-type sync must NOT render the "Sync Run failed"
// header — JSON and plain modes already distinguish sync_canceled, and
// the human header has to agree (Copilot finding on #307).
const syncWriterCanceledHuman = `Sync Run canceled
Sync Run: 59
Data Types: steps
Data Points: seen 40, new 40, updated 0
Rollups: seen 0, new 0, updated 0
Message: Sync Run canceled
`

func TestSyncWriterEmitsByteIdenticalOutputAcrossModes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		result syncResult
		mode   outputMode
		want   string
	}{
		{"rich json", syncWriterFixtureRich(), outputMode{json: true}, syncWriterRichJSON},
		{"rich plain", syncWriterFixtureRich(), outputMode{plain: true}, syncWriterRichPlain},
		{"rich human", syncWriterFixtureRich(), outputMode{}, syncWriterRichHuman},
		{"minimal json", syncWriterFixtureMinimal(), outputMode{json: true}, syncWriterMinimalJSON},
		{"minimal plain", syncWriterFixtureMinimal(), outputMode{plain: true}, syncWriterMinimalPlain},
		{"minimal human", syncWriterFixtureMinimal(), outputMode{}, syncWriterMinimalHuman},
		{"canceled human", syncWriterFixtureCanceled(), outputMode{}, syncWriterCanceledHuman},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			buffer := new(bytes.Buffer)
			if err := writeSyncResult(testCase.result, testCase.mode, buffer); err != nil {
				t.Fatalf("writeSyncResult: %v", err)
			}
			if got := buffer.String(); got != testCase.want {
				t.Fatalf("sync %s output drifted:\ngot:\n%q\nwant:\n%q", testCase.name, got, testCase.want)
			}
		})
	}
}

func syncFanOutWriterFixture() []syncResult {
	return []syncResult{
		{
			Status:            "sync_completed",
			SyncRunID:         58,
			DataTypes:         []string{"steps"},
			DataPointsSeen:    120,
			DataPointsNew:     110,
			DataPointsUpdated: 10,
			RollupsSeen:       2,
			RollupsNew:        1,
			RollupsUpdated:    1,
			Message:           "Sync Run completed",
		},
		{
			Status:    "sync_failed",
			DataTypes: []string{"heart-rate"},
			Message:   "Provider timeout after 30s",
		},
	}
}

const syncFanOutWriterJSON = `{
  "status": "sync_failed",
  "summary": {
    "data_types": [
      "steps",
      "heart-rate"
    ],
    "from": "2026-06-09",
    "to": "2026-06-10T00:00:00Z",
    "data_points_seen": 120,
    "data_points_new": 110,
    "data_points_updated": 10,
    "rollups_seen": 2,
    "rollups_new": 1,
    "rollups_updated": 1
  },
  "results": [
    {
      "status": "sync_completed",
      "sync_run_id": 58,
      "data_types": [
        "steps"
      ],
      "data_points_seen": 120,
      "data_points_new": 110,
      "data_points_updated": 10,
      "rollups_seen": 2,
      "rollups_new": 1,
      "rollups_updated": 1,
      "message": "Sync Run completed"
    },
    {
      "status": "sync_failed",
      "data_types": [
        "heart-rate"
      ],
      "data_points_seen": 0,
      "data_points_new": 0,
      "data_points_updated": 0,
      "rollups_seen": 0,
      "rollups_new": 0,
      "rollups_updated": 0,
      "message": "Provider timeout after 30s"
    }
  ],
  "message": "Sync Run summary: 2 Data Types attempted, at least one failed"
}
`

const syncFanOutWriterPlain = `status: sync_failed
results.0.status: sync_completed
results.0.data_type: steps
results.0.sync_run_id: 58
results.0.data_points_seen: 120
results.0.data_points_new: 110
results.0.data_points_updated: 10
results.0.rollups_seen: 2
results.0.rollups_new: 1
results.0.rollups_updated: 1
results.0.message: Sync Run completed
results.1.status: sync_failed
results.1.data_type: heart-rate
results.1.data_points_seen: 0
results.1.data_points_new: 0
results.1.data_points_updated: 0
results.1.rollups_seen: 0
results.1.rollups_new: 0
results.1.rollups_updated: 0
results.1.message: Provider timeout after 30s
totals.data_points_seen: 120
totals.data_points_new: 110
totals.data_points_updated: 10
totals.rollups_seen: 2
totals.rollups_new: 1
totals.rollups_updated: 1
message: Sync Run summary: 2 Data Types attempted, at least one failed
`

const syncFanOutWriterHuman = `Sync Run fan-out failed across 2 Data Types
- steps: sync_completed — Data Points new=110 updated=10, Rollups new=1 updated=1
- heart-rate: sync_failed — Data Points new=0 updated=0, Rollups new=0 updated=0
Totals: Data Points seen=120 new=110 updated=10, Rollups seen=2 new=1 updated=1
Sync Run summary: 2 Data Types attempted, at least one failed
`

// syncFanOutWriterCanceledFixture mirrors SIGINT during the second Data
// Type of a fan-out: steps finished, heart-rate was canceled mid-run,
// and any later types were skipped (absent from the slice).
func syncFanOutWriterCanceledFixture() []syncResult {
	return []syncResult{
		{
			Status:            "sync_completed",
			SyncRunID:         58,
			DataTypes:         []string{"steps"},
			DataPointsSeen:    120,
			DataPointsNew:     110,
			DataPointsUpdated: 10,
			RollupsSeen:       2,
			RollupsNew:        1,
			RollupsUpdated:    1,
			Message:           "Sync Run completed",
		},
		{
			Status:         "sync_canceled",
			SyncRunID:      59,
			DataTypes:      []string{"heart-rate"},
			DataPointsSeen: 40,
			DataPointsNew:  40,
			Message:        "Sync Run canceled",
		},
	}
}

const syncFanOutWriterCanceledHuman = `Sync Run fan-out canceled across 2 Data Types
- steps: sync_completed — Data Points new=110 updated=10, Rollups new=1 updated=1
- heart-rate: sync_canceled — Data Points new=40 updated=0, Rollups new=0 updated=0
Totals: Data Points seen=160 new=150 updated=10, Rollups seen=2 new=1 updated=1
Sync Run summary: 1 Data Types completed before cancellation
`

func TestSyncFanOutWriterEmitsByteIdenticalOutputAcrossModes(t *testing.T) {
	t.Parallel()
	options := syncCommandOptions{from: "2026-06-09", to: "2026-06-10T00:00:00Z"}
	for _, testCase := range []struct {
		name    string
		results []syncResult
		mode    outputMode
		want    string
	}{
		{"json", syncFanOutWriterFixture(), outputMode{json: true}, syncFanOutWriterJSON},
		{"plain", syncFanOutWriterFixture(), outputMode{plain: true}, syncFanOutWriterPlain},
		{"human", syncFanOutWriterFixture(), outputMode{}, syncFanOutWriterHuman},
		{"canceled human", syncFanOutWriterCanceledFixture(), outputMode{}, syncFanOutWriterCanceledHuman},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			buffer := new(bytes.Buffer)
			if err := writeSyncFanOutResult(testCase.results, options, testCase.mode, buffer); err != nil {
				t.Fatalf("writeSyncFanOutResult: %v", err)
			}
			if got := buffer.String(); got != testCase.want {
				t.Fatalf("sync fan-out %s output drifted:\ngot:\n%q\nwant:\n%q", testCase.name, got, testCase.want)
			}
		})
	}
}

func syncStatusWriterFixtureRich() syncStatusResult {
	return syncStatusResult{
		Status:      "ok",
		ArchivePath: "/home/bram/.local/share/gohealthcli/gohealthcli.sqlite",
		Window:      "15m0s",
		Runs: []syncStatusRun{
			{
				ID:              61,
				DataTypes:       []string{"steps"},
				Status:          "sync_completed",
				SeenCount:       120,
				NewCount:        110,
				UpdatedCount:    10,
				DurationSeconds: 69,
				StartedAt:       "2026-06-10T08:59:02Z",
				FinishedAt:      "2026-06-10T09:00:11Z",
				LastProgressAt:  "2026-06-10T09:00:10Z",
			},
			{
				ID:              62,
				DataTypes:       []string{"heart-rate"},
				Status:          "sync_running",
				SeenCount:       40,
				NewCount:        40,
				UpdatedCount:    0,
				DurationSeconds: 240,
				StartedAt:       "2026-06-10T09:01:00Z",
			},
			{
				ID:              63,
				DataTypes:       []string{"sleep"},
				Status:          "sync_failed",
				DurationSeconds: 5,
				StartedAt:       "2026-06-10T09:02:00Z",
				FinishedAt:      "2026-06-10T09:02:05Z",
				LastProgressAt:  "2026-06-10T09:02:04Z",
				ErrorSummary:    "Provider timeout after 30s",
			},
		},
		Message: "3 Sync Runs in the last 15m0s",
	}
}

const syncStatusWriterRichJSON = `{
  "status": "ok",
  "archive_path": "/home/bram/.local/share/gohealthcli/gohealthcli.sqlite",
  "window": "15m0s",
  "runs": [
    {
      "id": 61,
      "data_types": [
        "steps"
      ],
      "status": "sync_completed",
      "seen_count": 120,
      "new_count": 110,
      "updated_count": 10,
      "duration_seconds": 69,
      "started_at": "2026-06-10T08:59:02Z",
      "finished_at": "2026-06-10T09:00:11Z",
      "last_progress_at": "2026-06-10T09:00:10Z"
    },
    {
      "id": 62,
      "data_types": [
        "heart-rate"
      ],
      "status": "sync_running",
      "seen_count": 40,
      "new_count": 40,
      "updated_count": 0,
      "duration_seconds": 240,
      "started_at": "2026-06-10T09:01:00Z"
    },
    {
      "id": 63,
      "data_types": [
        "sleep"
      ],
      "status": "sync_failed",
      "seen_count": 0,
      "new_count": 0,
      "updated_count": 0,
      "duration_seconds": 5,
      "started_at": "2026-06-10T09:02:00Z",
      "finished_at": "2026-06-10T09:02:05Z",
      "last_progress_at": "2026-06-10T09:02:04Z",
      "error_summary": "Provider timeout after 30s"
    }
  ],
  "message": "3 Sync Runs in the last 15m0s"
}
`

const syncStatusWriterRichPlain = `status: ok
archive_path: /home/bram/.local/share/gohealthcli/gohealthcli.sqlite
window: 15m0s
sync_run.0.id: 61
sync_run.0.data_types: steps
sync_run.0.status: sync_completed
sync_run.0.seen_count: 120
sync_run.0.new_count: 110
sync_run.0.updated_count: 10
sync_run.0.duration_seconds: 69
sync_run.0.started_at: 2026-06-10T08:59:02Z
sync_run.0.finished_at: 2026-06-10T09:00:11Z
sync_run.0.last_progress_at: 2026-06-10T09:00:10Z
sync_run.1.id: 62
sync_run.1.data_types: heart-rate
sync_run.1.status: sync_running
sync_run.1.seen_count: 40
sync_run.1.new_count: 40
sync_run.1.updated_count: 0
sync_run.1.duration_seconds: 240
sync_run.1.started_at: 2026-06-10T09:01:00Z
sync_run.2.id: 63
sync_run.2.data_types: sleep
sync_run.2.status: sync_failed
sync_run.2.seen_count: 0
sync_run.2.new_count: 0
sync_run.2.updated_count: 0
sync_run.2.duration_seconds: 5
sync_run.2.started_at: 2026-06-10T09:02:00Z
sync_run.2.finished_at: 2026-06-10T09:02:05Z
sync_run.2.last_progress_at: 2026-06-10T09:02:04Z
sync_run.2.error_summary: Provider timeout after 30s
message: 3 Sync Runs in the last 15m0s
`

const syncStatusWriterRichHuman = `Sync Run status
Health Archive: /home/bram/.local/share/gohealthcli/gohealthcli.sqlite
ID  DATA_TYPES  STATUS          SEEN  NEW  UPDATED  DURATION  LAST_PROGRESS  ERROR
61  steps       sync_completed  120   110  10       1m9s      4m50s          -
62  heart-rate  sync_running    40    40   0        4m0s      -              -
63  sleep       sync_failed     0     0    0        5s        2m56s          Provider timeout after 30s
Message: 3 Sync Runs in the last 15m0s
`

func TestSyncStatusWriterEmitsByteIdenticalOutputAcrossModes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 9, 5, 0, 0, time.UTC)
	minimal := syncStatusResult{
		Status:      "sync_status_failed",
		ArchivePath: "/a",
		Window:      "15m0s",
		Message:     "open archive: no such file",
	}
	for _, testCase := range []struct {
		name   string
		result syncStatusResult
		mode   outputMode
		want   string
	}{
		{"rich json", syncStatusWriterFixtureRich(), outputMode{json: true}, syncStatusWriterRichJSON},
		{"rich plain", syncStatusWriterFixtureRich(), outputMode{plain: true}, syncStatusWriterRichPlain},
		{"rich human", syncStatusWriterFixtureRich(), outputMode{}, syncStatusWriterRichHuman},
		{"minimal json", minimal, outputMode{json: true}, "{\n  \"status\": \"sync_status_failed\",\n  \"archive_path\": \"/a\",\n  \"window\": \"15m0s\",\n  \"message\": \"open archive: no such file\"\n}\n"},
		{"minimal plain", minimal, outputMode{plain: true}, "status: sync_status_failed\narchive_path: /a\nwindow: 15m0s\nmessage: open archive: no such file\n"},
		{"minimal human", minimal, outputMode{}, "Sync Run status\nHealth Archive: /a\nMessage: open archive: no such file\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			buffer := new(bytes.Buffer)
			if err := writeSyncStatusResult(testCase.result, testCase.mode, now, buffer); err != nil {
				t.Fatalf("writeSyncStatusResult: %v", err)
			}
			if got := buffer.String(); got != testCase.want {
				t.Fatalf("sync --status %s output drifted:\ngot:\n%q\nwant:\n%q", testCase.name, got, testCase.want)
			}
		})
	}
}

// TestSyncWritersSurfaceTheFirstWriteError pins that every sync-family
// writer still returns the first write error to its caller (#274).
func TestSyncWritersSurfaceTheFirstWriteError(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 9, 5, 0, 0, time.UTC)
	options := syncCommandOptions{from: "2026-06-09", to: "2026-06-10T00:00:00Z"}
	for _, mode := range []outputMode{{json: true}, {plain: true}, {}} {
		if err := writeSyncResult(syncWriterFixtureRich(), mode, failingWriter{}); err == nil {
			t.Fatalf("writeSyncResult mode %+v error = nil, want write failure", mode)
		}
		if err := writeSyncFanOutResult(syncFanOutWriterFixture(), options, mode, failingWriter{}); err == nil {
			t.Fatalf("writeSyncFanOutResult mode %+v error = nil, want write failure", mode)
		}
		if err := writeSyncStatusResult(syncStatusWriterFixtureRich(), mode, now, failingWriter{}); err == nil {
			t.Fatalf("writeSyncStatusResult mode %+v error = nil, want write failure", mode)
		}
	}
}

// TestSyncStatusReportsFirstWriteErrorOnce pins the sync --status side
// of the write-output failure contract (#274) at the command level.
func TestSyncStatusReportsFirstWriteErrorOnce(t *testing.T) {
	t.Parallel()
	stderr := new(bytes.Buffer)

	code := runSyncWithRuntime(
		[]string{"--status", "--db", filepath.Join(t.TempDir(), "missing.sqlite")},
		CommonFlagValues{ConfigPath: defaultConfigPath(), ArchivePath: defaultArchivePath()},
		failingWriter{},
		stderr,
		productionRuntimeAdapters(),
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got, want := stderr.String(), "sync: write output: write failed\n"; got != want {
		t.Fatalf("stderr = %q, want exactly %q", got, want)
	}
	if count := strings.Count(stderr.String(), "write output"); count != 1 {
		t.Fatalf("write failure reported %d times, want once", count)
	}
}

// TestStatusReportsFirstWriteErrorOnce pins the write-output failure
// contract the sticky-error writer must preserve (#274): when stdout
// rejects the very first write, status exits 1 and the operator sees
// exactly one `status: write output: ...` line on stderr.
func TestStatusReportsFirstWriteErrorOnce(t *testing.T) {
	t.Parallel()
	stderr := new(bytes.Buffer)

	code := runStatus(
		[]string{"--db", filepath.Join(t.TempDir(), "missing.sqlite")},
		CommonFlagValues{ConfigPath: defaultConfigPath(), ArchivePath: defaultArchivePath()},
		failingWriter{},
		stderr,
		runtimeAdapters{},
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

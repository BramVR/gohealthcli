package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type dedupeReportWire struct {
	Status        string `json:"status"`
	ArchivePath   string `json:"archive_path"`
	TwinGroups    int    `json:"twin_groups"`
	RemovableRows int    `json:"removable_rows"`
	RemovedRows   int    `json:"removed_rows"`
	BlockedGroups int    `json:"blocked_groups"`
	Groups        []struct {
		KeepID       int64   `json:"keep_id"`
		DuplicateIDs []int64 `json:"duplicate_ids"`
		DataType     string  `json:"data_type"`
	} `json:"groups"`
	Blocked []dedupeBlockedWire `json:"blocked"`
}

type dedupeBlockedWire struct {
	KeepID       int64              `json:"keep_id"`
	DuplicateIDs []int64            `json:"duplicate_ids"`
	DataType     string             `json:"data_type"`
	Reasons      []dedupeReasonWire `json:"reasons"`
}

type dedupeReasonWire struct {
	DuplicateID int64  `json:"duplicate_id"`
	Reason      string `json:"reason"`
}

type twinDataPoint struct {
	dataType     string
	resourceName any
	recordKind   string
	startUTC     string
	endUTC       any
	dataSource   string
	rawJSON      string
}

var (
	activeEnergyTwin = twinDataPoint{
		dataType:   "active-energy-burned",
		recordKind: "interval",
		startUTC:   "2026-06-09T10:00:00Z",
		endUTC:     "2026-06-09T10:01:00Z",
		dataSource: `{"platform":"FITBIT","device":{"displayName":"Synthetic Watch"}}`,
		rawJSON:    `{"activeEnergyBurned":{"kcal":1.25},"interval":{"startTime":"2026-06-09T10:00:00Z","endTime":"2026-06-09T10:01:00Z"}}`,
	}
	heartRateVariabilityTwin = twinDataPoint{
		dataType:     "heart-rate-variability",
		resourceName: "users/me/dataTypes/heart-rate-variability/dataPoints/hrv-synthetic-1",
		recordKind:   "sample",
		startUTC:     "2026-06-09T03:00:00Z",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"heartRateVariability":{"rmssdMillis":41.5},"sampleTime":{"physicalTime":"2026-06-09T03:00:00Z"}}`,
	}
	oxygenSaturationTwin = twinDataPoint{
		dataType:   "oxygen-saturation",
		recordKind: "sample",
		startUTC:   "2026-06-09T04:00:00Z",
		dataSource: `{"platform":"FITBIT"}`,
		rawJSON:    `{"oxygenSaturation":{"percentage":97.0}}`,
	}
	activeMinutesTwin = twinDataPoint{
		dataType:   "active-minutes",
		recordKind: "interval",
		startUTC:   "2026-06-09T11:00:00Z",
		endUTC:     "2026-06-09T11:01:00Z",
		dataSource: `{"platform":"FITBIT"}`,
		rawJSON:    `{"activeMinutes":{"minutes":"1"}}`,
	}
	timeInHeartRateZoneTwin = twinDataPoint{
		dataType:   "time-in-heart-rate-zone",
		recordKind: "interval",
		startUTC:   "2026-06-09T12:00:00Z",
		endUTC:     "2026-06-09T12:01:00Z",
		dataSource: `{"platform":"FITBIT"}`,
		rawJSON:    `{"timeInHeartRateZone":{"zone":"FAT_BURN","duration":"60s"}}`,
	}
)

// newTwinArchive mirrors the 2026-06-09 concurrent double-insert shape:
// each twin pair lands on consecutive ids with byte-identical raw_json.
func newTwinArchive(t *testing.T) (string, string) {
	t.Helper()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, t.TempDir())
	insertStatusFixtureRows(t, archivePath)
	return configPath, archivePath
}

func insertTwinDataPoint(t *testing.T, archivePath string, point twinDataPoint) int64 {
	t.Helper()
	db := openArchiveForTest(t, archivePath)
	result, err := db.ExecContext(context.Background(), `INSERT INTO data_points (
		provider_name,
		connection_id,
		data_type,
		upstream_resource_name,
		record_kind,
		start_time_utc,
		end_time_utc,
		data_source_json,
		raw_json,
		inserted_at,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"googlehealth",
		"googlehealth:111111256096816351",
		point.dataType,
		point.resourceName,
		point.recordKind,
		point.startUTC,
		point.endUTC,
		point.dataSource,
		point.rawJSON,
		"2026-06-09T10:05:00Z",
		"2026-06-09T10:05:00Z",
	)
	if err != nil {
		t.Fatalf("insert twin Data Point %s: %v", point.dataType, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("twin Data Point id: %v", err)
	}
	return id
}

func insertTwinPair(t *testing.T, archivePath string, point twinDataPoint) (int64, int64) {
	t.Helper()
	return insertTwinDataPoint(t, archivePath, point), insertTwinDataPoint(t, archivePath, point)
}

func dataPointIDs(t *testing.T, archivePath string) []int64 {
	t.Helper()
	rows, err := openArchiveForTest(t, archivePath).QueryContext(context.Background(), `SELECT id FROM data_points ORDER BY id`)
	if err != nil {
		t.Fatalf("query Data Point ids: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan Data Point id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Data Point ids: %v", err)
	}
	return ids
}

func runDedupeJSON(t *testing.T, wantCode int, args ...string) dedupeReportWire {
	t.Helper()
	code, stdout, stderr := runCommand(t, append([]string{"dedupe", "--json"}, args...)...)
	if code != wantCode {
		t.Fatalf("dedupe exit code = %d, want %d\nstdout: %s\nstderr: %s", code, wantCode, stdout.String(), stderr.String())
	}
	var report dedupeReportWire
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode dedupe JSON: %v\nstdout: %s", err, stdout.String())
	}
	return report
}

func validateArchiveSnapshot(t *testing.T, archivePath string) error {
	t.Helper()
	snapshot, err := ExportHealthArchiveSnapshot(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("ExportHealthArchiveSnapshot err = %v", err)
	}
	return ValidateHealthArchiveSnapshot(snapshot)
}

func withoutIDs(ids []int64, drop ...int64) []int64 {
	return slices.DeleteFunc(slices.Clone(ids), func(id int64) bool { return slices.Contains(drop, id) })
}

func TestDedupeDryRunReportsTwinGroupsWithoutDeleting(t *testing.T) {
	t.Parallel()
	configPath, archivePath := newTwinArchive(t)
	energyKeep, energyDuplicate := insertTwinPair(t, archivePath, activeEnergyTwin)
	hrvKeep, hrvDuplicate := insertTwinPair(t, archivePath, heartRateVariabilityTwin)
	before := dataPointIDs(t, archivePath)

	report := runDedupeJSON(t, 0, "--config", configPath, "--db", archivePath)

	if report.Status != "dedupe_planned" || report.ArchivePath != archivePath {
		t.Fatalf("status/archive = %q/%q, want dedupe_planned/%q", report.Status, report.ArchivePath, archivePath)
	}
	if report.TwinGroups != 2 || report.RemovableRows != 2 || report.RemovedRows != 0 || report.BlockedGroups != 0 {
		t.Fatalf("counts = %+v, want 2 twin groups, 2 removable, 0 removed, 0 blocked", report)
	}
	type group struct {
		keep       int64
		duplicates []int64
		dataType   string
	}
	var got []group
	for _, g := range report.Groups {
		got = append(got, group{g.KeepID, g.DuplicateIDs, g.DataType})
	}
	want := []group{
		{energyKeep, []int64{energyDuplicate}, "active-energy-burned"},
		{hrvKeep, []int64{hrvDuplicate}, "heart-rate-variability"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %+v, want %+v", got, want)
	}
	if report.Blocked == nil || len(report.Blocked) != 0 {
		t.Fatalf("blocked = %#v, want empty array", report.Blocked)
	}
	if after := dataPointIDs(t, archivePath); !reflect.DeepEqual(after, before) {
		t.Fatalf("dry run changed Data Points: %v, want %v", after, before)
	}
}

func TestDedupeDryRunHumanAndPlainOutput(t *testing.T) {
	t.Parallel()
	configPath, archivePath := newTwinArchive(t)
	keep, duplicate := insertTwinPair(t, archivePath, activeEnergyTwin)

	code, stdout, stderr := runCommand(t, "dedupe", "--config", configPath, "--db", archivePath)
	if code != 0 {
		t.Fatalf("human dedupe exit = %d\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "--confirm") {
		t.Fatalf("human dry run does not tell the operator to rerun with --confirm:\n%s", stdout.String())
	}

	code, stdout, stderr = runCommand(t, "dedupe", "--plain", "--config", configPath, "--db", archivePath)
	if code != 0 {
		t.Fatalf("plain dedupe exit = %d\nstderr: %s", code, stderr.String())
	}
	for _, line := range []string{
		"status: dedupe_planned",
		"archive_path: " + archivePath,
		"twin_groups: 1",
		"removable_rows: 1",
		"removed_rows: 0",
		"blocked_groups: 0",
		"groups.0.keep_id: " + strconv.FormatInt(keep, 10),
		"groups.0.duplicate_ids: " + strconv.FormatInt(duplicate, 10),
		"groups.0.data_type: active-energy-burned",
	} {
		if !slices.Contains(strings.Split(stdout.String(), "\n"), line) {
			t.Fatalf("plain output missing %q:\n%s", line, stdout.String())
		}
	}
}

func TestDedupeConfirmRemovesTwinsAndRestoresSnapshotValidity(t *testing.T) {
	t.Parallel()
	configPath, archivePath := newTwinArchive(t)
	energyKeep, energyDuplicate := insertTwinPair(t, archivePath, activeEnergyTwin)
	hrvKeep, hrvDuplicate := insertTwinPair(t, archivePath, heartRateVariabilityTwin)
	minutesKeep, minutesDuplicate := insertTwinPair(t, archivePath, activeMinutesTwin)
	before := dataPointIDs(t, archivePath)

	if err := validateArchiveSnapshot(t, archivePath); err == nil || !strings.Contains(err.Error(), "duplicate Data Point identity") {
		t.Fatalf("twin archive validation err = %v, want duplicate Data Point identity", err)
	}

	report := runDedupeJSON(t, 0, "--confirm", "--config", configPath, "--db", archivePath)

	if report.Status != "dedupe_applied" || report.TwinGroups != 3 || report.RemovableRows != 3 || report.RemovedRows != 3 || report.BlockedGroups != 0 {
		t.Fatalf("confirm report = %+v, want dedupe_applied with 3 groups removed", report)
	}
	after := dataPointIDs(t, archivePath)
	if want := withoutIDs(before, energyDuplicate, hrvDuplicate, minutesDuplicate); !reflect.DeepEqual(after, want) {
		t.Fatalf("Data Points after confirm = %v, want %v", after, want)
	}
	for _, keep := range []int64{energyKeep, hrvKeep, minutesKeep} {
		if !slices.Contains(after, keep) {
			t.Fatalf("keeper %d removed; remaining %v", keep, after)
		}
	}
	if err := validateArchiveSnapshot(t, archivePath); err != nil {
		t.Fatalf("validation after confirm err = %v, want nil", err)
	}

	rerun := runDedupeJSON(t, 0, "--confirm", "--config", configPath, "--db", archivePath)
	if rerun.TwinGroups != 0 || rerun.RemovedRows != 0 || len(rerun.Groups) != 0 {
		t.Fatalf("rerun report = %+v, want zero twin groups", rerun)
	}
}

func TestDedupeBlocksGroupsItCannotProveIdentical(t *testing.T) {
	t.Parallel()
	configPath, archivePath := newTwinArchive(t)

	differsKeep := insertTwinDataPoint(t, archivePath, oxygenSaturationTwin)
	changed := oxygenSaturationTwin
	changed.rawJSON = `{"oxygenSaturation":{"percentage":97.5}}`
	differsDuplicate := insertTwinDataPoint(t, archivePath, changed)

	revisionKeep, revisionDuplicate := insertTwinPair(t, archivePath, heartRateVariabilityTwin)
	if _, err := openArchiveForTest(t, archivePath).ExecContext(context.Background(), `INSERT INTO data_point_revisions (
		data_point_id, previous_raw_json, replaced_at, replacement_reason
	) VALUES (?, ?, ?, ?)`, revisionDuplicate, `{"heartRateVariability":{"rmssdMillis":40.0}}`, "2026-06-09T10:06:00Z", "provider_correction"); err != nil {
		t.Fatalf("insert duplicate revision: %v", err)
	}

	attachmentKeep, attachmentDuplicate := insertTwinPair(t, archivePath, timeInHeartRateZoneTwin)
	storeSnapshotAttachmentForDataPoint(t, archivePath, attachmentDuplicate, []byte("<TrainingCenterDatabase/>"))

	cleanKeep, cleanDuplicate := insertTwinPair(t, archivePath, activeEnergyTwin)
	before := dataPointIDs(t, archivePath)

	wantBlocked := []dedupeBlockedWire{
		{differsKeep, []int64{differsDuplicate}, "oxygen-saturation", []dedupeReasonWire{{differsDuplicate, "content_differs"}}},
		{revisionKeep, []int64{revisionDuplicate}, "heart-rate-variability", []dedupeReasonWire{{revisionDuplicate, "duplicate_has_revisions"}}},
		{attachmentKeep, []int64{attachmentDuplicate}, "time-in-heart-rate-zone", []dedupeReasonWire{{attachmentDuplicate, "duplicate_has_attachments"}}},
	}

	planned := runDedupeJSON(t, 1, "--config", configPath, "--db", archivePath)
	if planned.TwinGroups != 4 || planned.BlockedGroups != 3 || planned.RemovableRows != 1 {
		t.Fatalf("dry run counts = %+v, want 4 groups, 3 blocked, 1 removable", planned)
	}
	if !reflect.DeepEqual(planned.Blocked, wantBlocked) {
		t.Fatalf("blocked = %+v, want %+v", planned.Blocked, wantBlocked)
	}
	if len(planned.Groups) != 1 || planned.Groups[0].KeepID != cleanKeep || !reflect.DeepEqual(planned.Groups[0].DuplicateIDs, []int64{cleanDuplicate}) {
		t.Fatalf("removable groups = %+v, want only the clean pair", planned.Groups)
	}
	if after := dataPointIDs(t, archivePath); !reflect.DeepEqual(after, before) {
		t.Fatalf("blocked dry run changed Data Points: %v, want %v", after, before)
	}

	applied := runDedupeJSON(t, 1, "--confirm", "--config", configPath, "--db", archivePath)
	if applied.Status != "dedupe_applied" || applied.RemovedRows != 1 || applied.BlockedGroups != 3 {
		t.Fatalf("confirm report = %+v, want 1 removed and 3 blocked", applied)
	}
	if !reflect.DeepEqual(applied.Blocked, wantBlocked) {
		t.Fatalf("confirm blocked = %+v, want %+v", applied.Blocked, wantBlocked)
	}
	if after := dataPointIDs(t, archivePath); !reflect.DeepEqual(after, withoutIDs(before, cleanDuplicate)) {
		t.Fatalf("Data Points after blocked confirm = %v, want only %d removed from %v", after, cleanDuplicate, before)
	}
}

func TestDedupeBlockedGroupListsEveryHeldDuplicate(t *testing.T) {
	t.Parallel()
	configPath, archivePath := newTwinArchive(t)
	keep := insertTwinDataPoint(t, archivePath, oxygenSaturationTwin)
	identical := insertTwinDataPoint(t, archivePath, oxygenSaturationTwin)
	changed := oxygenSaturationTwin
	changed.rawJSON = `{"oxygenSaturation":{"percentage":97.5}}`
	differs := insertTwinDataPoint(t, archivePath, changed)
	before := dataPointIDs(t, archivePath)

	want := []dedupeBlockedWire{{keep, []int64{identical, differs}, "oxygen-saturation", []dedupeReasonWire{{differs, "content_differs"}}}}
	for _, args := range [][]string{nil, {"--confirm"}} {
		report := runDedupeJSON(t, 1, append(args, "--config", configPath, "--db", archivePath)...)
		if report.TwinGroups != 1 || report.BlockedGroups != 1 || report.RemovableRows != 0 || report.RemovedRows != 0 {
			t.Fatalf("dedupe %v counts = %+v, want 1 blocked group and nothing removed", args, report)
		}
		if !reflect.DeepEqual(report.Blocked, want) {
			t.Fatalf("dedupe %v blocked = %+v, want %+v", args, report.Blocked, want)
		}
	}
	if after := dataPointIDs(t, archivePath); !reflect.DeepEqual(after, before) {
		t.Fatalf("blocked group changed Data Points: %v, want %v", after, before)
	}

	code, stdout, stderr := runCommand(t, "dedupe", "--plain", "--config", configPath, "--db", archivePath)
	if code != 1 {
		t.Fatalf("plain dedupe exit = %d, want 1\nstderr: %s", code, stderr.String())
	}
	lines := strings.Split(stdout.String(), "\n")
	for _, line := range []string{
		fmt.Sprintf("blocked.0.keep_id: %d", keep),
		fmt.Sprintf("blocked.0.duplicate_ids: %d,%d", identical, differs),
		"blocked.0.data_type: oxygen-saturation",
		fmt.Sprintf("blocked.0.reasons.0.duplicate_id: %d", differs),
		"blocked.0.reasons.0.reason: content_differs",
	} {
		if !slices.Contains(lines, line) {
			t.Fatalf("plain output missing %q:\n%s", line, stdout.String())
		}
	}
	if slices.ContainsFunc(lines, func(line string) bool { return strings.HasPrefix(line, "blocked.0.reasons.1.") }) {
		t.Fatalf("plain output reports a reason for the identical duplicate:\n%s", stdout.String())
	}

	code, stdout, stderr = runCommand(t, "dedupe", "--config", configPath, "--db", archivePath)
	if code != 1 {
		t.Fatalf("human dedupe exit = %d, want 1\nstderr: %s", code, stderr.String())
	}
	if held := fmt.Sprintf("%d,%d", identical, differs); !strings.Contains(stdout.String(), held) {
		t.Fatalf("human output does not list held duplicates %s:\n%s", held, stdout.String())
	}
}

func TestDedupeBlockedDuplicateReportsEachReasonInOneEntry(t *testing.T) {
	t.Parallel()
	configPath, archivePath := newTwinArchive(t)
	keep, duplicate := insertTwinPair(t, archivePath, timeInHeartRateZoneTwin)
	if _, err := openArchiveForTest(t, archivePath).ExecContext(context.Background(), `INSERT INTO data_point_revisions (
		data_point_id, previous_raw_json, replaced_at, replacement_reason
	) VALUES (?, ?, ?, ?)`, duplicate, `{"timeInHeartRateZone":{"zone":"PEAK","duration":"60s"}}`, "2026-06-09T12:06:00Z", "provider_correction"); err != nil {
		t.Fatalf("insert duplicate revision: %v", err)
	}
	storeSnapshotAttachmentForDataPoint(t, archivePath, duplicate, []byte("<TrainingCenterDatabase/>"))

	report := runDedupeJSON(t, 1, "--config", configPath, "--db", archivePath)

	want := []dedupeBlockedWire{{keep, []int64{duplicate}, "time-in-heart-rate-zone", []dedupeReasonWire{
		{duplicate, "duplicate_has_revisions"},
		{duplicate, "duplicate_has_attachments"},
	}}}
	if report.BlockedGroups != 1 || !reflect.DeepEqual(report.Blocked, want) {
		t.Fatalf("blocked_groups/blocked = %d/%+v, want 1/%+v", report.BlockedGroups, report.Blocked, want)
	}
}

func TestDedupeConfirmRefusesArchivesTheDryRunRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, db *sql.DB)
	}{
		{"stale schema", func(t *testing.T, db *sql.DB) {
			for _, stmt := range []string{
				fmt.Sprintf(`DELETE FROM schema_migrations WHERE version = %d`, currentSchemaVersion),
				fmt.Sprintf(`PRAGMA user_version = %d`, currentSchemaVersion-1),
			} {
				if _, err := db.ExecContext(context.Background(), stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}
		}},
		{"WAL journal", func(t *testing.T, db *sql.DB) {
			if _, err := db.ExecContext(context.Background(), `PRAGMA journal_mode=WAL`); err != nil {
				t.Fatalf("switch to WAL: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			configPath, archivePath := newTwinArchive(t)
			insertTwinPair(t, archivePath, activeEnergyTwin)
			db, err := openArchive(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			tc.prepare(t, db)
			var userVersion int
			if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&userVersion); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			ids := dataPointIDs(t, archivePath)
			files := snapshotRouteArchive(t, archivePath)

			var failures []failureJSONEnvelope
			for _, args := range [][]string{nil, {"--confirm"}} {
				code, stdout, stderr := runCommand(t, append([]string{"dedupe", "--json", "--config", configPath, "--db", archivePath}, args...)...)
				envelope := decodeRouteFailure(t, stdout)
				if code != 1 || envelope.Status != StatusOperationFailed {
					t.Fatalf("dedupe %v exit = %d stdout = %q stderr = %q, want operation_failed", args, code, stdout, stderr)
				}
				failures = append(failures, envelope)
			}
			if !reflect.DeepEqual(failures[1], failures[0]) {
				t.Fatalf("--confirm failure = %+v, want the dry run failure %+v", failures[1], failures[0])
			}
			if after := snapshotRouteArchive(t, archivePath); !maps.Equal(after, files) {
				t.Fatalf("refused dedupe changed the archive tree\nbefore: %v\nafter:  %v", files, after)
			}
			if after := dataPointIDs(t, archivePath); !reflect.DeepEqual(after, ids) {
				t.Fatalf("refused dedupe changed Data Points: %v, want %v", after, ids)
			}
			var afterVersion int
			if err := openArchiveForTest(t, archivePath).QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&afterVersion); err != nil {
				t.Fatal(err)
			}
			if afterVersion != userVersion {
				t.Fatalf("user_version after refused dedupe = %d, want %d", afterVersion, userVersion)
			}
		})
	}
}

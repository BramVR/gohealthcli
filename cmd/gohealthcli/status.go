package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
)

type statusResult struct {
	Status                string `json:"status"`
	ArchivePath           string `json:"archive_path"`
	SchemaVersion         int    `json:"schema_version,omitempty"`
	DataPointCount        int    `json:"data_point_count"`
	RollupCount           int    `json:"rollup_count"`
	ProfileSnapshotCount  int    `json:"profile_snapshot_count"`
	IdentitySnapshotCount int    `json:"identity_snapshot_count"`
	SyncRunCount          int    `json:"sync_run_count"`
	// KnownDataTypes is the flat array of Data Type names also
	// emitted as the `known_data_types: a,b,c` plain line (PRD #144
	// slice 9). Computed once in StatusSummary from DataTypes so the
	// plain and JSON writers share a single source. omitempty keeps
	// the JSON shape clean on a brand-new archive.
	KnownDataTypes []string         `json:"known_data_types,omitempty"`
	DataTypes      []statusDataType `json:"data_types,omitempty"`
	// PairedDeviceCount is a top-level mirror of
	// IdentitySnapshotsFreshness.PairedDeviceCount so `--json` carries
	// the same key as `--plain` (PRD #144 slice 9). The nested
	// location is preserved for back-compat. omitempty so a clean
	// archive does not advertise a misleading zero.
	PairedDeviceCount          int                      `json:"paired_device_count,omitempty"`
	IdentitySnapshotsFreshness *statusSnapshotFreshness `json:"identity_snapshots_freshness,omitempty"`
	Tier2                      *statusTier2             `json:"tier_2,omitempty"`
	LatestSuccessfulRun        *statusSyncRun           `json:"latest_successful_sync_run,omitempty"`
	LatestFailedRun            *statusSyncRun           `json:"latest_failed_sync_run,omitempty"`
	Message                    string                   `json:"message"`
}

// statusSnapshotFreshness summarises identity-level metadata recency:
// when each kind's latest snapshot was fetched, and how many devices
// are in the current paired-devices snapshot. Helps an operator (and
// an LLM) verify metadata freshness before running analysis.
type statusSnapshotFreshness struct {
	PairedDeviceCount int               `json:"paired_device_count"`
	LatestFetchedAt   map[string]string `json:"latest_fetched_at,omitempty"`
}

// statusTier2 summarises Tier 2 (ECG + IRN) coverage for the archive
// (#111). Both counts default to 0 when the scopes have not been
// granted — the AC: "defaulting to 0 when the scopes are not
// granted". The scope_granted flags let the plain writer decide
// whether to emit the corresponding line, and let downstream tooling
// distinguish "0 because no rows yet" from "0 because no scope".
type statusTier2 struct {
	ElectrocardiogramEventCount             int  `json:"electrocardiogram_event_count"`
	ElectrocardiogramScopeGranted           bool `json:"electrocardiogram_scope_granted"`
	IrregularRhythmNotificationCount        int  `json:"irregular_rhythm_notification_count"`
	IrregularRhythmNotificationScopeGranted bool `json:"irregular_rhythm_notification_scope_granted"`
}

type statusDataType struct {
	DataType                 string             `json:"data_type"`
	DataPointCount           int                `json:"data_point_count"`
	RollupCount              int                `json:"rollup_count"`
	NewestDataPointTimestamp string             `json:"newest_data_point_timestamp,omitempty"`
	NewestRollupTimestamp    string             `json:"newest_rollup_timestamp,omitempty"`
	SyncCursors              []statusSyncCursor `json:"sync_cursors,omitempty"`
}

type statusSyncCursor struct {
	SourceFamilyFilter string `json:"source_family_filter,omitempty"`
	RollupKind         string `json:"rollup_kind"`
	CursorTime         string `json:"cursor_time"`
	AdvancedAt         string `json:"advanced_at"`
}

type statusSyncRun struct {
	ID                 int64    `json:"id"`
	Status             string   `json:"status"`
	DataTypes          []string `json:"data_types,omitempty"`
	From               string   `json:"from,omitempty"`
	To                 string   `json:"to,omitempty"`
	EndpointFamily     string   `json:"endpoint_family,omitempty"`
	SourceFamilyFilter string   `json:"source_family_filter,omitempty"`
	SeenCount          int      `json:"seen_count"`
	NewCount           int      `json:"new_count"`
	UpdatedCount       int      `json:"updated_count"`
	StartedAt          string   `json:"started_at,omitempty"`
	FinishedAt         string   `json:"finished_at,omitempty"`
	ErrorSummary       string   `json:"error_summary,omitempty"`
}

func runStatus(args []string, configPath, archivePath string, configPathExplicit, archivePathExplicit bool, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)

	// Seed the two explicitness bits from the dispatch-time CommonFlagValues
	// so the readArchivePathResolver below sees the correct shape even when
	// the user passed --db / --config at the global slot (before the
	// subcommand). ParseCommon's Visit pass below ORs in subcommand-local
	// flag values too, so passing them on either side of the subcommand
	// works the same way.
	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:          configPath,
		ArchivePath:         archivePath,
		JSONOutput:          mode.json,
		PlainOutput:         mode.plain,
		ArchivePathExplicit: archivePathExplicit,
		ConfigPathExplicit:  configPathExplicit,
	})

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "status",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected status argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	resolvedArchivePath, err := resolveReadArchivePath(*common)
	if err != nil {
		result := statusResult{Status: "status_failed", ArchivePath: common.ArchivePath, Message: err.Error()}
		if writeErr := writeStatusResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure("status", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	// context.Background(): status is a synchronous read command with no
	// cancellation path today; the context keeps the archive reads on the
	// Context API (#305) without changing behavior.
	result, err := statusSetup(context.Background(), resolvedArchivePath, runtime.withDefaults().now().UTC())
	if err != nil {
		if result.Status == "" {
			result.Status = "status_failed"
		}
		result.Message = err.Error()
		if writeErr := writeStatusResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure("status", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	if err := writeStatusResult(result, mode, stdout); err != nil {
		return reportWriteFailure("status", err, mode, stdout, stderr)
	}
	return 0
}

func writeStatusResult(result statusResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeStatusPlain(writer, result)
	} else {
		writeStatusHuman(writer, result)
	}
	return writer.Err()
}

func writeStatusPlain(writer *stickyWriter, result statusResult) {
	writer.Printf("status: %s\n", result.Status)
	if result.ArchivePath != "" {
		writer.Printf("archive_path: %s\n", result.ArchivePath)
	}
	if result.SchemaVersion != 0 {
		writer.Printf("schema_version: %d\n", result.SchemaVersion)
	}
	writeStatusCounts(writer, result)
	if len(result.DataTypes) != 0 {
		// Read the shared KnownDataTypes field instead of
		// re-synthesising from DataTypes — PRD #144 slice 9
		// guarantees both modes carry the same array.
		writer.Printf("known_data_types: %s\n", strings.Join(result.KnownDataTypes, ","))
	}
	for _, dataType := range result.DataTypes {
		writeStatusDataTypePlain(writer, dataType)
	}
	writeStatusSnapshotFreshnessPlain(writer, result.IdentitySnapshotsFreshness)
	writeStatusTier2Plain(writer, result.Tier2)
	writeStatusSyncRunPlain(writer, "latest_successful_sync_run", result.LatestSuccessfulRun)
	writeStatusSyncRunPlain(writer, "latest_failed_sync_run", result.LatestFailedRun)
	writer.Printf("message: %s\n", result.Message)
}

func writeStatusDataTypePlain(writer *stickyWriter, dataType statusDataType) {
	prefix := "data_type." + dataType.DataType + "."
	writer.Printf("%sdata_point_count: %d\n", prefix, dataType.DataPointCount)
	writer.Printf("%srollup_count: %d\n", prefix, dataType.RollupCount)
	if dataType.NewestDataPointTimestamp != "" {
		writer.Printf("%snewest_data_point_timestamp: %s\n", prefix, dataType.NewestDataPointTimestamp)
	}
	if dataType.NewestRollupTimestamp != "" {
		writer.Printf("%snewest_rollup_timestamp: %s\n", prefix, dataType.NewestRollupTimestamp)
	}
	for index, cursor := range dataType.SyncCursors {
		cursorPrefix := fmt.Sprintf("%ssync_cursor.%d.", prefix, index)
		writer.Printf("%srollup_kind: %s\n", cursorPrefix, cursor.RollupKind)
		if cursor.SourceFamilyFilter != "" {
			writer.Printf("%ssource_family_filter: %s\n", cursorPrefix, cursor.SourceFamilyFilter)
		}
		writer.Printf("%scursor_time: %s\n", cursorPrefix, cursor.CursorTime)
		writer.Printf("%sadvanced_at: %s\n", cursorPrefix, cursor.AdvancedAt)
	}
}

func writeStatusSnapshotFreshnessPlain(writer *stickyWriter, freshness *statusSnapshotFreshness) {
	if freshness == nil {
		return
	}
	if freshness.PairedDeviceCount > 0 {
		writer.Printf("paired_device_count: %d\n", freshness.PairedDeviceCount)
	}
	// Emit kinds in a stable order so the output is reproducible.
	for _, kind := range identitySnapshotKinds {
		if ts, ok := freshness.LatestFetchedAt[kind]; ok {
			writer.Printf("identity_snapshot.%s.fetched_at: %s\n", kind, ts)
		}
	}
}

func writeStatusTier2Plain(writer *stickyWriter, tier2 *statusTier2) {
	if tier2 == nil {
		return
	}
	// Plain output omits Tier 2 lines when the scope has not been
	// granted — matches PR #128's omitted-when-missing convention for
	// snapshot kinds. JSON always carries the block so downstream
	// tooling sees a stable shape.
	if tier2.ElectrocardiogramScopeGranted {
		writer.Printf("electrocardiogram_event_count: %d\n", tier2.ElectrocardiogramEventCount)
	}
	if tier2.IrregularRhythmNotificationScopeGranted {
		writer.Printf("irregular_rhythm_notification_count: %d\n", tier2.IrregularRhythmNotificationCount)
	}
}

func writeStatusHuman(writer *stickyWriter, result statusResult) {
	if result.Status == "ok" {
		writer.Println("Health Archive status")
	} else {
		writer.Println("Health Archive status failed")
	}
	if result.ArchivePath != "" {
		writer.Printf("Health Archive: %s\n", result.ArchivePath)
	}
	if result.SchemaVersion != 0 {
		writer.Printf("Schema version: %d\n", result.SchemaVersion)
	}
	writer.Printf("Counts: %d Data Points, %d Rollups, %d Identity Snapshots (%d Profile), %d Sync Runs\n", result.DataPointCount, result.RollupCount, result.IdentitySnapshotCount, result.ProfileSnapshotCount, result.SyncRunCount)
	if len(result.DataTypes) != 0 {
		writer.Printf("Known Data Types: %s\n", strings.Join(statusDataTypeNames(result.DataTypes), ", "))
	}
	for _, dataType := range result.DataTypes {
		writeStatusDataTypeHuman(writer, dataType)
	}
	if result.LatestSuccessfulRun != nil {
		writer.Printf("Latest successful Sync Run: %d (%s to %s)\n", result.LatestSuccessfulRun.ID, result.LatestSuccessfulRun.From, result.LatestSuccessfulRun.To)
	}
	if result.LatestFailedRun != nil {
		writer.Printf("Latest failed Sync Run: %d (%s)\n", result.LatestFailedRun.ID, result.LatestFailedRun.ErrorSummary)
	}
	writer.Printf("Message: %s\n", result.Message)
}

func writeStatusDataTypeHuman(writer *stickyWriter, dataType statusDataType) {
	writer.Printf("- %s: %d Data Points, %d Rollups", dataType.DataType, dataType.DataPointCount, dataType.RollupCount)
	if dataType.NewestDataPointTimestamp != "" {
		writer.Printf(", newest Data Point %s", dataType.NewestDataPointTimestamp)
	}
	if dataType.NewestRollupTimestamp != "" {
		writer.Printf(", newest Rollup %s", dataType.NewestRollupTimestamp)
	}
	for _, cursor := range dataType.SyncCursors {
		label := cursor.RollupKind
		if cursor.SourceFamilyFilter != "" {
			label = label + "/" + cursor.SourceFamilyFilter
		}
		writer.Printf(", Sync Cursor (%s) %s", label, cursor.CursorTime)
	}
	writer.Println()
}

func writeStatusCounts(writer *stickyWriter, result statusResult) {
	for _, item := range []struct {
		key   string
		count int
	}{
		{"data_point_count", result.DataPointCount},
		{"rollup_count", result.RollupCount},
		{"profile_snapshot_count", result.ProfileSnapshotCount},
		{"identity_snapshot_count", result.IdentitySnapshotCount},
		{"sync_run_count", result.SyncRunCount},
	} {
		writer.Printf("%s: %d\n", item.key, item.count)
	}
}

func statusDataTypeNames(dataTypes []statusDataType) []string {
	names := make([]string, 0, len(dataTypes))
	for _, dataType := range dataTypes {
		names = append(names, dataType.DataType)
	}
	return names
}

func writeStatusSyncRunPlain(writer *stickyWriter, prefix string, run *statusSyncRun) {
	if run == nil {
		return
	}
	writer.Printf("%s_id: %d\n", prefix, run.ID)
	writer.Printf("%s_status: %s\n", prefix, run.Status)
	if len(run.DataTypes) != 0 {
		writer.Printf("%s_data_types: %s\n", prefix, strings.Join(run.DataTypes, ","))
	}
	if run.From != "" {
		writer.Printf("%s_from: %s\n", prefix, run.From)
	}
	if run.To != "" {
		writer.Printf("%s_to: %s\n", prefix, run.To)
	}
	if run.EndpointFamily != "" {
		writer.Printf("%s_endpoint_family: %s\n", prefix, run.EndpointFamily)
	}
	if run.SourceFamilyFilter != "" {
		writer.Printf("%s_source_family_filter: %s\n", prefix, escapePlainControlChars(run.SourceFamilyFilter))
	}
	writer.Printf("%s_seen_count: %d\n", prefix, run.SeenCount)
	writer.Printf("%s_new_count: %d\n", prefix, run.NewCount)
	writer.Printf("%s_updated_count: %d\n", prefix, run.UpdatedCount)
	if run.StartedAt != "" {
		writer.Printf("%s_started_at: %s\n", prefix, run.StartedAt)
	}
	if run.FinishedAt != "" {
		writer.Printf("%s_finished_at: %s\n", prefix, run.FinishedAt)
	}
	if run.ErrorSummary != "" {
		writer.Printf("%s_error_summary: %s\n", prefix, escapePlainControlChars(run.ErrorSummary))
	}
}

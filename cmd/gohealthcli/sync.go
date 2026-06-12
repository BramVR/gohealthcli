package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
)

type syncResult struct {
	Status            string   `json:"status"`
	SyncRunID         int64    `json:"sync_run_id,omitempty"`
	ConnectionID      string   `json:"connection_id,omitempty"`
	ProviderName      string   `json:"provider_name,omitempty"`
	DataTypes         []string `json:"data_types,omitempty"`
	From              string   `json:"from,omitempty"`
	ResumedFromCursor bool     `json:"resumed_from_cursor,omitempty"`
	To                string   `json:"to,omitempty"`
	EndpointFamily    string   `json:"endpoint_family,omitempty"`
	SourceFamily      string   `json:"source_family,omitempty"`
	DataPointsSeen    int      `json:"data_points_seen"`
	DataPointsNew     int      `json:"data_points_new"`
	DataPointsUpdated int      `json:"data_points_updated"`
	RollupsSeen       int      `json:"rollups_seen"`
	RollupsNew        int      `json:"rollups_new"`
	RollupsUpdated    int      `json:"rollups_updated"`
	Message           string   `json:"message"`
}

type syncCommandOptions struct {
	configPath   string
	archivePath  string
	dataTypes    []string
	allTypes     bool
	from         string
	to           string
	rollup       string
	sourceFamily string
}

func runSyncWithRuntime(args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  globals.ConfigPath,
		ArchivePath: globals.ArchivePath,
		JSONOutput:  globals.JSONOutput,
		PlainOutput: globals.PlainOutput,
	})
	syncTypes := flags.String("types", "", "comma-separated Data Types; defaults to \"steps\" when neither --types nor --all is set")
	syncAll := flags.Bool("all", false, "sync every default Data Type")
	syncFrom := flags.String("from", "", "inclusive sync range start; optional once a Sync Cursor exists")
	syncTo := flags.String("to", "", "exclusive sync range end")
	syncRollup := flags.String("rollup", "", "rollup kind to sync; supported: daily | hourly | weekly | window=<duration>")
	syncSourceFamily := flags.String("source-family", "", "source family filter; supported: wearable")
	syncStatus := flags.Bool("status", false, "list recent Sync Runs from the local archive instead of syncing")
	syncWindow := flags.String("window", "", "with --status: how far back to list finished Sync Runs (Go duration, default 15m, max 24h)")

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode := commonOutputMode(*common)
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "sync",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected sync argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}
	// --status flips sync into a read-only view of recent Sync Runs
	// (#236); any flag that shapes an actual sync is a usage error
	// alongside it (silent ignoring would read as "my filters applied").
	// --window inverts the rule: it only means something WITH --status.
	if err := validateSyncStatusFlagSet(flags, *syncStatus); err != nil {
		return ReportFailure(FailureReport{
			Command: "sync",
			Status:  StatusFlagInvalid,
			Message: err.Error(),
			Mode:    mode,
		}, stdout, stderr)
	}
	if *syncStatus {
		return runSyncStatusWithRuntime(*common, *syncWindow, mode, stdout, stderr, runtime)
	}

	dataTypes := parseCommaList(*syncTypes)
	if !*syncAll && len(dataTypes) == 0 {
		// Preserve the historical default for single-type invocations
		// (`gohealthcli sync` with no flags).
		dataTypes = []string{"steps"}
	}
	options := syncCommandOptions{
		configPath:   common.ConfigPath,
		archivePath:  common.ArchivePath,
		dataTypes:    dataTypes,
		allTypes:     *syncAll,
		from:         *syncFrom,
		to:           *syncTo,
		rollup:       *syncRollup,
		sourceFamily: *syncSourceFamily,
	}
	ctx, stopSignalHandler := installSyncCancelContext()
	defer stopSignalHandler()

	orchestrator := newSyncOrchestrator(runtime)
	results, err := orchestrator.Sync(ctx, options)
	if err != nil {
		// Preflight rejections from the orchestrator (gate-routed) surface
		// here. status is always sync_failed (never the empty string),
		// SyncRunID is unset so the JSON envelope omits sync_run_id, and
		// the user-supplied --from/--to round-trip so tooling that pivots
		// on the failed-range can still see what was attempted. The
		// no-audit-row contract is preserved upstream of this branch.
		fallback := syncResult{
			Status:    "sync_failed",
			DataTypes: dataTypes,
			From:      options.from,
			To:        options.to,
			Message:   err.Error(),
		}
		if writeErr := writeSyncResult(fallback, mode, stdout); writeErr != nil {
			return reportWriteFailure("sync", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	// Shape is determined by what the user requested, not by how many
	// results came back: --all and --types csv (more than one) always
	// emit the wrapped fan-out shape so downstream tooling can rely on
	// the envelope being predictable from the invocation flags. Bare
	// single-type sync (--types steps, or no flags) keeps the legacy
	// flat shape for backwards compatibility.
	fanOut := options.allTypes || len(dataTypes) > 1
	if !fanOut {
		// orchestrator.Sync returns an empty slice only when SIGINT
		// arrived before the first Data Type started — report that as
		// sync_canceled, not the misleading "sync produced no results"
		// the legacy path would surface as sync_failed.
		single := syncResult{Status: "sync_canceled", DataTypes: dataTypes, Message: "sync canceled before any Data Type started"}
		if len(results) > 0 {
			single = results[0]
		}
		if writeErr := writeSyncResult(single, mode, stdout); writeErr != nil {
			return reportWriteFailure("sync", writeErr, mode, stdout, stderr)
		}
		if single.Status != "sync_completed" {
			return 1
		}
		return 0
	}
	if writeErr := writeSyncFanOutResult(results, options, mode, stdout); writeErr != nil {
		return reportWriteFailure("sync", writeErr, mode, stdout, stderr)
	}
	// fanOutStatus folds the same empty-results case to sync_canceled
	// (see fanOutStatus), so an empty fan-out should also exit non-zero
	// rather than the silent exit-0 the per-result loop would produce.
	if fanOutStatus(results) != "sync_completed" {
		return 1
	}
	return 0
}

// installSyncCancelContext wires a SIGINT handler whose cancellation is
// delivered via the returned context. The context cancels when SIGINT
// fires or when the returned stop function is called (whichever is
// first); the stop function is idempotent and races cleanly with an
// in-flight signal. Because the context scopes every Provider HTTP
// request in the run (#284), SIGINT aborts the in-flight call rather
// than waiting for the next page boundary. Tests construct a
// context.WithCancel directly instead.
func installSyncCancelContext() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func writeSyncResult(result syncResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeSyncPlain(writer, result)
	} else {
		writeSyncHuman(writer, result)
	}
	return writer.Err()
}

func writeSyncPlain(writer *stickyWriter, result syncResult) {
	writer.Printf("status: %s\n", result.Status)
	if result.SyncRunID != 0 {
		writer.Printf("sync_run_id: %d\n", result.SyncRunID)
	}
	if result.ConnectionID != "" {
		writer.Printf("connection_id: %s\n", result.ConnectionID)
	}
	if result.ProviderName != "" {
		writer.Printf("provider_name: %s\n", result.ProviderName)
	}
	if len(result.DataTypes) != 0 {
		writer.Printf("data_types: %s\n", strings.Join(result.DataTypes, ","))
	}
	if result.From != "" {
		writer.Printf("from: %s\n", result.From)
	}
	if result.ResumedFromCursor {
		writer.Println("resumed_from_cursor: true")
	}
	if result.To != "" {
		writer.Printf("to: %s\n", result.To)
	}
	if result.EndpointFamily != "" {
		writer.Printf("endpoint_family: %s\n", result.EndpointFamily)
	}
	if result.SourceFamily != "" {
		writer.Printf("source_family: %s\n", result.SourceFamily)
	}
	writer.Printf("data_points_seen: %d\n", result.DataPointsSeen)
	writer.Printf("data_points_new: %d\n", result.DataPointsNew)
	writer.Printf("data_points_updated: %d\n", result.DataPointsUpdated)
	writer.Printf("rollups_seen: %d\n", result.RollupsSeen)
	writer.Printf("rollups_new: %d\n", result.RollupsNew)
	writer.Printf("rollups_updated: %d\n", result.RollupsUpdated)
	writer.Printf("message: %s\n", result.Message)
}

func writeSyncHuman(writer *stickyWriter, result syncResult) {
	switch result.Status {
	case "sync_completed":
		writer.Println("Sync Run completed")
	case "sync_canceled":
		writer.Println("Sync Run canceled")
	default:
		writer.Println("Sync Run failed")
	}
	if result.SyncRunID != 0 {
		writer.Printf("Sync Run: %d\n", result.SyncRunID)
	}
	if result.ConnectionID != "" {
		writer.Printf("Connection: %s\n", result.ConnectionID)
	}
	if len(result.DataTypes) != 0 {
		writer.Printf("Data Types: %s\n", strings.Join(result.DataTypes, ","))
	}
	if result.From != "" || result.To != "" {
		writer.Printf("Range: %s to %s\n", result.From, result.To)
	}
	if result.ResumedFromCursor {
		writer.Println("Resumed from Sync Cursor")
	}
	if result.SourceFamily != "" {
		writer.Printf("Source family: %s\n", result.SourceFamily)
	}
	writer.Printf("Data Points: seen %d, new %d, updated %d\n", result.DataPointsSeen, result.DataPointsNew, result.DataPointsUpdated)
	writer.Printf("Rollups: seen %d, new %d, updated %d\n", result.RollupsSeen, result.RollupsNew, result.RollupsUpdated)
	writer.Printf("Message: %s\n", result.Message)
}

// syncFanOutResult is the JSON/Plain wire shape for a multi-Data-Type
// sync. Single-type syncs keep emitting a flat syncResult to preserve
// backwards compatibility for downstream tooling that parses sync output.
type syncFanOutResult struct {
	Status  string            `json:"status"`
	Summary syncFanOutSummary `json:"summary"`
	Results []syncResult      `json:"results"`
	Message string            `json:"message"`
}

func writeSyncFanOutResult(results []syncResult, options syncCommandOptions, mode outputMode, stdout io.Writer) error {
	summary := summarizeSyncFanOut(results, options.from, options.to)
	status := fanOutStatus(results)
	message := fanOutMessage(status, results)
	if mode.json {
		envelope := syncFanOutResult{
			Status:  status,
			Summary: summary,
			Results: results,
			Message: message,
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(envelope)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeSyncFanOutPlain(writer, results, summary, status, message)
	} else {
		writeSyncFanOutHuman(writer, results, summary, status, message)
	}
	return writer.Err()
}

func writeSyncFanOutPlain(writer *stickyWriter, results []syncResult, summary syncFanOutSummary, status, message string) {
	writer.Printf("status: %s\n", status)
	for index, result := range results {
		writeSyncFanOutResultPlain(writer, fmt.Sprintf("results.%d.", index), result)
	}
	writer.Printf("totals.data_points_seen: %d\n", summary.DataPointsSeen)
	writer.Printf("totals.data_points_new: %d\n", summary.DataPointsNew)
	writer.Printf("totals.data_points_updated: %d\n", summary.DataPointsUpdated)
	writer.Printf("totals.rollups_seen: %d\n", summary.RollupsSeen)
	writer.Printf("totals.rollups_new: %d\n", summary.RollupsNew)
	writer.Printf("totals.rollups_updated: %d\n", summary.RollupsUpdated)
	writer.Printf("message: %s\n", message)
}

func writeSyncFanOutResultPlain(writer *stickyWriter, prefix string, result syncResult) {
	writer.Printf("%sstatus: %s\n", prefix, result.Status)
	if len(result.DataTypes) > 0 {
		writer.Printf("%sdata_type: %s\n", prefix, result.DataTypes[0])
	}
	if result.SyncRunID != 0 {
		writer.Printf("%ssync_run_id: %d\n", prefix, result.SyncRunID)
	}
	for _, counter := range []struct {
		key   string
		value int
	}{
		{"data_points_seen", result.DataPointsSeen},
		{"data_points_new", result.DataPointsNew},
		{"data_points_updated", result.DataPointsUpdated},
		{"rollups_seen", result.RollupsSeen},
		{"rollups_new", result.RollupsNew},
		{"rollups_updated", result.RollupsUpdated},
	} {
		writer.Printf("%s%s: %d\n", prefix, counter.key, counter.value)
	}
	if result.Message != "" {
		writer.Printf("%smessage: %s\n", prefix, result.Message)
	}
}

func writeSyncFanOutHuman(writer *stickyWriter, results []syncResult, summary syncFanOutSummary, status, message string) {
	switch status {
	case "sync_completed":
		writer.Printf("Sync Run fan-out completed across %d Data Types\n", len(results))
	case "sync_canceled":
		writer.Printf("Sync Run fan-out canceled across %d Data Types\n", len(results))
	default:
		writer.Printf("Sync Run fan-out failed across %d Data Types\n", len(results))
	}
	for _, result := range results {
		dataType := "?"
		if len(result.DataTypes) > 0 {
			dataType = result.DataTypes[0]
		}
		writer.Printf("- %s: %s — Data Points new=%d updated=%d, Rollups new=%d updated=%d\n", dataType, result.Status, result.DataPointsNew, result.DataPointsUpdated, result.RollupsNew, result.RollupsUpdated)
	}
	writer.Printf("Totals: Data Points seen=%d new=%d updated=%d, Rollups seen=%d new=%d updated=%d\n", summary.DataPointsSeen, summary.DataPointsNew, summary.DataPointsUpdated, summary.RollupsSeen, summary.RollupsNew, summary.RollupsUpdated)
	writer.Println(message)
}

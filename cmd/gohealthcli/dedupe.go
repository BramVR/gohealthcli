package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const dedupeConfirmFlagUsage = "delete removable twin Data Points instead of only reporting them"

func dedupeCommonFlagNames() []string {
	return []string{"config", "db", "json", "plain"}
}

type dedupeResult struct {
	Status        string               `json:"status"`
	ArchivePath   string               `json:"archive_path"`
	TwinGroups    int                  `json:"twin_groups"`
	RemovableRows int                  `json:"removable_rows"`
	RemovedRows   int                  `json:"removed_rows"`
	BlockedGroups int                  `json:"blocked_groups"`
	Groups        []dedupeGroupOutput  `json:"groups"`
	Blocked       []dedupeBlockedEntry `json:"blocked"`
	Message       string               `json:"message"`
}

type dedupeGroupOutput struct {
	KeepID       int64   `json:"keep_id"`
	DuplicateIDs []int64 `json:"duplicate_ids"`
	DataType     string  `json:"data_type"`
}

type dedupeBlockedEntry struct {
	KeepID      int64             `json:"keep_id"`
	DuplicateID int64             `json:"duplicate_id"`
	DataType    string            `json:"data_type"`
	Reason      dedupeBlockReason `json:"reason"`
}

func newDedupeResult(archivePath string, plan dedupePlan, confirmed bool, removed int) dedupeResult {
	result := dedupeResult{
		Status:      "dedupe_planned",
		ArchivePath: archivePath,
		TwinGroups:  len(plan.Groups),
		RemovedRows: removed,
		Groups:      []dedupeGroupOutput{},
		Blocked:     []dedupeBlockedEntry{},
	}
	if confirmed {
		result.Status = "dedupe_applied"
	}
	for _, group := range plan.Groups {
		if !group.removable() {
			result.BlockedGroups++
			for _, block := range group.Blocks {
				result.Blocked = append(result.Blocked, dedupeBlockedEntry{group.KeepID, block.DuplicateID, group.DataType, block.Reason})
			}
			continue
		}
		result.RemovableRows += len(group.DuplicateIDs)
		result.Groups = append(result.Groups, dedupeGroupOutput{group.KeepID, group.DuplicateIDs, group.DataType})
	}
	switch {
	case confirmed:
		result.Message = fmt.Sprintf("removed %d twin Data Points", removed)
	case result.RemovableRows > 0:
		result.Message = fmt.Sprintf("dry run: rerun with --confirm to remove %d twin Data Points", result.RemovableRows)
	default:
		result.Message = "dry run: no removable twin Data Points"
	}
	if result.BlockedGroups > 0 {
		result.Message += "; blocked twin groups were left unchanged"
	}
	return result
}

func runDedupe(args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("dedupe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common := RegisterCommon(flags, CommonFlagSpec{Accepted: dedupeCommonFlagNames()}, CommonFlagValues{
		ConfigPath:          globals.ConfigPath,
		ArchivePath:         globals.ArchivePath,
		JSONOutput:          globals.JSONOutput,
		PlainOutput:         globals.PlainOutput,
		ArchivePathExplicit: globals.ArchivePathExplicit,
		ConfigPathExplicit:  globals.ConfigPathExplicit,
	})
	confirm := flags.Bool("confirm", false, dedupeConfirmFlagUsage)
	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode := commonOutputMode(*common)
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{Command: "dedupe", Status: StatusUnexpectedArgument, Message: fmt.Sprintf("unexpected dedupe argument: %s", flags.Arg(0)), Mode: mode}, stdout, stderr)
	}
	fail := func(err error) int {
		err = archiveFailureRemediation(err)
		return ReportFailure(FailureReport{Command: "dedupe", Status: StatusOperationFailed, Message: err.Error(), Mode: mode, Cause: err}, stdout, stderr)
	}

	archivePath, err := resolveReadArchivePath(*common)
	if err != nil {
		return fail(err)
	}
	var plan dedupePlan
	removed := 0
	if *confirm {
		plan, removed, err = applyDataPointDedupe(context.Background(), archivePath)
	} else {
		plan, err = inspectDataPointDedupe(context.Background(), archivePath)
	}
	if err != nil {
		return fail(err)
	}
	result := newDedupeResult(archivePath, plan, *confirm, removed)
	if err := writeDedupeResult(result, mode, stdout); err != nil {
		return reportWriteFailure("dedupe", err, mode, stdout, stderr)
	}
	if result.BlockedGroups > 0 {
		return 1
	}
	return 0
}

func writeDedupeResult(result dedupeResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeDedupePlain(writer, result)
	} else {
		writeDedupeHuman(writer, result)
	}
	return writer.Err()
}

func writeDedupePlain(writer *stickyWriter, result dedupeResult) {
	writer.Printf("status: %s\n", result.Status)
	writer.Printf("archive_path: %s\n", escapePlainControlChars(result.ArchivePath))
	writer.Printf("twin_groups: %d\n", result.TwinGroups)
	writer.Printf("removable_rows: %d\n", result.RemovableRows)
	writer.Printf("removed_rows: %d\n", result.RemovedRows)
	writer.Printf("blocked_groups: %d\n", result.BlockedGroups)
	for i, group := range result.Groups {
		writer.Printf("groups.%d.keep_id: %d\n", i, group.KeepID)
		writer.Printf("groups.%d.duplicate_ids: %s\n", i, joinIDs(group.DuplicateIDs))
		writer.Printf("groups.%d.data_type: %s\n", i, escapePlainControlChars(group.DataType))
	}
	for i, blocked := range result.Blocked {
		writer.Printf("blocked.%d.keep_id: %d\n", i, blocked.KeepID)
		writer.Printf("blocked.%d.duplicate_id: %d\n", i, blocked.DuplicateID)
		writer.Printf("blocked.%d.data_type: %s\n", i, escapePlainControlChars(blocked.DataType))
		writer.Printf("blocked.%d.reason: %s\n", i, blocked.Reason)
	}
	writer.Printf("message: %s\n", result.Message)
}

func writeDedupeHuman(writer *stickyWriter, result dedupeResult) {
	if result.Status == "dedupe_applied" {
		writer.Println("Dedupe applied")
	} else {
		writer.Println("Dedupe planned (dry run)")
	}
	writer.Printf("Health Archive: %s\n", result.ArchivePath)
	writer.Printf("Twin groups: %d\n", result.TwinGroups)
	writer.Printf("Removable rows: %d\n", result.RemovableRows)
	writer.Printf("Removed rows: %d\n", result.RemovedRows)
	writer.Printf("Blocked groups: %d\n", result.BlockedGroups)
	for _, group := range result.Groups {
		writer.Printf("Keep %d, remove %s (%s)\n", group.KeepID, joinIDs(group.DuplicateIDs), escapePlainControlChars(group.DataType))
	}
	for _, blocked := range result.Blocked {
		writer.Printf("Blocked: keep %d, duplicate %d (%s): %s\n", blocked.KeepID, blocked.DuplicateID, escapePlainControlChars(blocked.DataType), blocked.Reason)
	}
	writer.Printf("Message: %s\n", result.Message)
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	backupmodule "github.com/BramVR/gohealthcli/internal/backup"
)

const (
	backupRepoFlagUsage      = "local encrypted backup Git checkout"
	backupRemoteFlagUsage    = "Git remote URL or path"
	backupIdentityFlagUsage  = "local age identity file"
	backupRecipientFlagUsage = "additional age public `string` recipient (repeatable)"
	backupNoPushFlagUsage    = "commit locally without pushing"
)

func backupCommonFlagNames() []string {
	return []string{"config", "db", "json", "plain"}
}

type recipientFlag []string

func (values *recipientFlag) String() string { return strings.Join(*values, ",") }

func (values *recipientFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func (values *recipientFlag) Get() any { return values.String() }

type backupInitOutput struct {
	Status string `json:"status"`
	backupmodule.InitResult
}

type backupPushOutput struct {
	Status string `json:"status"`
	backupmodule.PushResult
}

func runBackup(args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	action, parseArgs := extractBackupAction(args)
	name := "backup"
	if action == "init" || action == "push" || action == "status" {
		name += " " + action
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)

	configPath := backupmodule.DefaultConfigPath()
	if globals.ConfigPathExplicit {
		configPath = globals.ConfigPath
	}
	common := RegisterCommon(flags, CommonFlagSpec{Accepted: backupCommonFlagNames()}, CommonFlagValues{
		ConfigPath:          configPath,
		ArchivePath:         globals.ArchivePath,
		JSONOutput:          globals.JSONOutput,
		PlainOutput:         globals.PlainOutput,
		ArchivePathExplicit: globals.ArchivePathExplicit,
		ConfigPathExplicit:  globals.ConfigPathExplicit,
	})
	repo := flags.String("repo", "", backupRepoFlagUsage)
	remote := flags.String("remote", "", backupRemoteFlagUsage)
	identity := flags.String("identity", "", backupIdentityFlagUsage)
	var recipients recipientFlag
	flags.Var(&recipients, "recipient", backupRecipientFlagUsage)
	noPush := flags.Bool("no-push", false, backupNoPushFlagUsage)

	if err := ParseCommon(flags, common, parseArgs, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode := commonOutputMode(*common)
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{Command: "backup", Status: StatusUnexpectedArgument, Message: "unexpected backup arguments: " + strings.Join(flags.Args(), " "), Mode: mode}, stdout, stderr)
	}
	if action == "" {
		return ReportFailure(FailureReport{Command: "backup", Status: StatusFlagInvalid, Message: "backup requires an action: init, push, or status", Mode: mode}, stdout, stderr)
	}
	if action != "init" && action != "push" && action != "status" {
		return ReportFailure(FailureReport{Command: "backup", Status: StatusFlagInvalid, Message: fmt.Sprintf("unknown backup action: %s", action), Mode: mode}, stdout, stderr)
	}
	if action != "push" && common.ArchivePathExplicit {
		return ReportFailure(FailureReport{Command: "backup", Status: StatusFlagInvalid, Message: "--db is only valid with backup push", Mode: mode}, stdout, stderr)
	}

	opts := backupmodule.Options{
		ConfigPath: common.ConfigPath,
		Repo:       *repo,
		Remote:     *remote,
		Identity:   *identity,
		Recipients: append([]string(nil), recipients...),
	}
	switch action {
	case "init":
		opts.Push = !*noPush
		result, err := backupmodule.Init(context.Background(), opts)
		if err != nil {
			return backupFailure("init", err, mode, stdout, stderr)
		}
		if err := writeBackupInitOutput(backupInitOutput{Status: "backup_initialized", InitResult: result}, mode, stdout); err != nil {
			return reportWriteFailure("backup", err, mode, stdout, stderr)
		}
		return 0
	case "push":
		opts.Push = !*noPush
		result, err := backupmodule.PushCurrent(context.Background(), opts, func() (backupmodule.PushInput, error) {
			snapshot, err := ExportHealthArchiveSnapshot(context.Background(), common.ArchivePath)
			if err != nil {
				return backupmodule.PushInput{}, err
			}
			shards, err := EncodeHealthArchiveSnapshotJSONL(snapshot)
			if err != nil {
				return backupmodule.PushInput{}, err
			}
			plaintextShards := make([]backupmodule.PlaintextShard, 0, len(shards))
			for _, shard := range shards {
				plaintextShards = append(plaintextShards, backupmodule.PlaintextShard{
					Table: shard.Table,
					Path:  shard.Path,
					Rows:  shard.Rows,
					JSONL: shard.JSONL,
				})
			}
			return backupmodule.PushInput{
				SchemaVersion: snapshot.SchemaVersion,
				ExportedAt:    runtime.now(),
				Counts: backupmodule.Counts{
					Connections:          len(snapshot.Connections),
					DataPoints:           len(snapshot.DataPoints),
					DataPointRevisions:   len(snapshot.DataPointRevisions),
					DataPointAttachments: len(snapshot.DataPointAttachments),
					AttachmentPayloads:   len(snapshot.AttachmentPayloads),
					Rollups:              len(snapshot.Rollups),
					IdentitySnapshots:    len(snapshot.IdentitySnapshots),
					SyncRuns:             len(snapshot.SyncRuns),
					SyncCursors:          len(snapshot.SyncCursors),
				},
				Shards: plaintextShards,
			}, nil
		})
		if err != nil {
			return backupFailure("push", err, mode, stdout, stderr)
		}
		if err := writeBackupPushOutput(backupPushOutput{Status: "backup_pushed", PushResult: result}, mode, stdout); err != nil {
			return reportWriteFailure("backup", err, mode, stdout, stderr)
		}
		return 0
	case "status":
		if *noPush {
			return ReportFailure(FailureReport{Command: "backup", Status: StatusFlagInvalid, Message: "--no-push is only valid with backup init or backup push", Mode: mode}, stdout, stderr)
		}
		result, err := backupmodule.Status(context.Background(), opts)
		if err != nil {
			return backupFailure("status", err, mode, stdout, stderr)
		}
		if err := writeBackupStatusOutput(result, mode, stdout); err != nil {
			return reportWriteFailure("backup", err, mode, stdout, stderr)
		}
		return 0
	}
	return 0
}

func extractBackupAction(args []string) (string, []string) {
	stringFlags := map[string]struct{}{
		"config": {}, "db": {}, "repo": {}, "remote": {}, "identity": {}, "recipient": {},
	}
	flagsEnded := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" && !flagsEnded {
			flagsEnded = true
			continue
		}
		if !flagsEnded && strings.HasPrefix(arg, "-") && arg != "-" {
			name := strings.TrimLeft(arg, "-")
			if _, _, hasValue := strings.Cut(name, "="); !hasValue {
				if _, consumesNext := stringFlags[name]; consumesNext && index+1 < len(args) {
					index++
				}
			}
			continue
		}
		parseArgs := append([]string(nil), args[:index]...)
		parseArgs = append(parseArgs, args[index+1:]...)
		return arg, parseArgs
	}
	return "", append([]string(nil), args...)
}

func backupFailure(action string, err error, mode outputMode, stdout, stderr io.Writer) int {
	return ReportFailure(FailureReport{
		Command: "backup",
		Status:  StatusOperationFailed,
		Message: fmt.Sprintf("backup %s failed: %v", action, err),
		Mode:    mode,
		Cause:   err,
	}, stdout, stderr)
}

func writeBackupInitOutput(result backupInitOutput, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	writer.Printf("status: %s\n", result.Status)
	writer.Printf("repo_path: %s\n", escapePlainControlChars(result.RepoPath))
	if result.Remote != "" {
		writer.Printf("remote: %s\n", escapePlainControlChars(result.Remote))
	}
	writer.Printf("identity_path: %s\n", escapePlainControlChars(result.Identity))
	writer.Printf("recipient: %s\n", escapePlainControlChars(result.Recipient))
	writer.Printf("changed: %t\n", result.Changed)
	writer.Printf("pushed: %t\n", result.Pushed)
	if !mode.plain {
		writer.Printf("message: encrypted backup initialized\n")
	}
	return writer.Err()
}

func writeBackupStatusOutput(result backupmodule.StatusResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	writer.Printf("status: %s\n", result.Status)
	writer.Printf("repo_path: %s\n", escapePlainControlChars(result.RepoPath))
	writer.Printf("encrypted: %t\n", result.Encrypted)
	writer.Printf("shard_count: %d\n", result.ShardCount)
	if result.ExportedAt != "" {
		writer.Printf("exported_at: %s\n", escapePlainControlChars(result.ExportedAt))
	}
	if result.Counts != nil {
		writeBackupCountsPlain(writer, *result.Counts)
	}
	if !mode.plain {
		switch result.Status {
		case backupmodule.StatusUninitialized:
			writer.Printf("message: backup has not been initialized\n")
		case backupmodule.StatusEmpty:
			writer.Printf("message: backup repository has no Health Archive manifest yet\n")
		case backupmodule.StatusReady:
			writer.Printf("message: encrypted backup manifest is available\n")
		}
	}
	return writer.Err()
}

func writeBackupPushOutput(result backupPushOutput, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	writer.Printf("status: %s\n", result.Status)
	writer.Printf("repo_path: %s\n", escapePlainControlChars(result.RepoPath))
	writer.Printf("changed: %t\n", result.Changed)
	writer.Printf("pushed: %t\n", result.Pushed)
	writer.Printf("encrypted: %t\n", result.Encrypted)
	writer.Printf("shard_count: %d\n", result.ShardCount)
	writeBackupCountsPlain(writer, result.Counts)
	if !mode.plain {
		switch {
		case result.Changed && result.Pushed:
			writer.Printf("message: encrypted Health Archive Snapshot committed and pushed\n")
		case result.Changed:
			writer.Printf("message: encrypted Health Archive Snapshot committed locally\n")
		case result.Pushed:
			writer.Printf("message: Health Archive Snapshot unchanged; existing commit pushed\n")
		default:
			writer.Printf("message: Health Archive Snapshot unchanged; backup checkout clean\n")
		}
	}
	return writer.Err()
}

func writeBackupCountsPlain(writer *stickyWriter, counts backupmodule.Counts) {
	writer.Printf("health_archive.connections: %d\n", counts.Connections)
	writer.Printf("health_archive.data_points: %d\n", counts.DataPoints)
	writer.Printf("health_archive.data_point_revisions: %d\n", counts.DataPointRevisions)
	writer.Printf("health_archive.data_point_attachments: %d\n", counts.DataPointAttachments)
	writer.Printf("health_archive.attachment_payloads: %d\n", counts.AttachmentPayloads)
	writer.Printf("health_archive.rollups: %d\n", counts.Rollups)
	writer.Printf("health_archive.identity_snapshots: %d\n", counts.IdentitySnapshots)
	writer.Printf("health_archive.sync_runs: %d\n", counts.SyncRuns)
	writer.Printf("health_archive.sync_cursors: %d\n", counts.SyncCursors)
}

var _ flag.Getter = (*recipientFlag)(nil)

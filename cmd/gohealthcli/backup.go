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
	return []string{"config", "json", "plain"}
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

func runBackup(args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	action, parseArgs := extractBackupAction(args)
	name := "backup"
	if action != "" {
		name += " " + action
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)

	configPath := backupmodule.DefaultConfigPath()
	if globals.ConfigPathExplicit {
		configPath = globals.ConfigPath
	}
	common := RegisterCommon(flags, CommonFlagSpec{Accepted: backupCommonFlagNames()}, CommonFlagValues{
		ConfigPath:         configPath,
		JSONOutput:         globals.JSONOutput,
		PlainOutput:        globals.PlainOutput,
		ConfigPathExplicit: globals.ConfigPathExplicit,
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
		return ReportFailure(FailureReport{Command: "backup", Status: StatusFlagInvalid, Message: "backup requires an action: init or status", Mode: mode}, stdout, stderr)
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
	case "status":
		if *noPush {
			return ReportFailure(FailureReport{Command: "backup", Status: StatusFlagInvalid, Message: "--no-push is only valid with backup init", Mode: mode}, stdout, stderr)
		}
		result, err := backupmodule.Status(context.Background(), opts)
		if err != nil {
			return backupFailure("status", err, mode, stdout, stderr)
		}
		if err := writeBackupStatusOutput(result, mode, stdout); err != nil {
			return reportWriteFailure("backup", err, mode, stdout, stderr)
		}
		return 0
	default:
		return ReportFailure(FailureReport{Command: "backup", Status: StatusFlagInvalid, Message: fmt.Sprintf("unknown backup action: %s", action), Mode: mode}, stdout, stderr)
	}
}

func extractBackupAction(args []string) (string, []string) {
	stringFlags := map[string]struct{}{
		"config": {}, "repo": {}, "remote": {}, "identity": {}, "recipient": {},
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
		writer.Printf("health_archive.connections: %d\n", result.Counts.Connections)
		writer.Printf("health_archive.data_points: %d\n", result.Counts.DataPoints)
		writer.Printf("health_archive.data_point_revisions: %d\n", result.Counts.DataPointRevisions)
		writer.Printf("health_archive.data_point_attachments: %d\n", result.Counts.DataPointAttachments)
		writer.Printf("health_archive.attachment_payloads: %d\n", result.Counts.AttachmentPayloads)
		writer.Printf("health_archive.rollups: %d\n", result.Counts.Rollups)
		writer.Printf("health_archive.identity_snapshots: %d\n", result.Counts.IdentitySnapshots)
		writer.Printf("health_archive.sync_runs: %d\n", result.Counts.SyncRuns)
		writer.Printf("health_archive.sync_cursors: %d\n", result.Counts.SyncCursors)
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

var _ flag.Getter = (*recipientFlag)(nil)

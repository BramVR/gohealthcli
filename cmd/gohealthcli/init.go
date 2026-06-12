package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

type initResult struct {
	Status            string   `json:"status"`
	ConfigPath        string   `json:"config_path"`
	ArchivePath       string   `json:"archive_path"`
	OAuthClientSource string   `json:"oauth_client_source,omitempty"`
	DefaultDataTypes  []string `json:"default_data_types"`
	SchemaVersion     int      `json:"schema_version"`
	Message           string   `json:"message,omitempty"`
}

func runInit(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		JSONOutput:  mode.json,
		PlainOutput: mode.plain,
	})
	oauthClientFile := flags.String("oauth-client-file", "", "OAuth client JSON file reference")
	secretProvider := flags.String("secret-provider", "", "Secret Provider name for OAuth client setup")
	oauthClientItem := flags.String("oauth-client-item", "", "Secret Provider item name for OAuth client setup")

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "init",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected init argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	if fileExists(common.ConfigPath) && fileExists(common.ArchivePath) {
		if err := validateConfig(common.ConfigPath, common.ArchivePath); err != nil {
			return ReportFailure(FailureReport{
				Command: "init",
				Status:  StatusOperationFailed,
				Message: fmt.Sprintf("existing config is not initialized: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		lifecycle := healthArchiveLifecycle{path: common.ArchivePath}
		if err := lifecycle.Migrate(context.Background()); err != nil {
			return ReportFailure(FailureReport{
				Command: "init",
				Status:  StatusOperationFailed,
				Message: fmt.Sprintf("existing Health Archive is not initialized: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		if _, err := lifecycle.Inspect(context.Background(), false); err != nil {
			return ReportFailure(FailureReport{
				Command: "init",
				Status:  StatusOperationFailed,
				Message: fmt.Sprintf("existing Health Archive is not initialized: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		result := initResult{
			Status:           "already_initialized",
			ConfigPath:       common.ConfigPath,
			ArchivePath:      common.ArchivePath,
			DefaultDataTypes: defaultDataTypes,
			SchemaVersion:    currentSchemaVersion,
			Message:          "local gohealthcli setup already exists",
		}
		if err := writeInitResult(result, mode, stdout); err != nil {
			return reportWriteFailure("init", err, mode, stdout, stderr)
		}
		return 0
	}
	if fileExists(common.ConfigPath) || fileExists(common.ArchivePath) {
		return ReportFailure(FailureReport{
			Command: "init",
			Status:  StatusOperationFailed,
			Message: "refusing to overwrite partial local setup; move existing config or Health Archive first",
			Mode:    mode,
		}, stdout, stderr)
	}

	source, err := parseOAuthClientSource(*oauthClientFile, *secretProvider, *oauthClientItem)
	if err != nil {
		return ReportFailure(FailureReport{Command: "init", Status: StatusFlagInvalid, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	if err := validateOAuthClientConfig(source); err != nil {
		return ReportFailure(FailureReport{Command: "init", Status: StatusFlagInvalid, Message: err.Error(), Mode: mode}, stdout, stderr)
	}

	if err := createConfigFile(common.ConfigPath, common.ArchivePath, source); err != nil {
		return ReportFailure(FailureReport{
			Command: "init",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("create config: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	if err := createArchive(common.ArchivePath); err != nil {
		_ = os.Remove(common.ConfigPath)
		_ = os.Remove(common.ArchivePath)
		return ReportFailure(FailureReport{
			Command: "init",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("create Health Archive: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}

	result := initResult{
		Status:            "initialized",
		ConfigPath:        common.ConfigPath,
		ArchivePath:       common.ArchivePath,
		OAuthClientSource: source.kind,
		DefaultDataTypes:  defaultDataTypes,
		SchemaVersion:     currentSchemaVersion,
	}
	if err := writeInitResult(result, mode, stdout); err != nil {
		return reportWriteFailure("init", err, mode, stdout, stderr)
	}
	return 0
}

func createArchive(archivePath string) (err error) {
	if err := (healthArchiveLifecycle{path: archivePath}).Create(context.Background()); err != nil {
		return err
	}
	// Pre-create the attachment root so users running init see the
	// full archive shape without waiting for a sync to lazily create
	// it. Owner-only mode follows the rest of the archive.
	return ensureOwnerOnlyDir(attachmentRootDirForArchive(archivePath))
}

func writeInitResult(result initResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "config_path: %s\n", result.ConfigPath); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "archive_path: %s\n", result.ArchivePath); err != nil {
			return err
		}
		if result.OAuthClientSource != "" {
			if _, err := fmt.Fprintf(stdout, "oauth_client_source: %s\n", result.OAuthClientSource); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "default_data_types: %s\n", strings.Join(result.DefaultDataTypes, ",")); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "schema_version: %d\n", result.SchemaVersion); err != nil {
			return err
		}
		if result.Message != "" {
			if _, err := fmt.Fprintf(stdout, "message: %s\n", result.Message); err != nil {
				return err
			}
		}
		return nil
	}

	if result.Status == "already_initialized" {
		if _, err := fmt.Fprintln(stdout, "Already initialized"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(stdout, "Initialized gohealthcli"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "Config: %s\n", result.ConfigPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "Health Archive: %s\n", result.ArchivePath); err != nil {
		return err
	}
	if result.OAuthClientSource != "" {
		if _, err := fmt.Fprintf(stdout, "OAuth client source: %s\n", result.OAuthClientSource); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "Default Data Types: %s\n", strings.Join(result.DefaultDataTypes, ", ")); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "Schema version: %d\n", result.SchemaVersion)
	return err
}

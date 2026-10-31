package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

type doctorResult struct {
	Status             string                  `json:"status"`
	ConfigPath         string                  `json:"config_path"`
	ArchivePath        string                  `json:"archive_path"`
	OAuthClientSource  string                  `json:"oauth_client_source"`
	CredentialStore    string                  `json:"credential_store"`
	Timezone           string                  `json:"timezone,omitempty"`
	SchemaVersion      *int                    `json:"schema_version"`
	ConnectionCount    *int                    `json:"connection_count"`
	TokenStatus        string                  `json:"token_status"`
	AttachmentRootPath string                  `json:"attachment_root_path,omitempty"`
	AttachmentRootMode string                  `json:"attachment_root_mode,omitempty"`
	Attachments        *doctorAttachmentReport `json:"attachments,omitempty"`
	Message            string                  `json:"message"`
	Remediation        []string                `json:"remediation,omitempty"`
}

type doctorAttachmentReport struct {
	// Always emit both arrays so downstream tools can rely on the
	// shape; nil slices would encode as null, so the constructor
	// initialises them to []. omitempty would break the contract when
	// only one side has orphans.
	OrphanRows           []doctorOrphanRow         `json:"orphan_rows"`
	OrphanFiles          []doctorOrphanFile        `json:"orphan_files"`
	VerificationFailures []doctorAttachmentFailure `json:"verification_failures,omitempty"`
}

type doctorAttachmentFailure struct {
	AttachmentID int64  `json:"attachment_id"`
	DataPointID  int64  `json:"data_point_id"`
	SHA256       string `json:"sha256"`
	PathRelative string `json:"path_relative"`
	Reason       string `json:"reason"`
}

func inspectDoctorArchive(ctx context.Context, result *doctorResult) (err error) {
	handle, err := (healthArchiveLifecycle{path: result.ArchivePath}).openInspectionOnly(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, handle.Close()) }()
	count, tokenStatus, err := inspectConnectionTokenMetadata(ctx, handle.db)
	if err != nil {
		return err
	}
	result.SchemaVersion = &handle.schemaVersion
	result.ConnectionCount = &count
	result.TokenStatus = tokenStatus
	root, mode, err := inspectAttachmentRoot(result.ArchivePath)
	result.AttachmentRootPath = root
	result.AttachmentRootMode = mode
	if err != nil {
		return err
	}
	store := &attachmentStore{db: handle.db, archivePath: result.ArchivePath, rootDir: root}
	result.Attachments, err = collectAttachmentReport(ctx, store)
	if err != nil {
		return err
	}
	if result.Attachments != nil && len(result.Attachments.VerificationFailures) > 0 {
		return errors.New("Data Point Attachment verification failed; restore the matching Health Archive and sidecars from a known-good copy")
	}
	return nil
}

type doctorOrphanRow struct {
	SHA256       string `json:"sha256"`
	PathRelative string `json:"path_relative"`
	DataPointID  int64  `json:"data_point_id"`
}

type doctorOrphanFile struct {
	AbsolutePath string `json:"absolute_path"`
}

func runDoctorWithRuntime(args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  globals.ConfigPath,
		ArchivePath: globals.ArchivePath,
		JSONOutput:  globals.JSONOutput,
		PlainOutput: globals.PlainOutput,
	})
	doctorOnline := flags.Bool("online", false, "refresh tokens and check provider reachability")

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode := commonOutputMode(*common)
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "doctor",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected doctor argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	if fileExists(common.ConfigPath) && fileExists(common.ArchivePath) {
		if *doctorOnline {
			return runDoctorOnlineWithRuntime(common.ConfigPath, common.ArchivePath, mode, stdout, stderr, runtime)
		}
		config, err := inspectConfig(common.ConfigPath, common.ArchivePath)
		if err != nil {
			return runDoctorInvalid(common.ConfigPath, common.ArchivePath, fmt.Sprintf("config check failed: %v", err), mode, stdout, stderr)
		}
		result := doctorResult{
			Status:            "ok",
			ConfigPath:        common.ConfigPath,
			ArchivePath:       common.ArchivePath,
			OAuthClientSource: config.oauthClientSource,
			CredentialStore:   config.credentialStore,
			Timezone:          config.timezone,
			TokenStatus:       "unknown",
			Message:           "local gohealthcli setup is initialized",
		}
		inspectionErr := inspectDoctorArchive(context.Background(), &result)
		if inspectionErr != nil {
			result.Status = "setup_invalid"
			result.Message = inspectionErr.Error()
			if result.Attachments == nil || len(result.Attachments.VerificationFailures) == 0 {
				result.Remediation = normalizeRemediation([]remediationAction{remediationRunDoctor, remediationInitialize})
			}
		}
		if err := writeDoctorResult(result, mode, stdout); err != nil {
			return reportWriteFailure("doctor", err, mode, stdout, stderr)
		}
		if inspectionErr != nil {
			return 1
		}
		return 0
	}
	if fileExists(common.ConfigPath) || fileExists(common.ArchivePath) {
		return runDoctorInvalid(common.ConfigPath, common.ArchivePath, "partial local setup found; run `gohealthcli init` after moving existing config or Health Archive", mode, stdout, stderr)
	}

	result := doctorResult{
		Status:      "setup_missing",
		ConfigPath:  common.ConfigPath,
		ArchivePath: common.ArchivePath,
		TokenStatus: "unknown",
		Message:     "local gohealthcli setup not found",
		Remediation: normalizeRemediation([]remediationAction{remediationRunDoctor, remediationInitialize}),
	}

	if err := writeDoctorResult(result, mode, stdout); err != nil {
		return reportWriteFailure("doctor", err, mode, stdout, stderr)
	}

	// The structured envelope already landed on stdout via
	// writeDoctorResult above; the hint line stays as a plain stderr
	// write so JSON-mode callers get the envelope on stdout AND the
	// human hint on stderr. failureExitCode routes through the
	// Failure Reporter module's status→exit-code map so no site
	// references setupMissingExitCode directly (#178).
	fmt.Fprintln(stderr, "run `gohealthcli init` to create local config and Health Archive")
	return failureExitCode(StatusSetupMissing)
}

func runDoctorInvalid(configPath, archivePath, message string, mode outputMode, stdout, stderr io.Writer) int {
	result := doctorResult{
		Status:      "setup_invalid",
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		TokenStatus: "unknown",
		Message:     message,
		Remediation: normalizeRemediation([]remediationAction{remediationRunDoctor, remediationInitialize}),
	}
	if err := writeDoctorResult(result, mode, stdout); err != nil {
		return reportWriteFailure("doctor", err, mode, stdout, stderr)
	}
	return 1
}

func runDoctorOnlineWithRuntime(configPath, archivePath string, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	result, err := doctorOnlineSetupWithRuntime(configPath, archivePath, runtime)
	if err != nil {
		if result.Status == "" || result.Status == "ok" {
			result.Status = "connection_unhealthy"
		}
		if result.Message == "" {
			result.Message = err.Error()
			result.Remediation = remediationFromError(connectionFailureRemediation(err))
		}
		if writeErr := writeDoctorResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure("doctor", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	if err := writeDoctorResult(result, mode, stdout); err != nil {
		return reportWriteFailure("doctor", err, mode, stdout, stderr)
	}
	return 0
}

func doctorOnlineSetupWithRuntime(configPath, archivePath string, runtime runtimeAdapters) (doctorResult, error) {
	runtime = runtime.withDefaults()
	config, err := inspectFullConfig(configPath, archivePath)
	if err != nil {
		cause := setupFailureRemediation(err, fmt.Sprintf("config check failed: %v", err))
		return doctorResult{Status: "setup_invalid", ConfigPath: configPath, ArchivePath: archivePath, TokenStatus: "unknown"}, cause
	}
	result := doctorResult{
		Status:            "ok",
		ConfigPath:        configPath,
		ArchivePath:       archivePath,
		OAuthClientSource: config.oauthClient.kind,
		CredentialStore:   config.credentialStore.kind,
		Timezone:          config.timezone,
	}
	if err := inspectDoctorArchive(context.Background(), &result); err != nil {
		result.Status = "setup_invalid"
		if result.TokenStatus == "" {
			result.TokenStatus = "unknown"
		}
		result.Message = err.Error()
		return result, err
	}
	if *result.ConnectionCount == 0 {
		result.TokenStatus = "not_connected"
		return result, missingConnectionRemediation(sql.ErrNoRows)
	}
	archiveAPI, err := openExistingDoctorOnlineArchive(archivePath)
	if err != nil {
		result.Status = "setup_invalid"
		result.TokenStatus = "archive_unavailable"
		return result, err
	}
	defer archiveAPI.Close()
	connection, err := archiveAPI.CurrentConnection()
	if err != nil {
		result.TokenStatus = "connection_unavailable"
		return result, connectionFailureRemediation(err)
	}
	protectedPaths := []string{configPath, archivePath, config.oauthClient.path}
	connectionAccess := newCurrentConnectionAccessWithRuntime(config.credentialStore, connection, protectedPaths, runtime)
	tokenCheck, err := connectionAccess.RefreshableAccessToken(config.oauthClient)
	if err != nil {
		if result.TokenStatus == "metadata_present" {
			if isCurrentConnectionTokenMissing(err) {
				result.TokenStatus = "token_missing"
			} else {
				result.TokenStatus = "refresh_failed"
			}
		}
		return result, connectionFailureRemediation(err)
	}
	if tokenCheck.refreshedToken == nil {
		result.TokenStatus = "metadata_present"
	}
	if _, err := connectionAccess.FetchVerifiedIdentity(tokenCheck.accessToken); err != nil {
		result.TokenStatus = "provider_unreachable"
		if isCurrentConnectionIdentityMismatch(err) {
			result.TokenStatus = "identity_mismatch"
		}
		return result, connectionFailureRemediation(err)
	}
	if tokenCheck.refreshedToken != nil {
		if err := persistDoctorOnlineRefreshedTokenWithRuntime(archiveAPI, config.credentialStore, connection.ID, *tokenCheck.refreshedToken, tokenCheck.previousTokenMaterial, runtime); err != nil {
			result.TokenStatus = "refresh_failed"
			return result, err
		}
	}
	result.TokenStatus = "online_ok"
	result.Message = "online Google Health check passed"
	return result, nil
}

func openExistingDoctorOnlineArchive(archivePath string) (*sqliteHealthArchiveConnectionAPI, error) {
	dsn, err := archiveDSN(archivePath, false)
	if err != nil {
		return nil, err
	}
	db, err := openArchiveDSN(dsn + "&mode=rw")
	if err != nil {
		return nil, err
	}
	return &sqliteHealthArchiveConnectionAPI{db: db}, nil
}

func persistDoctorOnlineRefreshedTokenWithRuntime(archive connectionTokenWriter, credentialStore credentialStoreConfig, connectionID string, token oauthTokenResponse, previousTokenMaterial map[string]any, runtime runtimeAdapters) error {
	runtime = runtime.withDefaults()
	store, err := newCredentialStoreWithRuntime(credentialStore, runtime)
	if err != nil {
		return err
	}
	if err := store.Store(connectionID, token.rawTokenMaterialObject); err != nil {
		return err
	}
	if err := archive.UpdateConnectionTokenMetadata(connectionID, token, runtime.now()); err != nil {
		if rollbackErr := store.Store(connectionID, previousTokenMaterial); rollbackErr != nil {
			// The secondary rollback error is deliberately %v, not %w: only
			// the primary archive error may carry the typed-error chain
			// callers branch on (#272 translation layer).
			return fmt.Errorf("%w; rollback Credential Store token material: %v", err, rollbackErr) //nolint:errorlint // deliberate non-wrapping %v for the secondary error
		}
		return err
	}
	return nil
}

func writeDoctorResult(result doctorResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	writer := newStickyWriter(stdout)
	if mode.plain {
		writeDoctorPlain(writer, result)
	} else {
		writeDoctorHuman(writer, result)
	}
	return writer.Err()
}

func writeDoctorPlain(writer *stickyWriter, result doctorResult) {
	writer.Printf("status: %s\n", result.Status)
	writer.Printf("config_path: %s\n", result.ConfigPath)
	writer.Printf("archive_path: %s\n", result.ArchivePath)
	if result.OAuthClientSource != "" {
		writer.Printf("oauth_client_source: %s\n", result.OAuthClientSource)
	}
	if result.CredentialStore != "" {
		writer.Printf("credential_store: %s\n", result.CredentialStore)
	}
	if result.Timezone != "" {
		writer.Printf("timezone: %s\n", result.Timezone)
	}
	if result.SchemaVersion != nil {
		writer.Printf("schema_version: %d\n", *result.SchemaVersion)
	}
	if result.ConnectionCount != nil {
		writer.Printf("connection_count: %d\n", *result.ConnectionCount)
	}
	if result.TokenStatus != "" {
		writer.Printf("token_status: %s\n", result.TokenStatus)
	}
	if result.AttachmentRootPath != "" {
		writer.Printf("attachment_root_path: %s\n", result.AttachmentRootPath)
		if result.AttachmentRootMode != "" {
			writer.Printf("attachment_root_mode: %s\n", result.AttachmentRootMode)
		}
	}
	if result.Attachments != nil {
		if n := len(result.Attachments.OrphanFiles); n > 0 {
			writer.Printf("attachments_orphan_files: %d\n", n)
		}
		if n := len(result.Attachments.OrphanRows); n > 0 {
			writer.Printf("attachments_orphan_rows: %d\n", n)
		}
	}
	if result.Attachments != nil && len(result.Attachments.VerificationFailures) > 0 {
		writer.Printf("attachments_verification_failures: %d\n", len(result.Attachments.VerificationFailures))
		for i, failure := range result.Attachments.VerificationFailures {
			writer.Printf("attachments_verification_failures.%d.attachment_id: %d\n", i, failure.AttachmentID)
			writer.Printf("attachments_verification_failures.%d.data_point_id: %d\n", i, failure.DataPointID)
			writer.Printf("attachments_verification_failures.%d.sha256: %s\n", i, escapePlainControlChars(failure.SHA256))
			writer.Printf("attachments_verification_failures.%d.path_relative: %s\n", i, escapePlainControlChars(failure.PathRelative))
			writer.Printf("attachments_verification_failures.%d.reason: %s\n", i, escapePlainControlChars(failure.Reason))
		}
	}
	writer.Printf("message: %s\n", result.Message)
	for index, action := range result.Remediation {
		writer.Printf("remediation.%d: %s\n", index, action)
	}
}

func writeDoctorHuman(writer *stickyWriter, result doctorResult) {
	switch result.Status {
	case "ok":
		writer.Println("Setup ok")
	case "connection_unhealthy":
		writer.Println("Connection unhealthy")
	case "setup_invalid":
		writer.Println("Setup invalid")
	default:
		writer.Println("Setup missing")
	}
	writer.Printf("Config: %s\n", result.ConfigPath)
	writer.Printf("Health Archive: %s\n", result.ArchivePath)
	if result.OAuthClientSource != "" {
		writer.Printf("OAuth client source: %s\n", result.OAuthClientSource)
	}
	if result.CredentialStore != "" {
		writer.Printf("Credential Store: %s\n", result.CredentialStore)
	}
	if result.Timezone != "" {
		writer.Printf("Timezone: %s\n", result.Timezone)
	}
	if result.SchemaVersion != nil {
		writer.Printf("Schema version: %d\n", *result.SchemaVersion)
	}
	if result.ConnectionCount != nil {
		writer.Printf("Connections: %d\n", *result.ConnectionCount)
	}
	if result.TokenStatus != "" {
		writer.Printf("Token status: %s\n", result.TokenStatus)
	}
	if result.Attachments != nil {
		for _, failure := range result.Attachments.VerificationFailures {
			writer.Printf("Attachment %d (Data Point %d, sha256 %s, path %q): %s\n", failure.AttachmentID, failure.DataPointID, escapePlainControlChars(failure.SHA256), failure.PathRelative, escapePlainControlChars(failure.Reason))
		}
	}
	writer.Printf("Message: %s\n", result.Message)
}

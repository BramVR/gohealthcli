package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

type connectResult struct {
	Status             string `json:"status"`
	ConnectionID       string `json:"connection_id,omitempty"`
	ProviderName       string `json:"provider_name,omitempty"`
	GoogleHealthUserID string `json:"google_health_user_id,omitempty"`
	LegacyFitbitUserID string `json:"legacy_fitbit_user_id,omitempty"`
	CredentialStore    string `json:"credential_store,omitempty"`
	TokenStatus        string `json:"token_status,omitempty"`
	Message            string `json:"message"`
}

func runConnectWithRuntime(args []string, configPath, archivePath string, globalNoInput bool, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  configPath,
		ArchivePath: archivePath,
		JSONOutput:  mode.json,
		PlainOutput: mode.plain,
		NoInput:     globalNoInput,
	})
	// The keyword list is rendered from connectAddScopeKeywords so the
	// --help text can never drift from what expandConnectAddScopes
	// accepts again (#148: `nutrition` was accepted but invisible).
	connectAddScopes := flags.String("add-scopes", "", connectAddScopesUsage())

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode = outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "connect",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected connect argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	additionalScopes, err := expandConnectAddScopes(parseCommaList(*connectAddScopes))
	if err != nil {
		return ReportFailure(FailureReport{
			Command: "connect --add-scopes",
			Status:  StatusFlagInvalid,
			Message: err.Error(),
			Mode:    mode,
		}, stdout, stderr)
	}

	result, err := connectSetupWithRuntimeAndExtraScopes(common.ConfigPath, common.ArchivePath, common.NoInput, additionalScopes, runtime)
	if err != nil {
		result.Status = "connect_failed"
		result.Message = err.Error()
		if writeErr := writeConnectResult(result, mode, stdout); writeErr != nil {
			return reportWriteFailure("connect", writeErr, mode, stdout, stderr)
		}
		return 1
	}
	if err := writeConnectResult(result, mode, stdout); err != nil {
		return reportWriteFailure("connect", err, mode, stdout, stderr)
	}
	return 0
}

func connectSetupWithRuntimeAndExtraScopes(configPath, archivePath string, noInput bool, extraScopes []string, runtime runtimeAdapters) (connectResult, error) {
	runtime = runtime.withDefaults()
	config, err := inspectFullConfig(configPath, archivePath)
	if err != nil {
		return connectResult{}, fmt.Errorf("config check failed: %w", err)
	}
	if config.oauthClient.kind != "file" {
		return connectResult{CredentialStore: config.credentialStore.kind}, errors.New("connect requires an OAuth client file source; Secret Provider references are setup-only")
	}
	if _, err := (healthArchiveLifecycle{path: archivePath}).MigrateAndInspect(context.Background(), false); err != nil {
		var checkErr healthArchiveOpenError
		if errors.As(err, &checkErr) {
			return connectResult{}, err
		}
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	store, err := newCredentialStoreWithRuntime(config.credentialStore, runtime)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := validateCredentialStoreRuntimeWithRuntime(config.credentialStore, []string{configPath, archivePath, config.oauthClient.path}, runtime); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	client, err := loadOAuthClientConfig(config.oauthClient.path)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	requestedScopes := unionScopes(oauthScopesForDataTypes(config.defaultDataTypes), extraScopes)
	token, err := runtime.runOAuthFlow(client, requestedScopes, noInput)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	identity, err := runtime.fetchIdentity(token.accessToken)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	connectionID := "googlehealth:" + identity.healthUserID

	archive, err := openHealthArchiveConnectionAPI(archivePath)
	if err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	defer archive.Close()
	// context.Background(): connect is a synchronous interactive flow
	// with no cancellation path today (its OAuth POST rides
	// context.Background() the same way, #284); the context keeps the
	// Connection writes on the Context API (#305) without changing
	// behavior.
	if err := archive.EnsureSameGoogleIdentity(context.Background(), identity.healthUserID); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := store.Store(connectionID, token.rawTokenMaterialObject); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	if err := archive.UpsertConnection(context.Background(), connectionID, identity, token, runtime.now()); err != nil {
		return connectResult{CredentialStore: config.credentialStore.kind}, err
	}
	return connectResult{
		Status:             "connected",
		ConnectionID:       connectionID,
		ProviderName:       "googlehealth",
		GoogleHealthUserID: identity.healthUserID,
		LegacyFitbitUserID: identity.legacyFitbitUserID,
		CredentialStore:    config.credentialStore.kind,
		TokenStatus:        "metadata_present",
		Message:            "Google Identity connected",
	}, nil
}

func writeConnectResult(result connectResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if mode.plain {
		if _, err := fmt.Fprintf(stdout, "status: %s\n", result.Status); err != nil {
			return err
		}
		if result.ConnectionID != "" {
			if _, err := fmt.Fprintf(stdout, "connection_id: %s\n", result.ConnectionID); err != nil {
				return err
			}
		}
		if result.ProviderName != "" {
			if _, err := fmt.Fprintf(stdout, "provider_name: %s\n", result.ProviderName); err != nil {
				return err
			}
		}
		if result.GoogleHealthUserID != "" {
			if _, err := fmt.Fprintf(stdout, "google_health_user_id: %s\n", result.GoogleHealthUserID); err != nil {
				return err
			}
		}
		if result.LegacyFitbitUserID != "" {
			if _, err := fmt.Fprintf(stdout, "legacy_fitbit_user_id: %s\n", result.LegacyFitbitUserID); err != nil {
				return err
			}
		}
		if result.CredentialStore != "" {
			if _, err := fmt.Fprintf(stdout, "credential_store: %s\n", result.CredentialStore); err != nil {
				return err
			}
		}
		if result.TokenStatus != "" {
			if _, err := fmt.Fprintf(stdout, "token_status: %s\n", result.TokenStatus); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(stdout, "message: %s\n", result.Message)
		return err
	}
	if result.Status == "connected" {
		if _, err := fmt.Fprintln(stdout, "Connected Google Identity"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(stdout, "Connect failed"); err != nil {
		return err
	}
	if result.ConnectionID != "" {
		if _, err := fmt.Fprintf(stdout, "Connection: %s\n", result.ConnectionID); err != nil {
			return err
		}
	}
	if result.GoogleHealthUserID != "" {
		if _, err := fmt.Fprintf(stdout, "Google Health user ID: %s\n", result.GoogleHealthUserID); err != nil {
			return err
		}
	}
	if result.CredentialStore != "" {
		if _, err := fmt.Fprintf(stdout, "Credential Store: %s\n", result.CredentialStore); err != nil {
			return err
		}
	}
	if result.TokenStatus != "" {
		if _, err := fmt.Fprintf(stdout, "Token status: %s\n", result.TokenStatus); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "Message: %s\n", result.Message)
	return err
}

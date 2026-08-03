package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
)

type connectResult struct {
	Status             string   `json:"status"`
	AuthorizationURL   string   `json:"authorization_url,omitempty"`
	ExpiresAt          string   `json:"expires_at,omitempty"`
	ConnectionID       string   `json:"connection_id,omitempty"`
	ProviderName       string   `json:"provider_name,omitempty"`
	GoogleHealthUserID string   `json:"google_health_user_id,omitempty"`
	LegacyFitbitUserID string   `json:"legacy_fitbit_user_id,omitempty"`
	CredentialStore    string   `json:"credential_store,omitempty"`
	TokenStatus        string   `json:"token_status,omitempty"`
	Message            string   `json:"message"`
	Remediation        []string `json:"remediation,omitempty"`
}

func runConnectWithRuntime(args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:  globals.ConfigPath,
		ArchivePath: globals.ArchivePath,
		JSONOutput:  globals.JSONOutput,
		PlainOutput: globals.PlainOutput,
		NoInput:     globals.NoInput,
	})
	// The keyword list is rendered from connectAddScopeKeywords so the
	// --help text can never drift from what expandConnectAddScopes
	// accepts again (#148: `nutrition` was accepted but invisible).
	connectAddScopes := flags.String("add-scopes", "", connectAddScopesUsage())
	headlessStart := flags.Bool("headless-start", false, "start headless OAuth and print the authorization URL")
	headlessComplete := flags.Bool("complete", false, "complete headless OAuth from a redirected URL read on stdin")

	if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode := commonOutputMode(*common)
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
	if *headlessStart && *headlessComplete {
		return ReportFailure(FailureReport{
			Command: "connect",
			Status:  StatusFlagInvalid,
			Message: "--headless-start and --complete are mutually exclusive",
			Mode:    mode,
		}, stdout, stderr)
	}
	if *headlessComplete && common.NoInput {
		return ReportFailure(FailureReport{
			Command: "connect",
			Status:  StatusFlagInvalid,
			Message: "--complete cannot be combined with --no-input",
			Mode:    mode,
		}, stdout, stderr)
	}

	var result connectResult
	if *headlessStart {
		result, err = startHeadlessConnection(context.Background(), common.ConfigPath, common.ArchivePath, additionalScopes, runtime)
	} else if *headlessComplete {
		result, err = completeHeadlessConnection(context.Background(), common.ConfigPath, common.ArchivePath, additionalScopes, runtime)
	} else {
		result, err = connectSetupWithRuntimeAndExtraScopes(common.ConfigPath, common.ArchivePath, common.NoInput, additionalScopes, runtime)
	}
	if err != nil {
		result.Status = "connect_failed"
		result.Message = err.Error()
		result.Remediation = remediationFromError(connectionFailureRemediation(err))
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
	ctx := context.Background()
	prepared, err := prepareConnection(ctx, configPath, archivePath, extraScopes, runtime)
	result := connectResult{CredentialStore: prepared.credentialStoreKind}
	if err != nil {
		return result, err
	}
	token, err := runtime.runOAuthFlow(prepared.oauthClient, prepared.requestedScopes, noInput)
	if err != nil {
		return result, err
	}
	return finalizeConnection(ctx, prepared, token, runtime)
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
		if result.AuthorizationURL != "" {
			if _, err := fmt.Fprintf(stdout, "authorization_url: %s\n", result.AuthorizationURL); err != nil {
				return err
			}
		}
		if result.ExpiresAt != "" {
			if _, err := fmt.Fprintf(stdout, "expires_at: %s\n", result.ExpiresAt); err != nil {
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
		if _, err := fmt.Fprintf(stdout, "message: %s\n", result.Message); err != nil {
			return err
		}
		return writePlainRemediation(stdout, result.Remediation)
	}
	if result.Status == "connected" {
		if _, err := fmt.Fprintln(stdout, "Connected Google Identity"); err != nil {
			return err
		}
	} else if result.Status == "authorization_pending" {
		if _, err := fmt.Fprintln(stdout, "Headless authorization ready"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(stdout, "Connect failed"); err != nil {
		return err
	}
	if result.AuthorizationURL != "" {
		if _, err := fmt.Fprintf(stdout, "Authorization URL: %s\n", result.AuthorizationURL); err != nil {
			return err
		}
	}
	if result.ExpiresAt != "" {
		if _, err := fmt.Fprintf(stdout, "Expires at: %s\n", result.ExpiresAt); err != nil {
			return err
		}
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

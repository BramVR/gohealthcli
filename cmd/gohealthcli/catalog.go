package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

const (
	catalogDiscoveryURL      = "https://health.googleapis.com/$discovery/rest?version=v4"
	catalogDiscoveryTimeout  = 15 * time.Second
	catalogDiscoveryMaxBytes = 8 << 20
)

func catalogCommonFlagNames() []string {
	return []string{"json", "plain"}
}

func runCatalogWithRuntime(args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("catalog", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common := RegisterCommon(flags, CommonFlagSpec{Accepted: catalogCommonFlagNames()}, CommonFlagValues{
		JSONOutput:  globals.JSONOutput,
		PlainOutput: globals.PlainOutput,
	})
	discoveryPath := flags.String("discovery", "", "read a Google Health discovery document from PATH instead of the public endpoint")

	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		if len(args) == 0 {
			return ReportFailure(FailureReport{
				Command: "catalog",
				Status:  StatusUnexpectedArgument,
				Message: "expected action: verify",
			}, stdout, stderr)
		}
		if err := ParseCommon(flags, common, args, runtime.observeSubcommandFlagSet); err != nil {
			return commonFlagsExitCode(flags, err, stdout, stderr)
		}
		return 0
	}
	if args[0] != "verify" {
		return ReportFailure(FailureReport{
			Command: "catalog",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected catalog action: %s", args[0]),
			Mode:    commonOutputMode(*common),
		}, stdout, stderr)
	}
	if err := ParseCommon(flags, common, args[1:], runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode := commonOutputMode(*common)
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "catalog",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected catalog verify argument: %s", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}

	payload, source, err := loadCatalogDiscovery(*discoveryPath, runtime.withDefaults())
	result := googlehealth.CatalogAuditResult{Source: source}
	if err != nil {
		result.Status = googlehealth.CatalogDriftDetected
		result.Drift = []googlehealth.CatalogDrift{{Kind: "discovery_unavailable"}}
	} else {
		result = googlehealth.VerifyCatalogDiscovery(payload, source)
	}
	if err := writeCatalogAuditResult(result, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "catalog verify",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	if result.Status == googlehealth.CatalogDriftDetected {
		return 1
	}
	return 0
}

func loadCatalogDiscovery(path string, runtime runtimeAdapters) ([]byte, string, error) {
	if path != "" {
		payload, err := readLimitedFile(path, catalogDiscoveryMaxBytes)
		return payload, "file", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), catalogDiscoveryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogDiscoveryURL, nil)
	if err != nil {
		return nil, "live", err
	}
	request.Header.Set("Accept", "application/json")
	response, err := runtime.httpDoer.Do(request)
	if err != nil {
		return nil, "live", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "live", fmt.Errorf("discovery endpoint returned HTTP %d", response.StatusCode)
	}
	payload, err := readLimited(response.Body, catalogDiscoveryMaxBytes)
	return payload, "live", err
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file, limit)
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, errors.New("discovery document exceeds size limit")
	}
	return payload, nil
}

func writeCatalogAuditResult(result googlehealth.CatalogAuditResult, mode outputMode, stdout io.Writer) error {
	if mode.json {
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", payload)
		return err
	}

	writer := newStickyWriter(stdout)
	if mode.plain {
		writer.Printf("status: %s\n", result.Status)
		writer.Printf("source: %s\n", result.Source)
		if result.DiscoveryRevision != "" {
			writer.Printf("discovery_revision: %s\n", escapePlainControlChars(result.DiscoveryRevision))
		}
		for index, gap := range result.KnownGaps {
			writer.Printf("known_gap.%d.kind: %s\n", index, gap.Kind)
			writer.Printf("known_gap.%d.data_types: %s\n", index, strings.Join(gap.DataTypes, ","))
		}
		for index, fact := range result.Unverifiable {
			writer.Printf("unverifiable.%d.fact: %s\n", index, fact.Fact)
			writer.Printf("unverifiable.%d.reason: %s\n", index, fact.Reason)
		}
		for index, drift := range result.Drift {
			writer.Printf("drift.%d.kind: %s\n", index, drift.Kind)
			if drift.DataType != "" {
				writer.Printf("drift.%d.data_type: %s\n", index, drift.DataType)
			}
		}
		return writer.Err()
	}

	switch result.Status {
	case googlehealth.CatalogVerified:
		writer.Printf("Google Health catalog verified.\n")
	case googlehealth.CatalogVerifiedWithKnownGaps:
		writer.Printf("Google Health catalog verified with known gaps.\n")
	default:
		writer.Printf("Google Health catalog drift detected.\n")
	}
	writer.Printf("Discovery source: %s", result.Source)
	if result.DiscoveryRevision != "" {
		writer.Printf(" (revision %s)", escapePlainControlChars(result.DiscoveryRevision))
	}
	writer.Printf("\n")
	for _, gap := range result.KnownGaps {
		writer.Printf("Known gap %s: %s\n", gap.Kind, strings.Join(gap.DataTypes, ", "))
	}
	for _, fact := range result.Unverifiable {
		writer.Printf("Unverifiable %s: %s\n", fact.Fact, fact.Reason)
	}
	for _, drift := range result.Drift {
		writer.Printf("Drift %s", drift.Kind)
		if drift.DataType != "" {
			writer.Printf(": %s", drift.DataType)
		}
		writer.Printf("\n")
	}
	return writer.Err()
}

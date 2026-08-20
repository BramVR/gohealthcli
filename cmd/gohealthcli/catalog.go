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

var errCatalogDiscoveryTooLarge = errors.New("discovery document exceeds size limit")

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
	discoveryPath := flags.String("discovery", "", "with describe or verify: read a Google Health discovery document from PATH")
	live := flags.Bool("live", false, "with describe: read the fixed public Google Health discovery endpoint")

	parseArgs, action := catalogFlagArgs(args)
	if err := ParseCommon(flags, common, parseArgs, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode := commonOutputMode(*common)
	positionals := flags.Args()
	if action != "describe" && len(positionals) != 0 {
		message := fmt.Sprintf("unexpected catalog action: %s", flags.Arg(0))
		if action != "" {
			message = fmt.Sprintf("unexpected catalog %s argument: %s", action, flags.Arg(0))
		}
		return ReportFailure(FailureReport{
			Command: "catalog",
			Status:  StatusUnexpectedArgument,
			Message: message,
			Mode:    mode,
		}, stdout, stderr)
	}
	if action == "" {
		return ReportFailure(FailureReport{
			Command: "catalog",
			Status:  StatusUnexpectedArgument,
			Message: "expected action: list, scopes, verify, or describe",
			Mode:    mode,
		}, stdout, stderr)
	}
	if action == "list" {
		if *discoveryPath != "" {
			return ReportFailure(FailureReport{
				Command: "catalog list",
				Status:  StatusFlagInvalid,
				Message: "--discovery is supported only by catalog describe or verify",
				Mode:    mode,
			}, stdout, stderr)
		}
		if *live {
			return ReportFailure(FailureReport{
				Command: "catalog list",
				Status:  StatusFlagInvalid,
				Message: "--live is supported only by catalog describe",
				Mode:    mode,
			}, stdout, stderr)
		}
		err := writeCatalogDataTypes(googlehealth.CatalogDataTypes(), mode, stdout)
		if err != nil {
			return ReportFailure(FailureReport{
				Command: "catalog list",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 0
	}
	if action == "scopes" {
		if *discoveryPath != "" {
			return ReportFailure(FailureReport{
				Command: "catalog scopes",
				Status:  StatusFlagInvalid,
				Message: "--discovery is supported only by catalog describe or verify",
				Mode:    mode,
			}, stdout, stderr)
		}
		if *live {
			return ReportFailure(FailureReport{
				Command: "catalog scopes",
				Status:  StatusFlagInvalid,
				Message: "--live is supported only by catalog describe",
				Mode:    mode,
			}, stdout, stderr)
		}
		err := writeCatalogScopes(googlehealth.CatalogScopes(), mode, stdout)
		if err != nil {
			return ReportFailure(FailureReport{
				Command: "catalog scopes",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 0
	}
	if action == "describe" {
		return runCatalogDescribe(positionals, *discoveryPath, *live, mode, stdout, stderr, runtime.withDefaults())
	}
	if *live {
		return ReportFailure(FailureReport{
			Command: "catalog verify",
			Status:  StatusFlagInvalid,
			Message: "--live is supported only by catalog describe; catalog verify uses live discovery by default",
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

func runCatalogDescribe(args []string, discoveryPath string, live bool, mode outputMode, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	if len(args) != 1 {
		return ReportFailure(FailureReport{
			Command: "catalog describe",
			Status:  StatusFlagInvalid,
			Message: "catalog describe requires exactly one Data Type",
			Mode:    mode,
		}, stdout, stderr)
	}
	if discoveryPath != "" && live {
		return ReportFailure(FailureReport{
			Command: "catalog describe",
			Status:  StatusFlagInvalid,
			Message: "catalog describe accepts only one discovery source: --discovery or --live",
			Mode:    mode,
		}, stdout, stderr)
	}
	description, err := googlehealth.CatalogDataTypeDescription(args[0])
	if err != nil {
		return ReportFailure(FailureReport{
			Command: "catalog describe",
			Status:  StatusFlagInvalid,
			Message: fmt.Sprintf("catalog describe Data Type %q is not in the compiled catalog", args[0]),
			Mode:    mode,
		}, stdout, stderr)
	}
	payload, source, err := loadCatalogDescriptionDiscovery(discoveryPath, live, runtime)
	if err != nil {
		return reportCatalogDescribeDiscoveryFailure(err, args[0], mode, stdout, stderr)
	}
	description, err = googlehealth.EnrichCatalogDataTypeDescription(description, payload, source)
	if err != nil {
		return reportCatalogDescribeDiscoveryFailure(err, args[0], mode, stdout, stderr)
	}
	if err := writeCatalogDescription(description, mode, stdout); err != nil {
		return ReportFailure(FailureReport{
			Command: "catalog describe",
			Status:  StatusArchiveUnwritable,
			Message: fmt.Sprintf("write output: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

func loadCatalogDescriptionDiscovery(path string, live bool, runtime runtimeAdapters) ([]byte, string, error) {
	if path != "" {
		payload, err := readLimitedFile(path, catalogDiscoveryMaxBytes)
		return payload, "file", err
	}
	if live {
		return loadCatalogDiscovery("", runtime)
	}
	return googlehealth.CatalogDiscoverySnapshot(), "committed_snapshot", nil
}

func reportCatalogDescribeDiscoveryFailure(err error, dataType string, mode outputMode, stdout, stderr io.Writer) int {
	message := "discovery document is unavailable"
	switch {
	case errors.Is(err, errCatalogDiscoveryTooLarge):
		message = errCatalogDiscoveryTooLarge.Error()
	case errors.Is(err, googlehealth.ErrCatalogDiscoveryMalformed):
		message = googlehealth.ErrCatalogDiscoveryMalformed.Error()
	case errors.Is(err, googlehealth.ErrCatalogDiscoveryIncompatible):
		message = fmt.Sprintf("discovery document is incompatible with Data Type %q", dataType)
	}
	return ReportFailure(FailureReport{
		Command: "catalog describe",
		Status:  StatusOperationFailed,
		Message: message,
		Mode:    mode,
	}, stdout, stderr)
}

func writeCatalogDescription(description googlehealth.CatalogDescription, mode outputMode, stdout io.Writer) error {
	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(description)
	}

	writer := newStickyWriter(stdout)
	if mode.plain {
		writer.Printf("data_type: %s\n", description.DataType)
		writer.Printf("compiled.source: %s\n", description.Compiled.Source)
		writer.Printf("compiled.record_kind: %s\n", description.Compiled.RecordKind)
		writer.Printf("compiled.required_scopes: %s\n", strings.Join(description.Compiled.RequiredScopes, ","))
		writer.Printf("compiled.rollup_modes: %s\n", strings.Join(description.Compiled.RollupModes, ","))
		for index, endpoint := range description.Compiled.EndpointFamilies {
			writer.Printf("compiled.endpoint_family.%d.name: %s\n", index, endpoint.Name)
			writer.Printf("compiled.endpoint_family.%d.filter_field: %s\n", index, endpoint.FilterField)
			writer.Printf("compiled.endpoint_family.%d.lower_bound_only: %t\n", index, endpoint.LowerBoundOnly)
			writer.Printf("compiled.endpoint_family.%d.range_shape: %s\n", index, endpoint.RangeShape)
			writer.Printf("compiled.endpoint_family.%d.page_policy.pagination: %s\n", index, endpoint.PagePolicy.Pagination)
			writer.Printf("compiled.endpoint_family.%d.page_policy.page_size: %d\n", index, endpoint.PagePolicy.PageSize)
			writer.Printf("compiled.endpoint_family.%d.page_policy.page_size_policy: %s\n", index, endpoint.PagePolicy.PageSizePolicy)
			writer.Printf("compiled.endpoint_family.%d.page_policy.range_window_max_days: %d\n", index, endpoint.PagePolicy.RangeWindowMaxDays)
		}
		writeCatalogDiscoveryDescriptionPlain(writer, description.Discovery)
		return writer.Err()
	}

	writer.Printf("Google Health Data Type: %s\n", description.DataType)
	writer.Printf("Compiled catalog (%s)\n", description.Compiled.Source)
	writer.Printf("- Record kind: %s\n", description.Compiled.RecordKind)
	writer.Printf("- Required scopes: %s\n", strings.Join(description.Compiled.RequiredScopes, ", "))
	if len(description.Compiled.RollupModes) == 0 {
		writer.Printf("- Rollup modes: none\n")
	} else {
		writer.Printf("- Rollup modes: %s\n", strings.Join(description.Compiled.RollupModes, ", "))
	}
	writer.Printf("- Endpoint families:\n")
	for _, endpoint := range description.Compiled.EndpointFamilies {
		filter := endpoint.FilterField
		if filter == "" {
			filter = "none"
		}
		writer.Printf("  - %s: filter %s; lower-bound-only %t; range %s; pagination %s; page size %s", endpoint.Name, filter, endpoint.LowerBoundOnly, endpoint.RangeShape, endpoint.PagePolicy.Pagination, catalogPageSizeLabel(endpoint.PagePolicy))
		if endpoint.PagePolicy.RangeWindowMaxDays > 0 {
			writer.Printf("; max range %d days", endpoint.PagePolicy.RangeWindowMaxDays)
		}
		writer.Printf("\n")
	}
	if description.Discovery != nil {
		writer.Printf("Discovery schema (%s, revision %s)\n", escapePlainControlChars(description.Discovery.Source), escapePlainControlChars(description.Discovery.Revision))
		writer.Printf("- JSON field: %s\n", escapePlainControlChars(description.Discovery.JSONField))
		writer.Printf("- Schema: %s\n", escapePlainControlChars(description.Discovery.SchemaRef))
		writer.Printf("- Fields:\n")
		for _, field := range description.Discovery.Fields {
			if field.SchemaRef == "" {
				writer.Printf("  - %s: %s\n", escapePlainControlChars(field.Name), escapePlainControlChars(field.JSONType))
			} else {
				writer.Printf("  - %s: %s (%s)\n", escapePlainControlChars(field.Name), escapePlainControlChars(field.JSONType), escapePlainControlChars(field.SchemaRef))
			}
		}
	}
	return writer.Err()
}

func writeCatalogDiscoveryDescriptionPlain(writer *stickyWriter, discovery *googlehealth.CatalogDiscoveryDescription) {
	if discovery == nil {
		return
	}
	writer.Printf("discovery.source: %s\n", escapePlainControlChars(discovery.Source))
	writer.Printf("discovery.revision: %s\n", escapePlainControlChars(discovery.Revision))
	writer.Printf("discovery.json_field: %s\n", escapePlainControlChars(discovery.JSONField))
	writer.Printf("discovery.schema_ref: %s\n", escapePlainControlChars(discovery.SchemaRef))
	for index, field := range discovery.Fields {
		writer.Printf("discovery.field.%d.name: %s\n", index, escapePlainControlChars(field.Name))
		writer.Printf("discovery.field.%d.json_type: %s\n", index, escapePlainControlChars(field.JSONType))
		writer.Printf("discovery.field.%d.schema_ref: %s\n", index, escapePlainControlChars(field.SchemaRef))
	}
}

func catalogPageSizeLabel(policy googlehealth.CatalogPagePolicy) string {
	if policy.PageSizePolicy == "provider_default" {
		return policy.PageSizePolicy
	}
	return fmt.Sprintf("%d (%s)", policy.PageSize, policy.PageSizePolicy)
}

func writeCatalogScopes(scopes []googlehealth.CatalogScope, mode outputMode, stdout io.Writer) error {
	if mode.json {
		payload, err := json.Marshal(struct {
			Scopes []googlehealth.CatalogScope `json:"scopes"`
		}{Scopes: scopes})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", payload)
		return err
	}

	writer := newStickyWriter(stdout)
	if mode.plain {
		for index, scope := range scopes {
			writer.Printf("scope.%d.scope: %s\n", index, scope.Scope)
			writer.Printf("scope.%d.data_types: %s\n", index, strings.Join(scope.DataTypes, ","))
		}
		return writer.Err()
	}

	writer.Printf("Google Health OAuth Scopes (%d)\n", len(scopes))
	for _, scope := range scopes {
		writer.Printf("- %s\n", scope.Scope)
		writer.Printf("  Data Types: %s\n", strings.Join(scope.DataTypes, ", "))
	}
	return writer.Err()
}

func writeCatalogDataTypes(dataTypes []googlehealth.CatalogDataType, mode outputMode, stdout io.Writer) error {
	if mode.json {
		payload, err := json.Marshal(struct {
			DataTypes []googlehealth.CatalogDataType `json:"data_types"`
		}{DataTypes: dataTypes})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", payload)
		return err
	}

	writer := newStickyWriter(stdout)
	if mode.plain {
		for index, dataType := range dataTypes {
			writer.Printf("data_type.%d.data_type: %s\n", index, dataType.DataType)
			writer.Printf("data_type.%d.selection: %s\n", index, dataType.Selection)
			writer.Printf("data_type.%d.raw_data_points: %s\n", index, dataType.RawDataPoints)
			writer.Printf("data_type.%d.required_scopes: %s\n", index, strings.Join(dataType.RequiredScopes, ","))
		}
		return writer.Err()
	}

	writer.Printf("Google Health Data Types (%d)\n", len(dataTypes))
	for _, dataType := range dataTypes {
		selection := strings.ReplaceAll(dataType.Selection, "_", "-")
		scopeLabel := "scopes"
		if len(dataType.RequiredScopes) == 1 {
			scopeLabel = "scope"
		}
		writer.Printf("- %s: %s; raw Data Points %s; %s %s\n", dataType.DataType, selection, dataType.RawDataPoints, scopeLabel, strings.Join(dataType.RequiredScopes, ", "))
	}
	return writer.Err()
}

// catalogFlagArgs removes one fixed action and moves positional arguments after
// flags, allowing flags on either side of describe's Data Type. An action word
// consumed as --discovery's value remains a path.
func catalogFlagArgs(args []string) ([]string, string) {
	flagArgs := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	action := ""
	discoveryValueNext := false
	for _, arg := range args {
		if discoveryValueNext {
			flagArgs = append(flagArgs, arg)
			discoveryValueNext = false
			continue
		}
		if arg == "--discovery" || arg == "-discovery" {
			flagArgs = append(flagArgs, arg)
			discoveryValueNext = true
			continue
		}
		if action == "" && (arg == "list" || arg == "scopes" || arg == "verify" || arg == "describe") {
			action = arg
			continue
		}
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
		} else {
			positionals = append(positionals, arg)
		}
	}
	return append(flagArgs, positionals...), action
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
		return nil, errCatalogDiscoveryTooLarge
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

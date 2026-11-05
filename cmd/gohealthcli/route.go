package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

var routeCommonFlagUsageOverrides = map[string]string{
	"json":     "report failures as one JSON envelope on stdout; route bytes follow --format",
	"plain":    "report failures as status/message lines on stdout; route bytes follow --format",
	"no-input": "accepted for uniformity; route does no prompting",
}

const (
	routeOutputUsage = "write the route to a new owner-only file (never overwrites)"
	routeStdoutUsage = "write the route bytes to stdout"
)

type routeFormat struct {
	name   string
	render func(archivedExerciseRoute) ([]byte, error)
}

var routeFormats = []routeFormat{
	{name: "tcx", render: func(route archivedExerciseRoute) ([]byte, error) { return route.tcx, nil }},
}

func routeFormatNames() []string {
	names := make([]string, 0, len(routeFormats))
	for _, format := range routeFormats {
		names = append(names, format.name)
	}
	return names
}

func routeFormatUsage() string {
	return "route format: " + strings.Join(routeFormatNames(), ", ") + " (exact archived bytes)"
}

func findRouteFormat(name string) (routeFormat, bool) {
	for _, format := range routeFormats {
		if format.name == name {
			return format, true
		}
	}
	return routeFormat{}, false
}

func runRoute(args []string, globals CommonFlagValues, stdout, stderr io.Writer, runtime runtimeAdapters) int {
	flags := flag.NewFlagSet("route", flag.ContinueOnError)
	flags.SetOutput(stderr)
	commonSpec := AllCommonFlagsSpec()
	commonSpec.UsageOverrides = routeCommonFlagUsageOverrides
	common := RegisterCommon(flags, commonSpec, CommonFlagValues{
		ConfigPath:          globals.ConfigPath,
		ArchivePath:         globals.ArchivePath,
		JSONOutput:          globals.JSONOutput,
		PlainOutput:         globals.PlainOutput,
		ArchivePathExplicit: globals.ArchivePathExplicit,
		ConfigPathExplicit:  globals.ConfigPathExplicit,
	})
	formatName := flags.String("format", "tcx", routeFormatUsage())
	outputPath := flags.String("output", "", routeOutputUsage)
	toStdout := flags.Bool("stdout", false, routeStdoutUsage)

	reordered, positionals := partitionRawFlagArgs(flags, args)
	if err := ParseCommon(flags, common, reordered, runtime.observeSubcommandFlagSet); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	mode := commonOutputMode(*common)
	fail := func(status FailureStatus, err error) int {
		return ReportFailure(FailureReport{Command: "route", Status: status, Message: err.Error(), Mode: mode, Cause: err}, stdout, stderr)
	}
	if len(positionals) != 1 {
		return fail(StatusFlagInvalid, errors.New("route requires exactly one exercise identifier"))
	}
	identifier, err := parseRouteIdentifier(positionals[0])
	if err != nil {
		return fail(StatusFlagInvalid, err)
	}
	format, ok := findRouteFormat(*formatName)
	if !ok {
		return fail(StatusFlagInvalid, fmt.Errorf("route format %q is not supported; supported: %s", *formatName, strings.Join(routeFormatNames(), ", ")))
	}
	outputRequested := flagWasProvided(flags, "output")
	switch {
	case outputRequested && *toStdout:
		return fail(StatusFlagInvalid, errors.New("route accepts only one destination: --output or --stdout"))
	case outputRequested && *outputPath == "":
		return fail(StatusFlagInvalid, errors.New("--output requires a non-empty path"))
	case !outputRequested && !*toStdout:
		return fail(StatusFlagInvalid, errors.New("route requires --output PATH or --stdout"))
	}

	archivePath, err := resolveReadArchivePath(*common)
	if err != nil {
		return fail(StatusOperationFailed, err)
	}
	route, err := readArchivedExerciseRoute(context.Background(), archivePath, identifier)
	if err != nil {
		return reportRouteReadFailure(err, fail)
	}
	payload, err := format.render(route)
	if err != nil {
		return fail(StatusOperationFailed, err)
	}

	if !outputRequested {
		if _, err := newStickyWriter(stdout).Write(payload); err != nil {
			return reportWriteFailure("route", err, mode, stdout, stderr)
		}
		return 0
	}
	output, err := runtime.prepareRawOutput(*outputPath)
	if err != nil {
		var validationError *rawOutputValidationError
		if errors.As(err, &validationError) {
			return fail(StatusFlagInvalid, err)
		}
		return fail(StatusOperationFailed, err)
	}
	byteCount, err := output.Complete(payload)
	if err != nil {
		return fail(StatusOperationFailed, err)
	}
	if _, err := fmt.Fprintf(newStickyWriter(stderr), "route: wrote %d bytes to %q\n", byteCount, *outputPath); err != nil {
		return reportWriteFailure("route", err, mode, stdout, stderr)
	}
	return 0
}

func reportRouteReadFailure(err error, fail func(FailureStatus, error) int) int {
	var identifierErr *routeIdentifierError
	var notArchived *routeNotArchivedError
	var integrityErr *attachmentIntegrityError
	switch {
	case errors.As(err, &identifierErr):
		return fail(StatusFlagInvalid, err)
	case errors.As(err, &notArchived):
		return fail(StatusOperationFailed, withRemediation(err, remediationAction("gohealthcli connect --add-scopes tcx"), remediationSyncExerciseFrom))
	case errors.As(err, &integrityErr):
		return fail(StatusOperationFailed, withRemediation(err, remediationRunDoctor))
	}
	return fail(StatusOperationFailed, archiveFailureRemediation(err))
}

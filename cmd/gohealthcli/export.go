package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

type exportDatasetSpec struct {
	name             string
	view             string
	viewSQL          string
	migrationVersion int
	fields           []exportFieldSpec
	orderBy          string
}

type exportFieldSpec struct {
	name string
	kind string
}

type exportRow []string

// exportDatasetDefinitions is the canonical Normalized View registry.
// It lives next to the export writer for historical reasons (this
// package shipped exports before the Registry concept existed); the
// follow-up PR for #109 (describe-schema --json) splits these into
// per-category files (views_steps.go, views_sleep.go, views_identity.go,
// …) and the Registry becomes the only entry point. Until then, treat
// this slice and `normalizedViewsRegistry()` as the same thing — every
// consumer should go through the Registry, never read the slice
// directly.
var exportDatasetDefinitions = []exportDatasetSpec{
	dailyStepsViewSpec,
	heartRateSamplesViewSpec,
	restingHeartRateByDayViewSpec,
	sleepSessionsViewSpec,
	exerciseSessionsViewSpec,
	weightSamplesViewSpec,
	activeMinutesIntervalsViewSpec,
	activeZoneMinutesIntervalsViewSpec,
	altitudeIntervalsViewSpec,
	activityLevelIntervalsViewSpec,
	sedentaryPeriodIntervalsViewSpec,
	timeInHeartRateZoneIntervalsViewSpec,
	swimLengthsDataIntervalsViewSpec,
	vo2MaxSamplesViewSpec,
	runVo2MaxSamplesViewSpec,
	dailyVo2MaxViewSpec,
	dailyHeartRateZonesViewSpec,
	dailySleepTemperatureDerivationsViewSpec,
	respiratoryRateSleepSummaryViewSpec,
	floorsIntervalsViewSpec,
	electrocardiogramSessionsViewSpec,
	irregularRhythmNotificationsViewSpec,
	hydrationLogSessionsViewSpec,
	searchableTextViewSpec,
	sleepStagesViewSpec,
	exerciseSplitsViewSpec,
	currentIrnProfileViewSpec,
	pairedDevicesViewSpec,
	currentSettingsViewSpec,
	bodyFatSamplesViewSpec,
	bloodGlucoseSamplesViewSpec,
	coreBodyTemperatureSamplesViewSpec,
	heightSamplesViewSpec,
	currentHeightViewSpec,
}

var exportDatasetSpecs = exportDatasetSpecByName(exportDatasetDefinitions)

func exportDatasetSpecByName(definitions []exportDatasetSpec) map[string]exportDatasetSpec {
	specs := make(map[string]exportDatasetSpec, len(definitions))
	for _, definition := range definitions {
		if definition.name == "" {
			panic("export dataset definition missing name")
		}
		if _, exists := specs[definition.name]; exists {
			panic(fmt.Sprintf("duplicate export dataset definition: %s", definition.name))
		}
		specs[definition.name] = definition
	}
	return specs
}

func exportDatasetViewMigrationStatement(spec exportDatasetSpec) string {
	return fmt.Sprintf("CREATE VIEW %s AS\n%s", spec.view, strings.TrimSpace(spec.viewSQL))
}

// exportDatasetCatalog is the small discovery adapter over the
// exportDatasetDefinitions registry. It owns the three views the read
// surface needs but the registry itself was never shaped to provide:
//
//   - Names() — sorted, deduped list for `export --help` and the
//     README drift guard.
//   - Find(name) — case-sensitive lookup matching the registry's name
//     contract (mirrors `exportDatasetSpecs[name]`).
//   - Suggest(typo) — Levenshtein ≤ 3, top 3 by closeness then
//     alphabetical, for the `export <typo>` did-you-mean line.
//
// PRD #144 slice 3 (issue #157) introduces this seam so consumers
// (--help printer, typo error path, future docs generators) share one
// surface instead of each re-walking the registry. ADR 0007 keeps the
// registry as the source of truth for view SQL / migrations; the
// catalog only *projects* discovery views over it.
type exportDatasetCatalog struct {
	// names is precomputed once at construction time: sorted, deduped.
	// Cached because `export --help` and the typo error path each touch
	// it on every invocation, and the registry never changes at runtime.
	names []string
	specs map[string]exportDatasetSpec
}

// exportSuggestMaxDistance is the Levenshtein cutoff for export typo
// suggestions, fixed at 3 per PRD #144 slice 3. The looser bound (vs
// the top-level command registry's 2) reflects that dataset names are
// longer (averaging 18 chars) so a 2-edit cutoff misses common typos
// like `heart-rate-sample` → `heart-rate-samples` when paired with a
// second transposition.
const exportSuggestMaxDistance = 3

// exportSuggestMax is the hard cap on returned suggestions.
const exportSuggestMax = 3

// newExportDatasetCatalog builds a catalog over the given definitions.
// Duplicate names are tolerated here (only the first wins for Find);
// the registry seam (exportDatasetSpecByName) already panics on
// duplicates, so production callers using exportDatasetDefinitions
// never trigger the dedup branch. Tests pass synthetic registries.
func newExportDatasetCatalog(definitions []exportDatasetSpec) *exportDatasetCatalog {
	specs := make(map[string]exportDatasetSpec, len(definitions))
	names := make([]string, 0, len(definitions))
	for _, def := range definitions {
		if _, exists := specs[def.name]; exists {
			continue
		}
		specs[def.name] = def
		names = append(names, def.name)
	}
	sort.Strings(names)
	return &exportDatasetCatalog{names: names, specs: specs}
}

// Names returns the sorted, deduped list of dataset names. The returned
// slice is a fresh copy so callers may safely mutate it without
// disturbing the cached state.
func (c *exportDatasetCatalog) Names() []string {
	out := make([]string, len(c.names))
	copy(out, c.names)
	return out
}

// Find returns the spec for the given name and ok=true on hit,
// (zero-value spec, false) on miss. Case-sensitive — the registry's
// dataset names are kebab-case ASCII and never mixed-case.
func (c *exportDatasetCatalog) Find(name string) (exportDatasetSpec, bool) {
	spec, ok := c.specs[name]
	return spec, ok
}

// Suggest returns at most exportSuggestMax dataset names whose
// Levenshtein distance from `typo` is ≤ exportSuggestMaxDistance,
// ordered by (distance asc, name asc). An empty slice (not nil)
// indicates no close match; the typo error path falls back to the
// `export --help` pointer in that case.
//
// The algorithm is dependency-free; we reuse the levenshteinDistance
// helper that already lives in commands.go for the top-level
// command-name typo path.
func (c *exportDatasetCatalog) Suggest(typo string) []string {
	type candidate struct {
		name     string
		distance int
	}
	var candidates []candidate
	for _, name := range c.names {
		d := levenshteinDistance(typo, name)
		if d <= exportSuggestMaxDistance {
			candidates = append(candidates, candidate{name: name, distance: d})
		}
	}
	// Sort by (distance asc, name asc). c.names is already alphabetical
	// so stable sort by distance preserves the alphabetical tie-break
	// for free.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].distance < candidates[j].distance
	})
	if len(candidates) > exportSuggestMax {
		candidates = candidates[:exportSuggestMax]
	}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.name)
	}
	return out
}

// exportDatasetCatalogSingleton is the production catalog over the
// canonical registry. Built once at package init so the help printer
// and typo error path do not pay the construction cost per invocation.
var exportDatasetCatalogSingleton = newExportDatasetCatalog(exportDatasetDefinitions)

// exportCommonFlagUsageOverrides is export's divergence from the
// canonical commonFlagsSpec wording, declared once: the registry entry
// (commands.go, via withCommonOverrides) and the runtime CommonFlagSpec
// in runExport both consume this map, so `export --help`, the
// `schema --json` contract, and the generated docs/commands/export.md
// page render identical strings by construction (issue #76).
var exportCommonFlagUsageOverrides = map[string]string{
	"json":     "synonym for --format jsonl",
	"plain":    "synonym for --format csv",
	"no-input": "accepted for uniformity; export does no prompting",
}

func runExport(args []string, configPath, archivePath string, configPathExplicit, archivePathExplicit bool, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(stderr)

	// export accepts the full Common Flag Set ({config, db, json, plain,
	// no-input}) so the "every subcommand accepts the same global flags"
	// invariant (PRD #143) holds. --json is a documented synonym for
	// --format jsonl; --plain is a documented synonym for --format csv.
	// The export-specific --format / --output / --stdout flags are
	// registered AFTER RegisterCommon. --no-input is accepted but unused
	// by export (it's a read-only verb against the local archive); we
	// keep it in the spec so the global-flag pre-scan does not reject it.
	// The usage strings for the export-specific shared-flag semantics
	// come from exportCommonFlagUsageOverrides — the same map the
	// registry entry renders into the published schema — so `export
	// --help` reflects the documented synonym semantics instead of the
	// generic "write stable JSON to stdout" wording, without a second
	// hand-typed copy.
	commonSpec := AllCommonFlagsSpec()
	commonSpec.UsageOverrides = exportCommonFlagUsageOverrides
	common := RegisterCommon(flags, commonSpec, CommonFlagValues{
		ConfigPath:          configPath,
		ArchivePath:         archivePath,
		ArchivePathExplicit: archivePathExplicit,
		ConfigPathExplicit:  configPathExplicit,
	})
	exportFormat := flags.String("format", "csv", "export format: csv or jsonl (synonyms: --json → jsonl, --plain → csv)")
	exportOutputPath := flags.String("output", "", "write export to path")
	exportStdout := flags.Bool("stdout", false, "write export data to stdout")

	// `export --help` is the discovery surface for the 30+ normalized
	// datasets (PRD #144 slice 3). The stdlib default Usage prints the
	// flag block only; we wrap it to append the catalog list so an LLM
	// or script that asks the binary "what can you export?" gets a
	// complete answer from one call. The catalog earns its seam here:
	// the loop is one line because Names() already sorts and dedupes.
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage of %s:\n", flags.Name())
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nSupported datasets:")
		for _, name := range exportDatasetCatalogSingleton.Names() {
			fmt.Fprintf(flags.Output(), "  %s\n", name)
		}
	}

	positionals, parseArgs, err := splitExportArgs(args)
	if err != nil {
		// splitExportArgs runs BEFORE ParseCommon, so common.JSONOutput /
		// common.PlainOutput are not yet populated from inner flags. The
		// only failure shape splitExportArgs surfaces is "flag needs an
		// argument: --foo", which is a flag-shape error that defaults to
		// the bare `<cmd>: <msg>` line on stderr. The multi-positional
		// "export requires exactly one dataset" rejection used to fire
		// here too, but it was deferred to AFTER ParseCommon below so its
		// ReportFailure can honour --json / --plain. Every other
		// ReportFailure in runExport runs AFTER ParseCommon and carries
		// Mode so the unified --json / --plain failure contract is
		// honoured.
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: err.Error()}, stdout, stderr)
	}

	if err := ParseCommon(flags, common, parseArgs); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	// mode is the unified output-mode the failure_reporter contract pivots
	// on. Until slice 9's export migration is patched, every ReportFailure
	// here dropped Mode and silently fell back to default-mode output —
	// `--json` invocations never saw their JSON envelope. Threading mode
	// once and reusing it on every call site below restores the contract
	// without dragging an `outputMode` parameter through runExport's
	// signature (the runtime adapter still owns the registry-driven shape).
	mode := outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if len(positionals) == 0 || flags.NArg() != 0 {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: "export requires exactly one dataset", Mode: mode}, stdout, stderr)
	}
	if len(positionals) > 1 {
		// Multi-positional rejection deferred from splitExportArgs so the
		// failure surface honours --json / --plain like every other
		// post-ParseCommon ReportFailure below.
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: "export requires exactly one dataset", Mode: mode}, stdout, stderr)
	}
	dataset := positionals[0]
	spec, ok := exportDatasetCatalogSingleton.Find(dataset)
	if !ok {
		exit := ReportFailure(FailureReport{
			Command: "export",
			Status:  StatusFlagInvalid,
			Message: fmt.Sprintf("export dataset %q is not supported", dataset),
			Mode:    mode,
		}, stdout, stderr)
		// In --json mode the caller wants a single-line envelope on
		// stdout and nothing on stderr; appending hints would corrupt
		// that shape (the same constraint runUnknownCommand honours).
		// In default/--plain mode, surface the did-you-mean line plus
		// the `export --help` pointer so the human (or scripted LLM
		// retry) can recover without grepping source. The pointer is
		// emitted unconditionally because Suggest() can return an
		// empty slice for gibberish input — the help pointer is the
		// invariant fallback.
		if !mode.json {
			if suggestions := exportDatasetCatalogSingleton.Suggest(dataset); len(suggestions) > 0 {
				fmt.Fprintf(stderr, "Did you mean: %s?\n", strings.Join(suggestions, ", "))
			}
			fmt.Fprintln(stderr, "Run 'gohealthcli export --help' for the full list of supported datasets.")
		}
		return exit
	}
	// Resolve --json / --plain into --format. Mutual exclusion between
	// --plain and --json already fired in ParseCommon above (the
	// CommonFlagSet seam owns that invariant); the conflict between a
	// Common Flag synonym and an explicit --format value is
	// export-specific, so the validator lives here, not in common_flags.go.
	formatExplicit := flagWasProvided(flags, "format")
	resolvedFormat, err := resolveExportFormat(*exportFormat, formatExplicit, common.JSONOutput, common.PlainOutput)
	if err != nil {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	if *exportOutputPath == "" && !*exportStdout {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: "export requires --output PATH or --stdout", Mode: mode}, stdout, stderr)
	}
	if *exportOutputPath != "" && *exportStdout {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: "export accepts only one destination: --output or --stdout", Mode: mode}, stdout, stderr)
	}
	if err := validateExportFormat(resolvedFormat); err != nil {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: err.Error(), Mode: mode}, stdout, stderr)
	}

	resolvedArchivePath, err := resolveReadArchivePath(*common)
	if err != nil {
		return ReportFailure(FailureReport{Command: "export", Status: StatusOperationFailed, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	rows, err := exportRows(resolvedArchivePath, spec)
	if err != nil {
		return ReportFailure(FailureReport{Command: "export", Status: StatusOperationFailed, Message: err.Error(), Mode: mode}, stdout, stderr)
	}

	if *exportStdout {
		if err := writeExport(rows, spec, resolvedFormat, stdout); err != nil {
			return ReportFailure(FailureReport{
				Command: "export",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 0
	}
	if err := writeExportFile(rows, spec, resolvedFormat, *exportOutputPath); err != nil {
		status := StatusArchiveUnwritable
		if errors.Is(err, errExportOutputSymlink) {
			status = StatusFlagInvalid
		}
		return ReportFailure(FailureReport{
			Command: "export",
			Status:  status,
			Message: fmt.Sprintf("write export: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

// resolveExportFormat maps the Common Flag Set synonyms (--json, --plain)
// onto the export-specific --format value. It enforces the conflict
// invariant export.go owns (CommonFlagSet owns --plain --json mutual
// exclusion at the seam above):
//
//   - if --json is set with an explicit --format whose value is not "jsonl",
//     return a "--json conflicts with --format <value>" error;
//   - if --plain is set with an explicit --format whose value is not "csv",
//     return a "--plain conflicts with --format <value>" error;
//   - otherwise, --json overrides the default to "jsonl" and --plain
//     overrides the default to "csv" (when --format was NOT explicit).
//
// formatExplicit comes from flagWasProvided so a user passing the synonym
// alongside `--format jsonl` (redundant but not contradictory) does NOT
// error — only contradictory pairings do.
func resolveExportFormat(format string, formatExplicit, jsonSynonym, plainSynonym bool) (string, error) {
	if jsonSynonym && formatExplicit && format != "jsonl" {
		return "", fmt.Errorf("--json conflicts with --format %s", format)
	}
	if plainSynonym && formatExplicit && format != "csv" {
		return "", fmt.Errorf("--plain conflicts with --format %s", format)
	}
	if jsonSynonym && !formatExplicit {
		return "jsonl", nil
	}
	if plainSynonym && !formatExplicit {
		return "csv", nil
	}
	return format, nil
}

// splitExportArgs separates flag tokens from positional dataset args so
// the inner FlagSet can parse the flag block while runExport keeps the
// positional list intact for the post-ParseCommon dataset-count check.
//
// Returns the positional list (length 0 = missing dataset, length 1 =
// the canonical case, length >= 2 = duplicate-dataset error surfaced
// AFTER ParseCommon so its ReportFailure can honour --json / --plain),
// the flag-token slice ready for ParseCommon, and an error reserved for
// the rare "flag needs an argument: --foo" shape that only surfaces
// when an operator passes a non-bool flag without its value. Multi-
// positional rejection is intentionally NOT raised here: deferring it
// to the post-parse path is what lets the failure_reporter contract
// thread Mode through so `export --json a b` emits the JSON envelope
// instead of falling back to default mode.
func splitExportArgs(args []string) ([]string, []string, error) {
	var positionals []string
	var flagArgs []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if exportFlagNeedsValue(arg) && !strings.Contains(arg, "=") {
				index++
				if index >= len(args) {
					return nil, nil, fmt.Errorf("flag needs an argument: %s", arg)
				}
				flagArgs = append(flagArgs, args[index])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return positionals, flagArgs, nil
}

func exportFlagNeedsValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	switch name {
	case "config", "db", "format", "output":
		return true
	default:
		return false
	}
}

func validateExportFormat(format string) error {
	switch format {
	case "csv", "jsonl":
		return nil
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
}

func writeExportFile(rows []exportRow, spec exportDatasetSpec, format, path string) error {
	if usesPOSIXPermissions() {
		if err := restrictExistingExportOutput(path); err != nil {
			return err
		}
	}
	// exportOpenNoFollow (O_NOFOLLOW on POSIX) makes this open fail rather than
	// follow a symlink at the final path component, closing the TOCTOU window
	// between restrictExistingExportOutput's Lstat check and this open.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|exportOpenNoFollow, 0o600)
	if err != nil {
		return err
	}
	writeErr := writeExport(rows, spec, format, file)
	if writeErr == nil && usesPOSIXPermissions() {
		// Tighten via the open descriptor (fchmod), not a path-based chmod, so
		// the permission change cannot be redirected through a raced symlink.
		writeErr = file.Chmod(0o600)
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// errExportOutputSymlink reports that --output names a symbolic link. The
// export writer refuses such paths so it never chmods or truncates the link
// target; the caller surfaces this as a flag-invalid failure.
var errExportOutputSymlink = errors.New("symbolic link")

// restrictExistingExportOutput validates the --output path before the export
// writer opens it: it refuses a symbolic link (so the link target is never
// chmod'd or truncated) and a directory. Permission tightening of the written
// file happens fd-based in writeExportFile (fchmod), not here, so there is no
// path-based chmod that a raced symlink could redirect.
func restrictExistingExportOutput(path string) error {
	// os.Lstat does not follow symlinks, so it sees the link itself. Check it
	// BEFORE os.Stat (which follows symlinks) so a symlinked --output is
	// refused rather than chmod'd or truncated through the link target. This
	// gives a friendly error in the common case; the O_NOFOLLOW open in
	// writeExportFile is the race-proof backstop.
	linkInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: --output %q names a symbolic link; pass the resolved target path explicitly", errExportOutputSymlink, path)
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory", path)
	}
	return nil
}

func writeExport(rows []exportRow, spec exportDatasetSpec, format string, writer io.Writer) error {
	switch format {
	case "csv":
		return writeExportCSV(rows, spec, writer)
	case "jsonl":
		return writeExportJSONL(rows, spec, writer)
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
}

func writeExportCSV(rows []exportRow, spec exportDatasetSpec, writer io.Writer) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(exportFieldNames(spec)); err != nil {
		return err
	}
	for _, row := range rows {
		if err := csvWriter.Write([]string(row)); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func writeExportJSONL(rows []exportRow, spec exportDatasetSpec, writer io.Writer) error {
	for _, row := range rows {
		if _, err := fmt.Fprint(writer, "{"); err != nil {
			return err
		}
		for index, field := range spec.fields {
			if index > 0 {
				if _, err := fmt.Fprint(writer, ","); err != nil {
					return err
				}
			}
			name, err := json.Marshal(field.name)
			if err != nil {
				return err
			}
			if _, err := writer.Write(name); err != nil {
				return err
			}
			if _, err := fmt.Fprint(writer, ":"); err != nil {
				return err
			}
			if field.kind == "int" && row[index] != "" {
				value, err := strconv.ParseInt(row[index], 10, 64)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprint(writer, strconv.FormatInt(value, 10)); err != nil {
					return err
				}
				continue
			}
			value, err := json.Marshal(row[index])
			if err != nil {
				return err
			}
			if _, err := writer.Write(value); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(writer, "}"); err != nil {
			return err
		}
	}
	return nil
}

func exportFieldNames(spec exportDatasetSpec) []string {
	fields := make([]string, 0, len(spec.fields))
	for _, field := range spec.fields {
		fields = append(fields, field.name)
	}
	return fields
}

func exportRows(archivePath string, spec exportDatasetSpec) ([]exportRow, error) {
	reader, err := openHealthArchiveReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader.ExportRows(spec)
}

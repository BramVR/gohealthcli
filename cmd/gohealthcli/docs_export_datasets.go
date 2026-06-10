package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// PRD #144 slice 4 (issue #165): the README's "Normalized export
// datasets" section must be a pure projection of
// exportDatasetCatalogSingleton.Names() so the prose and the binary
// cannot drift. This file owns three small seams the make target and
// the drift test both call:
//
//   - renderExportDatasetsBlock(names) — the markdown shape (sorted,
//     one bullet per name, deterministic newlines).
//   - spliceREADMEExportDatasets(content, block) — the byte-level
//     rewrite between the stable markers; errors loudly when markers
//     are absent so we never overwrite the wrong region.
//   - extractREADMEExportDatasetsBlock(content) — the read side, used
//     by the drift test to compare committed bytes to a fresh render.
//
// runDocsExportDatasets is the hidden subcommand wiring; the registry
// entry lives in commands.go alongside the user-facing surface. The
// command is hidden because it is a build-time tool (mirroring the
// `schema` entry's Hidden: true treatment).

// exportDatasetsStartMarker / exportDatasetsEndMarker are the stable
// HTML-comment markers that bracket the auto-generated bullet list in
// README.md. They survive markdown rendering (comments are ignored)
// and are unique enough that a grep collision is impossible.
const (
	exportDatasetsStartMarker = "<!-- export-datasets:start -->"
	exportDatasetsEndMarker   = "<!-- export-datasets:end -->"
)

// renderExportDatasetsBlock returns the canonical markdown bullet list
// for the README's "Normalized export datasets" block: one bullet per
// dataset name, alphabetical, code-spanned. Always terminates with a
// trailing newline so the splice produces a stable byte sequence
// regardless of whether the caller passes the catalog's pre-sorted
// names or an unsorted slice from a test.
func renderExportDatasetsBlock(names []string) string {
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	var b strings.Builder
	for _, name := range sorted {
		b.WriteString("- `")
		b.WriteString(name)
		b.WriteString("`\n")
	}
	return b.String()
}

// locateExportDatasetsRegion returns the byte indices that bound the
// auto-generated bullet block in `content`: `bodyStart` is the first
// byte after the start marker (and its trailing newline, if any) and
// `endIdx` is the first byte of the end marker. The pair satisfies
// `content[bodyStart:endIdx]` == current bullet block and
// `content[:bodyStart] + block + content[endIdx:]` == rewritten file.
//
// Centralising the boundary calculation here keeps the splice and
// extract paths byte-identical — a future tweak (e.g. CRLF handling)
// lands in one place rather than diverging across two callers.
func locateExportDatasetsRegion(content string) (bodyStart, endIdx int, err error) {
	startIdx := strings.Index(content, exportDatasetsStartMarker)
	if startIdx < 0 {
		return 0, 0, fmt.Errorf("README missing %q marker", exportDatasetsStartMarker)
	}
	endIdx = strings.Index(content, exportDatasetsEndMarker)
	if endIdx < 0 {
		return 0, 0, fmt.Errorf("README missing %q marker", exportDatasetsEndMarker)
	}
	if endIdx < startIdx {
		return 0, 0, errors.New("README markers are in the wrong order: end before start")
	}
	// Splice between the newline after the start marker and the line
	// containing the end marker. That keeps both marker lines intact
	// (so the markers are NOT consumed by repeated splices) while
	// replacing only the bullet block in between.
	bodyStart = startIdx + len(exportDatasetsStartMarker)
	if bodyStart < len(content) && content[bodyStart] == '\n' {
		bodyStart++
	}
	return bodyStart, endIdx, nil
}

// spliceREADMEExportDatasets returns the input README content with the
// region between exportDatasetsStartMarker and exportDatasetsEndMarker
// replaced by the rendered block. The markers themselves and every
// byte outside the region are preserved verbatim. Missing markers
// surface as a loud error rather than a silent overwrite — the make
// target should fail visibly if the README is missing the seam.
//
// The splicer assumes each marker appears on its own line; the
// rendered block is inserted on the lines between them, so the marker
// lines act as both "start of region" and "first/last preserved line".
func spliceREADMEExportDatasets(content, block string) (string, error) {
	bodyStart, endIdx, err := locateExportDatasetsRegion(content)
	if err != nil {
		return "", err
	}
	return content[:bodyStart] + block + content[endIdx:], nil
}

// extractREADMEExportDatasetsBlock returns the bytes currently sitting
// between the two markers in the input content. Used by the drift
// guard test to compare committed bytes to a fresh render.
func extractREADMEExportDatasetsBlock(content string) (string, error) {
	bodyStart, endIdx, err := locateExportDatasetsRegion(content)
	if err != nil {
		return "", err
	}
	return content[bodyStart:endIdx], nil
}

// parseExportDatasetBlockNames pulls the inline-code names out of a
// rendered block. Used by the drift guard to surface a useful "missing
// X, extra Y" diff when the byte-equality check is too coarse to
// pinpoint the drift. The bullet shape is strict — `- ` + backtick +
// name + backtick + end-of-line, with no trailing characters — so a
// hand-edit that breaks the contract (a name without backticks, a
// stray trailing token, a missing closing backtick) is rejected loudly via the
// returned non-nil error rather than silently producing a misleading
// name list. Empty lines are tolerated so the helper composes with a
// trailing-newline block.
func parseExportDatasetBlockNames(block string) ([]string, error) {
	var out []string
	for lineNum, line := range strings.Split(block, "\n") {
		if line == "" {
			continue
		}
		rest, ok := strings.CutPrefix(line, "- `")
		if !ok {
			return nil, fmt.Errorf("line %d: expected `- `<name>``, got %q", lineNum+1, line)
		}
		name, ok := strings.CutSuffix(rest, "`")
		if !ok {
			return nil, fmt.Errorf("line %d: missing closing backtick: %q", lineNum+1, line)
		}
		if name == "" {
			return nil, fmt.Errorf("line %d: empty dataset name", lineNum+1)
		}
		if strings.ContainsAny(name, "` ") {
			// A stray space or extra backtick inside the inline-code
			// span is structurally invalid for the auto-generated
			// shape; the byte-equality drift guard catches the same
			// shape error, but rejecting here keeps the
			// "missing X, extra Y" diagnostic honest.
			return nil, fmt.Errorf("line %d: malformed dataset name %q", lineNum+1, name)
		}
		out = append(out, name)
	}
	return out, nil
}

// runDocsExportDatasets is the hidden subcommand the `make
// docs-export-datasets` target invokes. It reads the README at
// --readme, splices in a freshly rendered block, and writes it back.
// Errors land on stderr; success is silent (no stdout noise) so the
// target composes cleanly in shell pipelines.
func runDocsExportDatasets(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("docs-export-datasets", flag.ContinueOnError)
	flags.SetOutput(stderr)
	readmePath := flags.String("readme", "", "path to README.md to rewrite in place")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		// docs-export-datasets is a build-time rewrite verb invoked by
		// a Makefile target — silently accepting `docs-export-datasets
		// --readme README.md extra-typo` would mask a misconfigured
		// invocation (positional that should have been a flag, stray
		// shell glob). Reject loudly so the failure is visible.
		fmt.Fprintf(stderr, "docs-export-datasets: unexpected argument: %s\n", flags.Arg(0))
		return 1
	}
	if strings.TrimSpace(*readmePath) == "" {
		fmt.Fprintln(stderr, "docs-export-datasets: --readme PATH is required")
		return 1
	}
	content, err := os.ReadFile(*readmePath)
	if err != nil {
		fmt.Fprintf(stderr, "docs-export-datasets: read %s: %v\n", *readmePath, err)
		return 1
	}
	block := renderExportDatasetsBlock(exportDatasetCatalogSingleton.Names())
	rewritten, err := spliceREADMEExportDatasets(string(content), block)
	if err != nil {
		fmt.Fprintf(stderr, "docs-export-datasets: splice %s: %v\n", *readmePath, err)
		return 1
	}
	if rewritten == string(content) {
		// Idempotent no-op: nothing to write, no log noise. The CI
		// drift guard runs the same splice in-test, so a clean run is
		// the normal case.
		return 0
	}
	if err := os.WriteFile(*readmePath, []byte(rewritten), 0o644); err != nil {
		fmt.Fprintf(stderr, "docs-export-datasets: write %s: %v\n", *readmePath, err)
		return 1
	}
	return 0
}

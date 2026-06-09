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
	startIdx := strings.Index(content, exportDatasetsStartMarker)
	if startIdx < 0 {
		return "", fmt.Errorf("README missing %q marker", exportDatasetsStartMarker)
	}
	endIdx := strings.Index(content, exportDatasetsEndMarker)
	if endIdx < 0 {
		return "", fmt.Errorf("README missing %q marker", exportDatasetsEndMarker)
	}
	if endIdx < startIdx {
		return "", errors.New("README markers are in the wrong order: end before start")
	}
	// Splice between the newline after the start marker and the line
	// containing the end marker. That keeps both marker lines intact
	// (so the markers are NOT consumed by repeated splices) while
	// replacing only the bullet block in between.
	afterStart := startIdx + len(exportDatasetsStartMarker)
	if afterStart < len(content) && content[afterStart] == '\n' {
		afterStart++
	}
	return content[:afterStart] + block + content[endIdx:], nil
}

// extractREADMEExportDatasetsBlock returns the bytes currently sitting
// between the two markers in the input content. Used by the drift
// guard test to compare committed bytes to a fresh render.
func extractREADMEExportDatasetsBlock(content string) (string, error) {
	startIdx := strings.Index(content, exportDatasetsStartMarker)
	if startIdx < 0 {
		return "", fmt.Errorf("README missing %q marker", exportDatasetsStartMarker)
	}
	endIdx := strings.Index(content, exportDatasetsEndMarker)
	if endIdx < 0 {
		return "", fmt.Errorf("README missing %q marker", exportDatasetsEndMarker)
	}
	if endIdx < startIdx {
		return "", errors.New("README markers are in the wrong order: end before start")
	}
	afterStart := startIdx + len(exportDatasetsStartMarker)
	if afterStart < len(content) && content[afterStart] == '\n' {
		afterStart++
	}
	return content[afterStart:endIdx], nil
}

// parseExportDatasetBlockNames pulls the inline-code names out of a
// rendered block. Used by the drift guard to surface a useful "missing
// X, extra Y" diff when the byte-equality check is too coarse to
// pinpoint the drift. A line that does not match the `- ` + backtick
// shape is silently skipped — the byte-equality test is the
// load-bearing assertion for shape; this helper is for naming.
func parseExportDatasetBlockNames(block string) []string {
	var out []string
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimPrefix(line, "- `")
		if trimmed == line {
			continue
		}
		trimmed = strings.TrimSuffix(trimmed, "`")
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
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

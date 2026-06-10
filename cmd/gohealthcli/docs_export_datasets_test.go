package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderExportDatasetsBlockListsEveryName asserts the generator
// emits a markdown bullet for every name returned by Names(), in
// alphabetical order, exactly one per line, and nothing else. This is
// the first half of the PRD #144 slice 4 drift contract: the rendered
// block is a pure function of the catalog.
func TestRenderExportDatasetsBlockListsEveryName(t *testing.T) {
	names := []string{"alpha", "beta", "gamma"}
	got := renderExportDatasetsBlock(names)
	want := "- `alpha`\n- `beta`\n- `gamma`\n"
	if got != want {
		t.Fatalf("renderExportDatasetsBlock mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderExportDatasetsBlockSortsAlphabetically guards the
// "alphabetical order" half of the issue's acceptance criterion: even
// if a caller passes names out of order, the rendered block emits them
// sorted.
func TestRenderExportDatasetsBlockSortsAlphabetically(t *testing.T) {
	got := renderExportDatasetsBlock([]string{"gamma", "alpha", "beta"})
	want := "- `alpha`\n- `beta`\n- `gamma`\n"
	if got != want {
		t.Fatalf("renderExportDatasetsBlock did not sort:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestSpliceREADMEExportDatasetsReplacesBlock asserts the splicer
// rewrites the region between the stable markers and leaves the
// surrounding bytes (including the markers themselves) intact. This is
// the seam the make target and the drift test both call.
func TestSpliceREADMEExportDatasetsReplacesBlock(t *testing.T) {
	before := "prefix\n" +
		"<!-- export-datasets:start -->\n" +
		"- `old`\n" +
		"<!-- export-datasets:end -->\n" +
		"suffix\n"
	block := "- `alpha`\n- `beta`\n"
	got, err := spliceREADMEExportDatasets(before, block)
	if err != nil {
		t.Fatalf("splice error: %v", err)
	}
	want := "prefix\n" +
		"<!-- export-datasets:start -->\n" +
		"- `alpha`\n- `beta`\n" +
		"<!-- export-datasets:end -->\n" +
		"suffix\n"
	if got != want {
		t.Fatalf("splice mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestSpliceREADMEExportDatasetsRequiresMarkers guards the failure
// shape: a README without markers must trip a loud error so the
// generator never silently overwrites random regions.
func TestSpliceREADMEExportDatasetsRequiresMarkers(t *testing.T) {
	_, err := spliceREADMEExportDatasets("no markers here\n", "- `alpha`\n")
	if err == nil {
		t.Fatal("expected error when markers are missing")
	}
	if !strings.Contains(err.Error(), "export-datasets:start") {
		t.Fatalf("error should name the missing marker, got: %v", err)
	}
}

// TestREADMEExportDatasetsBlockMatchesCatalog is the CI drift guard
// for issue #165: the committed README's block between the export
// markers must equal the freshly-generated block from
// exportDatasetCatalogSingleton.Names(). Adding a dataset to the
// registry without running `make docs-export-datasets` fails this test.
func TestREADMEExportDatasetsBlockMatchesCatalog(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	want := renderExportDatasetsBlock(exportDatasetCatalogSingleton.Names())
	gotBlock, err := extractREADMEExportDatasetsBlock(string(content))
	if err != nil {
		t.Fatalf("extract committed block: %v", err)
	}
	if gotBlock != want {
		t.Fatalf("README export-datasets block drift; run `make docs-export-datasets`\ngot:\n%s\nwant:\n%s", gotBlock, want)
	}
}

// TestREADMEExportDatasetsBlockListsEveryRegistryName is the
// "contains every name from Names() and no extra entries" assertion
// the issue requires. It parses the committed block and compares the
// extracted name set against the catalog. Distinct from the byte-
// equality drift guard above: this one fails with a useful "missing
// X, extra Y" report when a registry entry slips out.
func TestREADMEExportDatasetsBlockListsEveryRegistryName(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	block, err := extractREADMEExportDatasetsBlock(string(content))
	if err != nil {
		t.Fatalf("extract block: %v", err)
	}
	got, err := parseExportDatasetBlockNames(block)
	if err != nil {
		t.Fatalf("parseExportDatasetBlockNames: %v", err)
	}
	want := exportDatasetCatalogSingleton.Names()
	if len(got) != len(want) {
		t.Fatalf("name count mismatch: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("position %d: got %q, want %q", i, got[i], name)
		}
	}
}

// TestParseExportDatasetBlockNamesRejectsMalformedLines pins the
// stricter shape check Copilot review surfaced: a hand-edit that
// breaks the strict bullet shape (`- ` + backticked name) surfaces a
// useful error instead of
// silently producing a misleading name list.
func TestParseExportDatasetBlockNamesRejectsMalformedLines(t *testing.T) {
	cases := []struct {
		name  string
		block string
		want  string
	}{
		{name: "missing dash", block: "alpha\n", want: "expected"},
		{name: "missing opening backtick", block: "- alpha\n", want: "expected"},
		{name: "missing closing backtick", block: "- `alpha\n", want: "missing closing"},
		{name: "trailing token", block: "- `alpha` extra\n", want: "missing closing"},
		{name: "empty name", block: "- ``\n", want: "empty"},
		{name: "name with space", block: "- `a b`\n", want: "malformed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseExportDatasetBlockNames(c.block)
			if err == nil {
				t.Fatalf("expected error for malformed block %q", c.block)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error should contain %q, got: %v", c.want, err)
			}
		})
	}
}

// TestParseExportDatasetBlockNamesTrailingNewlineOK guards the empty-
// line tolerance: a block produced by renderExportDatasetsBlock always
// ends with a newline, so an empty line at the tail must not trip the
// strict shape check.
func TestParseExportDatasetBlockNamesTrailingNewlineOK(t *testing.T) {
	got, err := parseExportDatasetBlockNames("- `alpha`\n- `beta`\n")
	if err != nil {
		t.Fatalf("trailing newline rejected: %v", err)
	}
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("got %v, want [alpha beta]", got)
	}
}

// TestRegeneratorIsIdempotent asserts running the splicer twice in a
// row against the same README yields identical bytes — the
// `git diff README.md` step after `make docs-export-datasets` on an
// unchanged registry must be empty.
func TestRegeneratorIsIdempotent(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	block := renderExportDatasetsBlock(exportDatasetCatalogSingleton.Names())
	once, err := spliceREADMEExportDatasets(string(content), block)
	if err != nil {
		t.Fatalf("first splice: %v", err)
	}
	twice, err := spliceREADMEExportDatasets(once, block)
	if err != nil {
		t.Fatalf("second splice: %v", err)
	}
	if once != twice {
		t.Fatal("regenerator is not idempotent: second splice changed bytes")
	}
}

// TestSyntheticDatasetReflowsBlock proves the generator is wired to
// the registry: a synthetic dataset injected into the catalog produces
// a block whose bullet list contains the synthetic name. This is the
// "add a synthetic dataset to the registry and re-running the generator
// updates the README" half of the issue's acceptance criterion.
func TestSyntheticDatasetReflowsBlock(t *testing.T) {
	synthetic := exportDatasetSpec{name: "zzz-synthetic-probe"}
	augmented := append([]exportDatasetSpec{synthetic}, exportDatasetDefinitions...)
	catalog := newExportDatasetCatalog(augmented)
	block := renderExportDatasetsBlock(catalog.Names())
	if !strings.Contains(block, "- `zzz-synthetic-probe`") {
		t.Fatalf("synthetic dataset missing from rendered block:\n%s", block)
	}
}

// TestRunDocsExportDatasetsRewritesREADME exercises the hidden
// subcommand end-to-end against a tempdir copy of the real README: the
// command must read the file, splice in a fresh block, and write the
// result back. This is the seam the Makefile target invokes.
func TestRunDocsExportDatasetsRewritesREADME(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	original, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	tempDir := t.TempDir()
	scratch := filepath.Join(tempDir, "README.md")
	// Pre-stage the README with an obviously-stale block so we can
	// assert the command actually rewrote it.
	stale := strings.Replace(
		string(original),
		"<!-- export-datasets:start -->",
		"<!-- export-datasets:start -->\n- `STALE-MUST-BE-REPLACED`",
		1,
	)
	if err := os.WriteFile(scratch, []byte(stale), 0o644); err != nil {
		t.Fatalf("write scratch README: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDocsExportDatasets([]string{"--readme", scratch}, stdout, stderr)
	if code != 0 {
		t.Fatalf("runDocsExportDatasets exit code = %d, want 0\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	rewritten, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatalf("read rewritten README: %v", err)
	}
	if strings.Contains(string(rewritten), "STALE-MUST-BE-REPLACED") {
		t.Fatal("stale bullet survived; subcommand did not splice")
	}
	want := renderExportDatasetsBlock(exportDatasetCatalogSingleton.Names())
	gotBlock, err := extractREADMEExportDatasetsBlock(string(rewritten))
	if err != nil {
		t.Fatalf("extract rewritten block: %v", err)
	}
	if gotBlock != want {
		t.Fatalf("rewritten block != freshly rendered\ngot:\n%s\nwant:\n%s", gotBlock, want)
	}
}

// TestRunDocsExportDatasetsRequiresREADMEFlag asserts the hidden
// subcommand refuses to splice an unspecified file: an empty --readme
// must error rather than silently writing to "" (a foot-gun shape).
func TestRunDocsExportDatasetsRequiresREADMEFlag(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDocsExportDatasets(nil, stdout, stderr)
	if code == 0 {
		t.Fatalf("runDocsExportDatasets without --readme should fail; got exit 0\nstderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--readme") {
		t.Fatalf("stderr should mention --readme, got: %q", stderr.String())
	}
}

// TestRunDocsExportDatasetsRejectsUnexpectedPositional pins the
// strict-arg check Copilot review surfaced: a positional argument
// after --readme must be rejected so a misconfigured Makefile target
// (e.g. an unquoted shell glob) fails loudly instead of silently
// rewriting the wrong file or ignoring the extra token.
func TestRunDocsExportDatasetsRejectsUnexpectedPositional(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDocsExportDatasets([]string{"--readme", "README.md", "stray-typo"}, stdout, stderr)
	if code == 0 {
		t.Fatalf("runDocsExportDatasets with stray positional should fail; got exit 0\nstderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "unexpected argument") {
		t.Fatalf("stderr should mention unexpected argument, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "stray-typo") {
		t.Fatalf("stderr should name the stray arg, got: %q", stderr.String())
	}
}

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
	got := parseExportDatasetBlockNames(block)
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

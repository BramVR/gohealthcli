package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDocsCheckFailsOnDrift is the issue #74 failure-path guard for
// `make docs-check`. ADR-0008 commits the generated command-reference
// pages (docs/commands.md + docs/commands/*.md); docs-check must fail
// with a diff naming the drifted file when a committed page disagrees
// with a fresh regeneration from `schema --json`.
//
// Mechanism: append a marker line to one committed generated page,
// run `make docs-check`, and assert (a) non-zero exit and (b) the
// drifted file's repo-relative path appears in the output. The
// mutation is restored via t.Cleanup so the working tree is left
// byte-identical to its starting state.
//
// Like TestDocsCommandsRegenIsStable, the test SKIPs when `make` or
// `node` are unavailable; CI runs with both installed.
func TestDocsCheckFailsOnDrift(t *testing.T) {
	// NOT t.Parallel(): mutates / regenerates the shared repo docs tree
	// (docs/commands*), so it must never overlap the sibling docs tests.
	requireDocsToolchain(t)
	repoRoot := docsCheckRepoRoot(t)

	relPath := filepath.Join("docs", "commands", firstGeneratedCommandPage(t, repoRoot))
	absPath := filepath.Join(repoRoot, relPath)
	original, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("read %s: %v", absPath, err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(absPath, original, 0o644); err != nil {
			t.Fatalf("restore %s: %v", absPath, err)
		}
	})

	mutated := append(append([]byte{}, original...), []byte("\ndrift-marker: docs-check failure-path test\n")...)
	if err := os.WriteFile(absPath, mutated, 0o644); err != nil {
		t.Fatalf("mutate %s: %v", absPath, err)
	}

	cmd := exec.Command("make", "docs-check")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("`make docs-check` exited zero despite drifted %s; output:\n%s", relPath, out)
	}
	if !strings.Contains(string(out), relPath) {
		t.Fatalf("`make docs-check` output does not identify drifted file %s; output:\n%s", relPath, out)
	}
}

// TestDocsCheckPassesOnCleanTree is the issue #74 success path:
// when the committed command-reference pages match a fresh
// regeneration (the invariant on every healthy checkout), `make
// docs-check` must exit zero so CI stays green. Guards against the
// check false-positively flagging the hand-written preserved pages
// (help.md, version.md) that the generator never emits.
func TestDocsCheckPassesOnCleanTree(t *testing.T) {
	// NOT t.Parallel(): mutates / regenerates the shared repo docs tree
	// (docs/commands*), so it must never overlap the sibling docs tests.
	requireDocsToolchain(t)
	repoRoot := docsCheckRepoRoot(t)

	cmd := exec.Command("make", "docs-check")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("`make docs-check` failed on a clean tree: %v\noutput:\n%s", err, out)
	}
}

// requireDocsToolchain skips the test when the docs regen pipeline's
// non-Go prerequisites (make, node) are missing from PATH.
func requireDocsToolchain(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"make", "node"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("docs-check requires %q on PATH: %v", bin, err)
		}
	}
}

// docsCheckRepoRoot resolves the repository root relative to this
// package (cmd/gohealthcli → ../..).
func docsCheckRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// firstGeneratedCommandPage returns the lexicographically first
// generated *.md page under docs/commands/, skipping the hand-written
// preserved pages (help.md, version.md) that the generator never
// emits — mutating one of those must not trip the drift check, so it
// would be the wrong target for the failure-path test.
func firstGeneratedCommandPage(t *testing.T, repoRoot string) string {
	t.Helper()
	dir := filepath.Join(repoRoot, "docs", "commands")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	preserved := map[string]bool{"help.md": true, "version.md": true}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || preserved[e.Name()] {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		t.Fatal("no generated command pages found under docs/commands")
	}
	sort.Strings(names)
	return names[0]
}

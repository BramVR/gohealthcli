package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestREADMEListsEverySyncableDataType is the drift guard for issue #145:
// the README's "Supported Data Point sync types" section must enumerate
// every Data Type in the Google Health catalog that `sync --types` will
// accept (i.e. every entry with a list or reconcile endpoint). If a new
// Data Type is added to the catalog without a README update, this test
// fails. Each name must appear as an inline code span (`name`) so the
// match is unambiguous and not satisfied by prose that happens to contain
// the substring.
//
// A catalog entry that sync rejects with "is not supported yet" (no
// list/reconcile endpoint) is covered by the companion caveat guard
// TestREADMECaveatListsCatalogTypesSyncRejects.
//
// The search is scoped to the "Supported Data Point sync types" section
// only (between the section marker and the next markdown section header).
// A name that appears in an unrelated code example elsewhere in the
// README must not silently satisfy the guard while the section itself
// stays stale.
func TestREADMEListsEverySyncableDataType(t *testing.T) {
	section := readmeSection(t, "Supported Data Point sync types")
	for _, dataType := range googleHealthDataTypes.order {
		if !syncDataPointDataTypeSupported(dataType) {
			continue
		}
		token := "`" + dataType + "`"
		if !strings.Contains(section, token) {
			t.Errorf("README \"Supported Data Point sync types\" section missing %s (looked for %q); update README.md to match the Google Health catalog", dataType, token)
		}
	}
}

// TestREADMECaveatListsCatalogTypesSyncRejects is the drift guard for the
// "known to the catalog but not supported by raw Data Point sync" caveat:
// every catalog entry that lacks a list/reconcile endpoint must be named
// in the caveat sentence. Today that is `total-calories`; if a future
// reserved entry is added, this test forces the caveat to grow with it.
func TestREADMECaveatListsCatalogTypesSyncRejects(t *testing.T) {
	section := readmeSection(t, "Supported Data Point sync types")
	for _, dataType := range googleHealthDataTypes.order {
		if syncDataPointDataTypeSupported(dataType) {
			continue
		}
		token := "`" + dataType + "`"
		if !strings.Contains(section, token) {
			t.Errorf("README \"Supported Data Point sync types\" section caveat missing %s (looked for %q); extend the \"known to the catalog but not supported\" sentence", dataType, token)
		}
	}
}

func readmeSection(t *testing.T, marker string) string {
	t.Helper()
	readmePath := filepath.Join("..", "..", "README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readme := string(content)
	start := strings.Index(readme, marker)
	if start < 0 {
		t.Fatalf("README section marker %q not found; the drift guard cannot enforce sync. Restore the section or update the marker constant.", marker)
	}
	rest := readme[start:]
	bodyStart := strings.Index(rest, "\n")
	if bodyStart < 0 {
		bodyStart = 0
	}
	body := rest[bodyStart:]
	end := strings.Index(body, "\n## ")
	if end < 0 {
		end = len(body)
	}
	return body[:end]
}

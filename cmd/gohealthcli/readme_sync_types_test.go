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
// The search is scoped to the bullet block only (between the section
// marker and the first blank line that ends the bullet list). The
// caveat paragraph and the adjacent "Normalized export datasets"
// section are deliberately excluded so a dataset name like
// `body-fat-samples` cannot accidentally satisfy this guard on behalf
// of a missing `body-fat` bullet, and a type mistakenly listed in the
// caveat cannot pose as supported.
func TestREADMEListsEverySyncableDataType(t *testing.T) {
	t.Parallel()
	bullets := readmeSyncTypesBulletBlock(t)
	for _, dataType := range googleHealthDataTypes.order {
		if !syncDataPointDataTypeSupported(dataType) {
			continue
		}
		token := "`" + dataType + "`"
		if !strings.Contains(bullets, token) {
			t.Errorf("README \"Supported Data Point sync types\" bullet block missing %s (looked for %q); update README.md to match the Google Health catalog", dataType, token)
		}
	}
}

// TestREADMECaveatListsCatalogTypesSyncRejects is the drift guard for the
// "known to the catalog but not supported by raw Data Point sync" caveat:
// every catalog entry that lacks a list/reconcile endpoint must be named
// in the caveat paragraph. Today that is `total-calories`; if a future
// reserved entry is added, this test forces the caveat to grow with it.
// The scope is the caveat paragraph specifically (the one containing the
// "known to the catalog" phrase) so a stray mention elsewhere in the
// section cannot satisfy the guard.
func TestREADMECaveatListsCatalogTypesSyncRejects(t *testing.T) {
	t.Parallel()
	caveat := readmeSyncTypesCaveatParagraph(t)
	for _, dataType := range googleHealthDataTypes.order {
		if syncDataPointDataTypeSupported(dataType) {
			continue
		}
		token := "`" + dataType + "`"
		if !strings.Contains(caveat, token) {
			t.Errorf("README sync-types caveat paragraph missing %s (looked for %q); extend the \"known to the catalog but not supported\" sentence", dataType, token)
		}
	}
}

const readmeSyncTypesSectionMarker = "Supported Data Point sync types"

// readmeSyncTypesBulletBlock returns just the bullet list following the
// "Supported Data Point sync types" marker — from the first bullet line
// through the blank line that terminates the list. The caveat
// paragraph and downstream sections are excluded.
func readmeSyncTypesBulletBlock(t *testing.T) string {
	t.Helper()
	body := readmeSectionBody(t, readmeSyncTypesSectionMarker)
	// Skip leading blank lines to the first bullet.
	bulletStart := strings.Index(body, "\n- ")
	if bulletStart < 0 {
		t.Fatalf("README sync-types section has no bullet list under %q", readmeSyncTypesSectionMarker)
	}
	bullets := body[bulletStart+1:] // drop the leading newline
	// The bullet block ends at the first blank line (two newlines in a row).
	end := strings.Index(bullets, "\n\n")
	if end < 0 {
		end = len(bullets)
	}
	return bullets[:end]
}

// readmeSyncTypesCaveatParagraph returns the paragraph in the sync-types
// section that names the catalog types sync rejects. The caveat is
// identified by the literal phrase "known to the catalog" so a future
// rewrite that drops the phrase trips this finder rather than silently
// matching the wrong paragraph.
func readmeSyncTypesCaveatParagraph(t *testing.T) string {
	t.Helper()
	body := readmeSectionBody(t, readmeSyncTypesSectionMarker)
	const phrase = "known to the catalog"
	hit := strings.Index(body, phrase)
	if hit < 0 {
		t.Fatalf("README sync-types caveat paragraph not found; looked for %q under %q", phrase, readmeSyncTypesSectionMarker)
	}
	// Walk back to the start of the paragraph (last blank line before hit).
	paraStart := strings.LastIndex(body[:hit], "\n\n")
	if paraStart < 0 {
		paraStart = 0
	} else {
		paraStart += 2
	}
	// Walk forward to the end of the paragraph (next blank line).
	paraEnd := strings.Index(body[hit:], "\n\n")
	if paraEnd < 0 {
		return body[paraStart:]
	}
	return body[paraStart : hit+paraEnd]
}

// readmeSectionBody returns the README region from the line after the
// given marker through to the next "## " heading (or EOF). The bullet-
// and caveat-specific helpers above narrow further before asserting.
func readmeSectionBody(t *testing.T, marker string) string {
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

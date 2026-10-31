package googlehealth

import (
	"os"
	"path/filepath"
	"runtime"
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
		if !SupportsSyncDataPoints(dataType) {
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
		if SupportsSyncDataPoints(dataType) {
			continue
		}
		token := "`" + dataType + "`"
		if !strings.Contains(caveat, token) {
			t.Errorf("README sync-types caveat paragraph missing %s (looked for %q); extend the \"known to the catalog but not supported\" sentence", dataType, token)
		}
	}
}

// TestREADMECitesThisDriftGuardFile pins the README's breadcrumb to this
// drift-guard suite: the "Supported Data Point sync types" section must
// cite this file by its repo-relative path. The #287/#312 extraction
// moved the suite from cmd/gohealthcli to internal/googlehealth and the
// README kept pointing at the old location (#313); computing the path
// at runtime makes the assertion follow the file through any future
// move instead of hard-coding the package again. The repo root is
// located by walking up to go.mod, so a depth change moves with it; the
// file name comes from runtime.Caller's base so a rename is picked up
// even under -trimpath builds.
func TestREADMECitesThisDriftGuardFile(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate this test file")
	}
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve package dir: %v", err)
	}
	repoRoot := repoRootDir(t)
	rel, err := filepath.Rel(repoRoot, filepath.Join(packageDir, filepath.Base(thisFile)))
	if err != nil {
		t.Fatalf("relativize %s against %s: %v", thisFile, repoRoot, err)
	}
	token := "`" + filepath.ToSlash(rel) + "`"
	body := readmeSectionBody(t, readmeSyncTypesSectionMarker)
	if !strings.Contains(body, token) {
		t.Errorf("README sync-types section does not cite this drift-guard file as %s; update the \"drift guard in ...\" paragraph in README.md", token)
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

// repoRootDir walks up from the package dir to the directory holding
// go.mod, so the suite keeps working if this package moves to a
// different depth in the tree.
func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve package dir: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from the package dir; cannot locate the repo root")
		}
		dir = parent
	}
}

// readmeSectionBody returns the README region from the line after the
// given marker through to the next "## " heading (or EOF). The bullet-
// and caveat-specific helpers above narrow further before asserting.
func readmeSectionBody(t *testing.T, marker string) string {
	t.Helper()
	readmePath := filepath.Join(repoRootDir(t), "README.md")
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

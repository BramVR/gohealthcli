package main

import (
	"reflect"
	"testing"
)

// TestSuggestReturnsCloseCommand exercises the canonical one-typo case from
// the PRD: a single transposition in "status" should surface that command as
// the suggestion. This test is intentionally narrow — it locks in the
// Levenshtein ≤ 2 contract through the public Suggest entry point so any
// future tweak to the helper (different metric, different threshold) still
// has to honour the AC.
func TestSuggestReturnsCloseCommand(t *testing.T) {
	got := commandRegistry(commands).Suggest("stauts")
	want := []string{"status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Suggest(\"stauts\") = %v, want %v", got, want)
	}
}

// TestSuggestReturnsNilForDistantTypo pins the upper-bound side of the
// Levenshtein contract: a string with no close match must surface as the
// empty result so the unknown-command path can suppress the "Did you mean"
// line entirely. The AC calls this out explicitly ("xyz" → no suggestion).
func TestSuggestReturnsNilForDistantTypo(t *testing.T) {
	got := commandRegistry(commands).Suggest("xyz")
	if len(got) != 0 {
		t.Fatalf("Suggest(\"xyz\") = %v, want empty slice", got)
	}
}

// TestSuggestExcludesHiddenCommands asserts that the build-time `schema`
// entry (the only Hidden command in the registry today) never surfaces as
// a suggestion. We type a one-character transposition of "schema" so the
// Levenshtein distance is 2 — well inside the cutoff — and verify the name
// is filtered out at the registry layer rather than relying on UX polish at
// the call site. AC: "Hidden commands (schema) are excluded from Suggest".
func TestSuggestExcludesHiddenCommands(t *testing.T) {
	got := commandRegistry(commands).Suggest("shcema")
	for _, name := range got {
		if name == "schema" {
			t.Fatalf("Suggest(\"shcema\") returned hidden command 'schema': %v", got)
		}
	}
}

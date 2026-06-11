package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

// withVersionVars swaps the package-level version/commit/built vars to
// deterministic test values and restores them when the test ends. The
// PRD #143 slice 5 contract is that build-time ldflags inject these at
// link time; tests pin them so the output shape is verifiable without
// depending on the actual build flags. version/commit/built are the one
// sanctioned set of mutable package vars (the linker must be able to
// stamp them), so the two tests that swap them stay deliberately serial
// — running them under t.Parallel would race the assignments.
func withVersionVars(t *testing.T, v, c, b string) {
	t.Helper()

	prevV, prevC, prevB := version, commit, built
	version = v
	commit = c
	built = b
	t.Cleanup(func() {
		version = prevV
		commit = prevC
		built = prevB
	})
}

func TestRenderVersionPlain(t *testing.T) {
	// NOT t.Parallel(): withVersionVars mutates the linker-stamped
	// package vars shared with TestRenderVersionJSON.
	withVersionVars(t, "v1.2.3", "abcdef1", "2025-01-02T03:04:05Z")

	var buf bytes.Buffer
	RenderVersion(outputMode{}, &buf)

	want := "gohealthcli v1.2.3 (abcdef1 built 2025-01-02T03:04:05Z)\n"
	if got := buf.String(); got != want {
		t.Fatalf("RenderVersion plain = %q, want %q", got, want)
	}
}

func TestRenderVersionJSON(t *testing.T) {
	// NOT t.Parallel(): withVersionVars mutates the linker-stamped
	// package vars shared with TestRenderVersionPlain.
	withVersionVars(t, "v2.0.0", "deadbee", "2026-06-08T00:00:00Z")

	var buf bytes.Buffer
	RenderVersion(outputMode{json: true}, &buf)

	out := buf.String()
	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Fatalf("RenderVersion JSON should end with newline, got %q", out)
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("RenderVersion JSON unmarshal: %v\nbody: %s", err, out)
	}
	want := map[string]string{
		"version": "v2.0.0",
		"commit":  "deadbee",
		"built":   "2026-06-08T00:00:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("RenderVersion JSON[%q] = %q, want %q\nbody: %s", k, got[k], v, out)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("RenderVersion JSON has extra fields: %v", got)
	}
}

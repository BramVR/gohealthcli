package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestVersionDoesNotCheckSetup(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "missing-config.toml"),
		"--db", filepath.Join(tempDir, "missing.sqlite"),
		"--version",
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	// Without `make build` ldflags, the three package vars all default to
	// "dev", so the canonical plain shape is "gohealthcli dev (dev built dev)".
	// PRD #143 slice 5 (issue #174).
	if got := strings.TrimSpace(stdout.String()); got != "gohealthcli dev (dev built dev)" {
		t.Fatalf("version stdout = %q, want %q", got, "gohealthcli dev (dev built dev)")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionJSON(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "missing-config.toml"),
		"--db", filepath.Join(tempDir, "missing.sqlite"),
		"--version",
		"--json",
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal --version --json: %v\nbody: %s", err, stdout.String())
	}
	for _, key := range []string{"version", "commit", "built"} {
		if got[key] == "" {
			t.Fatalf("--version --json[%q] empty; full body: %s", key, stdout.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionPlainAndJSONMutuallyExclusive(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "missing-config.toml"),
		"--db", filepath.Join(tempDir, "missing.sqlite"),
		"--version",
		"--plain",
		"--json",
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("stderr = %q, want to contain %q", stderr.String(), "mutually exclusive")
	}
}

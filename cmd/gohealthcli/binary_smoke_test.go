package main

// binary_smoke_test.go is the thin true-binary surface kept by issue
// #286: nearly every CLI test drives the dispatch in-process through
// run() (see runCommand in harness_test.go), so this file pins the part
// only the compiled binary can prove — main() wiring os.Args, the exit
// status reaching the operating system, and the stdout/stderr stream
// split surviving real process pipes.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinarySmokeVersionExitStatusAndStreamSplit(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runBinary(t, "--version", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if _, ok := got["version"].(string); !ok {
		t.Fatalf("version missing from JSON envelope: %s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestBinarySmokeDoctorReportsMissingSetupExitTwo(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	code, stdout, stderr := runBinary(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--json",
		"doctor",
	)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2\nstderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "setup_missing" {
		t.Fatalf("status = %v, want setup_missing", got["status"])
	}
	if !strings.Contains(stderr.String(), "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", stderr.String())
	}
}

func TestBinarySmokeUnknownCommandExitsNonZero(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runBinary(t, "no-such-command")
	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero\nstdout: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown command: no-such-command") {
		t.Fatalf("stderr = %q, want unknown command line", stderr.String())
	}
}

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailureReporterInitJSONShape locks the new --json failure shape
// for `init`: when no OAuth client source is supplied, the binary
// emits the single-line `{"status":"flag_invalid","message":"..."}`
// envelope on stdout (Failure Reporter contract) and an empty stderr.
// PRD #143 slice 7 AC: "New tests assert the --plain and --json
// failure shapes for at least three representative commands (init,
// sync, devices)."
func TestFailureReporterInitJSONShape(t *testing.T) {
	tempDir := t.TempDir()
	code, stdout, stderr := runCommand(t,
		"init",
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--json",
	)
	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	var envelope map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\nstdout: %s", err, stdout.String())
	}
	if envelope["status"] != "flag_invalid" {
		t.Errorf("status = %q, want flag_invalid", envelope["status"])
	}
	if envelope["message"] == "" {
		t.Errorf("message is empty, want descriptive text")
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty in --json failure mode", stderr.String())
	}
}

// TestFailureReporterInitPlainShape locks the new --plain failure
// shape: the stdout block is `status: <s>\nmessage: <m>\n` and the
// stderr line is `init: <m>\n`. Both streams carry the error so
// terminal users and script consumers both get a signal.
func TestFailureReporterInitPlainShape(t *testing.T) {
	tempDir := t.TempDir()
	code, stdout, stderr := runCommand(t,
		"init",
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--plain",
	)
	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if !strings.HasPrefix(stdout.String(), "status: flag_invalid\nmessage: ") {
		t.Errorf("stdout = %q, want flag_invalid status block", stdout.String())
	}
	if !strings.HasSuffix(stdout.String(), "\n") {
		t.Errorf("stdout = %q, want trailing newline", stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "init: ") {
		t.Errorf("stderr = %q, want init: prefix", stderr.String())
	}
}

// TestFailureReporterSyncJSONShape locks the new --json failure shape
// for `sync`: an unknown --types value reaches the preflight gate
// (returns a syncResult envelope via the result writer) so the JSON
// shape here is the syncResult envelope, NOT a Failure Reporter
// envelope. The Failure Reporter path applies to flag-parse and
// unexpected-arg failures upstream of the gate.
//
// This test exercises the unexpected-argument failure path: passing a
// positional argument the sync subcommand does not accept. That path
// goes directly through ReportFailure with StatusUnexpectedArgument.
func TestFailureReporterSyncJSONShape(t *testing.T) {
	code, stdout, stderr := runCommand(t,
		"--json",
		"sync",
		"unexpected-positional",
	)
	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	var envelope map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\nstdout: %s", err, stdout.String())
	}
	if envelope["status"] != "unexpected_argument" {
		t.Errorf("status = %q, want unexpected_argument", envelope["status"])
	}
	if !strings.Contains(envelope["message"], "unexpected-positional") {
		t.Errorf("message = %q, want to contain unexpected-positional", envelope["message"])
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty in --json failure mode", stderr.String())
	}
}

// TestFailureReporterSyncPlainShape locks the new --plain failure
// shape for sync's unexpected-argument path: stdout carries the
// `status: unexpected_argument\nmessage: ...` block, stderr carries
// the `sync: ...` line.
func TestFailureReporterSyncPlainShape(t *testing.T) {
	code, stdout, stderr := runCommand(t,
		"--plain",
		"sync",
		"unexpected-positional",
	)
	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if !strings.HasPrefix(stdout.String(), "status: unexpected_argument\nmessage: ") {
		t.Errorf("stdout = %q, want unexpected_argument status block", stdout.String())
	}
	if !strings.Contains(stderr.String(), "sync: unexpected sync argument: unexpected-positional") {
		t.Errorf("stderr = %q, want sync-prefixed line", stderr.String())
	}
}

// TestFailureReporterDevicesJSONShape locks the new --json failure
// shape for `devices` when run without a setup: the JSON envelope is
// the devicesResult shape via the result writer (devices_unavailable
// etc.). The Failure Reporter envelope shape applies to the
// unexpected-argument path, exercised here by passing a positional.
func TestFailureReporterDevicesJSONShape(t *testing.T) {
	code, stdout, stderr := runCommand(t,
		"--json",
		"devices",
		"unexpected-positional",
	)
	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	var envelope map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\nstdout: %s", err, stdout.String())
	}
	if envelope["status"] != "unexpected_argument" {
		t.Errorf("status = %q, want unexpected_argument", envelope["status"])
	}
	if !strings.Contains(envelope["message"], "unexpected-positional") {
		t.Errorf("message = %q, want to mention unexpected-positional", envelope["message"])
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty in --json failure mode", stderr.String())
	}
}

// TestFailureReporterDevicesPlainShape locks the --plain failure
// shape: stdout carries the status/message block, stderr carries the
// `devices: ...` line.
func TestFailureReporterDevicesPlainShape(t *testing.T) {
	code, stdout, stderr := runCommand(t,
		"--plain",
		"devices",
		"unexpected-positional",
	)
	if code == 0 {
		t.Fatalf("exit code = 0, want failure")
	}
	if !strings.HasPrefix(stdout.String(), "status: unexpected_argument\nmessage: ") {
		t.Errorf("stdout = %q, want unexpected_argument status block", stdout.String())
	}
	if !strings.Contains(stderr.String(), "devices: unexpected devices argument: unexpected-positional") {
		t.Errorf("stderr = %q, want devices-prefixed line", stderr.String())
	}
}

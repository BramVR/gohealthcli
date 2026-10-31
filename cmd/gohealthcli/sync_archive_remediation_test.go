package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveReadFailuresExposeSetupRemediationOnlyInStructuredModes(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "data"))
	missingArchive := filepath.Join(tempDir, "missing", "health.sqlite")
	tests := []struct {
		name string
		args func(mode string) []string
	}{
		{name: "query", args: func(mode string) []string {
			args := []string{"query", "--db", missingArchive}
			if mode != "" {
				args = append(args, mode)
			}
			return append(args, "SELECT 1")
		}},
		{name: "status", args: func(mode string) []string {
			args := []string{"status", "--db", missingArchive}
			if mode != "" {
				args = append(args, mode)
			}
			return args
		}},
		{name: "sync-status", args: func(mode string) []string {
			args := []string{"sync", "--status", "--db", missingArchive}
			if mode != "" {
				args = append(args, mode)
			}
			return args
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var jsonStdout, jsonStderr bytes.Buffer
			if code := run(test.args("--json"), &jsonStdout, &jsonStderr); code != 1 {
				t.Fatalf("JSON exit = %d, want 1", code)
			}
			if jsonStderr.Len() != 0 {
				t.Fatalf("JSON stderr = %q, want empty", jsonStderr.String())
			}
			var envelope struct {
				Remediation []string `json:"remediation"`
			}
			if err := json.Unmarshal(jsonStdout.Bytes(), &envelope); err != nil {
				t.Fatalf("decode JSON: %v\n%s", err, jsonStdout.String())
			}
			if len(envelope.Remediation) != 2 || envelope.Remediation[0] != "gohealthcli doctor" || envelope.Remediation[1] != "gohealthcli init" {
				t.Fatalf("JSON remediation = %#v, want diagnosis then init", envelope.Remediation)
			}

			var plainStdout, plainStderr bytes.Buffer
			if code := run(test.args("--plain"), &plainStdout, &plainStderr); code != 1 {
				t.Fatalf("plain exit = %d, want 1", code)
			}
			if !strings.Contains(plainStdout.String(), "remediation.0: gohealthcli doctor\n") || !strings.Contains(plainStdout.String(), "remediation.1: gohealthcli init\n") {
				t.Fatalf("plain remediation missing:\n%s", plainStdout.String())
			}

			var humanStdout, humanStderr bytes.Buffer
			if code := run(test.args(""), &humanStdout, &humanStderr); code != 1 {
				t.Fatalf("human exit = %d, want 1", code)
			}
			if strings.Contains(humanStdout.String()+humanStderr.String(), "remediation") || strings.Contains(humanStdout.String()+humanStderr.String(), "gohealthcli doctor") {
				t.Fatalf("human output changed with structured remediation: stdout=%q stderr=%q", humanStdout.String(), humanStderr.String())
			}
		})
	}
}

func TestCorruptArchiveFailuresDoNotFabricateRemediation(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "data"))
	archivePath := filepath.Join(tempDir, "corrupt.sqlite")
	if err := os.WriteFile(archivePath, []byte("not a SQLite archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := [][]string{
		{"query", "--db", archivePath, "--json", "SELECT 1"},
		{"status", "--db", archivePath, "--json"},
		{"sync", "--status", "--db", archivePath, "--json"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 {
			t.Fatalf("%v exit = %d, want 1", args, code)
		}
		var envelope map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %v JSON: %v\n%s", args, err, stdout.String())
		}
		if remediation, ok := envelope["remediation"]; ok {
			t.Fatalf("%v remediation = %#v, want omitted for corrupt archive", args, remediation)
		}
	}
}

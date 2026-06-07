package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunSchemaEmitsValidDocument(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runSchema(nil, stdout, stderr); code != 0 {
		t.Fatalf("runSchema exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var doc schemaDocument
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("schema output is not valid JSON: %v\noutput: %s", err, stdout.String())
	}
	if doc.Version != commandSchemaVersion {
		t.Errorf("version = %d, want %d", doc.Version, commandSchemaVersion)
	}
	if doc.Binary != "gohealthcli" {
		t.Errorf("binary = %q, want %q", doc.Binary, "gohealthcli")
	}
	if len(doc.Commands) == 0 {
		t.Fatalf("commands slice is empty")
	}
}

func TestRunSchemaIncludesDoctor(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runSchema(nil, stdout, stderr); code != 0 {
		t.Fatalf("runSchema exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var doc schemaDocument
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("schema output is not valid JSON: %v", err)
	}

	var doctor *commandDef
	for i := range doc.Commands {
		if doc.Commands[i].Name == "doctor" {
			doctor = &doc.Commands[i]
			break
		}
	}
	if doctor == nil {
		t.Fatalf("schema document does not contain doctor entry")
	}
	if doctor.Hidden {
		t.Errorf("doctor.hidden = true, want false")
	}
	if doctor.Short == "" {
		t.Errorf("doctor.short is empty")
	}
	if doctor.Long == "" {
		t.Errorf("doctor.long is empty")
	}
	wantFlags := []string{"config", "db", "json", "plain", "online", "no-input"}
	got := flagNames(doctor.Flags)
	for _, want := range wantFlags {
		if !contains(got, want) {
			t.Errorf("doctor flags missing %q; got %v", want, got)
		}
	}
}

func TestRunSchemaIncludesHiddenSchemaCommand(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runSchema(nil, stdout, stderr); code != 0 {
		t.Fatalf("runSchema exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var doc schemaDocument
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("schema output is not valid JSON: %v", err)
	}

	for _, c := range doc.Commands {
		if c.Name == "schema" {
			if !c.Hidden {
				t.Errorf("schema command should be marked hidden")
			}
			return
		}
	}
	t.Fatalf("schema document does not contain schema entry")
}

func TestRunSchemaRejectsPositionalArgs(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runSchema([]string{"surprise"}, stdout, stderr); code == 0 {
		t.Fatalf("runSchema with unexpected positional should fail; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unexpected schema argument") {
		t.Errorf("stderr should mention the unexpected argument; got %q", stderr.String())
	}
}

func TestRunSchemaRejectsNonJSONMode(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runSchema([]string{"--json=false"}, stdout, stderr); code == 0 {
		t.Fatalf("runSchema with --json=false should fail; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "supports --json output only") {
		t.Errorf("stderr should mention the unsupported mode; got %q", stderr.String())
	}
}

func flagNames(flags []flagSpec) []string {
	names := make([]string, 0, len(flags))
	for _, f := range flags {
		names = append(names, f.Name)
	}
	return names
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

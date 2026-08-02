package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
)

func TestRawReportsSetupAndConnectionRemediationWithoutProviderIO(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		prepare         func(t *testing.T) (string, string)
		wantRemediation []string
	}{
		{
			name: "missing setup",
			prepare: func(t *testing.T) (string, string) {
				tempDir := t.TempDir()
				return filepath.Join(tempDir, "config.toml"), filepath.Join(tempDir, "archive.sqlite")
			},
			wantRemediation: []string{"gohealthcli doctor", "gohealthcli init"},
		},
		{
			name: "missing Connection",
			prepare: func(t *testing.T) (string, string) {
				configPath, archivePath, _ := initializeFileCredentialSetup(t, t.TempDir())
				return configPath, archivePath
			},
			wantRemediation: []string{"gohealthcli doctor", "gohealthcli connect"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, archivePath := test.prepare(t)
			runtime := newConnectFakeRuntime(t, fakeConnectConfig{failIfCalled: true})
			stdout := new(bytes.Buffer)
			code := runWithRuntime([]string{"--json", "raw", "endpoint", "getIdentity", "--config", configPath, "--db", archivePath}, stdout, new(bytes.Buffer), runtime)
			if code != 1 {
				t.Fatalf("raw exit code = %d, want 1", code)
			}
			var result failureJSONEnvelope
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("decode raw result: %v", err)
			}
			if !slices.Equal(result.Remediation, test.wantRemediation) {
				t.Fatalf("remediation = %#v, want %#v", result.Remediation, test.wantRemediation)
			}
			assertNoSecretWords(t, stdout.String())
		})
	}
}

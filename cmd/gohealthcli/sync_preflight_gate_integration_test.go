package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// preflightFailureExpectation captures the AC-level contract: a preflight
// rejection must (a) exit non-zero from the CLI surface and (b) leave the
// sync_runs row count unchanged. The JSON envelope must carry
// status="sync_failed" (never the empty string, AC #5) and MUST NOT
// contain a sync_run_id field, since no audit row was written.
type preflightFailureExpectation struct {
	name          string
	args          []string
	wantErrSubstr string
}

// TestRunSyncPreflightFailuresDoNotWriteAuditRow exercises the no-audit-row
// contract end-to-end: for every representative preflight rule, the
// orchestrator-level call exits non-zero and sync_runs count stays at 0.
// AC #4 + AC #5 of issue #152.
func TestRunSyncPreflightFailuresDoNotWriteAuditRow(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	if _, err := connectSetupWithRuntimeAndExtraScopes(configPath, archivePath, false, nil, testRuntime); err != nil {
		t.Fatalf("connect setup: %v", err)
	}
	testRuntime.now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	cases := []preflightFailureExpectation{
		{
			name:          "unsupported_data_type",
			args:          []string{"--types", "unsupported-type", "--from", "2026-01-01", "--to", "2026-01-02T00:00:00Z", "--json"},
			wantErrSubstr: "is not supported yet",
		},
		{
			name:          "rollup_parse_failure",
			args:          []string{"--types", "steps", "--rollup", "weird", "--from", "2026-01-01", "--to", "2026-01-02", "--json"},
			wantErrSubstr: "not supported; expected one of",
		},
		{
			name:          "rollup_source_family_conflict",
			args:          []string{"--types", "steps", "--rollup", "daily", "--source-family", "wearable", "--from", "2026-01-01", "--to", "2026-01-02", "--json"},
			wantErrSubstr: "--source-family cannot be combined with --rollup",
		},
		{
			name:          "all_vs_types_conflict",
			args:          []string{"--all", "--types", "steps", "--json"},
			wantErrSubstr: "--all cannot be combined with --types",
		},
		{
			name:          "duplicate_types",
			args:          []string{"--types", "steps,steps", "--from", "2026-01-01", "--to", "2026-01-02T00:00:00Z", "--json"},
			wantErrSubstr: "more than once",
		},
		{
			// Range-ordering invariants live in the gate (#153); fake-
			// provider integration confirms an inverted --from/--to
			// never reaches an upstream HTTP call AND never writes a
			// sync_runs row. The runtime here uses the connect fake's
			// HTTP transport; if the gate let this through, the
			// downstream Plan call would touch it.
			name:          "range_order_inverted",
			args:          []string{"--types", "steps", "--from", "2026-06-08", "--to", "2026-06-01", "--json"},
			wantErrSubstr: "from must be earlier than to",
		},
		{
			name:          "range_zero_width",
			args:          []string{"--types", "steps", "--from", "2026-06-01", "--to", "2026-06-01", "--json"},
			wantErrSubstr: "zero-width sync window",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := runSyncWithRuntime(tc.args, configPath, archivePath, outputMode{}, stdout, stderr, testRuntime)
			if code == 0 {
				t.Fatalf("runSyncWithRuntime exit = 0, want non-zero (preflight rule must reject)\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
			}

			// AC #5: JSON envelope on preflight failure has
			// status="sync_failed" and no sync_run_id field.
			var envelope map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("decode JSON envelope: %v\nstdout: %s", err, stdout.String())
			}
			if status, _ := envelope["status"].(string); status != "sync_failed" {
				t.Errorf("status = %q, want %q (never empty)", status, "sync_failed")
			}
			if _, present := envelope["sync_run_id"]; present {
				t.Errorf("envelope contains sync_run_id; no audit row should have been written")
			}
			if message, _ := envelope["message"].(string); !strings.Contains(message, tc.wantErrSubstr) {
				t.Errorf("message = %q, want substring %q", message, tc.wantErrSubstr)
			}

			// AC #4: sync_runs row count unchanged (zero, since this is a
			// fresh archive that has never run a sync).
			assertArchiveTableCount(t, archivePath, "sync_runs", 0)
		})
	}
}

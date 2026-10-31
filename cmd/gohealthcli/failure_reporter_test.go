package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestRemediationErrorPreservesCauseAndNormalizesActions(t *testing.T) {
	t.Parallel()
	cause := errors.New("root cause")
	wrapped := fmt.Errorf("outer context: %w", withRemediation(cause,
		remediationAction("  gohealthcli\t--help\n"),
		remediationShowHelp,
		remediationAction("gohealthcli doctor"),
		remediationAction("gohealthcli connect"),
		remediationAction("gohealthcli status"),
		remediationAction("SELECT * FROM health_data"),
	))

	if !errors.Is(wrapped, cause) {
		t.Fatalf("errors.Is(wrapped, cause) = false")
	}
	var carrier *remediationError
	if !errors.As(wrapped, &carrier) {
		t.Fatalf("errors.As(wrapped, *remediationError) = false")
	}
	want := []string{"gohealthcli --help", "gohealthcli doctor", "gohealthcli connect"}
	if got := remediationFromError(wrapped); !reflect.DeepEqual(got, want) {
		t.Fatalf("remediation = %#v, want %#v", got, want)
	}
}

func TestReportFailureRemediationRejectsNonCatalogContent(t *testing.T) {
	t.Parallel()
	cause := withRemediation(errors.New("safe message"),
		remediationAction("token ya29.secret"),
		remediationAction("authorization_code=secret-code"),
		remediationAction("Google Identity 123456789"),
		remediationAction("open /Users/person/private/archive.sqlite"),
		remediationAction("heart-rate: 72"),
		remediationAction("SELECT * FROM data_points"),
		remediationAction("Provider response: raw body"),
		remediationAction("repeat unexpected-positional"),
		remediationShowHelp,
	)
	var stdout, stderr bytes.Buffer
	code := ReportFailure(FailureReport{
		Status:  StatusFlagInvalid,
		Message: "safe message",
		Mode:    outputMode{json: true},
		Cause:   cause,
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"status":"flag_invalid","message":"safe message","remediation":["gohealthcli --help"]}` + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestAuthRemediationPreservesTypedCauses(t *testing.T) {
	t.Parallel()
	missing := missingConnectionRemediation(sql.ErrNoRows)
	if !errors.Is(missing, sql.ErrNoRows) {
		t.Fatal("missing Connection remediation lost sql.ErrNoRows")
	}
	if got := remediationFromError(missing); !reflect.DeepEqual(got, []string{"gohealthcli doctor", "gohealthcli connect"}) {
		t.Fatalf("missing remediation = %#v", got)
	}

	httpCause := &googlehealth.HTTPError{StatusCode: 401, Endpoint: "identity"}
	rejected := connectionFailureRemediation(googlehealth.NormalizeError(httpCause))
	if !errors.Is(rejected, googlehealth.ErrUnauthorized) {
		t.Fatal("rejected-token remediation lost ErrUnauthorized")
	}
	var gotHTTP *googlehealth.HTTPError
	if !errors.As(rejected, &gotHTTP) || gotHTTP != httpCause {
		t.Fatal("rejected-token remediation lost typed HTTPError")
	}
	if got := remediationFromError(rejected); !reflect.DeepEqual(got, []string{"gohealthcli doctor --online", "gohealthcli connect"}) {
		t.Fatalf("rejected-token remediation = %#v", got)
	}

	messageOnly := connectionFailureRemediation(errors.New(googlehealth.ErrUnauthorized.Error()))
	if got := remediationFromError(messageOnly); len(got) != 0 {
		t.Fatalf("message-only error remediation = %#v, want none", got)
	}
}

func TestScopeRemediationAllowsOnlySortedCatalogKeywords(t *testing.T) {
	t.Parallel()
	cause := withRemediation(errors.New("safe"),
		remediationAction("gohealthcli connect --add-scopes settings,ecg"),
		remediationAction("gohealthcli connect --add-scopes ecg,unknown"),
		remediationAction("gohealthcli connect --add-scopes ecg,settings"),
		remediationAction("gohealthcli connect --add-scopes ecg,ecg"),
	)
	want := []string{"gohealthcli connect --add-scopes ecg,settings"}
	if got := remediationFromError(cause); !reflect.DeepEqual(got, want) {
		t.Fatalf("remediation = %#v, want %#v", got, want)
	}
}

func TestReportFailureDoesNotInferRemediationFromMessage(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := ReportFailure(FailureReport{
		Status:  StatusFlagInvalid,
		Message: "run gohealthcli --help",
		Mode:    outputMode{json: true},
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"status":"flag_invalid","message":"run gohealthcli --help"}` + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestReportFailureStructuredRemediationParity(t *testing.T) {
	t.Parallel()
	cause := withRemediation(errors.New("boom"), remediationRunDoctor, remediationReconnect, remediationStatus)
	for _, testCase := range []struct {
		name       string
		mode       outputMode
		wantStdout string
		wantStderr string
	}{
		{
			name:       "json",
			mode:       outputMode{json: true},
			wantStdout: `{"status":"operation_failed","message":"boom","remediation":["gohealthcli doctor","gohealthcli connect","gohealthcli status"]}` + "\n",
		},
		{
			name:       "plain",
			mode:       outputMode{plain: true},
			wantStdout: "status: operation_failed\nmessage: boom\nremediation.0: gohealthcli doctor\nremediation.1: gohealthcli connect\nremediation.2: gohealthcli status\n",
			wantStderr: "sync: boom\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := ReportFailure(FailureReport{
				Command: "sync",
				Status:  StatusOperationFailed,
				Message: "boom",
				Mode:    testCase.mode,
				Cause:   cause,
			}, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if stdout.String() != testCase.wantStdout {
				t.Fatalf("stdout = %q, want %q", stdout.String(), testCase.wantStdout)
			}
			if stderr.String() != testCase.wantStderr {
				t.Fatalf("stderr = %q, want %q", stderr.String(), testCase.wantStderr)
			}
		})
	}
}

func TestReportFailureOmitsEmptyRemediation(t *testing.T) {
	t.Parallel()
	cause := withRemediation(errors.New("boom"), remediationAction("unsafe user-provided step"))
	var stdout, stderr bytes.Buffer
	code := ReportFailure(FailureReport{
		Status:  StatusOperationFailed,
		Message: "boom",
		Mode:    outputMode{json: true},
		Cause:   cause,
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"status":"operation_failed","message":"boom"}` + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// TestReportFailureDefaultModeMatchesLegacyShape locks the byte-for-byte
// shape the unified Failure Reporter emits in the no-flag (default) mode:
// `<cmd>: <message>\n` to stderr, nothing to stdout, exit code 1. Every
// migrated call site relied on that exact prefix before slice 7, so the
// default shape MUST match byte-for-byte or the existing per-command
// failure-path tests would break wholesale.
func TestReportFailureDefaultModeMatchesLegacyShape(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := ReportFailure(FailureReport{
		Command: "sync",
		Status:  StatusFlagInvalid,
		Message: "missing --types",
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.String() != "sync: missing --types\n" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), "sync: missing --types\n")
	}
}

// TestReportFailureJSONModeWritesOneLineEnvelope locks the --json failure
// shape: `{"status":"<status>","message":"<message>"}\n` on stdout, no
// `<cmd>:` prefix on stderr. The single-line envelope matches the
// existing success JSON shape's "one line per result" contract.
func TestReportFailureJSONModeWritesOneLineEnvelope(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := ReportFailure(FailureReport{
		Command: "sync",
		Status:  StatusFlagInvalid,
		Message: "missing --types",
		Mode:    outputMode{json: true},
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"status":"flag_invalid","message":"missing --types"}` + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// TestReportFailurePlainModeWritesBothStreams locks the --plain failure
// shape: `<cmd>: <msg>\n` to stderr AND `status: <status>\nmessage:
// <msg>\n` to stdout. The two-stream output keeps stderr useful for
// terminal users while stdout carries the machine-readable block.
func TestReportFailurePlainModeWritesBothStreams(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := ReportFailure(FailureReport{
		Command: "sync",
		Status:  StatusFlagInvalid,
		Message: "missing --types",
		Mode:    outputMode{plain: true},
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.String() != "sync: missing --types\n" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), "sync: missing --types\n")
	}
	want := "status: flag_invalid\nmessage: missing --types\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

// TestReportFailureSetupMissingReturnsTwo locks the one exit code that
// diverges from 1: `StatusSetupMissing` returns 2 to match the existing
// `setupMissingExitCode` the doctor command was using directly. This is
// the only status whose semantic is "your environment is not ready" as
// distinct from "the operation failed".
func TestReportFailureSetupMissingReturnsTwo(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := ReportFailure(FailureReport{
		Command: "doctor",
		Status:  StatusSetupMissing,
		Message: "run `gohealthcli init` to create local config and Health Archive",
	}, &stdout, &stderr)

	if code != setupMissingExitCode {
		t.Fatalf("exit code = %d, want %d", code, setupMissingExitCode)
	}
}

// TestReportFailureEmptyCommandUsesBinaryName locks the top-level
// surface: when Command is empty (e.g. unknown-command, top-level flag
// parse), the prefix is the binary name `gohealthcli` rather than a
// bare `: <msg>` line.
func TestReportFailureEmptyCommandUsesBinaryName(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := ReportFailure(FailureReport{
		Command: "",
		Status:  StatusFlagInvalid,
		Message: "--plain and --json are mutually exclusive",
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.String() != "gohealthcli: --plain and --json are mutually exclusive\n" {
		t.Fatalf("stderr = %q, want gohealthcli-prefixed line", stderr.String())
	}
}

// TestReportFailureTableEnumeratesEveryStatusEveryMode is the
// table-driven enumeration the issue's AC calls out: every
// FailureStatus paired with every outputMode, asserting exact bytes
// and exit code. The matrix is the deletion test — if ReportFailure
// ever forgets a status or mode, the wholesale set fails.
func TestReportFailureTableEnumeratesEveryStatusEveryMode(t *testing.T) {
	t.Parallel()
	statuses := []struct {
		status   FailureStatus
		wantCode int
	}{
		{StatusFlagInvalid, 1},
		{StatusUnexpectedArgument, 1},
		{StatusSetupMissing, setupMissingExitCode},
		{StatusArchiveUnwritable, 1},
		{StatusProviderUnreachable, 1},
		{StatusOperationFailed, 1},
	}
	modes := []struct {
		name string
		mode outputMode
	}{
		{"default", outputMode{}},
		{"plain", outputMode{plain: true}},
		{"json", outputMode{json: true}},
	}
	for _, s := range statuses {
		for _, m := range modes {
			t.Run(string(s.status)+"/"+m.name, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				report := FailureReport{
					Command: "sync",
					Status:  s.status,
					Message: "boom",
					Mode:    m.mode,
				}
				code := ReportFailure(report, &stdout, &stderr)
				if code != s.wantCode {
					t.Fatalf("exit code = %d, want %d", code, s.wantCode)
				}
				switch {
				case m.mode.json:
					want := `{"status":"` + string(s.status) + `","message":"boom"}` + "\n"
					if stdout.String() != want {
						t.Fatalf("stdout = %q, want %q", stdout.String(), want)
					}
					if stderr.Len() != 0 {
						t.Fatalf("stderr = %q, want empty", stderr.String())
					}
				case m.mode.plain:
					if stderr.String() != "sync: boom\n" {
						t.Fatalf("stderr = %q, want sync prefix", stderr.String())
					}
					want := "status: " + string(s.status) + "\nmessage: boom\n"
					if stdout.String() != want {
						t.Fatalf("stdout = %q, want %q", stdout.String(), want)
					}
				default:
					if stderr.String() != "sync: boom\n" {
						t.Fatalf("stderr = %q, want sync prefix", stderr.String())
					}
					if stdout.Len() != 0 {
						t.Fatalf("stdout = %q, want empty", stdout.String())
					}
				}
			})
		}
	}
}

// TestReportFailureJSONFailsOverToStderrWhenStdoutBroken locks the
// broken-stdout fallback: the migrated `write output:` paths route
// failures back through ReportFailure after the result writer already
// failed once. If --json mode tried the same broken stdout again, the
// operator would see nothing at all; the fallback writes the bare
// `<cmd>: <msg>` line on stderr so a write-output failure always
// surfaces somewhere.
func TestReportFailureJSONFailsOverToStderrWhenStdoutBroken(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	stdout := failingFailureWriter{}
	code := ReportFailure(FailureReport{
		Command: "init",
		Status:  StatusArchiveUnwritable,
		Message: "write output: pipe closed",
		Mode:    outputMode{json: true},
	}, stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.String() != "init: write output: pipe closed\n" {
		t.Fatalf("stderr = %q, want init-prefixed fallback line", stderr.String())
	}
}

// failingFailureWriter is the io.Writer that always errors. Used by
// TestReportFailureJSONFailsOverToStderrWhenStdoutBroken to exercise
// the broken-stdout fallback without touching anything else.
type failingFailureWriter struct{}

func (failingFailureWriter) Write(p []byte) (int, error) {
	return 0, errFailingFailureWriter
}

var errFailingFailureWriter = errorString("failing failure writer")

type errorString string

func (e errorString) Error() string { return string(e) }

// TestReportFailureJSONEscapesMessageContents guards the JSON branch
// against unescaped quotes / backslashes in the message: the body uses
// encoding/json so values containing `"` or `\` produce valid JSON
// rather than a corrupt line. This is the bug a hand-rolled
// fmt.Fprintf would introduce.
func TestReportFailureJSONEscapesMessageContents(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := ReportFailure(FailureReport{
		Command: "raw",
		Status:  StatusOperationFailed,
		Message: `unexpected character "x" in token`,
		Mode:    outputMode{json: true},
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), `\"x\"`) {
		t.Fatalf("stdout = %q, want escaped quotes", stdout.String())
	}
	// The body must still parse as JSON, so confirm the brace shape rather
	// than fight with json.Marshal's escape choices.
	if !strings.HasPrefix(stdout.String(), `{"status":"operation_failed"`) {
		t.Fatalf("stdout = %q, want operation_failed envelope", stdout.String())
	}
	if !strings.HasSuffix(stdout.String(), "}\n") {
		t.Fatalf("stdout = %q, want trailing newline-after-brace", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

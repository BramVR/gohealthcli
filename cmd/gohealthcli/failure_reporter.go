package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// FailureStatus is the six-valued enum the unified Failure Reporter uses
// to classify every CLI failure exit. Each status maps to a single
// user-facing concept the operator can reason about:
//
//   - StatusFlagInvalid          — flag parse, mutual-exclusion, unknown flag,
//     missing required flag, "no SQL statement".
//   - StatusUnexpectedArgument   — positional arg the subcommand did not expect
//     ("unexpected sync argument: foo").
//   - StatusSetupMissing         — config + Health Archive absent; the only
//     status that returns exit code 2 to match the
//     prior `setupMissingExitCode` semantics.
//   - StatusArchiveUnwritable    — writing stdout/stdout-result/export-file
//     failed at the io.Writer layer.
//   - StatusProviderUnreachable  — non-auth Provider HTTP failure (403,
//     5xx, ...) or a network failure reaching the
//     Provider, classified by the typed-error
//     translation layer (issue #272). Upstream 401
//     auth rejections are NOT unreachable — they
//     surface as StatusOperationFailed carrying the
//     "run `gohealthcli connect` again" message.
//   - StatusOperationFailed      — any other operation error (DB, schema,
//     config check, OAuth, etc.).
//
// The enum's wire shape (snake_case strings) is part of the --json
// failure contract; downstream tooling pivots on the literal value.
type FailureStatus string

const (
	StatusFlagInvalid         FailureStatus = "flag_invalid"
	StatusUnexpectedArgument  FailureStatus = "unexpected_argument"
	StatusSetupMissing        FailureStatus = "setup_missing"
	StatusArchiveUnwritable   FailureStatus = "archive_unwritable"
	StatusProviderUnreachable FailureStatus = "provider_unreachable"
	StatusOperationFailed     FailureStatus = "operation_failed"
)

// FailureReport carries one failure's worth of context: which subcommand
// hit it (Command, empty for the top-level surface), how it classifies
// (Status), what to tell the operator (Message — single line, no
// trailing newline), which output mode the invocation was running in
// (Mode), and an optional typed Cause whose Reporter-owned remediation
// actions may be rendered in structured modes. Message remains separate
// so adding a cause never changes the stable user-facing text.
type FailureReport struct {
	Command string
	Status  FailureStatus
	Message string
	Mode    outputMode
	Cause   error
}

type remediationAction string

const (
	remediationShowHelp   remediationAction = "gohealthcli --help"
	remediationRunDoctor  remediationAction = "gohealthcli doctor"
	remediationReconnect  remediationAction = "gohealthcli connect"
	remediationStatus     remediationAction = "gohealthcli status"
	maxRemediationActions                   = 3
)

// remediationError is the only carrier the Failure Reporter reads.
// Unwrap preserves the original error chain for errors.Is/errors.As;
// actions are fixed Reporter vocabulary rather than caller text.
type remediationError struct {
	cause   error
	actions []string
}

func (err *remediationError) Error() string { return err.cause.Error() }
func (err *remediationError) Unwrap() error { return err.cause }

func (err *remediationError) Remediation() []string {
	return append([]string(nil), err.actions...)
}

func withRemediation(cause error, actions ...remediationAction) error {
	return &remediationError{cause: cause, actions: normalizeRemediation(actions)}
}

func unknownCommandError(command string) error {
	return withRemediation(fmt.Errorf("unknown command: %s", command), remediationShowHelp)
}

// normalizeRemediation collapses whitespace, removes duplicates without
// reordering, rejects anything outside the fixed Reporter catalog, and
// caps the structured output. The catalog contains only argument-free
// gohealthcli commands, so paths, identifiers, Provider text, SQL, health
// data, secrets, and echoed user input cannot cross this boundary.
func normalizeRemediation(actions []remediationAction) []string {
	remediation := make([]string, 0, min(len(actions), maxRemediationActions))
	seen := make(map[string]struct{}, len(actions))
	for _, action := range actions {
		normalized := strings.Join(strings.Fields(string(action)), " ")
		if !isReporterRemediation(normalized) {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		remediation = append(remediation, normalized)
		if len(remediation) == maxRemediationActions {
			break
		}
	}
	return remediation
}

func isReporterRemediation(action string) bool {
	switch remediationAction(action) {
	case remediationShowHelp, remediationRunDoctor, remediationReconnect, remediationStatus:
		return true
	default:
		return false
	}
}

func remediationFromError(err error) []string {
	var carrier *remediationError
	if !errors.As(err, &carrier) {
		return nil
	}
	return carrier.Remediation()
}

// failureJSONEnvelope is the on-the-wire shape of `--json` failure
// output. remediation is additive and omitted when empty. Field order
// (status, message, remediation) follows struct declaration order, which
// encoding/json guarantees, so emitted bytes stay stable across builds.
type failureJSONEnvelope struct {
	Status      FailureStatus `json:"status"`
	Message     string        `json:"message"`
	Remediation []string      `json:"remediation,omitempty"`
}

// ReportFailure writes a FailureReport in the requested output mode and
// returns the process exit code:
//
//   - default (no --plain, no --json): `<cmd>: <message>\n` to stderr.
//     Empty Command uses `gohealthcli` instead of an empty prefix.
//   - --plain: same stderr line AND `status: <s>\nmessage: <m>\n` block,
//     followed by zero-based remediation.N lines when present, on stdout.
//     Terminal users see the prefix and scripts can parse stdout.
//   - --json: a single line `{"status":"<s>","message":"<m>"}\n` on
//     stdout, nothing on stderr. encoding/json escapes the message so a
//     payload with quotes or backslashes cannot corrupt the envelope.
//
// Exit code is 2 for StatusSetupMissing (matching the existing
// setupMissingExitCode the doctor command relied on), 1 for every other
// status. The exit-code map lives inside this function so call sites
// never need to reference setupMissingExitCode directly — that's the
// whole point of the seam.
func ReportFailure(report FailureReport, stdout, stderr io.Writer) int {
	prefix := report.Command
	if prefix == "" {
		prefix = "gohealthcli"
	}

	switch {
	case report.Mode.json:
		// json.Marshal (not Encoder.Encode) so we own the trailing newline
		// exactly. encoding/json escapes the message contents; a
		// hand-rolled fmt.Fprintf would not.
		payload, err := json.Marshal(failureJSONEnvelope{
			Status:      report.Status,
			Message:     report.Message,
			Remediation: remediationFromError(report.Cause),
		})
		// If json.Marshal fails (struct of one FailureStatus + one
		// string — should never happen) OR stdout itself rejects the
		// write (the broken-stdout failure path migrates `write
		// output:` errors back through ReportFailure; writing the
		// envelope to that same broken stdout would also fail
		// silently), fall back to the bare `<cmd>: <msg>` line on
		// stderr so the operator still gets a signal.
		if err == nil {
			if _, writeErr := fmt.Fprintf(stdout, "%s\n", payload); writeErr == nil {
				break
			}
		}
		fmt.Fprintf(stderr, "%s: %s\n", prefix, report.Message)
	case report.Mode.plain:
		// Two-stream output: terminal users see the `<cmd>: <msg>` line
		// on stderr exactly like the default mode, AND stdout gets the
		// parseable status/message block. Scripts pivoting on stdout
		// can rely on the block shape; tail/grep on stderr still works.
		// The stderr line is always written even if the stdout block
		// fails, so a broken-stdout failure path still surfaces.
		fmt.Fprintf(stderr, "%s: %s\n", prefix, report.Message)
		fmt.Fprintf(stdout, "status: %s\nmessage: %s\n", report.Status, report.Message)
		for index, action := range remediationFromError(report.Cause) {
			fmt.Fprintf(stdout, "remediation.%d: %s\n", index, action)
		}
	default:
		fmt.Fprintf(stderr, "%s: %s\n", prefix, report.Message)
	}

	return failureExitCode(report.Status)
}

// failureExitCode is the single source of truth for the status →
// exit-code mapping. Today only StatusSetupMissing diverges from 1
// (returning 2 to match the existing `setupMissingExitCode` doctor
// relied on). Callers that already own their stderr shape (the doctor
// setup_missing path emits a structured envelope to stdout plus a
// hint line to stderr that pre-date the reporter) reach for this
// helper rather than ReportFailure so they get the unified exit-code
// semantics without a second write pass clobbering their envelope.
// Every other site uses ReportFailure end-to-end.
func failureExitCode(status FailureStatus) int {
	if status == StatusSetupMissing {
		return setupMissingExitCode
	}
	return 1
}

const setupMissingExitCode = 2

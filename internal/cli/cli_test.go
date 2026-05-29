package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/cli"
	"github.com/BramVR/gohealthcli/internal/version"
)

// run executes the CLI in-process, capturing stdout/stderr and the exit code.
func run(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = cli.Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// missingPaths returns config and db paths inside a fresh temp dir that do not
// exist yet, modeling a doctor run before init.
func missingPaths(t *testing.T) (configPath, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "config.toml"), filepath.Join(dir, "archive.sqlite")
}

func assertNoSecrets(t *testing.T, label, s string) {
	t.Helper()
	for _, banned := range []string{"token", "secret", "password", "refresh_token"} {
		if strings.Contains(strings.ToLower(s), banned) {
			t.Errorf("%s leaked sensitive substring %q: %s", label, banned, s)
		}
	}
}

func TestDoctorJSONSetupMissing(t *testing.T) {
	cfg, db := missingPaths(t)

	code, stdout, stderr := run("doctor", "--json", "--config", cfg, "--db", db)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 for setup-missing", code)
	}

	var report struct {
		Status string `json:"status"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if report.Status != "setup_missing" {
		t.Errorf("status = %q, want setup_missing", report.Status)
	}
	if len(report.Checks) == 0 {
		t.Error("expected at least one check in the report")
	}

	// The human hint must be on stderr, not stdout.
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected a human hint on stderr, got none")
	}
	if strings.Contains(stderr, "{") {
		t.Errorf("machine-readable JSON leaked to stderr: %s", stderr)
	}

	assertNoSecrets(t, "stdout", stdout)
	assertNoSecrets(t, "stderr", stderr)
}

func TestDoctorPlainSetupMissing(t *testing.T) {
	cfg, db := missingPaths(t)

	code, stdout, stderr := run("doctor", "--plain", "--config", cfg, "--db", db)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 for setup-missing", code)
	}
	if !strings.Contains(stdout, "status: setup_missing") {
		t.Errorf("plain stdout missing stable status line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "config: missing") || !strings.Contains(stdout, "archive: missing") {
		t.Errorf("plain stdout missing stable check lines:\n%s", stdout)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected a human hint on stderr, got none")
	}
}

func TestDoctorHumanRoutesHintsToStderr(t *testing.T) {
	cfg, db := missingPaths(t)

	code, stdout, stderr := run("doctor", "--config", cfg, "--db", db)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 for setup-missing", code)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("expected human status output on stdout, got none")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected a human hint on stderr, got none")
	}
	// Machine-readable data must never appear on stderr.
	if strings.Contains(stderr, "{") || strings.Contains(stderr, "status: setup_missing") {
		t.Errorf("machine-readable data leaked to stderr: %s", stderr)
	}
}

func TestDoctorSetupPresent(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	db := filepath.Join(dir, "archive.sqlite")
	for _, p := range []string{cfg, db} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("seeding %s: %v", p, err)
		}
	}

	code, stdout, stderr := run("doctor", "--json", "--config", cfg, "--db", db)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for healthy setup\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"status": "ok"`) {
		t.Errorf("expected ok status in stdout:\n%s", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("expected no hints on stderr for healthy setup, got: %s", stderr)
	}
}

func TestVersionPrintsAndSkipsSetupCheck(t *testing.T) {
	// Even with a config/db path that does not exist, --version must not run a
	// setup check; it prints and exits 0.
	cfg, db := missingPaths(t)

	code, stdout, stderr := run("--version", "--config", cfg, "--db", db)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, version.String()) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, version.String())
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("expected clean stderr for --version, got: %s", stderr)
	}
}

func TestVersionAfterCommandStillSkipsSetupCheck(t *testing.T) {
	// --version should win even when written after a command.
	code, stdout, _ := run("doctor", "--version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, version.String()) {
		t.Errorf("stdout = %q, want version string", stdout)
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	code, stdout, stderr := run("frobnicate")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for unknown command", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected nothing on stdout for usage error, got: %s", stdout)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("expected unknown-command error on stderr, got: %s", stderr)
	}
}

func TestHelpExitsZero(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		code, _, _ := run(arg)
		if code != 0 {
			t.Errorf("%s exit code = %d, want 0", arg, code)
		}
	}
}

func TestUnexpectedPositionalArgsAreRejected(t *testing.T) {
	code, stdout, stderr := run("doctor", "extra")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for unexpected args", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected nothing on stdout, got: %s", stdout)
	}
	if !strings.Contains(stderr, "unexpected arguments") {
		t.Errorf("expected unexpected-arguments error on stderr, got: %s", stderr)
	}
}

func TestJSONAndPlainAreMutuallyExclusive(t *testing.T) {
	cfg, db := missingPaths(t)
	code, _, stderr := run("doctor", "--json", "--plain", "--config", cfg, "--db", db)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for conflicting modes", code)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("expected mutual-exclusion error on stderr, got: %s", stderr)
	}
}

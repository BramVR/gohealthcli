package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var testBinaryPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gohealthcli-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	testBinaryPath = filepath.Join(dir, "gohealthcli")
	build := exec.Command("go", "build", "-o", testBinaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build command: %v\n%s", err, string(output))
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestDoctorJSONReportsMissingSetup(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	archivePath := filepath.Join(tempDir, "gohealthcli.sqlite")

	code, stdout, stderr := runCommand(t,
		"--config", configPath,
		"--db", archivePath,
		"--json",
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}

	if got["status"] != "setup_missing" {
		t.Fatalf("status = %v, want setup_missing", got["status"])
	}
	assertJSONString(t, got, "config_path", configPath)
	assertJSONString(t, got, "archive_path", archivePath)

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestDoctorJSONReportsMissingSetupAfterCommand(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"doctor",
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--json",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if got["status"] != "setup_missing" {
		t.Fatalf("status = %v, want setup_missing", got["status"])
	}
	if got["config_path"] != filepath.Join(tempDir, "config.toml") {
		t.Fatalf("config_path = %v, want command flag value", got["config_path"])
	}
	if got["archive_path"] != filepath.Join(tempDir, "gohealthcli.sqlite") {
		t.Fatalf("archive_path = %v, want command flag value", got["archive_path"])
	}

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestDoctorRejectsUnknownFlagAfterCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "doctor", "--bogus")

	if code == 0 || code == 2 {
		t.Fatalf("exit code = %d, want flag error", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr missing flag error: %q", stderr.String())
	}
}

func TestDoctorAcceptsNoInputBeforeCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "--no-input", "doctor", "--plain")

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "status: setup_missing\n") {
		t.Fatalf("stdout missing setup status: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", stderr.String())
	}
}

func TestDoctorAcceptsNoInputAfterCommand(t *testing.T) {
	code, stdout, stderr := runCommand(t, "doctor", "--no-input", "--plain")

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "status: setup_missing\n") {
		t.Fatalf("stdout missing setup status: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", stderr.String())
	}
}

func TestDoctorPlainReportsMissingSetup(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"--plain",
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	outText := stdout.String()
	for _, want := range []string{
		"status: setup_missing\n",
		"config_path: " + filepath.Join(tempDir, "config.toml") + "\n",
		"archive_path: " + filepath.Join(tempDir, "gohealthcli.sqlite") + "\n",
	} {
		if !strings.Contains(outText, want) {
			t.Fatalf("stdout missing %q:\n%s", want, outText)
		}
	}

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestDoctorHumanReportsMissingSetup(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "config.toml"),
		"--db", filepath.Join(tempDir, "gohealthcli.sqlite"),
		"doctor",
	)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	outText := stdout.String()
	if !strings.Contains(outText, "Setup missing") {
		t.Fatalf("stdout missing human setup status: %q", outText)
	}
	if strings.Contains(outText, "run `gohealthcli init`") {
		t.Fatalf("stdout contains human hint that should be stderr: %q", outText)
	}

	errText := stderr.String()
	if !strings.Contains(errText, "run `gohealthcli init`") {
		t.Fatalf("stderr missing init hint: %q", errText)
	}
	assertNoSecretWords(t, stdout.String()+errText)
}

func TestVersionDoesNotCheckSetup(t *testing.T) {
	tempDir := t.TempDir()

	code, stdout, stderr := runCommand(t,
		"--config", filepath.Join(tempDir, "missing-config.toml"),
		"--db", filepath.Join(tempDir, "missing.sqlite"),
		"--version",
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "gohealthcli dev" {
		t.Fatalf("version stdout = %q, want %q", got, "gohealthcli dev")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestHelpExitsSuccessfully(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"doctor", "--help"},
	} {
		code, stdout, stderr := runCommand(t, args...)

		if code != 0 {
			t.Fatalf("%v exit code = %d, want 0\nstderr: %s", args, code, stderr.String())
		}
		if stdout.String() != "" {
			t.Fatalf("%v stdout = %q, want empty", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "Usage of") {
			t.Fatalf("%v stderr missing usage: %q", args, stderr.String())
		}
	}
}

func TestDoctorDefaultPathsAreUsable(t *testing.T) {
	home := t.TempDir()
	xdgConfig := filepath.Join(home, "xdg-config")
	xdgData := filepath.Join(home, "xdg-data")

	code, stdout, stderr := runCommandWithEnv(t,
		[]string{
			"HOME=" + home,
			"XDG_CONFIG_HOME=" + xdgConfig,
			"XDG_DATA_HOME=" + xdgData,
		},
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
	if got["config_path"] != filepath.Join(xdgConfig, "gohealthcli", "config.toml") {
		t.Fatalf("config_path = %v, want XDG config path", got["config_path"])
	}
	if got["archive_path"] != filepath.Join(xdgData, "gohealthcli", "gohealthcli.sqlite") {
		t.Fatalf("archive_path = %v, want XDG data path", got["archive_path"])
	}
	if strings.Contains(stdout.String(), "~") {
		t.Fatalf("stdout contains unexpanded home path: %s", stdout.String())
	}
}

func runCommand(t *testing.T, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	return runCommandWithEnv(t, nil, args...)
}

func runCommandWithEnv(t *testing.T, env []string, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd := exec.Command(testBinaryPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), env...)

	err := cmd.Run()
	if err == nil {
		return 0, stdout, stderr
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), stdout, stderr
	}
	t.Fatalf("run command: %v\nstderr: %s", err, stderr.String())
	return 1, stdout, stderr
}

func assertJSONString(t *testing.T, got map[string]any, key, want string) {
	t.Helper()

	value, ok := got[key].(string)
	if !ok {
		t.Fatalf("%s = %T(%v), want string %q", key, got[key], got[key], want)
	}
	if value != want {
		t.Fatalf("%s = %q, want %q", key, value, want)
	}
}

func assertNoSecretWords(t *testing.T, text string) {
	t.Helper()
	for _, word := range []string{"access_token", "refresh_token", "client_secret"} {
		if strings.Contains(text, word) {
			t.Fatalf("output leaked %s: %s", word, text)
		}
	}
}

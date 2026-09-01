package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

var errSyntheticRawOutputWrite = errors.New("synthetic output write failure")

func requireRawOutputPlatform(t *testing.T) {
	t.Helper()
	if err := rawOutputPlatformSupported(); err != nil {
		t.Skip(err)
	}
}

func TestRawOutputWritesExactProviderBytesToNewPrivateFile(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	payload := []byte(" {\"dataPoints\":[{\"value\":7}]} ")
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(googlehealth.RawRequest) []byte {
		return payload
	})
	outputPath := filepath.Join(t.TempDir(), "response.json")

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw", "data-type", "steps",
		"--from", "2026-01-01",
		"--to", "2026-01-02",
		"--config", configPath,
		"--db", archivePath,
		"--output", outputPath,
	}, stdout, stderr, testRuntime)
	if code != 0 {
		t.Fatalf("raw --output exit code = %d, want 0\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty for file output", stdout.String())
	}
	wantStatus := fmt.Sprintf("raw: wrote %d bytes to %q\n", len(payload), outputPath)
	if stderr.String() != wantStatus {
		t.Fatalf("stderr = %q, want status %q", stderr.String(), wantStatus)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read raw output: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("raw output bytes = %q, want exact Provider bytes %q", got, payload)
	}
	if usesPOSIXPermissions() {
		info, err := os.Stat(outputPath)
		if err != nil {
			t.Fatalf("stat raw output: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("raw output mode = %04o, want 0600", info.Mode().Perm())
		}
	}
	assertArchiveTableCount(t, archivePath, "data_points", 0)
	assertArchiveTableCount(t, archivePath, "sync_runs", 0)
}

func TestRawOutputRefusesEveryExistingDestinationWithoutProviderRead(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	tempDir := t.TempDir()
	tests := []struct {
		name string
		seed func(string) error
	}{
		{name: "regular file", seed: func(path string) error {
			if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
				return err
			}
			if usesPOSIXPermissions() {
				return os.Chmod(path, 0o644)
			}
			return nil
		}},
		{name: "directory", seed: func(path string) error { return os.Mkdir(path, 0o700) }},
	}
	tests = append(tests, struct {
		name string
		seed func(string) error
	}{name: "symbolic link", seed: func(path string) error {
		target := filepath.Join(tempDir, "symlink-target")
		if err := os.WriteFile(target, []byte("keep-target"), 0o600); err != nil {
			return err
		}
		return os.Symlink(target, path)
	}})

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outputPath := filepath.Join(tempDir, strings.ReplaceAll(test.name, " ", "-"))
			if err := test.seed(outputPath); err != nil {
				if test.name == "symbolic link" {
					t.Skipf("symbolic link setup is unavailable: %v", err)
				}
				t.Fatalf("seed destination: %v", err)
			}
			before, _ := os.ReadFile(outputPath)
			beforeInfo, _ := os.Lstat(outputPath)
			testRuntime := runtimeAdapters{
				fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
					t.Fatal("Provider must not run for an invalid --output destination")
					return nil, nil
				},
			}
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := runWithRuntime([]string{
				"raw", "endpoint", "getIdentity",
				"--config", filepath.Join(t.TempDir(), "missing-config.toml"),
				"--db", filepath.Join(t.TempDir(), "missing-archive.sqlite"),
				"--output", outputPath,
			}, stdout, stderr, testRuntime)
			if code == 0 {
				t.Fatalf("raw --output exit code = 0, want refusal\nstderr: %s", stderr.String())
			}
			if !strings.Contains(stderr.String(), "--output") {
				t.Fatalf("stderr = %q, want --output refusal", stderr.String())
			}
			if strings.Contains(test.name, "symbolic") && !strings.Contains(stderr.String(), "symbolic link") {
				t.Fatalf("stderr = %q, want symbolic link refusal", stderr.String())
			}
			if test.name == "regular file" {
				after, err := os.ReadFile(outputPath)
				if err != nil {
					t.Fatalf("read existing destination: %v", err)
				}
				if !bytes.Equal(after, before) {
					t.Fatalf("existing destination changed from %q to %q", before, after)
				}
				if usesPOSIXPermissions() {
					afterInfo, err := os.Lstat(outputPath)
					if err != nil {
						t.Fatalf("lstat existing destination: %v", err)
					}
					if afterInfo.Mode().Perm() != beforeInfo.Mode().Perm() {
						t.Fatalf("existing destination mode changed from %04o to %04o", beforeInfo.Mode().Perm(), afterInfo.Mode().Perm())
					}
				}
			}
		})
	}
}

func TestRawOutputRejectsInvalidParentTypeBeforeProviderRead(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	tempDir := t.TempDir()
	fileParent := filepath.Join(tempDir, "not-a-directory")
	if err := os.WriteFile(fileParent, []byte("keep-parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		parentPath string
		want       string
	}{
		{name: "file", parentPath: fileParent, want: "is not a directory"},
		{name: "missing", parentPath: filepath.Join(tempDir, "missing"), want: "inspect --output parent"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testRuntime := runtimeAdapters{
				fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
					t.Fatal("Provider must not run for an invalid --output parent")
					return nil, nil
				},
			}
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := runWithRuntime([]string{
				"raw", "endpoint", "getIdentity",
				"--config", filepath.Join(t.TempDir(), "missing-config.toml"),
				"--db", filepath.Join(t.TempDir(), "missing-archive.sqlite"),
				"--output", filepath.Join(test.parentPath, "response.json"),
			}, stdout, stderr, testRuntime)
			if code == 0 {
				t.Fatalf("raw --output exit code = 0, want invalid-parent refusal")
			}
			if !strings.Contains(stderr.String(), "parent") || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want parent error %q", stderr.String(), test.want)
			}
		})
	}
}

func TestRawOutputRejectsInvalidFlagCombinationsBeforeSetup(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "plan", args: []string{"--plan", "--output", "response.json"}, want: "--output is not supported with --plan"},
		{name: "empty path", args: []string{"--output", ""}, want: "--output requires a non-empty path"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testRuntime := runtimeAdapters{
				fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
					t.Fatal("Provider must not run for invalid --output flags")
					return nil, nil
				},
			}
			args := append([]string{"raw", "endpoint", "getIdentity"}, test.args...)
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			code := runWithRuntime(args, stdout, stderr, testRuntime)
			if code == 0 {
				t.Fatalf("raw invalid --output exit code = 0, want failure")
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

type shortRawOutputDestination struct {
	rawOutputDestination
}

func (destination *shortRawOutputDestination) Write(payload []byte) (int, error) {
	return destination.rawOutputDestination.Write(payload[:len(payload)/2])
}

type failingRawOutputDestination struct {
	rawOutputDestination
}

type chmodFailingRawOutputDestination struct {
	rawOutputDestination
}

func (destination *chmodFailingRawOutputDestination) Chmod(os.FileMode) error {
	return errSyntheticRawOutputWrite
}

type closeFailingRawOutputDestination struct {
	rawOutputDestination
}

func (destination *closeFailingRawOutputDestination) Close() error {
	_ = destination.rawOutputDestination.Close()
	return errSyntheticRawOutputWrite
}

func (destination *failingRawOutputDestination) Write(payload []byte) (int, error) {
	written, _ := destination.rawOutputDestination.Write(payload[:1])
	return written, errSyntheticRawOutputWrite
}

type racingCommitRawOutputDestination struct {
	rawOutputDestination
	targetPath string
}

func (destination *racingCommitRawOutputDestination) Commit() error {
	if err := os.WriteFile(destination.targetPath, []byte("concurrent replacement"), 0o600); err != nil {
		return err
	}
	return destination.rawOutputDestination.Commit()
}

func TestWriteRawOutputFileCleansUpShortAndFailedWrites(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	payload := []byte("synthetic-provider-response")
	tests := []struct {
		name      string
		wrap      func(rawOutputDestination) rawOutputDestination
		wantError error
	}{
		{name: "short write", wrap: func(destination rawOutputDestination) rawOutputDestination {
			return &shortRawOutputDestination{rawOutputDestination: destination}
		}, wantError: io.ErrShortWrite},
		{name: "write error", wrap: func(destination rawOutputDestination) rawOutputDestination {
			return &failingRawOutputDestination{rawOutputDestination: destination}
		}, wantError: errSyntheticRawOutputWrite},
		{name: "close error", wrap: func(destination rawOutputDestination) rawOutputDestination {
			return &closeFailingRawOutputDestination{rawOutputDestination: destination}
		}, wantError: errSyntheticRawOutputWrite},
	}
	if usesPOSIXPermissions() {
		tests = append(tests, struct {
			name      string
			wrap      func(rawOutputDestination) rawOutputDestination
			wantError error
		}{name: "chmod error", wrap: func(destination rawOutputDestination) rawOutputDestination {
			return &chmodFailingRawOutputDestination{rawOutputDestination: destination}
		}, wantError: errSyntheticRawOutputWrite})
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outputPath := filepath.Join(t.TempDir(), "response.json")
			openDestination := func(path string) (rawOutputDestination, error) {
				destination, err := openStagedRawOutput(path)
				if err != nil {
					return nil, err
				}
				return test.wrap(destination), nil
			}
			_, err := writeRawOutputFileWithOpen(outputPath, payload, openDestination)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("writeRawOutputFileWithOpen error = %v, want %v", err, test.wantError)
			}
			if _, statErr := os.Lstat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed raw output left destination behind: %v", statErr)
			}
			stagingFiles, err := filepath.Glob(filepath.Join(filepath.Dir(outputPath), ".gohealthcli-raw-*"))
			if err != nil {
				t.Fatalf("glob staging files: %v", err)
			}
			if len(stagingFiles) != 0 {
				t.Fatalf("failed raw output left staging files: %v", stagingFiles)
			}
		})
	}
}

func TestRawOutputPublishPreservesConcurrentReplacement(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "response.json")
	openDestination := func(path string) (rawOutputDestination, error) {
		destination, err := openStagedRawOutput(path)
		if err != nil {
			return nil, err
		}
		return &racingCommitRawOutputDestination{rawOutputDestination: destination, targetPath: path}, nil
	}
	_, err := writeRawOutputFileWithOpen(outputPath, []byte("synthetic-provider-response"), openDestination)
	if err == nil {
		t.Fatal("writeRawOutputFileWithOpen error = nil, want no-clobber failure")
	}
	replacement, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if string(replacement) != "concurrent replacement" {
		t.Fatalf("replacement = %q, want unchanged", replacement)
	}
	stagingFiles, err := filepath.Glob(filepath.Join(tempDir, ".gohealthcli-raw-*"))
	if err != nil {
		t.Fatalf("glob staging files: %v", err)
	}
	if len(stagingFiles) != 0 {
		t.Fatalf("failed publish left staging files: %v", stagingFiles)
	}
}

func TestRawOutputErrorNeverContainsProviderBytes(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	payload := []byte("{\"syntheticSensitiveValue\":\"never-print-this\"}")
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(googlehealth.RawRequest) []byte { return payload })
	testRuntime.prepareRawOutput = func(path string) (preparedRawOutput, error) {
		destination, err := openStagedRawOutput(path)
		if err != nil {
			return nil, err
		}
		return &preparedRawOutputFile{path: path, destination: &failingRawOutputDestination{rawOutputDestination: destination}}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw", "endpoint", "getIdentity",
		"--config", configPath,
		"--db", archivePath,
		"--output", filepath.Join(t.TempDir(), "response.json"),
	}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatal("raw --output exit code = 0, want write failure")
	}
	combined := append(append([]byte(nil), stdout.Bytes()...), stderr.Bytes()...)
	if bytes.Contains(combined, payload) || strings.Contains(string(combined), "never-print-this") {
		t.Fatalf("raw output error leaked Provider bytes: %q", combined)
	}
	if !strings.Contains(stderr.String(), errSyntheticRawOutputWrite.Error()) {
		t.Fatalf("stderr = %q, want write failure", stderr.String())
	}
}

func TestRawOutputFailureUsesOperationFailedStatus(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(googlehealth.RawRequest) []byte {
		return []byte("synthetic-provider-response")
	})
	testRuntime.prepareRawOutput = func(path string) (preparedRawOutput, error) {
		destination, err := openStagedRawOutput(path)
		if err != nil {
			return nil, err
		}
		return &preparedRawOutputFile{path: path, destination: &failingRawOutputDestination{rawOutputDestination: destination}}, nil
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"--json",
		"raw", "endpoint", "getIdentity",
		"--config", configPath,
		"--db", archivePath,
		"--output", filepath.Join(t.TempDir(), "response.json"),
	}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatal("raw --output JSON failure exit code = 0, want failure")
	}
	var failure failureJSONEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &failure); err != nil {
		t.Fatalf("decode raw --output failure: %v; stdout=%q", err, stdout.String())
	}
	if failure.Status != StatusOperationFailed {
		t.Fatalf("failure status = %q, want %q", failure.Status, StatusOperationFailed)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty JSON failure stream", stderr.String())
	}
}

func TestRawOutputPreparationIOFailureUsesOperationFailedStatus(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.prepareRawOutput = func(string) (preparedRawOutput, error) {
		return nil, errSyntheticRawOutputWrite
	}
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		t.Fatal("Provider must not run after output preparation failure")
		return nil, nil
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"--json",
		"raw", "endpoint", "getIdentity",
		"--config", configPath,
		"--db", archivePath,
		"--output", filepath.Join(t.TempDir(), "response.json"),
	}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatal("raw preparation failure exit code = 0, want failure")
	}
	var failure failureJSONEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &failure); err != nil {
		t.Fatalf("decode raw preparation failure: %v; stdout=%q", err, stdout.String())
	}
	if failure.Status != StatusOperationFailed {
		t.Fatalf("failure status = %q, want %q", failure.Status, StatusOperationFailed)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty JSON failure stream", stderr.String())
	}
}

func TestRawOutputNoClobberCheckRepeatsAfterProviderRead(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	outputPath := filepath.Join(t.TempDir(), "response.json")
	payload := []byte("synthetic-provider-response")
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(googlehealth.RawRequest) []byte {
		if err := os.WriteFile(outputPath, []byte("racing destination"), 0o600); err != nil {
			t.Fatalf("seed racing destination: %v", err)
		}
		return payload
	})

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw", "endpoint", "getIdentity",
		"--config", configPath,
		"--db", archivePath,
		"--output", outputPath,
	}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatal("raw racing --output exit code = 0, want no-clobber failure")
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read racing destination: %v", err)
	}
	if string(got) != "racing destination" {
		t.Fatalf("racing destination = %q, want unchanged", got)
	}
	if bytes.Contains(append(stdout.Bytes(), stderr.Bytes()...), payload) {
		t.Fatalf("racing destination error leaked Provider bytes")
	}
}

func TestRawOutputProviderFailureCleansPreparedStagingFile(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		return nil, errSyntheticRawOutputWrite
	}
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "response.json")
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw", "endpoint", "getIdentity",
		"--config", configPath,
		"--db", archivePath,
		"--output", outputPath,
	}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatal("raw Provider failure exit code = 0, want failure")
	}
	if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Provider failure created final output: %v", err)
	}
	stagingFiles, err := filepath.Glob(filepath.Join(outputDir, ".gohealthcli-raw-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stagingFiles) != 0 {
		t.Fatalf("Provider failure left staging files: %v", stagingFiles)
	}
}

func TestRawOutputConfigFailureCleansPreparedStagingFile(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "response.json")
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw", "endpoint", "getIdentity",
		"--config", filepath.Join(t.TempDir(), "missing-config.toml"),
		"--db", filepath.Join(t.TempDir(), "missing-archive.sqlite"),
		"--output", outputPath,
	}, stdout, stderr, runtimeAdapters{})
	if code == 0 {
		t.Fatal("raw config failure exit code = 0, want failure")
	}
	if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config failure created final output: %v", err)
	}
	stagingFiles, err := filepath.Glob(filepath.Join(outputDir, ".gohealthcli-raw-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stagingFiles) != 0 {
		t.Fatalf("config failure left staging files: %v", stagingFiles)
	}
}

func TestRawStdoutTreatsShortWriteAsFailure(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(googlehealth.RawRequest) []byte {
		return []byte("synthetic-provider-response")
	})
	stdout := shortWriter{}
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{
		"raw", "endpoint", "getIdentity",
		"--config", configPath,
		"--db", archivePath,
	}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatal("raw stdout short write exit code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), io.ErrShortWrite.Error()) {
		t.Fatalf("stderr = %q, want short-write failure", stderr.String())
	}
}

func TestRawOutputStatusTreatsShortWriteAsFailureWithoutDeletingCompleteFile(t *testing.T) {
	t.Parallel()
	requireRawOutputPlatform(t)
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	payload := []byte("synthetic-provider-response")
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(googlehealth.RawRequest) []byte { return payload })
	outputPath := filepath.Join(t.TempDir(), "response.json")

	stdout := new(bytes.Buffer)
	stderr := shortWriter{}
	code := runWithRuntime([]string{
		"raw", "endpoint", "getIdentity",
		"--config", configPath,
		"--db", archivePath,
		"--output", outputPath,
	}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatal("raw status short write exit code = 0, want failure")
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read complete raw output after status failure: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("raw output = %q, want complete payload %q", got, payload)
	}
}

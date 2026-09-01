package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

var errSyntheticRawOutputWrite = errors.New("synthetic output write failure")

func TestRawOutputWritesExactProviderBytesToNewPrivateFile(t *testing.T) {
	t.Parallel()
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
	tempDir := t.TempDir()
	tests := []struct {
		name string
		seed func(string) error
	}{
		{name: "regular file", seed: func(path string) error { return os.WriteFile(path, []byte("keep"), 0o600) }},
		{name: "directory", seed: func(path string) error { return os.Mkdir(path, 0o700) }},
	}
	if runtime.GOOS != "windows" {
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
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outputPath := filepath.Join(tempDir, strings.ReplaceAll(test.name, " ", "-"))
			if err := test.seed(outputPath); err != nil {
				t.Fatalf("seed destination: %v", err)
			}
			before, _ := os.ReadFile(outputPath)
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
			}
		})
	}
}

func TestRawOutputRejectsInvalidParentTypeBeforeProviderRead(t *testing.T) {
	t.Parallel()
	parentPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentPath, []byte("keep-parent"), 0o600); err != nil {
		t.Fatal(err)
	}
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
		"--output", filepath.Join(parentPath, "response.json"),
	}, stdout, stderr, testRuntime)
	if code == 0 {
		t.Fatalf("raw --output exit code = 0, want invalid-parent refusal")
	}
	if !strings.Contains(stderr.String(), "parent") || !strings.Contains(stderr.String(), "directory") {
		t.Fatalf("stderr = %q, want invalid parent type", stderr.String())
	}
}

type shortRawOutputFile struct {
	*os.File
}

func (file *shortRawOutputFile) Write(payload []byte) (int, error) {
	return file.File.Write(payload[:len(payload)/2])
}

type failingRawOutputFile struct {
	*os.File
}

func (file *failingRawOutputFile) Write(payload []byte) (int, error) {
	written, _ := file.File.Write(payload[:1])
	return written, errSyntheticRawOutputWrite
}

func TestWriteRawOutputFileCleansUpShortAndFailedWrites(t *testing.T) {
	t.Parallel()
	payload := []byte("synthetic-provider-response")
	for _, test := range []struct {
		name      string
		wrap      func(*os.File) rawOutputFile
		wantError error
	}{
		{name: "short write", wrap: func(file *os.File) rawOutputFile { return &shortRawOutputFile{File: file} }, wantError: io.ErrShortWrite},
		{name: "write error", wrap: func(file *os.File) rawOutputFile { return &failingRawOutputFile{File: file} }, wantError: errSyntheticRawOutputWrite},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outputPath := filepath.Join(t.TempDir(), "response.json")
			openFile := func(path string) (rawOutputFile, error) {
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|exportOpenNoFollow, 0o600)
				if err != nil {
					return nil, err
				}
				return test.wrap(file), nil
			}
			_, err := writeRawOutputFileWithOpen(outputPath, payload, openFile, os.Remove)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("writeRawOutputFileWithOpen error = %v, want %v", err, test.wantError)
			}
			if _, statErr := os.Lstat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed raw output left destination behind: %v", statErr)
			}
		})
	}
}

func TestRawOutputErrorNeverContainsProviderBytes(t *testing.T) {
	t.Parallel()
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	payload := []byte("{\"syntheticSensitiveValue\":\"never-print-this\"}")
	bindRawFetchFake(t, &testRuntime, "connect-access-secret", func(googlehealth.RawRequest) []byte { return payload })
	testRuntime.writeRawOutput = func(string, []byte) (int, error) {
		return 0, errSyntheticRawOutputWrite
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

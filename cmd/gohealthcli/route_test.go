package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

const routeExerciseResource = "users/me/dataTypes/exercise/dataPoints/run-1"

var (
	routeOlderTCX = []byte(`{"tcxData":"synthetic legacy envelope"}`)
	routeNewerTCX = []byte(`<?xml version="1.0" encoding="UTF-8"?><TrainingCenterDatabase><Activities><Activity Sport="Running"><Id>2026-06-08T17:00:00Z</Id></Activity></Activities></TrainingCenterDatabase>`)
)

type routeArchive struct {
	configPath  string
	archivePath string
}

func newRouteArchive(t *testing.T) routeArchive {
	t.Helper()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, t.TempDir())
	insertStatusFixtureRows(t, archivePath)
	return routeArchive{configPath: configPath, archivePath: archivePath}
}

func (archive routeArchive) insertDataPoint(t *testing.T, dataType, resourceName string) int64 {
	t.Helper()
	insertExportDataPoint(t, archive.archivePath, exportDataPointFixture{
		dataType:     dataType,
		resourceName: resourceName,
		recordKind:   "session",
		startUTC:     "2026-06-08T17:00:00Z",
		endUTC:       "2026-06-08T17:30:00Z",
		dataSource:   `{"platform":"FITBIT"}`,
		rawJSON:      `{"exercise":{"exerciseType":"RUNNING"}}`,
	})
	var id int64
	db := openArchiveForTest(t, archive.archivePath)
	if err := db.QueryRowContext(context.Background(), `SELECT max(id) FROM data_points WHERE upstream_resource_name = ?`, resourceName).Scan(&id); err != nil {
		t.Fatalf("look up Data Point %q: %v", resourceName, err)
	}
	return id
}

func (archive routeArchive) storeTCX(t *testing.T, dataPointID int64, payload []byte, fetchedAt string) attachmentRecord {
	t.Helper()
	store, err := openAttachmentStoreMode(archive.archivePath, writeArchive)
	if err != nil {
		t.Fatalf("open attachment store: %v", err)
	}
	defer store.Close()
	record, err := store.Store(context.Background(), dataPointID, "tcx", payload, fetchedAt)
	if err != nil {
		t.Fatalf("store tcx: %v", err)
	}
	return record
}

func (archive routeArchive) run(t *testing.T, args ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"route"}, args...)
	full = append(full, "--config", archive.configPath, "--db", archive.archivePath)
	code := runWithRuntime(full, &stdout, &stderr, forbiddenRouteRuntime(t))
	return code, &stdout, &stderr
}

func forbiddenRouteRuntime(t *testing.T) runtimeAdapters {
	t.Helper()
	forbidden := func(name string) {
		t.Fatalf("route invoked forbidden adapter: %s", name)
	}
	credentialRead := func(context.Context, string, string) ([]byte, error) {
		forbidden("Credential Store read")
		return nil, errors.New("unreachable")
	}
	credentialWrite := func(context.Context, string, string, []byte) error {
		forbidden("Credential Store write")
		return errors.New("unreachable")
	}
	credentialDelete := func(context.Context, string, string) error {
		forbidden("Credential Store delete")
		return errors.New("unreachable")
	}
	return runtimeAdapters{
		httpDoer: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			forbidden("Provider HTTP")
			return nil, errors.New("unreachable")
		})},
		runOAuthFlow: func(oauthClientConfig, []string, bool) (oauthTokenResponse, error) {
			forbidden("OAuth flow")
			return oauthTokenResponse{}, errors.New("unreachable")
		},
		refreshOAuthToken: func(oauthClientConfig, string, []string) (oauthTokenResponse, error) {
			forbidden("token refresh")
			return oauthTokenResponse{}, errors.New("unreachable")
		},
		openBrowser: func(context.Context, string) error {
			forbidden("browser")
			return errors.New("unreachable")
		},
		fetchIdentity: func(string) (googleIdentity, error) {
			forbidden("identity fetch")
			return googleIdentity{}, errors.New("unreachable")
		},
		fetchProfile: func(string) (googleProfile, error) {
			forbidden("profile fetch")
			return googleProfile{}, errors.New("unreachable")
		},
		fetchPairedDevices: func(string) (googlePairedDevices, error) {
			forbidden("paired devices fetch")
			return googlePairedDevices{}, errors.New("unreachable")
		},
		fetchSettings: func(string) (googleSettings, error) {
			forbidden("settings fetch")
			return googleSettings{}, errors.New("unreachable")
		},
		fetchIRNProfile: func(string) (googleIRNProfile, error) {
			forbidden("IRN profile fetch")
			return googleIRNProfile{}, errors.New("unreachable")
		},
		fetchRawProvider: func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
			forbidden("Provider request")
			return nil, errors.New("unreachable")
		},
		retrySleeper: func(context.Context, time.Duration) bool {
			forbidden("retry sleeper")
			return true
		},
		openHealthArchiveWriter: func(string) (healthArchiveWriter, error) {
			forbidden("Health Archive writer")
			return nil, errors.New("unreachable")
		},
		openSyncPlanningArchive: func(context.Context, string) (syncPlanningArchive, error) {
			forbidden("Sync planning archive")
			return nil, errors.New("unreachable")
		},
		now: func() time.Time {
			forbidden("clock")
			return time.Time{}
		},
		sleep: func(time.Duration) {
			forbidden("sleep")
		},
		findExecutable: func(string) (string, error) {
			forbidden("executable lookup")
			return "", errors.New("unreachable")
		},
		runSecurityAddGenericPassword:    credentialWrite,
		runSecurityFindGenericPassword:   credentialRead,
		runSecurityDeleteGenericPassword: credentialDelete,
		runSecretToolStore:               credentialWrite,
		runSecretToolLookup:              credentialRead,
		runSecretToolClear:               credentialDelete,
		runWindowsCredentialWrite:        credentialWrite,
		runWindowsCredentialRead:         credentialRead,
		runWindowsCredentialDelete:       credentialDelete,
	}
}

func TestRouteStdoutWritesNewestArchivedTCXBytes(t *testing.T) {
	t.Parallel()
	archive := newRouteArchive(t)
	exerciseID := archive.insertDataPoint(t, "exercise", routeExerciseResource)
	older := archive.storeTCX(t, exerciseID, routeOlderTCX, "2026-06-08T17:35:00Z")
	archive.storeTCX(t, exerciseID, routeNewerTCX, "2026-06-09T08:00:00Z")

	for _, args := range [][]string{
		{routeExerciseResource, "--stdout"},
		{"--stdout", "run-1"},
		{"run-1", "--format", "tcx", "--stdout"},
	} {
		code, stdout, stderr := archive.run(t, args...)
		if code != 0 {
			t.Fatalf("route %v exit = %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
		}
		if !bytes.Equal(stdout.Bytes(), routeNewerTCX) {
			t.Fatalf("route %v stdout = %q, want newest archived bytes", args, stdout.Bytes())
		}
		if stderr.Len() != 0 {
			t.Fatalf("route %v stderr = %q, want empty", args, stderr)
		}
	}
	olderBytes, err := os.ReadFile(filepath.Join(attachmentRootDirForArchive(archive.archivePath), filepath.FromSlash(older.PathRelative)))
	if err != nil || !bytes.Equal(olderBytes, routeOlderTCX) {
		t.Fatalf("older sidecar changed: %q, %v", olderBytes, err)
	}
}

func TestRouteSelectsNewestByFetchedAtThenID(t *testing.T) {
	t.Parallel()
	archive := newRouteArchive(t)
	reversed := archive.insertDataPoint(t, "exercise", "users/me/dataTypes/exercise/dataPoints/reversed")
	archive.storeTCX(t, reversed, routeNewerTCX, "2026-06-09T08:00:00Z")
	archive.storeTCX(t, reversed, routeOlderTCX, "2026-06-08T17:35:00Z")
	tied := archive.insertDataPoint(t, "exercise", "users/me/dataTypes/exercise/dataPoints/tied")
	archive.storeTCX(t, tied, routeOlderTCX, "2026-06-09T08:00:00Z")
	archive.storeTCX(t, tied, routeNewerTCX, "2026-06-09T08:00:00Z")

	for _, id := range []string{"reversed", "tied"} {
		code, stdout, stderr := archive.run(t, id, "--stdout")
		if code != 0 || !bytes.Equal(stdout.Bytes(), routeNewerTCX) {
			t.Fatalf("route %s exit = %d stdout = %q stderr = %q, want newest bytes", id, code, stdout.Bytes(), stderr)
		}
	}
}

func TestRouteOutputWritesOwnerOnlyNewFile(t *testing.T) {
	t.Parallel()
	archive := newRouteArchive(t)
	exerciseID := archive.insertDataPoint(t, "exercise", routeExerciseResource)
	record := archive.storeTCX(t, exerciseID, routeNewerTCX, "2026-06-09T08:00:00Z")
	destination := filepath.Join(t.TempDir(), "run.tcx")

	code, stdout, stderr := archive.run(t, "--output", destination, routeExerciseResource)
	if code != 0 {
		t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if want := fmt.Sprintf("route: wrote %d bytes to %q\n", len(routeNewerTCX), destination); stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
	written, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	hash := sha256.Sum256(written)
	if hex.EncodeToString(hash[:]) != record.SHA256 {
		t.Fatalf("destination sha256 does not match the Attachment row")
	}
	if usesPOSIXPermissions() {
		info, err := os.Stat(destination)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("destination mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}

	code, stdout, stderr = archive.run(t, routeExerciseResource, "--output", destination, "--json")
	if code != 1 || !strings.Contains(stdout.String(), `"status":"flag_invalid"`) || !strings.Contains(stdout.String(), "refusing to overwrite") {
		t.Fatalf("second --output exit = %d stdout = %q stderr = %q, want flag_invalid overwrite refusal", code, stdout, stderr)
	}
	again, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(again, routeNewerTCX) {
		t.Fatalf("existing destination changed: %v", err)
	}
}

func TestRouteIdentifierFailuresHappenBeforeSidecarAccess(t *testing.T) {
	t.Parallel()
	archive := newRouteArchive(t)
	exerciseID := archive.insertDataPoint(t, "exercise", routeExerciseResource)
	archive.storeTCX(t, exerciseID, routeNewerTCX, "2026-06-09T08:00:00Z")
	archive.insertDataPoint(t, "exercise", "users/me/dataTypes/exercise/dataPoints/shared")
	archive.insertDataPoint(t, "sleep", "users/me/dataTypes/sleep/dataPoints/shared")
	archive.insertDataPoint(t, "sleep", "users/me/dataTypes/sleep/dataPoints/night-1")
	archive.insertDataPoint(t, "sleep", "users/me/dataTypes/sleep/dataPoints/night-2")
	archive.insertDataPoint(t, "sleep", "users/me/dataTypes/sleep/dataPoints/night-2")
	archive.insertDataPoint(t, "exercise", "users/me/dataTypes/exercise/dataPoints/twice")
	archive.insertDataPoint(t, "exercise", "users/me/dataTypes/exercise/dataPoints/twice")
	archive.insertDataPoint(t, "distance", "users/me/dataTypes/distance/dataPoints/x9")
	archive.insertDataPoint(t, "distance", "users/me/dataTypes/distance/dataPoints/x9")
	archive.insertDataPoint(t, "exercise", "users/me/dataTypes/exercise/dataPoints/x9")
	archive.insertDataPoint(t, "exercise", "users/me/dataTypes/exercise/dataPoints/run%1")
	archive.insertDataPoint(t, "exercise", "users/me/dataTypes/exercise/dataPoints/run_1")
	root := attachmentRootDirForArchive(archive.archivePath)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove attachment root: %v", err)
	}
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("replace attachment root: %v", err)
	}

	tests := []struct {
		name       string
		identifier string
		want       string
	}{
		{name: "unknown", identifier: "missing", want: `no archived Data Point matches \"missing\"`},
		{name: "unknown resource", identifier: "users/me/dataTypes/exercise/dataPoints/missing", want: "no archived Data Point matches"},
		{name: "ambiguous", identifier: "shared", want: "matches more than one archived Data Point; pass the full upstream_resource_name"},
		{name: "ambiguous across types", identifier: "x9", want: "matches more than one archived Data Point; pass the full upstream_resource_name"},
		{name: "non-exercise", identifier: "night-1", want: "is a sleep Data Point"},
		{name: "non-exercise resource", identifier: "users/me/dataTypes/sleep/dataPoints/night-1", want: "is a sleep Data Point"},
		{name: "duplicated non-exercise resource", identifier: "users/me/dataTypes/sleep/dataPoints/night-2", want: "is a sleep Data Point"},
		{name: "ambiguous resource", identifier: "users/me/dataTypes/exercise/dataPoints/twice", want: "matches more than one archived Data Point\""},
		{name: "percent is literal", identifier: "run%", want: "no archived Data Point matches"},
		{name: "underscore is literal", identifier: "run_", want: "no archived Data Point matches"},
		{name: "partial trailing ID", identifier: "un-1", want: `no archived Data Point matches \"un-1\"`},
		{name: "duplicated non-exercise", identifier: "night-2", want: "is a sleep Data Point"},
		{name: "case is exact", identifier: "RUN-1", want: "no archived Data Point matches"},
		{name: "empty", identifier: "", want: "must be an upstream_resource_name"},
		{name: "whitespace", identifier: " run-1", want: "must be an upstream_resource_name"},
		{name: "control", identifier: "run-1\n", want: "must be an upstream_resource_name"},
		{name: "leading slash", identifier: "/run-1", want: "must be an upstream_resource_name"},
		{name: "trailing slash", identifier: routeExerciseResource + "/", want: "must be an upstream_resource_name"},
		{name: "partial resource", identifier: "dataPoints/run-1", want: "must be an upstream_resource_name"},
		{name: "empty segment", identifier: "users//dataTypes/exercise/dataPoints/run-1", want: "must be an upstream_resource_name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := archive.run(t, "--json", "--stdout", test.identifier)
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			envelope := decodeRouteFailure(t, stdout)
			if envelope.Status != StatusFlagInvalid || !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("envelope = %s, want flag_invalid containing %q", stdout, test.want)
			}
		})
	}
}

func TestRouteIntegrityFailuresEmitNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		damage func(t *testing.T, archive routeArchive, newest attachmentRecord)
	}{
		{name: "missing sidecar", damage: func(t *testing.T, archive routeArchive, newest attachmentRecord) {
			mustRemove(t, archive.sidecarPath(newest))
		}},
		{name: "missing root", damage: func(t *testing.T, archive routeArchive, _ attachmentRecord) {
			mustRemove(t, attachmentRootDirForArchive(archive.archivePath))
		}},
		{name: "symlinked sidecar", damage: func(t *testing.T, archive routeArchive, newest attachmentRecord) {
			elsewhere := filepath.Join(t.TempDir(), "elsewhere.tcx")
			if err := os.WriteFile(elsewhere, routeNewerTCX, 0o600); err != nil {
				t.Fatal(err)
			}
			mustRemove(t, archive.sidecarPath(newest))
			mustSymlink(t, elsewhere, archive.sidecarPath(newest))
		}},
		{name: "symlinked root", damage: func(t *testing.T, archive routeArchive, _ attachmentRecord) {
			root := attachmentRootDirForArchive(archive.archivePath)
			moved := filepath.Join(t.TempDir(), "moved-root")
			if err := os.Rename(root, moved); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, moved, root)
		}},
		{name: "path outside root", damage: func(t *testing.T, archive routeArchive, newest attachmentRecord) {
			archive.updateAttachment(t, newest.ID, "path_relative", "../escape.tcx")
		}},
		{name: "non-canonical in-root path", damage: func(t *testing.T, archive routeArchive, newest attachmentRecord) {
			moved := "tcx/" + newest.SHA256 + ".tcx"
			if err := os.Rename(archive.sidecarPath(newest), filepath.Join(attachmentRootDirForArchive(archive.archivePath), filepath.FromSlash(moved))); err != nil {
				t.Fatal(err)
			}
			archive.updateAttachment(t, newest.ID, "path_relative", moved)
		}},
		{name: "byte size off by one", damage: func(t *testing.T, archive routeArchive, newest attachmentRecord) {
			archive.updateAttachment(t, newest.ID, "byte_size", newest.ByteSize+1)
		}},
		{name: "same-length byte flip", damage: func(t *testing.T, archive routeArchive, newest attachmentRecord) {
			flipped := bytes.Clone(routeNewerTCX)
			flipped[len(flipped)-2] ^= 0x01
			if err := os.WriteFile(archive.sidecarPath(newest), flipped, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		for _, destination := range []string{"stdout", "output"} {
			t.Run(test.name+"/"+destination, func(t *testing.T) {
				t.Parallel()
				archive := newRouteArchive(t)
				exerciseID := archive.insertDataPoint(t, "exercise", routeExerciseResource)
				archive.storeTCX(t, exerciseID, routeOlderTCX, "2026-06-08T17:35:00Z")
				newest := archive.storeTCX(t, exerciseID, routeNewerTCX, "2026-06-09T08:00:00Z")
				test.damage(t, archive, newest)
				outputDir := t.TempDir()
				target := filepath.Join(outputDir, "run.tcx")
				args := []string{"--json", routeExerciseResource, "--stdout"}
				if destination == "output" {
					args = []string{"--json", routeExerciseResource, "--output", target}
				}

				code, stdout, stderr := archive.run(t, args...)
				if code != 1 {
					t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
				}
				envelope := decodeRouteFailure(t, stdout)
				if envelope.Status != StatusOperationFailed || !slices.Equal(envelope.Remediation, []string{"gohealthcli doctor"}) {
					t.Fatalf("envelope = %s, want operation_failed with doctor remediation", stdout)
				}
				if bytes.Contains(stdout.Bytes(), []byte("TrainingCenterDatabase")) || bytes.Contains(stdout.Bytes(), []byte("synthetic legacy")) {
					t.Fatalf("stdout carried route bytes: %s", stdout)
				}
				entries, err := os.ReadDir(outputDir)
				if err != nil || len(entries) != 0 {
					t.Fatalf("output directory entries = %v, %v; want none", entries, err)
				}
			})
		}
	}
}

func TestRouteDestinationRules(t *testing.T) {
	t.Parallel()
	archive := newRouteArchive(t)
	exerciseID := archive.insertDataPoint(t, "exercise", routeExerciseResource)
	archive.storeTCX(t, exerciseID, routeNewerTCX, "2026-06-09T08:00:00Z")
	existing := filepath.Join(t.TempDir(), "existing.tcx")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no destination", args: []string{"run-1"}, want: "route requires --output PATH or --stdout"},
		{name: "both destinations", args: []string{"run-1", "--stdout", "--output", existing}, want: "route accepts only one destination: --output or --stdout"},
		{name: "empty output", args: []string{"run-1", "--output", ""}, want: "--output requires a non-empty path"},
		{name: "existing output", args: []string{"run-1", "--output", existing}, want: "refusing to overwrite"},
		{name: "unsupported format", args: []string{"run-1", "--stdout", "--format", "csv"}, want: `route format \"csv\" is not supported; supported: tcx`},
		{name: "no identifier", args: []string{"--stdout"}, want: "route requires exactly one exercise identifier"},
		{name: "two identifiers", args: []string{"run-1", "run-2", "--stdout"}, want: "route requires exactly one exercise identifier"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := archive.run(t, append([]string{"--json"}, test.args...)...)
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			if envelope := decodeRouteFailure(t, stdout); envelope.Status != StatusFlagInvalid || !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("envelope = %s, want flag_invalid containing %q", stdout, test.want)
			}
		})
	}
	kept, err := os.ReadFile(existing)
	if err != nil || string(kept) != "keep" {
		t.Fatalf("existing destination changed: %q, %v", kept, err)
	}
}

func TestRouteWithoutArchivedTCXNamesScopeAndSyncRemediation(t *testing.T) {
	t.Parallel()
	archive := newRouteArchive(t)
	archive.insertDataPoint(t, "exercise", routeExerciseResource)
	wantMessage := `no archived route for exercise "run-1"; grant the tcx scope with gohealthcli connect --add-scopes tcx, then re-sync that session's window with gohealthcli sync --types exercise --from YYYY-MM-DD`
	wantRemediation := []string{"gohealthcli connect --add-scopes tcx", "gohealthcli sync --types exercise --from YYYY-MM-DD"}

	code, stdout, stderr := archive.run(t, "run-1", "--stdout")
	if code != 1 || stdout.Len() != 0 || stderr.String() != "route: "+wantMessage+"\n" {
		t.Fatalf("human exit = %d stdout = %q stderr = %q", code, stdout, stderr)
	}

	code, stdout, stderr = archive.run(t, "run-1", "--stdout", "--plain")
	wantPlain := "status: operation_failed\nmessage: " + wantMessage + "\nremediation.0: " + wantRemediation[0] + "\nremediation.1: " + wantRemediation[1] + "\n"
	if code != 1 || stdout.String() != wantPlain || stderr.String() != "route: "+wantMessage+"\n" {
		t.Fatalf("plain exit = %d stdout = %q stderr = %q", code, stdout, stderr)
	}

	var globalStdout, globalStderr bytes.Buffer
	code = runWithRuntime([]string{"--json", "route", "run-1", "--stdout", "--config", archive.configPath, "--db", archive.archivePath}, &globalStdout, &globalStderr, forbiddenRouteRuntime(t))
	envelope := decodeRouteFailure(t, &globalStdout)
	if code != 1 || globalStderr.Len() != 0 || envelope.Status != StatusOperationFailed || envelope.Message != wantMessage || !slices.Equal(envelope.Remediation, wantRemediation) {
		t.Fatalf("json exit = %d stdout = %q stderr = %q", code, globalStdout.String(), globalStderr.String())
	}
}

func TestRouteSyncExerciseRemediationIsReporterVocabulary(t *testing.T) {
	t.Parallel()
	got := normalizeRemediation([]remediationAction{remediationSyncExerciseFrom})
	if !slices.Equal(got, []string{"gohealthcli sync --types exercise --from YYYY-MM-DD"}) {
		t.Fatalf("normalizeRemediation = %v", got)
	}
}

func TestRouteMissingArchiveUsesArchiveRemediation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(directory, "missing.sqlite")
	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{"route", "run-1", "--stdout", "--json", "--db", archivePath}, &stdout, &stderr, forbiddenRouteRuntime(t))
	envelope := decodeRouteFailure(t, &stdout)
	if code != 1 || envelope.Status != StatusOperationFailed || !slices.Equal(envelope.Remediation, []string{"gohealthcli doctor", "gohealthcli init"}) {
		t.Fatalf("exit = %d stdout = %q stderr = %q, want operation_failed with archive remediation", code, stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(archivePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("route created the missing archive: %v", err)
	}
	if _, err := os.Lstat(attachmentRootDirForArchive(archivePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("route created the attachment root: %v", err)
	}
}

func TestRouteHasZeroSideEffects(t *testing.T) {
	t.Parallel()
	archive := newRouteArchive(t)
	exerciseID := archive.insertDataPoint(t, "exercise", routeExerciseResource)
	archive.storeTCX(t, exerciseID, routeOlderTCX, "2026-06-08T17:35:00Z")
	archive.storeTCX(t, exerciseID, routeNewerTCX, "2026-06-09T08:00:00Z")
	archive.insertDataPoint(t, "exercise", "users/me/dataTypes/exercise/dataPoints/indoor-1")
	before := snapshotRouteArchive(t, archive.archivePath)

	runs := []struct {
		args     []string
		wantCode int
	}{
		{args: []string{routeExerciseResource, "--stdout"}, wantCode: 0},
		{args: []string{"run-1", "--output", filepath.Join(t.TempDir(), "run.tcx")}, wantCode: 0},
		{args: []string{"indoor-1", "--stdout", "--json"}, wantCode: 1},
		{args: []string{"missing", "--stdout", "--json"}, wantCode: 1},
	}
	for _, run := range runs {
		if code, stdout, stderr := archive.run(t, run.args...); code != run.wantCode {
			t.Fatalf("route %v exit = %d, want %d\nstdout: %s\nstderr: %s", run.args, code, run.wantCode, stdout, stderr)
		}
	}
	if after := snapshotRouteArchive(t, archive.archivePath); !maps.Equal(before, after) {
		t.Fatalf("route changed the Health Archive or sidecar tree\nbefore: %v\nafter:  %v", before, after)
	}
}

func TestRouteRejectsStaleSchemaWithoutMigrating(t *testing.T) {
	t.Parallel()
	archive := newRouteArchive(t)
	db, err := openArchive(archive.archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion-1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotRouteArchive(t, archive.archivePath)

	code, stdout, stderr := archive.run(t, "run-1", "--stdout", "--json")
	envelope := decodeRouteFailure(t, stdout)
	if code != 1 || envelope.Status != StatusOperationFailed || !strings.Contains(envelope.Message, fmt.Sprintf("schema version %d", currentSchemaVersion-1)) {
		t.Fatalf("exit = %d stdout = %q stderr = %q, want stale-schema operation_failed", code, stdout, stderr)
	}
	if after := snapshotRouteArchive(t, archive.archivePath); !maps.Equal(before, after) {
		t.Fatalf("route changed a stale archive\nbefore: %v\nafter:  %v", before, after)
	}
}

func snapshotRouteArchive(t *testing.T, archivePath string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(filepath.Dir(archivePath), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		fingerprint := info.Mode().String()
		if info.Mode().IsRegular() {
			payload, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(payload)
			fingerprint += " " + hex.EncodeToString(hash[:])
		}
		snapshot[path] = fingerprint
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot archive: %v", err)
	}
	return snapshot
}

func decodeRouteFailure(t *testing.T, stdout *bytes.Buffer) failureJSONEnvelope {
	t.Helper()
	var envelope failureJSONEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode failure envelope %q: %v", stdout.String(), err)
	}
	return envelope
}

func (archive routeArchive) sidecarPath(record attachmentRecord) string {
	return filepath.Join(attachmentRootDirForArchive(archive.archivePath), filepath.FromSlash(record.PathRelative))
}

func (archive routeArchive) updateAttachment(t *testing.T, id int64, column string, value any) {
	t.Helper()
	db := openArchiveForTest(t, archive.archivePath)
	if _, err := db.ExecContext(context.Background(), `UPDATE data_point_attachments SET `+column+` = ? WHERE id = ?`, value, id); err != nil {
		t.Fatalf("update attachment %s: %v", column, err)
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

func TestRouteRegistryHelpSurfaces(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "route", "--help")
	if code != 0 {
		t.Fatalf("route --help exit = %d\nstderr: %s", code, stderr)
	}
	help := stdout.String() + stderr.String()
	for _, flagName := range []string{"-format", "-output", "-stdout", routeFormatUsage(), routeCommonFlagUsageOverrides["json"]} {
		if !strings.Contains(help, flagName) {
			t.Fatalf("route --help missing %q:\n%s", flagName, help)
		}
	}
	code, stdout, stderr = runCommand(t)
	if code != 0 || !strings.Contains(stdout.String(), "Write one archived exercise route as its exact TCX file.") {
		t.Fatalf("top-level help exit = %d stdout = %q stderr = %q, want route listed", code, stdout, stderr)
	}
}

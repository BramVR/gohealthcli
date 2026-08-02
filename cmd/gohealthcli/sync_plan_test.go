package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestSyncPlanCoversSingleDataTypeModesWithoutSideEffects(t *testing.T) {
	configPath, archivePath, tokenPath := initializeFileCredentialSetup(t, t.TempDir())
	testRuntime := newConnectFakeRuntime(t, fakeConnectConfig{
		now:                time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
		accessToken:        "plan-access-secret",
		refreshToken:       "plan-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	mustConnect(t, configPath, archivePath, testRuntime)
	if err := os.Remove(tokenPath); err != nil {
		t.Fatalf("remove synthetic credential material: %v", err)
	}
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		t.Fatal("sync --plan made a Provider request")
		return nil, nil
	}
	testRuntime.fetchIdentity = func(string) (googleIdentity, error) {
		t.Fatal("sync --plan performed online identity verification")
		return googleIdentity{}, nil
	}
	testRuntime.refreshOAuthToken = func(oauthClientConfig, string, []string) (oauthTokenResponse, error) {
		t.Fatal("sync --plan refreshed a token")
		return oauthTokenResponse{}, nil
	}
	testRuntime.openHealthArchiveWriter = func(string) (healthArchiveWriter, error) {
		t.Fatal("sync --plan opened the Health Archive writer")
		return nil, nil
	}

	beforeArchive := capturePlanningArchiveState(t, archivePath)
	attachmentRoot := attachmentRootDirForArchive(archivePath)
	beforeAttachments := readDirectoryEntryNames(t, attachmentRoot)
	tests := []struct {
		name              string
		args              []string
		wantEndpoint      string
		wantMethod        string
		wantSource        string
		wantArchiveEffect string
		wantConditional   bool
	}{
		{
			name:              "list",
			args:              []string{"--types", "steps", "--from", "2026-01-01T00:00:00Z", "--to", "2026-01-02T00:00:00Z"},
			wantEndpoint:      "list",
			wantMethod:        "GET",
			wantSource:        "all",
			wantArchiveEffect: "write_data_points_and_sync_run",
		},
		{
			name:              "reconcile",
			args:              []string{"--types", "steps", "--source-family", "wearable", "--from", "2026-01-01T00:00:00Z", "--to", "2026-01-02T00:00:00Z"},
			wantEndpoint:      "reconcile",
			wantMethod:        "GET",
			wantSource:        "wearable",
			wantArchiveEffect: "write_data_points_and_sync_run",
		},
		{
			name:              "daily Rollup",
			args:              []string{"--types", "steps", "--rollup", "daily", "--from", "2026-01-01", "--to", "2026-01-05"},
			wantEndpoint:      "dailyRollUp",
			wantMethod:        "POST",
			wantSource:        "all",
			wantArchiveEffect: "write_rollups_and_sync_run",
		},
		{
			name:              "window Rollup",
			args:              []string{"--types", "steps", "--rollup", "hourly", "--from", "2026-01-01T00:00:00Z", "--to", "2026-01-02T00:00:00Z"},
			wantEndpoint:      "rollUp",
			wantMethod:        "POST",
			wantSource:        "all",
			wantArchiveEffect: "write_rollups_and_sync_run",
		},
		{
			name:              "exercise TCX",
			args:              []string{"--types", "exercise", "--from", "2026-01-01", "--to", "2026-01-02"},
			wantEndpoint:      "list",
			wantMethod:        "GET",
			wantSource:        "all",
			wantArchiveEffect: "write_data_points_and_sync_run",
			wantConditional:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"sync", "--plan", "--config", configPath, "--db", archivePath, "--json"}
			args = append(args, test.args...)
			var stdout, stderr bytes.Buffer
			if code := runWithRuntime(args, &stdout, &stderr, testRuntime); code != 0 {
				t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			var got struct {
				Status         string   `json:"status"`
				Ready          bool     `json:"ready"`
				DataType       string   `json:"data_type"`
				EndpointFamily string   `json:"endpoint_family"`
				SourceFamily   string   `json:"source_family"`
				RequiredScopes []string `json:"required_scopes"`
				Request        struct {
					Method string          `json:"method"`
					URL    string          `json:"url"`
					Body   json.RawMessage `json:"body"`
				} `json:"request"`
				PredictedSyncEffects struct {
					ProviderRequests  string `json:"provider_requests"`
					CredentialStore   string `json:"credential_store"`
					TokenRefresh      string `json:"token_refresh"`
					HealthArchive     string `json:"health_archive"`
					SyncCursor        string `json:"sync_cursor"`
					AttachmentSidecar string `json:"attachment_sidecar"`
				} `json:"predicted_sync_effects"`
				Readiness struct {
					OnlineChecksNotPerformed []string `json:"online_checks_not_performed"`
				} `json:"readiness"`
				ConditionalOperations []struct {
					Name string `json:"name"`
				} `json:"conditional_operations"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode JSON: %v\n%s", err, stdout.String())
			}
			if got.Status != "plan_ready" || !got.Ready || got.DataType == "" {
				t.Errorf("plan status = (%q, %v, %q), want ready named plan", got.Status, got.Ready, got.DataType)
			}
			if got.EndpointFamily != test.wantEndpoint || got.Request.Method != test.wantMethod {
				t.Errorf("operation = (%q, %q), want (%q, %q)", got.EndpointFamily, got.Request.Method, test.wantEndpoint, test.wantMethod)
			}
			if got.SourceFamily != test.wantSource {
				t.Errorf("source_family = %q, want %q", got.SourceFamily, test.wantSource)
			}
			if len(got.RequiredScopes) == 0 || !strings.Contains(got.Request.URL, "/users/me/") {
				t.Errorf("scope/request preview incomplete: scopes=%v request=%+v", got.RequiredScopes, got.Request)
			}
			if got.PredictedSyncEffects.ProviderRequests != "read" || got.PredictedSyncEffects.CredentialStore != "read" || got.PredictedSyncEffects.TokenRefresh != "conditional" || got.PredictedSyncEffects.HealthArchive != test.wantArchiveEffect || got.PredictedSyncEffects.SyncCursor != "advance_after_sync_completed" {
				t.Errorf("predicted effects = %+v", got.PredictedSyncEffects)
			}
			if len(got.Readiness.OnlineChecksNotPerformed) != 3 {
				t.Errorf("online checks = %v, want credential/identity/reachability exclusions", got.Readiness.OnlineChecksNotPerformed)
			}
			if (len(got.ConditionalOperations) == 1) != test.wantConditional {
				t.Errorf("conditional operations = %+v, want TCX=%v", got.ConditionalOperations, test.wantConditional)
			}
			if test.wantConditional && (got.ConditionalOperations[0].Name != "exercise_tcx" || got.PredictedSyncEffects.AttachmentSidecar != "conditional_exercise_tcx") {
				t.Errorf("TCX plan = operations %+v, effects %+v", got.ConditionalOperations, got.PredictedSyncEffects)
			}
			for _, secret := range []string{"plan-access-secret", "plan-refresh-secret", "111111256096816351", "A1B2C3"} {
				if strings.Contains(stdout.String(), secret) {
					t.Fatalf("plan output contains secret or identifying value")
				}
			}
		})
	}
	afterArchive := capturePlanningArchiveState(t, archivePath)
	if !reflect.DeepEqual(afterArchive, beforeArchive) {
		t.Fatalf("sync --plan mutated Health Archive state:\nbefore: %+v\nafter:  %+v", beforeArchive, afterArchive)
	}
	if afterAttachments := readDirectoryEntryNames(t, attachmentRoot); afterAttachments != beforeAttachments {
		t.Fatalf("sync --plan mutated Attachment sidecar: before=%s after=%s", beforeAttachments, afterAttachments)
	}
}

func TestSyncPlanBlockedAndOutputContracts(t *testing.T) {
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
		accessToken:        "plan-access-secret",
		refreshToken:       "plan-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	t.Run("missing cursor is a detailed local blocker", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWithRuntime([]string{"sync", "--plan", "--config", configPath, "--db", archivePath, "--to", "2026-01-10T12:00:00Z", "--json"}, &stdout, &stderr, testRuntime)
		if code == 0 {
			t.Fatalf("exit = 0, want nonzero\n%s", stdout.String())
		}
		var got struct {
			Status   string `json:"status"`
			Ready    bool   `json:"ready"`
			Blockers []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"blockers"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Status != "plan_blocked" || got.Ready || len(got.Blockers) != 1 || got.Blockers[0].Code != "missing_sync_cursor" || !strings.Contains(got.Blockers[0].Message, "--from") {
			t.Fatalf("blocked plan = %+v", got)
		}
	})

	t.Run("missing required scope is a detailed local blocker", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWithRuntime([]string{
			"sync", "--plan", "--config", configPath, "--db", archivePath,
			"--types", "electrocardiogram",
			"--from", "2026-01-01T00:00:00Z", "--to", "2026-01-02T00:00:00Z", "--json",
		}, &stdout, &stderr, testRuntime)
		if code == 0 {
			t.Fatalf("exit = 0, want nonzero\n%s", stdout.String())
		}
		var got struct {
			Status   string `json:"status"`
			Blockers []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"blockers"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Status != "plan_blocked" || len(got.Blockers) != 1 || got.Blockers[0].Code != "missing_required_scope" || !strings.Contains(got.Blockers[0].Message, "connect --add-scopes ecg") {
			t.Fatalf("scope-blocked plan = %+v", got)
		}
		assertArchiveTableCount(t, archivePath, "sync_runs", 0)
	})

	t.Run("plain and human carry the same ready contract", func(t *testing.T) {
		base := []string{"sync", "--plan", "--config", configPath, "--db", archivePath, "--from", "2026-01-01T00:00:00Z", "--to", "2026-01-02T00:00:00Z"}
		var plainOut, plainErr bytes.Buffer
		if code := runWithRuntime(append(append([]string(nil), base...), "--plain"), &plainOut, &plainErr, testRuntime); code != 0 {
			t.Fatalf("plain exit = %d\n%s\n%s", code, plainOut.String(), plainErr.String())
		}
		for _, want := range []string{"status: plan_ready", "ready: true", "data_type: steps", "endpoint_family: list", "source_family: all", "readiness.online_checks_not_performed.0: credential_availability", "planning_effects.provider_request: false", "planning_effects.archive_write: false"} {
			if !strings.Contains(plainOut.String(), want+"\n") {
				t.Errorf("plain output missing %q:\n%s", want, plainOut.String())
			}
		}
		var humanOut, humanErr bytes.Buffer
		if code := runWithRuntime(base, &humanOut, &humanErr, testRuntime); code != 0 {
			t.Fatalf("human exit = %d\n%s\n%s", code, humanOut.String(), humanErr.String())
		}
		for _, want := range []string{"Sync plan ready", "Data Type: steps", "Endpoint: list", "Online checks not performed: credential availability, Google Identity match, Provider reachability", "Planning performed no Provider, credential, OAuth refresh, or Health Archive write effects."} {
			if !strings.Contains(humanOut.String(), want) {
				t.Errorf("human output missing %q:\n%s", want, humanOut.String())
			}
		}
	})

	t.Run("fixed clock is deterministic", func(t *testing.T) {
		args := []string{"sync", "--plan", "--config", configPath, "--db", archivePath, "--types", "steps", "--from", "yesterday", "--to", "today", "--json"}
		var first, firstErr, second, secondErr bytes.Buffer
		if code := runWithRuntime(args, &first, &firstErr, testRuntime); code != 0 {
			t.Fatalf("first exit = %d: %s %s", code, first.String(), firstErr.String())
		}
		if code := runWithRuntime(args, &second, &secondErr, testRuntime); code != 0 {
			t.Fatalf("second exit = %d: %s %s", code, second.String(), secondErr.String())
		}
		if first.String() != second.String() {
			t.Fatalf("fixed-clock plans differ:\nfirst: %s\nsecond: %s", first.String(), second.String())
		}
	})
}

func TestSyncPlanResolvesCursorWithoutArchiveMutation(t *testing.T) {
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
		accessToken:        "plan-access-secret",
		refreshToken:       "plan-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		archive.Close()
		t.Fatal(err)
	}
	if err := archive.CommitSyncCursor(context.Background(), syncCursorKey{
		connectionID: connection.ID,
		dataType:     "steps",
		rollupKind:   syncCursorRollupKindNone,
	}, syncRunOutcomeCompleted, "2026-01-03T00:00:00Z", "2026-01-03T00:00:01Z"); err != nil {
		archive.Close()
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	before := capturePlanningArchiveState(t, archivePath)
	testRuntime.openHealthArchiveWriter = func(string) (healthArchiveWriter, error) {
		t.Fatal("sync --plan opened the Health Archive writer")
		return nil, nil
	}

	var stdout, stderr bytes.Buffer
	if code := runWithRuntime([]string{
		"sync", "--plan", "--config", configPath, "--db", archivePath,
		"--to", "2026-01-10T12:00:00Z", "--json",
	}, &stdout, &stderr, testRuntime); code != 0 {
		t.Fatalf("exit = %d, want 0\n%s\n%s", code, stdout.String(), stderr.String())
	}
	var got struct {
		Status string `json:"status"`
		Range  struct {
			From              string `json:"from"`
			ResumedFromCursor bool   `json:"resumed_from_cursor"`
		} `json:"range"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "plan_ready" || got.Range.From != "2026-01-03T00:00:00Z" || !got.Range.ResumedFromCursor {
		t.Fatalf("cursor plan = %+v", got)
	}
	after := capturePlanningArchiveState(t, archivePath)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("cursor planning mutated archive:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestBuildSyncPlanHonorsCanceledContextBeforeInspection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runtime := runtimeAdapters{
		openSyncPlanningArchive: func(context.Context, string) (syncPlanningArchive, error) {
			t.Fatal("canceled sync --plan inspected the Health Archive")
			return nil, nil
		},
	}
	result := buildSyncPlan(ctx, syncCommandOptions{
		dataTypes: []string{"steps"},
		from:      "2026-01-01T00:00:00Z",
		to:        "2026-01-02T00:00:00Z",
	}, runtime)
	if result.Ready || result.Status != "plan_blocked" || len(result.Blockers) != 1 || result.Blockers[0].Code != "planning_canceled" {
		t.Fatalf("canceled plan = %+v, want planning_canceled blocker", result)
	}
}

func TestSyncPlanRejectsStatusConflict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"sync", "--plan", "--status", "--json"}, want: "cannot be combined"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(test.args, &stdout, &stderr); code == 0 {
			t.Fatalf("%v exit = 0, want nonzero", test.args)
		}
		if !strings.Contains(stdout.String()+stderr.String(), test.want) {
			t.Errorf("%v output missing %q: stdout=%q stderr=%q", test.args, test.want, stdout.String(), stderr.String())
		}
	}
}

func TestSyncPlanFanOutRejectsGlobalSelectionConflicts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		code string
	}{
		{name: "duplicate repeated type", args: []string{"--types", "steps", "--types", "steps"}, code: "duplicate_data_type"},
		{name: "all with repeated type", args: []string{"--all", "--types", "steps"}, code: "all_vs_types_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"sync", "--plan", "--json"}, test.args...)
			var stdout, stderr bytes.Buffer
			if exitCode := run(args, &stdout, &stderr); exitCode == 0 {
				t.Fatalf("exit = 0, want selection conflict\n%s", stdout.String())
			}
			var got syncPlanFanOutResult
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != "plan_blocked" || got.Ready || len(got.Operations) != 0 || len(got.Blockers) != 1 || got.Blockers[0].Code != test.code {
				t.Fatalf("global blocker = %+v, want %q", got, test.code)
			}
		})
	}
}

func TestSyncTypesFlagRepeatsOnlyAccumulateForPlanning(t *testing.T) {
	t.Parallel()
	var value syncTypesFlag
	if err := value.Set("steps"); err != nil {
		t.Fatal(err)
	}
	if err := value.Set("heart-rate,sleep"); err != nil {
		t.Fatal(err)
	}
	if got, want := value.dataTypes(true), []string{"steps", "heart-rate", "sleep"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planning Data Types = %v, want %v", got, want)
	}
	if got, want := value.dataTypes(false), []string{"heart-rate", "sleep"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("real sync Data Types = %v, want last occurrence %v", got, want)
	}
}

func TestSyncPlanFansOutWithPerTypeRangesAndIsolatedBlockers(t *testing.T) {
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
		accessToken:        "plan-access-secret",
		refreshToken:       "plan-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	testRuntime.fetchRawProvider = func(context.Context, googlehealth.RawRequest, string) ([]byte, error) {
		t.Fatal("fan-out sync --plan made a Provider request")
		return nil, nil
	}
	testRuntime.refreshOAuthToken = func(oauthClientConfig, string, []string) (oauthTokenResponse, error) {
		t.Fatal("fan-out sync --plan refreshed a token")
		return oauthTokenResponse{}, nil
	}
	testRuntime.openHealthArchiveWriter = func(string) (healthArchiveWriter, error) {
		t.Fatal("fan-out sync --plan opened the Health Archive writer")
		return nil, nil
	}
	before := capturePlanningArchiveState(t, archivePath)

	args := []string{
		"sync", "--plan", "--config", configPath, "--db", archivePath,
		"--types", "steps", "--types", "daily-resting-heart-rate,electrocardiogram",
		"--from", "yesterday", "--to", "today", "--json",
	}
	var first, firstErr, second, secondErr bytes.Buffer
	if code := runWithRuntime(args, &first, &firstErr, testRuntime); code == 0 {
		t.Fatalf("first exit = 0, want nonzero for one isolated blocker\n%s", first.String())
	}
	if code := runWithRuntime(args, &second, &secondErr, testRuntime); code == 0 {
		t.Fatalf("second exit = 0, want nonzero for one isolated blocker\n%s", second.String())
	}
	if firstErr.String() != "" || secondErr.String() != "" {
		t.Fatalf("stderr = (%q, %q), want empty", firstErr.String(), secondErr.String())
	}
	if first.String() != second.String() {
		t.Fatalf("fixed-clock fan-out plans differ:\nfirst: %s\nsecond: %s", first.String(), second.String())
	}
	var got struct {
		Status     string           `json:"status"`
		Ready      bool             `json:"ready"`
		Operations []syncPlanResult `json:"operations"`
		Results    json.RawMessage  `json:"results"`
		Summary    json.RawMessage  `json:"summary"`
	}
	if err := json.Unmarshal(first.Bytes(), &got); err != nil {
		t.Fatalf("decode fan-out JSON: %v\n%s", err, first.String())
	}
	if got.Status != "plan_blocked" || got.Ready {
		t.Fatalf("fan-out status = (%q, %v), want plan_blocked false", got.Status, got.Ready)
	}
	if len(got.Results) != 0 || len(got.Summary) != 0 {
		t.Fatalf("plan envelope reused Sync Run shape: results=%s summary=%s", got.Results, got.Summary)
	}
	wantTypes := []string{"steps", "daily-resting-heart-rate", "electrocardiogram"}
	if len(got.Operations) != len(wantTypes) {
		t.Fatalf("operations = %d, want %d\n%s", len(got.Operations), len(wantTypes), first.String())
	}
	for index, want := range wantTypes {
		if got.Operations[index].DataType != want {
			t.Errorf("operations[%d].data_type = %q, want %q", index, got.Operations[index].DataType, want)
		}
	}
	if !got.Operations[0].Ready || got.Operations[0].Range.From != "2026-01-09T00:00:00Z" || got.Operations[0].Range.To != "2026-01-10T00:00:00Z" {
		t.Errorf("physical operation = %+v", got.Operations[0])
	}
	if !got.Operations[1].Ready || got.Operations[1].Range.From != "2026-01-09" || got.Operations[1].Range.To != "2026-01-10" {
		t.Errorf("daily operation = %+v", got.Operations[1])
	}
	blocked := got.Operations[2]
	if blocked.Ready || len(blocked.Blockers) != 1 || blocked.Blockers[0].Code != "missing_required_scope" {
		t.Errorf("isolated blocked operation = %+v", blocked)
	}
	for index := range got.Operations[:2] {
		if got.Operations[index].PlanningEffects.ProviderRequest || got.Operations[index].PlanningEffects.CredentialRead || got.Operations[index].PlanningEffects.ArchiveWrite {
			t.Errorf("operations[%d] planning effects = %+v, want zero", index, got.Operations[index].PlanningEffects)
		}
	}
	after := capturePlanningArchiveState(t, archivePath)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("fan-out planning mutated archive:\nbefore: %+v\nafter:  %+v", before, after)
	}

	for _, output := range []struct {
		name string
		flag string
		want []string
	}{
		{
			name: "plain",
			flag: "--plain",
			want: []string{"operations.0.data_type: steps", "operations.1.data_type: daily-resting-heart-rate", "operations.2.data_type: electrocardiogram", "operations.2.blockers.0.code: missing_required_scope"},
		},
		{
			name: "human",
			want: []string{"Sync plan fan-out blocked across 3 Data Types", "Data Type: steps", "Data Type: daily-resting-heart-rate", "Data Type: electrocardiogram", "Blocker [missing_required_scope]"},
		},
	} {
		t.Run(output.name, func(t *testing.T) {
			modeArgs := append([]string(nil), args[:len(args)-1]...)
			if output.flag != "" {
				modeArgs = append(modeArgs, output.flag)
			}
			var stdout, stderr bytes.Buffer
			if code := runWithRuntime(modeArgs, &stdout, &stderr, testRuntime); code == 0 {
				t.Fatalf("exit = 0, want isolated blocker\n%s", stdout.String())
			}
			for _, want := range output.want {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("output missing %q:\n%s", want, stdout.String())
				}
			}
		})
	}
}

func TestSyncPlanAllUsesCatalogOrderAndOutputParity(t *testing.T) {
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
		accessToken:        "plan-access-secret",
		refreshToken:       "plan-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	base := []string{
		"sync", "--plan", "--config", configPath, "--db", archivePath,
		"--all", "--from", "2026-01-01", "--to", "2026-01-02",
	}
	var jsonOut, jsonErr bytes.Buffer
	if code := runWithRuntime(append(append([]string(nil), base...), "--json"), &jsonOut, &jsonErr, testRuntime); code != 0 {
		t.Fatalf("JSON exit = %d\n%s\n%s", code, jsonOut.String(), jsonErr.String())
	}
	var envelope struct {
		Status     string           `json:"status"`
		Ready      bool             `json:"ready"`
		Operations []syncPlanResult `json:"operations"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"steps", "heart-rate", "daily-resting-heart-rate", "heart-rate-variability",
		"daily-heart-rate-variability", "oxygen-saturation", "daily-oxygen-saturation",
		"daily-respiratory-rate", "sleep", "exercise", "distance", "weight",
	}
	if envelope.Status != "plan_ready" || !envelope.Ready || len(envelope.Operations) != len(want) {
		t.Fatalf("all envelope = status %q ready %v operations %d", envelope.Status, envelope.Ready, len(envelope.Operations))
	}
	for index, operation := range envelope.Operations {
		if operation.DataType != want[index] {
			t.Errorf("operations[%d].data_type = %q, want catalog-order %q", index, operation.DataType, want[index])
		}
	}

	var plainOut, plainErr bytes.Buffer
	if code := runWithRuntime(append(append([]string(nil), base...), "--plain"), &plainOut, &plainErr, testRuntime); code != 0 {
		t.Fatalf("plain exit = %d\n%s\n%s", code, plainOut.String(), plainErr.String())
	}
	for _, wantLine := range []string{
		"status: plan_ready", "ready: true", "operations.0.data_type: steps",
		"operations.11.data_type: weight", "operations.0.planning_effects.provider_request: false",
	} {
		if !strings.Contains(plainOut.String(), wantLine+"\n") {
			t.Errorf("plain output missing %q:\n%s", wantLine, plainOut.String())
		}
	}

	var humanOut, humanErr bytes.Buffer
	if code := runWithRuntime(base, &humanOut, &humanErr, testRuntime); code != 0 {
		t.Fatalf("human exit = %d\n%s\n%s", code, humanOut.String(), humanErr.String())
	}
	for _, wantText := range []string{"Sync plan fan-out ready across 12 Data Types", "Data Type: steps", "Data Type: weight"} {
		if !strings.Contains(humanOut.String(), wantText) {
			t.Errorf("human output missing %q:\n%s", wantText, humanOut.String())
		}
	}
}

func TestSyncPlanFanOutResolvesPerTypeCursorsAndDefaultRanges(t *testing.T) {
	configPath, archivePath, testRuntime := connectedArchive(t, fakeConnectConfig{
		now:                time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
		accessToken:        "plan-access-secret",
		refreshToken:       "plan-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})
	archive, err := openHealthArchiveWriter(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := archive.CurrentConnection(context.Background())
	if err != nil {
		archive.Close()
		t.Fatal(err)
	}
	for _, cursor := range []struct {
		dataType string
		to       string
	}{
		{dataType: "steps", to: "2026-01-03T04:05:06Z"},
		{dataType: "daily-resting-heart-rate", to: "2026-01-04"},
	} {
		if err := archive.CommitSyncCursor(context.Background(), syncCursorKey{
			connectionID: connection.ID,
			dataType:     cursor.dataType,
			rollupKind:   syncCursorRollupKindNone,
		}, syncRunOutcomeCompleted, cursor.to, "2026-01-04T00:00:00Z"); err != nil {
			archive.Close()
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	before := capturePlanningArchiveState(t, archivePath)
	testRuntime.openHealthArchiveWriter = func(string) (healthArchiveWriter, error) {
		t.Fatal("fan-out cursor planning opened the Health Archive writer")
		return nil, nil
	}

	var stdout, stderr bytes.Buffer
	code := runWithRuntime([]string{
		"sync", "--plan", "--config", configPath, "--db", archivePath,
		"--types", "steps,daily-resting-heart-rate,heart-rate", "--json",
	}, &stdout, &stderr, testRuntime)
	if code == 0 {
		t.Fatalf("exit = 0, want one missing-cursor blocker\n%s", stdout.String())
	}
	var got struct {
		Operations []syncPlanResult `json:"operations"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Operations) != 3 {
		t.Fatalf("operations = %d, want 3", len(got.Operations))
	}
	if operation := got.Operations[0]; !operation.Ready || operation.Range.From != "2026-01-03T04:05:06Z" || operation.Range.To != "2026-01-10T12:00:00Z" || !operation.Range.ResumedFromCursor {
		t.Errorf("steps cursor operation = %+v", operation)
	}
	if operation := got.Operations[1]; !operation.Ready || operation.Range.From != "2026-01-04" || operation.Range.To != "2026-01-10" || !operation.Range.ResumedFromCursor {
		t.Errorf("daily cursor operation = %+v", operation)
	}
	if operation := got.Operations[2]; operation.Ready || operation.Range.To != "2026-01-10T12:00:00Z" || len(operation.Blockers) != 1 || operation.Blockers[0].Code != "missing_sync_cursor" {
		t.Errorf("missing cursor operation = %+v", operation)
	}
	after := capturePlanningArchiveState(t, archivePath)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("fan-out cursor planning mutated archive:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

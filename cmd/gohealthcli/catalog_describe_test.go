package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestCatalogDescribeDefaultSnapshotJSONContract(t *testing.T) {
	t.Parallel()

	providerCalled := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("network access is forbidden")
	})}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runWithRuntime([]string{"catalog", "describe", "steps", "--json"}, stdout, stderr, runtimeAdapters{httpDoer: client})
	if code != 0 {
		t.Fatalf("catalog describe exit code = %d; stderr=%q; stdout=%q", code, stderr.String(), stdout.String())
	}
	if providerCalled {
		t.Fatal("default catalog describe accessed the network")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := "{\"data_type\":\"steps\",\"compiled\":{\"source\":\"compiled_catalog\",\"endpoint_families\":[" +
		"{\"name\":\"list\",\"filter_field\":\"steps.interval.start_time\",\"lower_bound_only\":false,\"range_shape\":\"physical\",\"page_policy\":{\"pagination\":\"nextPageToken\",\"page_size\":10000,\"page_size_policy\":\"explicit\"}}," +
		"{\"name\":\"reconcile\",\"filter_field\":\"steps.interval.start_time\",\"lower_bound_only\":false,\"range_shape\":\"physical\",\"page_policy\":{\"pagination\":\"nextPageToken\",\"page_size\":10000,\"page_size_policy\":\"explicit\"}}," +
		"{\"name\":\"rollUp\",\"lower_bound_only\":false,\"range_shape\":\"physical\",\"page_policy\":{\"pagination\":\"nextPageToken\",\"page_size\":0,\"page_size_policy\":\"provider_default\"}}," +
		"{\"name\":\"dailyRollUp\",\"lower_bound_only\":false,\"range_shape\":\"daily\",\"page_policy\":{\"pagination\":\"nextPageToken\",\"page_size\":0,\"page_size_policy\":\"provider_default\",\"range_window_max_days\":90}}]," +
		"\"required_scopes\":[\"https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly\"],\"record_kind\":\"interval\",\"rollup_modes\":[\"daily\",\"hourly\",\"weekly\",\"window=<duration>\"]}," +
		"\"discovery\":{\"source\":\"committed_snapshot\",\"revision\":\"20260817\",\"json_field\":\"steps\",\"schema_ref\":\"Steps\",\"fields\":[{\"name\":\"count\",\"json_type\":\"string\"},{\"name\":\"interval\",\"json_type\":\"object\",\"schema_ref\":\"ObservationTimeInterval\"}]}}\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestCatalogDescribeHumanAndPlainContracts(t *testing.T) {
	t.Parallel()

	code, human, stderr := runCommand(t, "catalog", "describe", "total-calories")
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("human exit=%d stderr=%q", code, stderr.String())
	}
	wantHuman := "Google Health Data Type: total-calories\n" +
		"Compiled catalog (compiled_catalog)\n" +
		"- Record kind: none\n" +
		"- Required scopes: https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly\n" +
		"- Rollup modes: daily, hourly, weekly, window=<duration>\n" +
		"- Endpoint families:\n" +
		"  - rollUp: filter none; lower-bound-only false; range physical; pagination nextPageToken; page size provider_default; max range 14 days\n" +
		"  - dailyRollUp: filter none; lower-bound-only false; range daily; pagination nextPageToken; page size provider_default; max range 14 days\n" +
		"Discovery schema (committed_snapshot, revision 20260817)\n" +
		"- JSON field: totalCalories\n" +
		"- Schema: TotalCaloriesRollupValue\n" +
		"- Fields:\n" +
		"  - kcalSum: number\n"
	if human.String() != wantHuman {
		t.Errorf("human stdout = %q, want %q", human.String(), wantHuman)
	}

	code, plain, stderr := runCommand(t, "catalog", "describe", "total-calories", "--plain")
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("plain exit=%d stderr=%q", code, stderr.String())
	}
	wantPlain := "data_type: total-calories\n" +
		"compiled.source: compiled_catalog\n" +
		"compiled.record_kind: none\n" +
		"compiled.required_scopes: https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly\n" +
		"compiled.rollup_modes: daily,hourly,weekly,window=<duration>\n" +
		"compiled.endpoint_family.0.name: rollUp\n" +
		"compiled.endpoint_family.0.filter_field: \n" +
		"compiled.endpoint_family.0.lower_bound_only: false\n" +
		"compiled.endpoint_family.0.range_shape: physical\n" +
		"compiled.endpoint_family.0.page_policy.pagination: nextPageToken\n" +
		"compiled.endpoint_family.0.page_policy.page_size: 0\n" +
		"compiled.endpoint_family.0.page_policy.page_size_policy: provider_default\n" +
		"compiled.endpoint_family.0.page_policy.range_window_max_days: 14\n" +
		"compiled.endpoint_family.1.name: dailyRollUp\n" +
		"compiled.endpoint_family.1.filter_field: \n" +
		"compiled.endpoint_family.1.lower_bound_only: false\n" +
		"compiled.endpoint_family.1.range_shape: daily\n" +
		"compiled.endpoint_family.1.page_policy.pagination: nextPageToken\n" +
		"compiled.endpoint_family.1.page_policy.page_size: 0\n" +
		"compiled.endpoint_family.1.page_policy.page_size_policy: provider_default\n" +
		"compiled.endpoint_family.1.page_policy.range_window_max_days: 14\n" +
		"discovery.source: committed_snapshot\n" +
		"discovery.revision: 20260817\n" +
		"discovery.json_field: totalCalories\n" +
		"discovery.schema_ref: TotalCaloriesRollupValue\n" +
		"discovery.field.0.name: kcalSum\n" +
		"discovery.field.0.json_type: number\n" +
		"discovery.field.0.schema_ref: \n"
	if plain.String() != wantPlain {
		t.Errorf("plain stdout = %q, want %q", plain.String(), wantPlain)
	}
}

func TestCatalogDescribeDiscoverySources(t *testing.T) {
	t.Parallel()

	fixture := filepath.Join("..", "..", "internal", "googlehealth", "testdata", "google-health-discovery-v4.json")
	code, fileOutput, fileStderr := runCommand(t, "catalog", "--json", "describe", "steps", "--discovery", fixture)
	if code != 0 || fileStderr.Len() != 0 {
		t.Fatalf("file exit=%d stderr=%q", code, fileStderr.String())
	}
	if !strings.Contains(fileOutput.String(), `"source":"file"`) || !strings.Contains(fileOutput.String(), `"revision":"20260817"`) {
		t.Errorf("file output = %q", fileOutput.String())
	}

	payload, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != catalogDiscoveryURL {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "" {
			t.Error("live discovery request included authorization")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload)), Header: make(http.Header)}, nil
	})}
	liveOutput := new(bytes.Buffer)
	liveStderr := new(bytes.Buffer)
	code = runWithRuntime([]string{"catalog", "describe", "steps", "--live", "--json"}, liveOutput, liveStderr, runtimeAdapters{httpDoer: client})
	if code != 0 || liveStderr.Len() != 0 {
		t.Fatalf("live exit=%d stderr=%q", code, liveStderr.String())
	}
	if !strings.Contains(liveOutput.String(), `"source":"live"`) || !strings.Contains(liveOutput.String(), `"revision":"20260817"`) {
		t.Errorf("live output = %q", liveOutput.String())
	}
}

func TestCatalogDescribeRejectsInvalidInputsBeforeIO(t *testing.T) {
	t.Parallel()

	providerCalled := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("network access is forbidden")
	})}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing Data Type", args: []string{"catalog", "describe", "--json"}, want: "catalog describe requires exactly one Data Type"},
		{name: "extra Data Type", args: []string{"catalog", "describe", "steps", "sleep", "--json"}, want: "catalog describe requires exactly one Data Type"},
		{name: "unknown Data Type", args: []string{"catalog", "describe", "not-a-data-type", "--live", "--json"}, want: `catalog describe Data Type \"not-a-data-type\" is not in the compiled catalog`},
		{name: "mutually exclusive sources", args: []string{"catalog", "describe", "steps", "--discovery", "missing.json", "--live", "--json"}, want: "catalog describe accepts only one discovery source: --discovery or --live"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			if code := runWithRuntime(test.args, stdout, stderr, runtimeAdapters{httpDoer: client}); code != 1 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), `"status":"flag_invalid"`) || !strings.Contains(stdout.String(), test.want) {
				t.Errorf("stdout=%q, want stable flag failure containing %q", stdout.String(), test.want)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr=%q", stderr.String())
			}
		})
	}
	if providerCalled {
		t.Fatal("invalid input accessed live discovery")
	}
}

func TestCatalogDescribeStableDiscoveryFailures(t *testing.T) {
	t.Parallel()

	temporary := t.TempDir()
	malformed := filepath.Join(temporary, "malformed.json")
	if err := os.WriteFile(malformed, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	incompatible := filepath.Join(temporary, "incompatible.json")
	if err := os.WriteFile(incompatible, []byte(`{"kind":"discovery#restDescription","name":"other","version":"v4","revision":"1","schemas":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(temporary, "oversized.json")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("x"), catalogDiscoveryMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "unavailable", path: filepath.Join(temporary, "missing.json"), want: "discovery document is unavailable"},
		{name: "malformed", path: malformed, want: "discovery document is malformed"},
		{name: "incompatible", path: incompatible, want: `discovery document is incompatible with Data Type \"steps\"`},
		{name: "oversized", path: oversized, want: "discovery document exceeds size limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, "catalog", "describe", "steps", "--discovery", test.path, "--json")
			if code != 1 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), `"status":"operation_failed"`) || !strings.Contains(stdout.String(), test.want) {
				t.Errorf("stdout=%q, want stable operation failure containing %q", stdout.String(), test.want)
			}
		})
	}
}

func TestCatalogDescribeUnexpectedCompiledFailureIsOperationFailure(t *testing.T) {
	t.Parallel()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := reportCatalogDescribeCompiledFailure(errors.New("synthetic compiled failure"), "steps", outputMode{json: true}, stdout, stderr)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	want := "{\"status\":\"operation_failed\",\"message\":\"compiled catalog description failed\"}\n"
	if stdout.String() != want {
		t.Errorf("stdout=%q, want %q", stdout.String(), want)
	}
}

func TestCatalogDescribePropagatesWriteErrors(t *testing.T) {
	t.Parallel()

	for _, modeArg := range []string{"", "--plain", "--json"} {
		args := []string{"describe", "steps"}
		if modeArg != "" {
			args = append(args, modeArg)
		}
		stderr := new(bytes.Buffer)
		if code := runCatalogWithRuntime(args, CommonFlagValues{}, failingWriter{}, stderr, runtimeAdapters{}); code == 0 {
			t.Fatalf("catalog %v exit code = 0, want write failure", args)
		}
	}
}

func TestCatalogDescribeEscapesDiscoveryControlsInTerminalModes(t *testing.T) {
	t.Parallel()

	description := googlehealth.CatalogDescription{
		DataType: "steps",
		Compiled: googlehealth.CatalogCompiledDescription{
			Source:         "compiled_catalog",
			RequiredScopes: []string{"scope"},
			RecordKind:     "interval",
			RollupModes:    []string{},
		},
		Discovery: &googlehealth.CatalogDiscoveryDescription{
			Source:    "file",
			Revision:  "revision\nforged\x1b",
			JSONField: "steps",
			SchemaRef: "Steps\rSchema",
			Fields: []googlehealth.CatalogDiscoveryField{{
				Name:      "count\ncompiled.record_kind: forged",
				JSONType:  "string\tvalue",
				SchemaRef: "Value\x1bSchema",
			}},
		},
	}
	for _, mode := range []outputMode{{}, {plain: true}} {
		stdout := new(bytes.Buffer)
		if err := writeCatalogDescription(description, mode, stdout); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stdout.String(), "count\ncompiled.record_kind: forged") || strings.ContainsRune(stdout.String(), '\x1b') {
			t.Fatalf("terminal output contains raw discovery controls: %q", stdout.String())
		}
		for _, escaped := range []string{`revision\nforged\x1b`, `Steps\rSchema`, `count\ncompiled.record_kind: forged`, `string\tvalue`, `Value\x1bSchema`} {
			if !strings.Contains(stdout.String(), escaped) {
				t.Errorf("terminal output %q missing escaped value %q", stdout.String(), escaped)
			}
		}
	}
}

func TestCatalogDescribeBinaryNeedsNoHomeConfigOrArchive(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runBinaryWithEnv(t, []string{"HOME=", "XDG_CONFIG_HOME=", "XDG_DATA_HOME="}, "catalog", "describe", "steps", "--json")
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"source":"committed_snapshot"`) {
		t.Errorf("stdout=%q", stdout.String())
	}
}

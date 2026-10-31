package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestRawEndpointNamesDiscoverableWithoutSetup(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"raw", "--help"}, {"help", "raw"}, {"schema", "--json"}, {"__completeNoDesc", "raw", "endpoint", ""}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runWithRuntime(args, &stdout, &stderr, forbiddenRawPlanRuntime(t)); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			texts := []string{stdout.String()}
			switch args[0] {
			case "help":
				texts = append(texts, stderr.String())
			case "schema":
				var doc schemaDocument
				if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				texts[0] = ""
				for _, command := range doc.Commands {
					if command.Name == "raw" {
						texts[0] = command.Long
					}
				}
			case "__completeNoDesc":
				if got, want := completionCandidates(stdout.String()), googlehealth.RawEndpointNames(); !reflect.DeepEqual(got, want) {
					t.Fatalf("completion=%v, want %v", got, want)
				}
				if completionDirective(stdout.String()) != 4 || stderr.String() != "Completion ended with directive: ShellCompDirectiveNoFileComp\n" {
					t.Fatalf("completion stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				return
			}
			if args[0] != "help" && stderr.Len() != 0 {
				t.Fatalf("stderr=%q, want empty", stderr.String())
			}
			for _, text := range texts {
				for _, name := range googlehealth.RawEndpointNames() {
					if !strings.Contains(text, "\n- "+name+"\n") {
						t.Errorf("missing supported endpoint %q", name)
					}
				}
				if !strings.Contains(text, "Supported endpoints:") || !strings.Contains(text, "`dataTypes.<data-type>.list` aliases") {
					t.Error("missing endpoint help or list-alias note")
				}
			}
		})
	}
}

func TestRawUnsupportedEndpointReportsNamesAndHintsBeforeSetup(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "getIdentty", want: "getIdentity"},
		{name: "dataTypes.stesp.list", want: "dataTypes.sleep.list, dataTypes.steps.list"},
		{name: ""},
		{name: "unrelated-endpoint"},
		{name: "dataTypes.bogus.list"},
		{name: "dataTypes..list"},
		{name: "dataTypes.total-calories.list"},
	} {
		for _, mode := range []string{"normal", "json-plan", "plain-plan", "human-plan", "output"} {
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				setup := filepath.Join(root, "missing")
				output := filepath.Join(root, "response.json")
				args := []string{"raw", "endpoint", test.name, "--config", filepath.Join(setup, "config.toml"), "--db", filepath.Join(setup, "health.db")}
				switch mode {
				case "json-plan":
					args = append(args, "--plan", "--json")
				case "plain-plan":
					args = append(args, "--plan", "--plain")
				case "human-plan":
					args = append(args, "--plan")
				case "output":
					args = append(args, "--output", output)
				}
				runtime := forbiddenRawPlanRuntime(t)
				runtime.now = func() time.Time {
					t.Fatal("invalid endpoint accessed runtime clock")
					return time.Time{}
				}
				runtime.prepareRawOutput = func(string) (preparedRawOutput, error) {
					t.Fatal("invalid endpoint prepared output")
					return nil, nil
				}
				var stdout, stderr bytes.Buffer
				if code := runWithRuntime(args, &stdout, &stderr, runtime); code != 1 {
					t.Fatalf("exit=%d, want 1", code)
				}
				message := fmt.Sprintf("unsupported raw endpoint %q; supported: %s", test.name, strings.Join(googlehealth.RawEndpointNames(), ", "))
				if mode == "json-plan" {
					var result failureJSONEnvelope
					if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result.Status != StatusFlagInvalid || result.Message != message || stderr.Len() != 0 {
						t.Fatalf("failure=%+v stderr=%q", result, stderr.String())
					}
				} else {
					wantStderr := "raw: " + message + "\n"
					if test.want != "" {
						wantStderr += "Did you mean: " + test.want + "?\n"
					}
					wantStderr += "Run 'gohealthcli raw --help' for the full list of supported endpoints.\n"
					if stderr.String() != wantStderr {
						t.Fatalf("stderr=%q, want %q", stderr.String(), wantStderr)
					}
					wantStdout := ""
					if mode == "plain-plan" {
						wantStdout = "status: flag_invalid\nmessage: " + message + "\n"
					}
					if stdout.String() != wantStdout {
						t.Fatalf("stdout=%q, want %q", stdout.String(), wantStdout)
					}
				}
				for _, path := range []string{setup, output} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Errorf("invalid endpoint touched %q: %v", path, err)
					}
				}
			})
		}
	}
}

func TestRawREADMEIdentityEndpointScopesMatchCatalog(t *testing.T) {
	t.Parallel()
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, raw, ok := strings.Cut(string(readme), "Explore raw provider JSON:")
	if !ok {
		t.Fatal("README raw section missing")
	}
	raw, _, _ = strings.Cut(raw, "\n## ")
	for _, name := range googlehealth.RawEndpointNames() {
		if strings.HasPrefix(name, "dataTypes.") {
			continue
		}
		found := false
		for _, line := range strings.Split(raw, "\n") {
			if !strings.Contains(line, "`"+name+"`") {
				continue
			}
			matchesScopes := true
			for _, scope := range googlehealth.IdentityEndpointScopes(name) {
				matchesScopes = matchesScopes && strings.Contains(line, "`"+strings.TrimPrefix(scope, "https://www.googleapis.com/auth/googlehealth.")+"`")
			}
			found = found || matchesScopes
		}
		if !found {
			t.Errorf("README raw section missing endpoint %q with catalog scopes %v", name, googlehealth.IdentityEndpointScopes(name))
		}
	}
	for _, text := range []string{"gohealthcli raw --help", "connect --add-scopes settings", "connect --add-scopes irn"} {
		if !strings.Contains(raw, text) {
			t.Errorf("README raw section missing %q", text)
		}
	}
}

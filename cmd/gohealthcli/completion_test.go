package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

func TestCompletionBashEmitsShellScript(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "completion", "bash")
	if code != 0 {
		t.Fatalf("completion bash exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "bash completion") {
		t.Fatalf("completion bash output is not a Bash completion script:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestCompletionZshEmitsShellScript(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "completion", "zsh")
	if code != 0 {
		t.Fatalf("completion zsh exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "zsh completion") {
		t.Fatalf("completion zsh output is not a Zsh completion script:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestCompletionFishEmitsShellScript(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "completion", "fish")
	if code != 0 {
		t.Fatalf("completion fish exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fish completion") {
		t.Fatalf("completion fish output is not a Fish completion script:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestCompletionPowerShellEmitsShellScript(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "completion", "powershell")
	if code != 0 {
		t.Fatalf("completion powershell exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "powershell completion") {
		t.Fatalf("completion powershell output is not a PowerShell completion script:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestCompletionRegistryDeclaresShellOperand(t *testing.T) {
	t.Parallel()

	def, ok := lookupCommand("completion")
	if !ok {
		t.Fatal("completion missing from registry")
	}
	if def.PositionalArgs != "<shell>" {
		t.Fatalf("completion positional args = %q, want %q", def.PositionalArgs, "<shell>")
	}
}

func TestCompletionProtocolProjectsVisibleRegistryAndHelp(t *testing.T) {
	t.Parallel()

	code, stdout, _ := runCommand(t, "__completeNoDesc", "")
	if code != 0 {
		t.Fatalf("completion protocol exit code = %d, want 0", code)
	}
	got := completionCandidates(stdout.String())

	want := []string{"help"}
	for _, cmd := range commands {
		if !cmd.Hidden {
			want = append(want, cmd.Name)
		}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("root candidates = %v, want visible registry + help %v", got, want)
	}
}

func TestCompletionProtocolFlagsMatchAcceptedRegistryFlags(t *testing.T) {
	t.Parallel()

	t.Run("global slot", func(t *testing.T) {
		code, stdout, _ := runCommand(t, "__completeNoDesc", "--")
		if code != 0 {
			t.Fatalf("root flag completion exit code = %d, want 0", code)
		}
		want := []string{"--help", "--version"}
		for _, spec := range commonFlagsSpec {
			want = append(want, "--"+spec.Name)
		}
		sort.Strings(want)
		if got := completionCandidates(stdout.String()); !reflect.DeepEqual(got, want) {
			t.Fatalf("root flag candidates = %v, want %v", got, want)
		}
	})

	for _, cmd := range commands {
		if cmd.Hidden {
			continue
		}
		t.Run(cmd.Name, func(t *testing.T) {
			code, stdout, _ := runCommand(t, "__completeNoDesc", cmd.Name, "--")
			if code != 0 {
				t.Fatalf("%s flag completion exit code = %d, want 0", cmd.Name, code)
			}
			want := []string{"--help"}
			for _, spec := range cmd.Flags {
				want = append(want, "--"+spec.Name)
			}
			sort.Strings(want)
			if got := completionCandidates(stdout.String()); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s flag candidates = %v, want registry flags %v", cmd.Name, got, want)
			}
		})
	}
}

func TestCompletionProtocolSuggestsSupportedShells(t *testing.T) {
	t.Parallel()

	code, stdout, _ := runCommand(t, "__completeNoDesc", "completion", "")
	if code != 0 {
		t.Fatalf("completion shell candidates exit code = %d, want 0", code)
	}
	want := append([]string(nil), completionShells...)
	sort.Strings(want)
	if got := completionCandidates(stdout.String()); !reflect.DeepEqual(got, want) {
		t.Fatalf("completion shell candidates = %v, want %v", got, want)
	}
}

func TestCompletionProtocolSuggestsExportDatasets(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "__completeNoDesc", "export", "")
	if code != 0 {
		t.Fatalf("export dataset completion exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	want := exportDatasetCatalogSingleton.Names()
	if got := completionCandidates(stdout.String()); !reflect.DeepEqual(got, want) {
		t.Fatalf("export dataset candidates = %v, want catalog %v", got, want)
	}
}

func TestCompletionProtocolSuggestsCatalogFlagValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		args          []string
		want          []string
		wantDirective int
	}{
		{
			name:          "sync Data Types",
			args:          []string{"__completeNoDesc", "sync", "--types", ""},
			want:          googlehealth.SyncableDataTypes(),
			wantDirective: 6,
		},
		{
			name:          "connect scope keywords",
			args:          []string{"__completeNoDesc", "connect", "--add-scopes", ""},
			want:          strings.Split(supportedAddScopeKeywords(), ", "),
			wantDirective: 6,
		},
		{
			name:          "sync source family",
			args:          []string{"__completeNoDesc", "sync", "--source-family", ""},
			want:          googlehealth.SupportedSourceFamilies(),
			wantDirective: 4,
		},
		{
			name:          "raw source family",
			args:          []string{"__completeNoDesc", "raw", "--source-family", ""},
			want:          googlehealth.SupportedSourceFamilies(),
			wantDirective: 4,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, test.args...)
			if code != 0 {
				t.Fatalf("completion exit code = %d, want 0\nstderr: %s", code, stderr.String())
			}
			sort.Strings(test.want)
			if got := completionCandidates(stdout.String()); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("candidates = %v, want canonical catalog %v", got, test.want)
			}
			if got := completionDirective(stdout.String()); got != test.wantDirective {
				t.Fatalf("directive = %d, want %d", got, test.wantDirective)
			}
		})
	}
}

func TestCompletionProtocolPreservesCSVPREFIXAndSuppressesDuplicates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "sync Data Type prefix",
			args: []string{"__completeNoDesc", "sync", "--types", "steps,he"},
			want: []string{"steps,heart-rate", "steps,heart-rate-variability", "steps,height"},
		},
		{
			name: "scope keyword prefix",
			args: []string{"__completeNoDesc", "connect", "--add-scopes", "ecg,i"},
			want: []string{"ecg,irn"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, test.args...)
			if code != 0 {
				t.Fatalf("completion exit code = %d, want 0\nstderr: %s", code, stderr.String())
			}
			if got := completionCandidates(stdout.String()); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("candidates = %v, want prefix-preserving values %v", got, test.want)
			}
			if got := completionDirective(stdout.String()); got != 6 {
				t.Fatalf("directive = %d, want no-space + no-file", got)
			}
		})
	}
}

func TestCompletionProtocolSuggestsRollupModes(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "__completeNoDesc", "sync", "--rollup", "")
	if code != 0 {
		t.Fatalf("rollup completion exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if got, want := completionCandidates(stdout.String()), []string{"daily", "hourly", "weekly"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rollup candidates = %v, want fixed canonical modes %v", got, want)
	}
	if got := completionDirective(stdout.String()); got != 4 {
		t.Fatalf("directive = %d, want ShellCompDirectiveNoFileComp", got)
	}

	code, stdout, stderr = runCommand(t, "__completeNoDesc", "sync", "--rollup", "wind")
	if code != 0 {
		t.Fatalf("window rollup completion exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if got, want := completionCandidates(stdout.String()), []string{"window="}; !reflect.DeepEqual(got, want) {
		t.Fatalf("window rollup candidates = %v, want %v", got, want)
	}
	if got := completionDirective(stdout.String()); got != 6 {
		t.Fatalf("directive = %d, want no-space + no-file", got)
	}

	code, stdout, stderr = runCommand(t, "__completeNoDesc", "sync", "--rollup", "window=")
	if code != 0 {
		t.Fatalf("window duration completion exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if got := completionCandidates(stdout.String()); len(got) != 0 {
		t.Fatalf("window duration candidates = %v, want free-form duration", got)
	}
	if got := completionDirective(stdout.String()); got != 4 {
		t.Fatalf("directive = %d, want no-file for duration", got)
	}
}

func TestCompletionProtocolSuggestsRawTargetsAndValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "targets",
			args: []string{"__completeNoDesc", "raw", ""},
			want: googlehealth.RawTargetNames(),
		},
		{
			name: "Data Types",
			args: []string{"__completeNoDesc", "raw", "data-type", ""},
			want: googlehealth.RawDataTypes(),
		},
		{
			name: "endpoints",
			args: []string{"__completeNoDesc", "raw", "endpoint", ""},
			want: googlehealth.RawEndpointNames(),
		},
		{
			name: "get and reconcile operations",
			args: []string{"__completeNoDesc", "raw", "data-type", "weight", ""},
			want: []string{"get", "reconcile"},
		},
		{
			name: "reconcile operation",
			args: []string{"__completeNoDesc", "raw", "data-type", "steps", ""},
			want: []string{"reconcile"},
		},
		{
			name: "reconcile-only Data Type operation",
			args: []string{"__completeNoDesc", "raw", "data-type", "floors", ""},
			want: []string{"reconcile"},
		},
		{
			name: "list-only Data Type operation",
			args: []string{"__completeNoDesc", "raw", "data-type", "electrocardiogram", ""},
			want: nil,
		},
		{
			name: "endpoint prefix",
			args: []string{"__completeNoDesc", "raw", "endpoint", "dataTypes.he"},
			want: []string{
				"dataTypes.heart-rate-variability.list",
				"dataTypes.heart-rate.list",
				"dataTypes.height.list",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, test.args...)
			if code != 0 {
				t.Fatalf("completion exit code = %d, want 0\nstderr: %s", code, stderr.String())
			}
			sort.Strings(test.want)
			if got := completionCandidates(stdout.String()); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("candidates = %v, want canonical catalog %v", got, test.want)
			}
			if got := completionDirective(stdout.String()); got != 4 {
				t.Fatalf("directive = %d, want ShellCompDirectiveNoFileComp", got)
			}
		})
	}
}

func TestCompletionProtocolFilesystemFallbackIsPathOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		args          []string
		wantDirective int
	}{
		{name: "config path", args: []string{"__completeNoDesc", "status", "--config", ""}, wantDirective: 0},
		{name: "archive path", args: []string{"__completeNoDesc", "status", "--db", ""}, wantDirective: 0},
		{name: "OAuth client path", args: []string{"__completeNoDesc", "init", "--oauth-client-file", ""}, wantDirective: 0},
		{name: "export output path", args: []string{"__completeNoDesc", "export", "--output", ""}, wantDirective: 0},
		{name: "raw output path", args: []string{"__completeNoDesc", "raw", "--output", ""}, wantDirective: 0},
		{name: "query SQL", args: []string{"__completeNoDesc", "query", "SEL"}, wantDirective: 4},
		{name: "sync date", args: []string{"__completeNoDesc", "sync", "--from", ""}, wantDirective: 4},
		{name: "sync timezone", args: []string{"__completeNoDesc", "sync", "--timezone", ""}, wantDirective: 4},
		{name: "raw timezone", args: []string{"__completeNoDesc", "raw", "--timezone", ""}, wantDirective: 4},
		{name: "secret provider", args: []string{"__completeNoDesc", "init", "--secret-provider", ""}, wantDirective: 4},
		{name: "secret item", args: []string{"__completeNoDesc", "init", "--oauth-client-item", ""}, wantDirective: 4},
		{name: "page size", args: []string{"__completeNoDesc", "raw", "--page-size", ""}, wantDirective: 4},
		{name: "page token", args: []string{"__completeNoDesc", "raw", "--page-token", ""}, wantDirective: 4},
		{name: "command without positionals", args: []string{"__completeNoDesc", "status", ""}, wantDirective: 4},
		{name: "export after dataset", args: []string{"__completeNoDesc", "export", "daily-steps", ""}, wantDirective: 4},
		{name: "completion after shell", args: []string{"__completeNoDesc", "completion", "bash", ""}, wantDirective: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, test.args...)
			if code != 0 {
				t.Fatalf("completion exit code = %d, want 0\nstderr: %s", code, stderr.String())
			}
			if got := completionCandidates(stdout.String()); len(got) != 0 {
				t.Fatalf("candidates = %v, want none", got)
			}
			if got := completionDirective(stdout.String()); got != test.wantDirective {
				t.Fatalf("directive = %d, want %d", got, test.wantDirective)
			}
		})
	}
}

func TestCompletionRegistryClassifiesEveryValueSurface(t *testing.T) {
	t.Parallel()

	for _, spec := range commonFlagsSpec {
		if (spec.Type == "string" || spec.Type == "int") && spec.ValueCompletion == valueCompletionUnspecified {
			t.Errorf("common flag --%s has no value completion policy", spec.Name)
		}
	}
	for _, command := range commands {
		if command.PositionalArgs != "" && command.PositionalCompletion == valueCompletionUnspecified {
			t.Errorf("%s positional %s has no value completion policy", command.Name, command.PositionalArgs)
		}
		for _, spec := range command.Flags {
			if (spec.Type == "string" || spec.Type == "int") && spec.ValueCompletion == valueCompletionUnspecified {
				t.Errorf("%s flag --%s has no value completion policy", command.Name, spec.Name)
			}
		}
	}
}

func TestCompletionProtocolCatalogCandidatesAreStableAndSorted(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"__completeNoDesc", "export", ""},
		{"__completeNoDesc", "sync", "--types", ""},
		{"__completeNoDesc", "connect", "--add-scopes", ""},
		{"__completeNoDesc", "raw", "endpoint", ""},
	}
	for _, args := range tests {
		code, first, stderr := runCommand(t, append([]string(nil), args...)...)
		if code != 0 {
			t.Fatalf("%v first completion exit code = %d, want 0\nstderr: %s", args, code, stderr.String())
		}
		code, second, stderr := runCommand(t, append([]string(nil), args...)...)
		if code != 0 {
			t.Fatalf("%v second completion exit code = %d, want 0\nstderr: %s", args, code, stderr.String())
		}
		if first.String() != second.String() {
			t.Fatalf("%v completion changed between identical requests", args)
		}
		got := completionCandidatesInOrder(first.String())
		want := append([]string(nil), got...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%v candidates = %v, want lexical order %v", args, got, want)
		}
	}
}

func TestCompletionProtocolSuggestsCatalogActions(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "__completeNoDesc", "catalog", "")
	if code != 0 {
		t.Fatalf("completion exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got, want := completionCandidates(stdout.String()), []string{"describe", "list", "scopes", "verify"}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

func TestCompletionProtocolSuggestsCatalogDescribeDataTypes(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCommand(t, "__completeNoDesc", "catalog", "describe", "hea")
	if code != 0 {
		t.Fatalf("completion exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got, want := completionCandidates(stdout.String()), []string{"heart-rate", "heart-rate-variability"}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

func TestCompletionProtocolTraversesGlobalFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		candidate string
	}{
		{
			name:      "subcommand after boolean global",
			args:      []string{"__completeNoDesc", "--json", ""},
			candidate: "status",
		},
		{
			name:      "command flag after valued global",
			args:      []string{"__completeNoDesc", "--config", "/tmp/config.toml", "status", "--"},
			candidate: "--db",
		},
		{
			name:      "shell after global",
			args:      []string{"__completeNoDesc", "--plain", "completion", ""},
			candidate: "bash",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, test.args...)
			if code != 0 {
				t.Fatalf("completion protocol exit code = %d, want 0\nstderr: %s", code, stderr.String())
			}
			if got := completionCandidates(stdout.String()); !containsString(got, test.candidate) {
				t.Fatalf("candidates = %v, want %q", got, test.candidate)
			}
		})
	}
}

func TestCompletionNoDescriptionsSelectsNoDescriptionProtocol(t *testing.T) {
	t.Parallel()

	for _, shell := range completionShells {
		t.Run(shell, func(t *testing.T) {
			code, described, stderr := runCommand(t, "completion", shell)
			if code != 0 {
				t.Fatalf("completion %s exit code = %d, want 0\nstderr: %s", shell, code, stderr.String())
			}
			code, plain, stderr := runCommand(t, "completion", shell, "--no-descriptions")
			if code != 0 {
				t.Fatalf("completion %s --no-descriptions exit code = %d, want 0\nstderr: %s", shell, code, stderr.String())
			}
			if strings.Contains(described.String(), "__completeNoDesc") {
				t.Fatalf("completion %s unexpectedly selected the no-description protocol", shell)
			}
			if !strings.Contains(plain.String(), "__completeNoDesc") {
				t.Fatalf("completion %s --no-descriptions did not select the no-description protocol", shell)
			}
		})
	}
}

func TestCompletionScriptsAreDeterministic(t *testing.T) {
	t.Parallel()

	for _, shell := range completionShells {
		for _, noDescriptions := range []bool{false, true} {
			name := shell
			args := []string{"completion", shell}
			if noDescriptions {
				name += "-no-descriptions"
				args = append(args, "--no-descriptions")
			}
			t.Run(name, func(t *testing.T) {
				code, first, stderr := runCommand(t, args...)
				if code != 0 {
					t.Fatalf("first generation exit code = %d, want 0\nstderr: %s", code, stderr.String())
				}
				code, second, stderr := runCommand(t, args...)
				if code != 0 {
					t.Fatalf("second generation exit code = %d, want 0\nstderr: %s", code, stderr.String())
				}
				if first.String() != second.String() {
					t.Fatalf("%s completion output changed between identical generations", name)
				}
			})
		}
	}
}

func TestCompletionFailuresUseFailureReporter(t *testing.T) {
	t.Parallel()

	t.Run("unsupported shell default mode", func(t *testing.T) {
		code, stdout, stderr := runCommand(t, "completion", "nushell")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if stdout.String() != "" {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
		const want = "completion: unsupported shell \"nushell\"; choose bash, zsh, fish, or powershell\n"
		if stderr.String() != want {
			t.Fatalf("stderr = %q, want %q", stderr.String(), want)
		}
	})

	t.Run("missing shell JSON mode", func(t *testing.T) {
		code, stdout, stderr := runCommand(t, "--json", "completion")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		const want = "{\"status\":\"flag_invalid\",\"message\":\"shell is required; choose bash, zsh, fish, or powershell\"}\n"
		if stdout.String() != want {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
		if stderr.String() != "" {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
	})

	t.Run("extra argument plain mode", func(t *testing.T) {
		code, stdout, stderr := runCommand(t, "--plain", "completion", "bash", "extra")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		const wantOut = "status: unexpected_argument\nmessage: unexpected arguments after bash: extra\n"
		if stdout.String() != wantOut {
			t.Fatalf("stdout = %q, want %q", stdout.String(), wantOut)
		}
		const wantErr = "completion: unexpected arguments after bash: extra\n"
		if stderr.String() != wantErr {
			t.Fatalf("stderr = %q, want %q", stderr.String(), wantErr)
		}
	})

	t.Run("unknown flag JSON mode", func(t *testing.T) {
		code, stdout, stderr := runCommand(t, "--json", "completion", "bash", "--bogus")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		const want = "{\"status\":\"flag_invalid\",\"message\":\"flag provided but not defined: -bogus\"}\n"
		if stdout.String() != want {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
		if stderr.String() != "" {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
	})
}

func TestCompletionBuiltBinaryHasNoLocalOrProviderSideEffects(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "invalid-config.toml")
	archivePath := filepath.Join(tempDir, "invalid-archive.sqlite")
	if err := os.WriteFile(configPath, []byte("not valid TOML"), 0o600); err != nil {
		t.Fatalf("write config sentinel: %v", err)
	}
	if err := os.WriteFile(archivePath, []byte("not a SQLite archive"), 0o600); err != nil {
		t.Fatalf("write archive sentinel: %v", err)
	}
	before := snapshotCompletionSandbox(t, tempDir)
	privateEnv := []string{
		"HOME=" + tempDir,
		"XDG_CONFIG_HOME=" + filepath.Join(tempDir, "xdg-config"),
		"XDG_DATA_HOME=" + filepath.Join(tempDir, "xdg-data"),
	}

	for _, shell := range completionShells {
		code, _, stderr := runBinaryInDirWithEnv(t, tempDir, privateEnv,
			"--config", configPath,
			"--db", archivePath,
			"completion", shell,
		)
		if code != 0 {
			t.Fatalf("completion %s exit code = %d, want 0\nstderr: %s", shell, code, stderr.String())
		}
	}
	code, stdout, _ := runBinaryInDirWithEnv(t, tempDir, privateEnv,
		"__completeNoDesc",
		"--config", configPath,
		"--db", archivePath,
		"status", "",
	)
	if code != 0 {
		t.Fatalf("completion protocol exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), ":") {
		t.Fatalf("completion protocol output missing directive: %q", stdout.String())
	}

	if after := snapshotCompletionSandbox(t, tempDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("completion changed sandbox\nbefore: %v\nafter:  %v", before, after)
	}
}

func TestCompletionWriteFailureUsesFailureReporter(t *testing.T) {
	t.Parallel()

	stderr := new(strings.Builder)
	code := runCompletionWithRegistry(
		[]string{"bash"},
		commands,
		outputMode{},
		failingWriter{},
		stderr,
		nil,
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	const want = "completion: write completion script: write failed\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestCompletionHonorsEndOfFlagsMarker(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCommand(t, "completion", "--", "--no-descriptions")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	const want = "completion: unsupported shell \"--no-descriptions\"; choose bash, zsh, fish, or powershell\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestGeneratedCompletionScriptsParseInInstalledShells(t *testing.T) {
	tests := []struct {
		shell       string
		executables []string
		parseArgs   func(string) []string
	}{
		{shell: "bash", executables: []string{"bash"}, parseArgs: func(path string) []string {
			return []string{"-n", path}
		}},
		{shell: "zsh", executables: []string{"zsh"}, parseArgs: func(path string) []string {
			return []string{"-n", path}
		}},
		{shell: "fish", executables: []string{"fish"}, parseArgs: func(path string) []string {
			return []string{"-n", path}
		}},
		{shell: "powershell", executables: []string{"pwsh", "powershell"}, parseArgs: func(path string) []string {
			quotedPath := strings.ReplaceAll(path, "'", "''")
			program := fmt.Sprintf(
				"$tokens=$null;$errors=$null;[System.Management.Automation.Language.Parser]::ParseFile('%s',[ref]$tokens,[ref]$errors)|Out-Null;if($errors.Count -ne 0){$errors|ForEach-Object{Write-Error $_};exit 1}",
				quotedPath,
			)
			return []string{"-NoProfile", "-NonInteractive", "-Command", program}
		}},
	}

	for _, test := range tests {
		t.Run(test.shell, func(t *testing.T) {
			var executable string
			for _, candidate := range test.executables {
				path, err := exec.LookPath(candidate)
				if err == nil {
					executable = path
					break
				}
			}
			if executable == "" {
				t.Skipf("%s parser is not installed", test.shell)
			}

			code, stdout, stderr := runBinary(t, "completion", test.shell)
			if code != 0 {
				t.Fatalf("completion %s exit code = %d, want 0\nstderr: %s", test.shell, code, stderr.String())
			}
			scriptPath := filepath.Join(t.TempDir(), "gohealthcli-completion")
			if err := os.WriteFile(scriptPath, stdout.Bytes(), 0o600); err != nil {
				t.Fatalf("write %s completion script: %v", test.shell, err)
			}
			parser := exec.CommandContext(context.Background(), executable, test.parseArgs(scriptPath)...)
			if output, err := parser.CombinedOutput(); err != nil {
				t.Fatalf("%s parser rejected generated script: %v\n%s", test.shell, err, output)
			}
		})
	}
}

func snapshotCompletionSandbox(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(content)
		}
		snapshot[relative] = value
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot completion sandbox: %v", err)
	}
	return snapshot
}

func completionCandidates(output string) []string {
	candidates := completionCandidatesInOrder(output)
	sort.Strings(candidates)
	return candidates
}

func completionCandidatesInOrder(output string) []string {
	var candidates []string
	for _, line := range strings.Split(output, "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		candidate, _, _ := strings.Cut(line, "\t")
		candidates = append(candidates, candidate)
	}
	return candidates
}

func completionDirective(output string) int {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, ":") {
			continue
		}
		value, err := strconv.Atoi(strings.TrimPrefix(line, ":"))
		if err != nil {
			return -1
		}
		return value
	}
	return -1
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

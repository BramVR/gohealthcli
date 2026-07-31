package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
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
	var candidates []string
	for _, line := range strings.Split(output, "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		candidate, _, _ := strings.Cut(line, "\t")
		candidates = append(candidates, candidate)
	}
	sort.Strings(candidates)
	return candidates
}

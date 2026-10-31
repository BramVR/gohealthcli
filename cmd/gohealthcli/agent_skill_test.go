package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BramVR/gohealthcli/internal/googlehealth"
)

const agentSkillRelativePath = "../../skills/gohealthcli/SKILL.md"

func TestAgentSkillPortableFrontmatterAndSize(t *testing.T) {
	t.Parallel()
	content := readAgentSkill(t)
	lines := strings.Split(content, "\n")
	if len(lines) >= 500 {
		t.Fatalf("SKILL.md has %d lines, want fewer than 500", len(lines))
	}
	if len(lines) < 5 || lines[0] != "---" {
		t.Fatal("SKILL.md missing opening YAML frontmatter delimiter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		t.Fatal("SKILL.md missing closing YAML frontmatter delimiter")
	}
	fields := map[string]string{}
	for _, line := range lines[1:end] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("frontmatter line %q is not a key/value pair", line)
		}
		fields[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	if len(fields) != 2 {
		t.Fatalf("frontmatter fields = %v, want only name and description", fields)
	}
	if fields["name"] != "gohealthcli" {
		t.Fatalf("frontmatter name = %q, want gohealthcli", fields["name"])
	}
	if strings.TrimSpace(fields["description"]) == "" {
		t.Fatal("frontmatter description is empty")
	}
}

func TestAgentSkillCommandsAndFlagsMatchRegistry(t *testing.T) {
	t.Parallel()
	content := readAgentSkill(t)
	invocations := agentSkillInvocations(content)
	if len(invocations) == 0 {
		t.Fatal("SKILL.md has no gohealthcli invocations to verify")
	}

	visible := make(map[string]commandDef)
	allVisibleFlags := make(map[string]bool)
	for _, command := range commands {
		if command.Hidden {
			continue
		}
		visible[command.Name] = command
		for _, spec := range command.Flags {
			allVisibleFlags[spec.Name] = true
		}
	}
	for _, invocation := range invocations {
		words := strings.Fields(invocation)
		if len(words) < 2 {
			t.Fatalf("incomplete invocation %q", invocation)
		}
		command, ok := visible[words[1]]
		if !ok {
			t.Errorf("invocation %q references non-visible command %q", invocation, words[1])
			continue
		}
		flags := make(map[string]bool, len(command.Flags))
		for _, spec := range command.Flags {
			flags[spec.Name] = true
		}
		for _, word := range words[2:] {
			if !strings.HasPrefix(word, "--") {
				continue
			}
			name := strings.TrimPrefix(word, "--")
			if before, _, found := strings.Cut(name, "="); found {
				name = before
			}
			name = strings.TrimRight(name, "`'\",.;:")
			if !flags[name] {
				t.Errorf("invocation %q references flag --%s absent from %s registry entry", invocation, name, command.Name)
			}
		}
	}
	referencedFlag := regexp.MustCompile(`--([a-z][a-z0-9-]*)`)
	for _, match := range referencedFlag.FindAllStringSubmatch(content, -1) {
		if !allVisibleFlags[match[1]] {
			t.Errorf("SKILL.md references flag --%s absent from every visible command registry entry", match[1])
		}
	}
}

func TestAgentSkillDataTypesMatchGoogleHealthCatalog(t *testing.T) {
	t.Parallel()
	content := readAgentSkill(t)
	references := map[string]bool{}
	typesFlag := regexp.MustCompile(`--types(?:=|\s+)([a-z0-9,-]+)`)
	for _, match := range typesFlag.FindAllStringSubmatch(content, -1) {
		for _, name := range strings.Split(match[1], ",") {
			references[name] = true
		}
	}
	rawTarget := regexp.MustCompile(`gohealthcli raw data-type ([a-z0-9-]+)([^\n]*)`)
	for _, match := range rawTarget.FindAllStringSubmatch(content, -1) {
		name := match[1]
		references[name] = true
		options := googlehealth.RawRequestOptions{
			Target:     []string{"data-type", name},
			From:       "2026-01-01",
			To:         "2026-01-02",
			ResolvedAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		}
		switch {
		case strings.Contains(match[2], " daily-rollup"):
			options.Target = append(options.Target, "daily-rollup")
		case strings.Contains(match[2], " rollup"):
			options.Target = append(options.Target, "rollup")
			options.Window = "1h"
			options.WindowProvided = true
		case strings.Contains(match[2], " reconcile"):
			options.Target = append(options.Target, "reconcile")
			options.SourceFamily = "wearable"
			options.SourceFamilyProvided = true
		case strings.Contains(match[2], " get"):
			options.Target = append(options.Target, "get")
			options.ID = "synthetic-id"
			options.IDProvided = true
			options.From = ""
			options.To = ""
		}
		if _, err := googlehealth.BuildRawRequest(options); err != nil {
			t.Errorf("SKILL.md raw Data Type %q is absent from the canonical raw request catalog: %v", name, err)
		}
	}
	namedType := regexp.MustCompile("Data Type `([a-z0-9-]+)`")
	for _, match := range namedType.FindAllStringSubmatch(content, -1) {
		references[match[1]] = true
	}
	if len(references) == 0 {
		t.Fatal("SKILL.md has no concrete Data Type reference to verify")
	}
	if len(references) >= 10 {
		t.Fatalf("SKILL.md contains %d concrete Data Types; keep examples concise instead of embedding a static catalog", len(references))
	}
	for name := range references {
		if !slices.Contains(googlehealth.RawDataTypes(), name) {
			t.Errorf("SKILL.md references Data Type %q, which has no raw operation in the canonical Google Health catalog", name)
		}
	}
}

func TestAgentSkillDatasetsAndViewsMatchRegistry(t *testing.T) {
	t.Parallel()
	content := readAgentSkill(t)

	datasetPattern := regexp.MustCompile("(?:gohealthcli export|export dataset) `?([a-z0-9-]+)")
	datasets := datasetPattern.FindAllStringSubmatch(content, -1)
	if len(datasets) == 0 {
		t.Fatal("SKILL.md has no export dataset reference to verify")
	}
	for _, match := range datasets {
		if _, ok := exportDatasetCatalogSingleton.Find(match[1]); !ok {
			t.Errorf("SKILL.md references export dataset %q absent from the canonical registry", match[1])
		}
	}

	viewsBySQLName := make(map[string]bool)
	registry := normalizedViewsRegistry()
	for _, name := range registry.Catalog() {
		spec, ok := registry.View(name)
		if ok {
			viewsBySQLName[spec.view] = true
		}
	}
	referencedViews := map[string]bool{}
	viewPattern := regexp.MustCompile("Normalized View `([a-z0-9_]+)`")
	for _, match := range viewPattern.FindAllStringSubmatch(content, -1) {
		referencedViews[match[1]] = true
	}
	fromPattern := regexp.MustCompile(`\bFROM ([a-z0-9_]+)\b`)
	for _, match := range fromPattern.FindAllStringSubmatch(content, -1) {
		referencedViews[match[1]] = true
	}
	if len(referencedViews) == 0 {
		t.Fatal("SKILL.md has no Normalized View reference to verify")
	}
	for name := range referencedViews {
		if !viewsBySQLName[name] {
			t.Errorf("SKILL.md references Normalized View %q absent from the canonical registry", name)
		}
	}
}

func TestAgentSkillPinsSafetyBoundaries(t *testing.T) {
	t.Parallel()
	content := strings.Join(strings.Fields(readAgentSkill(t)), " ")
	required := []string{
		"does not write or delete Provider health data",
		"cannot complete OAuth headlessly",
		"Do not upload or share",
		"Do not schedule background",
		"Do not provide medical interpretation",
		"one Google Identity per Health Archive",
		"Never reconnect an existing archive to a different identity",
		"Never infer a cursor from the newest archived timestamp",
		"user-approved private destination",
		"built-in exact-byte output path",
		"refuses symbolic links and every overwrite",
		"ACL-protected local application-data directory",
	}
	for _, phrase := range required {
		if !strings.Contains(content, phrase) {
			t.Errorf("SKILL.md missing safety boundary %q", phrase)
		}
	}
	for _, invocation := range agentSkillInvocations(readAgentSkill(t)) {
		redirected := strings.Contains(invocation, ">") || strings.Contains(invocation, "| Set-Content ")
		planned := strings.Contains(invocation, " --plan")
		fileOutput := strings.Contains(invocation, " --output ")
		if strings.HasPrefix(invocation, "gohealthcli raw ") && !planned && !redirected && !fileOutput {
			t.Errorf("raw Provider invocation must use safe file output or redirect sensitive stdout: %q", invocation)
		}
	}
}

func TestREADMEIncludesAgentSkillInstallCommand(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	const install = "npx skills add BramVR/gohealthcli --skill gohealthcli"
	if !strings.Contains(string(content), install) {
		t.Fatalf("README missing Agent Skill install command %q", install)
	}
}

func TestNormalizeAgentSkillTextHandlesCRLF(t *testing.T) {
	t.Parallel()
	const input = "---\r\nname: gohealthcli\r\n---\r\n"
	const want = "---\nname: gohealthcli\n---\n"
	if got := normalizeAgentSkillText([]byte(input)); got != want {
		t.Fatalf("normalizeAgentSkillText() = %q, want %q", got, want)
	}
}

func readAgentSkill(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(agentSkillRelativePath)
	if err != nil {
		t.Fatalf("read %s: %v", agentSkillRelativePath, err)
	}
	return normalizeAgentSkillText(content)
}

func normalizeAgentSkillText(content []byte) string {
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func agentSkillInvocations(content string) []string {
	seen := map[string]bool{}
	var invocations []string
	add := func(invocation string) {
		invocation = strings.TrimSpace(invocation)
		if invocation == "" || seen[invocation] {
			return
		}
		seen[invocation] = true
		invocations = append(invocations, invocation)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gohealthcli ") {
			add(line)
		}
	}
	inline := regexp.MustCompile("`(gohealthcli [^`\\n]+)`")
	for _, match := range inline.FindAllStringSubmatch(content, -1) {
		add(match[1])
	}
	return invocations
}

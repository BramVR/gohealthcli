package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestNoArgsPrintsHelpToStdout pins the PRD #143 slice 3 behaviour: a bare
// `gohealthcli` invocation now exits 0 and prints the top-level help (the
// "Subcommands:" listing) to stdout, matching `git` / `kubectl` / `docker`
// discoverability conventions. The explicit `--help` path is unchanged and
// keeps writing to stderr per stdlib flag-package convention — see
// TestHelpExitsSuccessfully below.
func TestNoArgsPrintsHelpToStdout(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Subcommands:") {
		t.Fatalf("stdout missing 'Subcommands:' help block; got:\n%s", stdout.String())
	}
	if strings.Contains(stderr.String(), "missing command") {
		t.Fatalf("stderr still emits 'missing command'; got: %q", stderr.String())
	}
}

// TestUnknownCommandPrintsHintAndExitsNonZero covers the unknown-command
// path for an input that has no close Levenshtein match: stderr must carry
// both the "unknown command" line (preserved from the pre-slice-3 shape) and
// the canonical "Run 'gohealthcli --help'" hint introduced by slice 3, and
// the process must exit 1. We deliberately choose "bogus-cmd" because it is
// far from every registered command, ensuring no "Did you mean" line
// interleaves and the hint line is the sole new addition under test.
func TestUnknownCommandPrintsHintAndExitsNonZero(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "bogus-cmd")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "unknown command: bogus-cmd") {
		t.Fatalf("stderr missing 'unknown command: bogus-cmd'; got: %q", out)
	}
	if !strings.Contains(out, "Run 'gohealthcli --help' for a list of commands.") {
		t.Fatalf("stderr missing canonical help hint; got: %q", out)
	}
	if strings.Contains(out, "Did you mean") {
		t.Fatalf("stderr should not suggest for unrelated typo; got: %q", out)
	}
}

// TestUnknownCommandSuggestsCloseMatch closes the loop on the suggestion
// pipeline: when the typo lands within the Levenshtein cutoff, stderr must
// carry a "Did you mean: <name>?" line between the unknown-command banner
// and the help hint. We assert on the full sentence so a future tweak to
// the suggestion phrasing trips this test rather than silently shipping a
// regression in UX.
func TestUnknownCommandSuggestsCloseMatch(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "stauts")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "Did you mean: status?") {
		t.Fatalf("stderr missing 'Did you mean: status?'; got: %q", out)
	}
	if !strings.Contains(out, "Run 'gohealthcli --help' for a list of commands.") {
		t.Fatalf("stderr missing canonical help hint; got: %q", out)
	}
}

func TestHelpExitsSuccessfully(t *testing.T) {
	t.Parallel()
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

func TestTopLevelHelpListsVisibleSubcommandsFromRegistry(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	out := stderr.String()
	// Assert each visible command appears on its OWN dedicated line of the form
	// "  <name>   <short>". A bare strings.Contains check would false-pass when
	// the command name shows up inside another command's Short (e.g. "raw" is
	// substring of the "raw" command's own Short "Print raw provider JSON ...").
	lines := strings.Split(out, "\n")
	for _, cmd := range commands {
		if cmd.Hidden {
			continue
		}
		found := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			fields := strings.SplitN(trimmed, " ", 2)
			if len(fields) == 2 && fields[0] == cmd.Name && strings.Contains(line, cmd.Short) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--help missing dedicated line for %q (%q)\noutput:\n%s", cmd.Name, cmd.Short, out)
		}
	}
}

func TestTopLevelHelpOmitsHiddenSubcommands(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	// The "schema" command is the only hidden entry; assert it is filtered.
	// We look for it as a whole word in a "  schema  <short>" line so that
	// substrings inside other words (e.g. "describe-schema") don't false-positive.
	for _, line := range strings.Split(stderr.String(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "schema ") || trimmed == "schema" {
			t.Fatalf("--help should not list hidden subcommand 'schema'; got line %q\nfull output:\n%s", line, stderr.String())
		}
	}
}

func TestTopLevelHelpStillShowsGlobalFlags(t *testing.T) {
	t.Parallel()
	code, _, stderr := runCommand(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	out := stderr.String()
	for _, flagName := range []string{"-config", "-db", "-json", "-plain", "-no-input", "-version"} {
		if !strings.Contains(out, flagName) {
			t.Errorf("--help missing global flag %q\noutput:\n%s", flagName, out)
		}
	}
}

// TestHelpVerbMatchesTopLevelHelp asserts `gohealthcli help` is an alias for
// `gohealthcli --help`: same exit code, identical stderr bytes. The verb form
// reads more naturally than the flag form and is what users reach for first.
func TestHelpVerbMatchesTopLevelHelp(t *testing.T) {
	t.Parallel()
	codeFlag, _, stderrFlag := runCommand(t, "--help")
	codeVerb, stdoutVerb, stderrVerb := runCommand(t, "help")

	if codeFlag != 0 {
		t.Fatalf("--help exit code = %d, want 0\nstderr: %s", codeFlag, stderrFlag.String())
	}
	if codeVerb != 0 {
		t.Fatalf("help exit code = %d, want 0\nstderr: %s", codeVerb, stderrVerb.String())
	}
	if stdoutVerb.String() != "" {
		t.Fatalf("help stdout = %q, want empty", stdoutVerb.String())
	}
	if stderrVerb.String() != stderrFlag.String() {
		t.Fatalf("help stderr differs from --help stderr\nhelp:\n%s\n--help:\n%s", stderrVerb.String(), stderrFlag.String())
	}
}

// TestHelpVerbWithKnownSubcommand asserts `gohealthcli help status` prints
// the status subcommand's Long description followed by its accepted flags,
// exits 0, and does not write to stdout.
func TestHelpVerbWithKnownSubcommand(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "help", "status")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	// Look up the entry in the same registry the implementation reads.
	var statusDef commandDef
	for _, cmd := range commands {
		if cmd.Name == "status" {
			statusDef = cmd
			break
		}
	}
	if statusDef.Name == "" {
		t.Fatalf("registry missing status entry; cannot validate help output")
	}

	out := stderr.String()
	// The Long body is multi-paragraph; assert on its first sentence so the
	// test stays meaningful without re-encoding the whole prose.
	firstSentence := strings.SplitN(statusDef.Long, ".", 2)[0]
	if !strings.Contains(out, firstSentence) {
		t.Errorf("help status missing Long prefix %q\noutput:\n%s", firstSentence, out)
	}
	// Status accepts the standard common flags — assert each is listed.
	for _, flagName := range []string{"-config", "-db", "-json", "-plain", "-no-input"} {
		if !strings.Contains(out, flagName) {
			t.Errorf("help status missing flag %q\noutput:\n%s", flagName, out)
		}
	}
}

// TestHelpVerbWithUnknownSubcommand asserts `gohealthcli help bogus` exits 1
// with `unknown command: bogus` on stderr. The did-you-mean suggestion list
// is deferred to slice 3 of PRD #143 and explicitly NOT asserted here.
func TestHelpVerbWithUnknownSubcommand(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "help", "bogus")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown command: bogus") {
		t.Fatalf("stderr missing unknown-command message: %q", stderr.String())
	}
}

// TestHelpVerbWithHiddenSubcommand asserts that hidden registry entries (the
// `schema` build-time helper) are still surfaced when looked up explicitly:
// `gohealthcli help schema` prints its Long description and exits 0. Hidden
// only means "filtered from the top-level --help listing"; an explicit help
// lookup must still resolve it.
func TestHelpVerbWithHiddenSubcommand(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runCommand(t, "help", "schema")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	var schemaDef commandDef
	for _, cmd := range commands {
		if cmd.Name == "schema" {
			schemaDef = cmd
			break
		}
	}
	if schemaDef.Name == "" || !schemaDef.Hidden {
		t.Fatalf("registry missing hidden schema entry; cannot validate help output")
	}

	firstSentence := strings.SplitN(schemaDef.Long, ".", 2)[0]
	if !strings.Contains(stderr.String(), firstSentence) {
		t.Errorf("help schema missing Long prefix %q\noutput:\n%s", firstSentence, stderr.String())
	}
}

// TestHelpVerbRejectsExtraArguments asserts the `help` verb fails fast when
// given unexpected positional arguments, rather than silently dropping them.
// This mirrors how every other subcommand rejects unknown positionals and
// surfaces typos like `help status extra` instead of masking them.
func TestHelpVerbRejectsExtraArguments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"extras after known cmd", []string{"help", "status", "extra"}},
		{"extras after --help form", []string{"help", "--help", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCommand(t, tc.args...)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), "unexpected arguments") {
				t.Fatalf("stderr missing 'unexpected arguments' message: %q", stderr.String())
			}
		})
	}
}

func TestCountArchiveRowsRejectsUnknownTable(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open memory archive: %v", err)
	}
	defer db.Close()

	_, err = countArchiveRows(context.Background(), db, "data_points; DROP TABLE data_points")
	if err == nil {
		t.Fatal("countArchiveRows error = nil, want unsupported table")
	}
	if !strings.Contains(err.Error(), "unsupported Health Archive table") {
		t.Fatalf("countArchiveRows error = %v, want unsupported table", err)
	}
}

func TestParseStepsDailyRollupRequiresCivilEndTime(t *testing.T) {
	t.Parallel()
	_, err := parseGoogleHealthRollup(archivedConnection{
		providerName: "googlehealth",
		id:           "googlehealth:111111256096816351",
	}, "steps", "dailyRollUp", json.RawMessage(`{
		"steps": {"countSum": "1234"},
		"civilStartTime": {"date": {"year": 2026, "month": 1, "day": 1}}
	}`))
	if err == nil || !strings.Contains(err.Error(), "missing civilEndTime") {
		t.Fatalf("parse error = %v, want missing civilEndTime", err)
	}
}

func TestDailyNamedDataTypeListRequestIsNotRollup(t *testing.T) {
	t.Parallel()
	request, err := buildGoogleHealthDataTypeListRawRequest("daily-resting-heart-rate", "2026-01-01", "2026-01-02", 0, "")
	if err != nil {
		t.Fatalf("build daily-named list request: %v", err)
	}
	if request.endpointName != "dataTypes.daily-resting-heart-rate.list" {
		t.Fatalf("endpointName = %q, want daily Data Type list", request.endpointName)
	}
	if request.method != http.MethodGet {
		t.Fatalf("method = %q, want GET", request.method)
	}
	if len(request.body) != 0 {
		t.Fatalf("request body = %s, want empty list request body", string(request.body))
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/daily-resting-heart-rate/dataPoints" {
		t.Fatalf("path = %q, want Data Points list path", parsedURL.Path)
	}
	if strings.Contains(request.endpointName+parsedURL.Path, "RollUp") || strings.Contains(parsedURL.Path, "rollUp") {
		t.Fatalf("daily Data Type request used Rollup endpoint: %s %s", request.endpointName, parsedURL.Path)
	}
	wantFilter := `daily_resting_heart_rate.date >= "2026-01-01" AND daily_resting_heart_rate.date < "2026-01-02"`
	if got := parsedURL.Query().Get("filter"); got != wantFilter {
		t.Fatalf("filter = %q, want %q", got, wantFilter)
	}
}

func TestBuildGoogleHealthRawRequestUsesProviderNamingConventions(t *testing.T) {
	t.Parallel()
	request, err := buildGoogleHealthRawRequest([]string{"endpoint", "dataTypes.heart-rate.list"}, "2026-01-01", "", 0, "")
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	parsedURL, err := url.Parse(request.url)
	if err != nil {
		t.Fatalf("parse raw URL: %v", err)
	}
	if parsedURL.Path != "/v4/users/me/dataTypes/heart-rate/dataPoints" {
		t.Fatalf("path = %q, want kebab-case Data Type path", parsedURL.Path)
	}
	wantFilter := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00Z"`
	if parsedURL.Query().Get("filter") != wantFilter {
		t.Fatalf("filter = %q, want snake-case filter", parsedURL.Query().Get("filter"))
	}
}

func TestBuildGoogleHealthRawRequestRejectsNonListableDataTypes(t *testing.T) {
	t.Parallel()
	_, err := buildGoogleHealthRawRequest([]string{"data-type", "total-calories"}, "2026-01-01", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported Data Type")
	}
	if !strings.Contains(err.Error(), "not supported by dataPoints.list") {
		t.Fatalf("error = %v, want unsupported dataPoints.list", err)
	}
}

// TestBuildGoogleHealthRawRequestEndpointCatalog pins PRD #142 slice 7:
// every identity-style endpoint exposed by `raw endpoint <name>` must
// source its requiredScopes from googleHealthIdentityEndpointScopes so
// the scope contract for `raw` and the introspection commands (devices,
// settings, profile, irn-profile) can never drift apart. When slice 2
// of the PRD revises pairedDevices/getSettings scopes empirically, the
// catalog entry changes and this test follows automatically — no inline
// scope literals to update in main.go.
func TestBuildGoogleHealthRawRequestEndpointCatalog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		wantURL string
	}{
		{name: "getIdentity", wantURL: googleHealthIdentityURL},
		{name: "getProfile", wantURL: googleHealthProfileURL},
		{name: "getSettings", wantURL: googleHealthSettingsURL},
		{name: "pairedDevices", wantURL: googleHealthPairedDevicesURL},
		{name: "getIrnProfile", wantURL: googleHealthIRNProfileURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := buildGoogleHealthRawRequest([]string{"endpoint", tt.name}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", tt.name, err)
			}
			if request.endpointName != tt.name {
				t.Fatalf("endpointName = %q, want %q", request.endpointName, tt.name)
			}
			if request.url != tt.wantURL {
				t.Fatalf("url = %q, want %q", request.url, tt.wantURL)
			}
			wantScopes := googleHealthIdentityEndpointScopes[tt.name]
			if len(wantScopes) == 0 {
				t.Fatalf("catalog missing entry for %q — slice 1 contract violated", tt.name)
			}
			if len(request.requiredScopes) != len(wantScopes) {
				t.Fatalf("requiredScopes = %v, want %v (catalog entry)", request.requiredScopes, wantScopes)
			}
			for i, want := range wantScopes {
				if request.requiredScopes[i] != want {
					t.Fatalf("requiredScopes[%d] = %q, want %q (catalog entry)", i, request.requiredScopes[i], want)
				}
			}
		})
	}
}

// TestBuildGoogleHealthRawRequestUnknownEndpoint guards the
// not-found fall-through so a typo (or a renamed endpoint that
// outpaces a catalog update) still surfaces as a clear error rather
// than a nil request.
func TestBuildGoogleHealthRawRequestUnknownEndpoint(t *testing.T) {
	t.Parallel()
	_, err := buildGoogleHealthRawRequest([]string{"endpoint", "nonexistent"}, "", "", 0, "")
	if err == nil {
		t.Fatal("build raw request error = nil, want unsupported raw endpoint")
	}
	if !strings.Contains(err.Error(), `unsupported raw endpoint "nonexistent"`) {
		t.Fatalf("error = %v, want unsupported raw endpoint %q", err, "nonexistent")
	}
}

// TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog pins PRD #142
// slice 7 AC: no `[]string{googleHealthProfileReadonlyScope}` inline
// literal remains in buildGoogleHealthRawRequest. The only source of
// truth for endpoint scopes is the catalog. We verify behaviourally:
// a catalog mutation for the duration of the test flows through to the
// request's requiredScopes — proving the branch did a catalog lookup,
// not a hard-coded literal.
func TestBuildGoogleHealthRawRequestEndpointsReadFromCatalog(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"getIdentity", "getProfile", "getSettings", "pairedDevices", "getIrnProfile"} {
		t.Run(endpoint, func(t *testing.T) {
			original, ok := googleHealthIdentityEndpointScopes[endpoint]
			if !ok {
				t.Fatalf("catalog missing %q — slice 1 contract violated", endpoint)
			}
			sentinel := "https://example.invalid/scope/sentinel-" + endpoint
			googleHealthIdentityEndpointScopes[endpoint] = []string{sentinel}
			t.Cleanup(func() { googleHealthIdentityEndpointScopes[endpoint] = original })

			request, err := buildGoogleHealthRawRequest([]string{"endpoint", endpoint}, "", "", 0, "")
			if err != nil {
				t.Fatalf("build raw request for %q: %v", endpoint, err)
			}
			if len(request.requiredScopes) != 1 || request.requiredScopes[0] != sentinel {
				t.Fatalf("requiredScopes = %v, want catalog-driven %q — branch is using an inline scope literal", request.requiredScopes, sentinel)
			}
		})
	}
}

func TestGoogleHealthRawFilterFieldsCoverFirstReleaseDataTypes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		dataType string
		from     string
		want     string
	}{
		{
			dataType: "steps",
			from:     "2026-01-01",
			want:     `steps.interval.start_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "oxygen-saturation",
			from:     "2026-01-01",
			want:     `oxygen_saturation.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "heart-rate-variability",
			from:     "2026-01-01",
			want:     `heart_rate_variability.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "daily-resting-heart-rate",
			from:     "2026-01-01",
			want:     `daily_resting_heart_rate.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-heart-rate-variability",
			from:     "2026-01-01",
			want:     `daily_heart_rate_variability.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-oxygen-saturation",
			from:     "2026-01-01",
			want:     `daily_oxygen_saturation.date >= "2026-01-01"`,
		},
		{
			dataType: "daily-respiratory-rate",
			from:     "2026-01-01",
			want:     `daily_respiratory_rate.date >= "2026-01-01"`,
		},
		{
			dataType: "exercise",
			from:     "2026-01-01",
			want:     `exercise.interval.civil_start_time >= "2026-01-01"`,
		},
		{
			dataType: "sleep",
			from:     "2026-01-01",
			want:     `sleep.interval.civil_end_time >= "2026-01-01"`,
		},
		{
			dataType: "distance",
			from:     "2026-01-01",
			want:     `distance.interval.start_time >= "2026-01-01T00:00:00Z"`,
		},
		{
			dataType: "weight",
			from:     "2026-01-01",
			want:     `weight.sample_time.physical_time >= "2026-01-01T00:00:00Z"`,
		},
	} {
		t.Run(test.dataType, func(t *testing.T) {
			filter, err := googleHealthDataTypeListFilter(test.dataType, test.from, "")
			if err != nil {
				t.Fatalf("filter: %v", err)
			}
			if filter != test.want {
				t.Fatalf("filter = %q, want %q", filter, test.want)
			}
		})
	}
}

func TestGoogleHealthRawFilterPreservesFractionalRFC3339Bounds(t *testing.T) {
	t.Parallel()
	filter, err := googleHealthDataTypeListFilter("heart-rate", "2026-01-01T00:00:00.500Z", "2026-01-01T01:02:03.123456789+02:00")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	want := `heart_rate.sample_time.physical_time >= "2026-01-01T00:00:00.5Z" AND heart_rate.sample_time.physical_time < "2025-12-31T23:02:03.123456789Z"`
	if filter != want {
		t.Fatalf("filter = %q, want %q", filter, want)
	}
}

func TestParseOAuthClientConfigContentPinsHTTPSAndGoogleHosts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name:    "http auth_uri rejected",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"http://accounts.google.com/o/oauth2/v2/auth"}}`,
			wantErr: true,
		},
		{
			name:    "attacker-host token_uri rejected",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","token_uri":"https://attacker.example.com/token"}}`,
			wantErr: true,
		},
		{
			name:    "empty uris default to Google and accepted",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret"}}`,
			wantErr: false,
		},
		{
			name:    "valid Google https uris accepted",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"https://accounts.google.com/o/oauth2/v2/auth","token_uri":"https://oauth2.googleapis.com/token"}}`,
			wantErr: false,
		},
		{
			name:    "uppercase scheme and host accepted (case-insensitive)",
			content: `{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"HTTPS://Accounts.Google.Com/o/oauth2/v2/auth","token_uri":"HTTPS://OAuth2.GoogleAPIs.Com/token"}}`,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseOAuthClientConfigContent([]byte(tt.content))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseOAuthClientConfigContent error = nil, want https/Google host rejection")
				}
				if !strings.Contains(err.Error(), "https") || !strings.Contains(err.Error(), "Google OAuth host") {
					t.Fatalf("error = %q, want mention of https and Google OAuth host", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOAuthClientConfigContent error = %v, want accepted", err)
			}
		})
	}
}

func TestOAuthScopesUseRecognizedGoogleHealthScopes(t *testing.T) {
	t.Parallel()
	scopes := oauthScopesForDataTypes(defaultDataTypes)
	wantScopes := []string{
		googleHealthActivityReadonlyScope,
		googleHealthHealthMetricsReadonlyScope,
		googleHealthSleepReadonlyScope,
		googleHealthProfileReadonlyScope,
	}
	if !slices.Equal(scopes, wantScopes) {
		t.Fatalf("scopes = %v, want configured Google Health readonly scopes %v", scopes, wantScopes)
	}
	for _, scope := range scopes {
		for _, invalid := range []string{"settings.readonly"} {
			if strings.Contains(scope, invalid) {
				t.Fatalf("scopes include unrecognized Google Health scope %q: %v", invalid, scopes)
			}
		}
	}
}

func TestOAuthScopesForEmptyDataTypesRequestOnlyProfileScope(t *testing.T) {
	t.Parallel()
	for name, dataTypes := range map[string][]string{"nil": nil, "empty": {}} {
		if scopes := oauthScopesForDataTypes(dataTypes); !slices.Equal(scopes, []string{googleHealthProfileReadonlyScope}) {
			t.Fatalf("scopes for %s dataTypes = %v, want only the profile scope", name, scopes)
		}
	}
}

func TestListenForOAuthRedirectPreservesEmptyLoopbackPath(t *testing.T) {
	t.Parallel()
	listener, redirectURI, err := listenForOAuthRedirect([]string{"http://localhost"})
	if err != nil {
		t.Fatalf("listen for OAuth redirect: %v", err)
	}
	defer listener.Close()

	parsed, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect URI: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" {
		t.Fatalf("redirect URI = %s, want dynamic loopback with empty path", redirectURI)
	}
}

func TestParseOAuthTokenResponseRequiresRefreshToken(t *testing.T) {
	t.Parallel()
	_, err := parseOAuthTokenResponse([]byte(`{
		"access_token": "access-secret-value",
		"expires_in": 3600,
		"scope": "https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly",
		"token_type": "Bearer"
	}`), time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "refresh token") {
		t.Fatalf("parse token response error = %v, want missing refresh token", err)
	}
}

func TestFetchGoogleHealthRawUsesBearerAndHidesErrorBody(t *testing.T) {
	t.Parallel()
	doer := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != googleHealthIdentityURL {
			t.Fatalf("raw URL = %q, want identity URL", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer access-secret-value" {
			t.Fatalf("Authorization = %q, want bearer token", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"access-secret-value rejected"}`)),
		}, nil
	})}

	_, err := fetchGoogleHealthRaw(context.Background(), doer, rawProviderRequest{endpointName: "getIdentity", url: googleHealthIdentityURL}, "access-secret-value")
	if err == nil {
		t.Fatal("fetch raw error = nil, want HTTP failure")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("fetch raw error = %v, want status", err)
	}
	if strings.Contains(err.Error(), "access-secret-value") {
		t.Fatalf("fetch raw error leaked token/body: %v", err)
	}
}

func TestReadLimitedBodyReportsOversize(t *testing.T) {
	t.Parallel()
	body, tooLarge, err := readLimitedBody(strings.NewReader("abcdef"), 5)
	if err != nil {
		t.Fatalf("read limited body: %v", err)
	}
	if !tooLarge {
		t.Fatal("tooLarge = false, want true")
	}
	if body != nil {
		t.Fatalf("body = %q, want nil when oversized", string(body))
	}
}

func TestOSNativeCredentialStoreDoesNotSendTokenAsArgument(t *testing.T) {
	t.Parallel()
	testRuntime := productionRuntimeAdapters()
	testRuntime.currentOS = "darwin"

	var gotService string
	var gotKey string
	var gotContent []byte
	testRuntime.runSecurityAddGenericPassword = func(_ context.Context, service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStoreWithRuntime(credentialStoreConfig{kind: "os_native", service: "gohealthcli"}, testRuntime)
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("security command target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("security command content missing token material: %s", string(gotContent))
	}
}

func TestSecurityCredentialStoreFeedsPromptWithoutTokenArgument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake security executable uses POSIX shell")
	}

	tempDir := t.TempDir()
	argvPath := filepath.Join(tempDir, "argv.txt")
	stdinPath := filepath.Join(tempDir, "stdin.txt")
	securityPath := filepath.Join(tempDir, "security")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GOHEALTHCLI_TEST_SECURITY_ARGV\"\ncat > \"$GOHEALTHCLI_TEST_SECURITY_STDIN\"\n"
	if err := os.WriteFile(securityPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake security: %v", err)
	}
	t.Setenv("GOHEALTHCLI_TEST_SECURITY_ARGV", argvPath)
	t.Setenv("GOHEALTHCLI_TEST_SECURITY_STDIN", stdinPath)
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	content := []byte(`{"access_token":"access-secret-value"}`)
	if err := runSecurityAddGenericPasswordCommand(context.Background(), "gohealthcli", "googlehealth:111", content); err != nil {
		t.Fatalf("security command: %v", err)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if bytes.Contains(argv, []byte("access-secret-value")) {
		t.Fatalf("security argv contains token material: %s", string(argv))
	}
	if !bytes.Contains(argv, []byte("-w")) {
		t.Fatalf("security argv missing prompt flag: %s", string(argv))
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	wantStdin := string(content) + "\n" + string(content) + "\n"
	if string(stdin) != wantStdin {
		t.Fatalf("security stdin = %q, want password and confirmation", string(stdin))
	}
}

func TestLinuxOSNativeCredentialStoreUsesSecretToolContent(t *testing.T) {
	t.Parallel()
	testRuntime := productionRuntimeAdapters()
	testRuntime.currentOS = "linux"

	var gotService string
	var gotKey string
	var gotContent []byte
	testRuntime.runSecretToolStore = func(_ context.Context, service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStoreWithRuntime(credentialStoreConfig{kind: "os_native", service: "gohealthcli"}, testRuntime)
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("secret-tool target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("secret-tool content missing token material: %s", string(gotContent))
	}
}

func TestWindowsOSNativeCredentialStoreUsesCredentialManagerContent(t *testing.T) {
	t.Parallel()
	testRuntime := productionRuntimeAdapters()
	testRuntime.currentOS = "windows"

	var gotService string
	var gotKey string
	var gotContent []byte
	testRuntime.runWindowsCredentialWrite = func(_ context.Context, service, key string, content []byte) error {
		gotService = service
		gotKey = key
		gotContent = append([]byte(nil), content...)
		return nil
	}

	store, err := newCredentialStoreWithRuntime(credentialStoreConfig{kind: "os_native", service: "gohealthcli"}, testRuntime)
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	if err := store.Store("googlehealth:111", map[string]any{"access_token": "access-secret-value"}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	if gotService != "gohealthcli" || gotKey != "googlehealth:111" {
		t.Fatalf("Windows Credential Manager target = (%q, %q), want service/key", gotService, gotKey)
	}
	if !bytes.Contains(gotContent, []byte("access-secret-value")) {
		t.Fatalf("Windows Credential Manager content missing token material: %s", string(gotContent))
	}
}

func TestValidateConfigDoesNotCreateMissingParent(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	parentPath := filepath.Join(tempDir, "missing")

	err := validateConfig(filepath.Join(parentPath, "config.toml"), filepath.Join(tempDir, "archive.sqlite"))
	if err == nil {
		t.Fatal("validateConfig error = nil, want missing parent failure")
	}
	if _, statErr := os.Stat(parentPath); !os.IsNotExist(statErr) {
		t.Fatalf("parent stat err = %v, want not exist", statErr)
	}
}

func TestArchiveDSNUsesAbsoluteFileURI(t *testing.T) {
	t.Parallel()
	dsn, err := archiveDSN("relative.sqlite", false)
	if err != nil {
		t.Fatalf("archiveDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:///") {
		t.Fatalf("dsn = %q, want absolute file URI", dsn)
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys%3Don") {
		t.Fatalf("dsn = %q, want foreign key pragma", dsn)
	}
	readOnlyDSN, err := archiveDSN("relative.sqlite", true)
	if err != nil {
		t.Fatalf("archiveDSN readonly: %v", err)
	}
	if !strings.Contains(readOnlyDSN, "mode=ro") {
		t.Fatalf("dsn = %q, want readonly mode", readOnlyDSN)
	}
}

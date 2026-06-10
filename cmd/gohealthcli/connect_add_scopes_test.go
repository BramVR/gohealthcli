package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestExpandConnectAddScopesMapsKeywordsToScopeStrings is the slice A
// tracer for #99: the --add-scopes CLI shortcut accepts a CSV of
// keywords (irn, ecg) and the implementation maps each to its full
// Google Health API scope URL. Unknown keywords surface as an error
// so a typo doesn't silently shrink the OAuth scope set.
func TestExpandConnectAddScopesMapsKeywordsToScopeStrings(t *testing.T) {
	scopes, err := expandConnectAddScopes([]string{"irn"})
	if err != nil {
		t.Fatalf("expand irn: %v", err)
	}
	if len(scopes) != 1 || !strings.Contains(scopes[0], "irn.readonly") {
		t.Fatalf("scopes = %v, want one IRN scope URL", scopes)
	}

	scopes, err = expandConnectAddScopes([]string{"ecg", "irn"})
	if err != nil {
		t.Fatalf("expand ecg+irn: %v", err)
	}
	if len(scopes) != 2 {
		t.Fatalf("scopes len = %d, want 2", len(scopes))
	}

	scopes, err = expandConnectAddScopes([]string{"nutrition"})
	if err != nil {
		t.Fatalf("expand nutrition: %v", err)
	}
	if len(scopes) != 1 || !strings.Contains(scopes[0], "nutrition.readonly") {
		t.Fatalf("scopes = %v, want one nutrition.readonly scope URL", scopes)
	}

	// `tcx` unlocks the location.readonly scope that Google's
	// exportExerciseTcx endpoint requires on top of
	// activity_and_fitness.readonly (#140). User-facing keyword and the
	// Google bucket name diverge here because Google buckets the GPS
	// route bytes under "location", not "tcx" — the keyword reflects
	// the user's intent ("I want TCX exports").
	scopes, err = expandConnectAddScopes([]string{"tcx"})
	if err != nil {
		t.Fatalf("expand tcx: %v", err)
	}
	if len(scopes) != 1 || !strings.Contains(scopes[0], "location.readonly") {
		t.Fatalf("scopes = %v, want one location.readonly scope URL", scopes)
	}

	// `settings` (#176) unlocks googlehealth.settings.readonly, which
	// users.getSettings and users.pairedDevices.list require — Google's
	// own per-method docs confirm profile.readonly alone returns 403.
	scopes, err = expandConnectAddScopes([]string{"settings"})
	if err != nil {
		t.Fatalf("expand settings: %v", err)
	}
	if len(scopes) != 1 || !strings.Contains(scopes[0], "settings.readonly") {
		t.Fatalf("scopes = %v, want one settings.readonly scope URL", scopes)
	}

	if _, err := expandConnectAddScopes([]string{"typo"}); err == nil {
		t.Fatal("expand unknown keyword: err = nil, want unknown-keyword failure")
	} else if !strings.Contains(err.Error(), "typo") {
		t.Fatalf("err = %v, want mention of unknown keyword", err)
	}

	scopes, err = expandConnectAddScopes(nil)
	if err != nil {
		t.Fatalf("expand nil: %v", err)
	}
	if len(scopes) != 0 {
		t.Fatalf("scopes from nil keywords = %v, want empty", scopes)
	}
}

// TestConnectHelpAddScopesUsageListsEveryAcceptedKeyword is the #148
// drift guard for the `connect --help` flag block: the `--add-scopes`
// usage text must name every keyword `expandConnectAddScopes` accepts,
// rendered exactly as supportedAddScopeKeywords() renders them (the
// same rendering the unknown-keyword error uses). Deriving the
// expectation from connectAddScopeKeywords means adding a keyword to
// the map without surfacing it in --help fails here instead of
// shipping (the `nutrition` keyword shipped invisible to --help for
// two days because nothing pinned the two together).
func TestConnectHelpAddScopesUsageListsEveryAcceptedKeyword(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runConnect([]string{"--help"}, "config.json", "archive.db", false, outputMode{}, stdout, stderr)
	if code != 0 {
		t.Fatalf("connect --help exit = %d, want 0 (stderr=%s)", code, stderr.String())
	}

	help := stderr.String()
	want := connectAddScopesUsage()
	if !strings.Contains(help, want) {
		t.Fatalf("connect --help output missing canonical --add-scopes usage line\nwant substring: %q\ngot:\n%s", want, help)
	}
	for keyword := range connectAddScopeKeywords {
		if !strings.Contains(help, keyword) {
			t.Fatalf("connect --help output does not mention accepted --add-scopes keyword %q:\n%s", keyword, help)
		}
	}
}

// TestConnectSchemaAddScopesUsageMatchesAcceptedKeywords extends the
// #148 drift guard to the published schema: the `schema --json`
// contract (and the docs/commands/connect.md page generated from it)
// renders the connect command's add-scopes usage from the same
// commandDef entry, so that entry must carry the identical
// canonically-rendered keyword list the runtime flag registration
// uses. With both surfaces pinned to connectAddScopesUsage(), a new
// keyword in connectAddScopeKeywords propagates to --help, the schema,
// and the regenerated reference page without any hand-edited list.
func TestConnectSchemaAddScopesUsageMatchesAcceptedKeywords(t *testing.T) {
	for _, command := range commands {
		if command.Name != "connect" {
			continue
		}
		for _, spec := range command.Flags {
			if spec.Name != "add-scopes" {
				continue
			}
			if want := connectAddScopesUsage(); spec.Usage != want {
				t.Fatalf("connect schema add-scopes usage = %q, want %q (derived from connectAddScopeKeywords)", spec.Usage, want)
			}
			return
		}
		t.Fatal("connect commandDef has no add-scopes flagSpec")
	}
	t.Fatal("command registry has no connect commandDef")
}

// TestGoogleHealthIdentityEndpointScopesCatalog pins the AC for PRD
// #142 slice 1 plus the slice-2 revision (#176): the declarative
// catalog has entries for getProfile, getSettings, pairedDevices,
// getIrnProfile, getIdentity. Slice 2 flipped getSettings and
// pairedDevices to googlehealth.settings.readonly after empirical
// probing confirmed Google's per-method documentation
// (https://developers.google.com/health/api/reference/rest/v4/users/getSettings,
// https://developers.google.com/health/api/reference/rest/v4/users.pairedDevices/list);
// profile.readonly returns HTTP 403 for those two endpoints.
func TestGoogleHealthIdentityEndpointScopesCatalog(t *testing.T) {
	tests := []struct {
		endpoint   string
		wantScopes []string
	}{
		{endpoint: "getProfile", wantScopes: []string{googleHealthProfileReadonlyScope}},
		{endpoint: "getSettings", wantScopes: []string{googleHealthSettingsReadonlyScope}},
		{endpoint: "pairedDevices", wantScopes: []string{googleHealthSettingsReadonlyScope}},
		{endpoint: "getIrnProfile", wantScopes: []string{googleHealthIrnReadonlyScope}},
		{endpoint: "getIdentity", wantScopes: []string{googleHealthProfileReadonlyScope}},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			got, ok := googleHealthIdentityEndpointScopes[tt.endpoint]
			if !ok {
				t.Fatalf("googleHealthIdentityEndpointScopes missing entry for %q", tt.endpoint)
			}
			if len(got) != len(tt.wantScopes) {
				t.Fatalf("scopes for %q = %v, want %v", tt.endpoint, got, tt.wantScopes)
			}
			for i, want := range tt.wantScopes {
				if got[i] != want {
					t.Fatalf("scopes[%d] for %q = %q, want %q", i, tt.endpoint, got[i], want)
				}
			}
		})
	}
}

// TestConnectAddScopesIsCommunicatedToOAuthFlow is the slice A
// end-to-end behaviour: `connect --add-scopes irn` produces an OAuth
// authorisation URL whose scope parameter includes the IRN scope on
// top of the default scope set.
func TestConnectAddScopesIsCommunicatedToOAuthFlow(t *testing.T) {
	tempDir := t.TempDir()
	configPath, archivePath, _ := initializeFileCredentialSetup(t, tempDir)
	runtime := newConnectFakeRuntime(t, fakeConnectConfig{
		accessToken:        "connect-access-secret",
		refreshToken:       "connect-refresh-secret",
		healthUserID:       "111111256096816351",
		legacyFitbitUserID: "A1B2C3",
	})

	var capturedScopes []string
	runtime.runOAuthFlow = func(client oauthClientConfig, scopes []string, noInput bool) (oauthTokenResponse, error) {
		capturedScopes = append([]string(nil), scopes...)
		return oauthTokenResponse{
			accessToken: "connect-access-secret",
			scopes:      scopes,
			rawTokenMaterialObject: map[string]any{
				"access_token": "connect-access-secret",
				"scopes":       scopes,
			},
		}, nil
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	code := runConnectWithRuntime([]string{"--config", configPath, "--db", archivePath, "--add-scopes", "irn", "--json"}, configPath, archivePath, true, outputMode{json: true}, stdout, stderr, runtime)
	if code != 0 {
		t.Fatalf("connect exit = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}

	foundIRN := false
	for _, scope := range capturedScopes {
		if strings.Contains(scope, "irn.readonly") {
			foundIRN = true
			break
		}
	}
	if !foundIRN {
		t.Fatalf("captured OAuth scopes = %v, want IRN scope included", capturedScopes)
	}
}

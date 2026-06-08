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

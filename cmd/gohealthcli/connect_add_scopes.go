package main

import (
	"fmt"
	"sort"
	"strings"
)

// connectAddScopeKeywords maps the user-facing `--add-scopes` keyword
// to the actual Google Health API scope URL. PRD #93 §"Tier 2 Data
// Types" picks `ecg` (electrocardiogram) and `irn`
// (irregular-rhythm-notification) as the two opt-in expansions;
// `nutrition` covers hydration-log (#103) and any future
// nutrition.readonly Data Types.
var connectAddScopeKeywords = map[string]string{
	"irn":       "https://www.googleapis.com/auth/googlehealth.irn.readonly",
	"ecg":       "https://www.googleapis.com/auth/googlehealth.electrocardiogram.readonly",
	"nutrition": "https://www.googleapis.com/auth/googlehealth.nutrition.readonly",
}

// expandConnectAddScopes turns the CLI-side keyword list into the
// fully-qualified Google scope URLs that go into the OAuth flow.
// Unknown keywords surface as an error so a typo cannot silently
// shrink the requested scope set.
func expandConnectAddScopes(keywords []string) ([]string, error) {
	if len(keywords) == 0 {
		return nil, nil
	}
	scopes := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		scope, ok := connectAddScopeKeywords[keyword]
		if !ok {
			return nil, fmt.Errorf("unknown --add-scopes keyword %q (supported: %s)", keyword, supportedAddScopeKeywords())
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// unionScopes returns the union of two scope slices, preserving the
// order from `base` first and appending any new entries from `extra`.
// Duplicates within either slice are de-duplicated.
func unionScopes(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	result := make([]string, 0, len(base)+len(extra))
	for _, scope := range base {
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	for _, scope := range extra {
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	return result
}

func supportedAddScopeKeywords() string {
	keywords := make([]string, 0, len(connectAddScopeKeywords))
	for keyword := range connectAddScopeKeywords {
		keywords = append(keywords, keyword)
	}
	sort.Strings(keywords)
	return strings.Join(keywords, ", ")
}

// addScopeKeywordsForScopes reverses connectAddScopeKeywords for the
// missing-scope error path (#104): given a list of scope URLs,
// return the matching CLI keywords in deterministic alphabetical
// order so the user sees a stable `--add-scopes ecg,irn` hint
// regardless of slice order. Scopes that are not opt-in keyword
// scopes are dropped; callers compare `len(returned) == len(input)`
// to decide whether every missing scope is recoverable via
// `--add-scopes` (otherwise the hint must fall back to the generic
// "run `connect` again" message).
func addScopeKeywordsForScopes(scopes []string) []string {
	reverse := make(map[string]string, len(connectAddScopeKeywords))
	for keyword, scope := range connectAddScopeKeywords {
		reverse[scope] = keyword
	}
	keywords := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if keyword, ok := reverse[scope]; ok {
			keywords = append(keywords, keyword)
		}
	}
	sort.Strings(keywords)
	return keywords
}

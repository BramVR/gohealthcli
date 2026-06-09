package main

import (
	"strings"
	"testing"
)

// TestSettingsRejectsNoInputFlag pins issue #171: the dead --no-input
// flag is removed from settings' spec. The command never blocks on
// browser input, so accepting --no-input would imply a behaviour it
// does not have. Passing it now produces the Common Flag Set's
// targeted "--no-input is not supported by settings" rejection and
// exits non-zero.
func TestSettingsRejectsNoInputFlag(t *testing.T) {
	code, stdout, stderr := runCommand(t, "settings", "--no-input")
	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	const want = "--no-input is not supported by settings"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want substring %q", stderr.String(), want)
	}
}

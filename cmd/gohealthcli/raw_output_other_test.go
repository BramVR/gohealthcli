//go:build !darwin && !linux && !windows

package main

import (
	"strings"
	"testing"
)

func TestRawOutputReportsUnsupportedPlatform(t *testing.T) {
	err := rawOutputPlatformSupported()
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("raw output platform error = %v, want unsupported-platform refusal", err)
	}
}

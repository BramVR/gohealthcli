package diagnostics_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BramVR/gohealthcli/internal/config"
	"github.com/BramVR/gohealthcli/internal/diagnostics"
)

func TestRunReportsSetupMissing(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{
		Config:  filepath.Join(dir, "config.toml"),
		Archive: filepath.Join(dir, "archive.sqlite"),
	}

	got := diagnostics.Run(paths)

	if got.Report.Status != diagnostics.ReportSetupMissing {
		t.Errorf("status = %q, want %q", got.Report.Status, diagnostics.ReportSetupMissing)
	}
	if got.Report.OK() {
		t.Error("OK() = true, want false for missing setup")
	}
	if len(got.Hints) == 0 {
		t.Error("expected a human hint for missing setup")
	}
	for _, c := range got.Report.Checks {
		if c.Status != diagnostics.StatusMissing {
			t.Errorf("check %q status = %q, want missing", c.Name, c.Status)
		}
	}
}

func TestRunReportsOKWhenPresent(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	db := filepath.Join(dir, "archive.sqlite")
	for _, p := range []string{cfg, db} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("seeding %s: %v", p, err)
		}
	}

	got := diagnostics.Run(config.Paths{Config: cfg, Archive: db})

	if !got.Report.OK() {
		t.Errorf("status = %q, want ok", got.Report.Status)
	}
	if len(got.Hints) != 0 {
		t.Errorf("expected no hints for healthy setup, got %v", got.Hints)
	}
}

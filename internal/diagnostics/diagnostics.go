// Package diagnostics implements the local checks behind the doctor command.
//
// Default diagnostics stay local: they never touch the network, refresh
// tokens, or call a Provider. They also never emit token or client secret
// values; only presence and shape are reported.
package diagnostics

import (
	"fmt"
	"os"

	"github.com/BramVR/gohealthcli/internal/config"
)

// CheckStatus is the outcome of a single local diagnostic check.
type CheckStatus string

const (
	// StatusOK means the checked resource is present and readable.
	StatusOK CheckStatus = "ok"
	// StatusMissing means the checked resource does not exist yet.
	StatusMissing CheckStatus = "missing"
)

// Overall report statuses.
const (
	// ReportOK means a usable local setup was found.
	ReportOK = "ok"
	// ReportSetupMissing means no local gohealthcli setup exists yet.
	ReportSetupMissing = "setup_missing"
)

// Check is one local diagnostic result. It never carries secret values.
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
}

// Report is the machine-readable result of a doctor run. It is the stable
// stdout payload for --json and --plain modes.
type Report struct {
	Status string  `json:"status"`
	Checks []Check `json:"checks"`
}

// OK reports whether the diagnostic found a usable local setup.
func (r Report) OK() bool { return r.Status == ReportOK }

// Result bundles the machine-readable report with the human hints that belong
// on stderr.
type Result struct {
	Report Report
	Hints  []string
}

// Run performs the default local-only doctor checks for the given paths. It is
// safe to call before init: missing config and archive produce a setup-missing
// report rather than an error.
func Run(paths config.Paths) Result {
	checks := []Check{
		fileCheck("config", paths.Config),
		fileCheck("archive", paths.Archive),
	}

	report := Report{Status: ReportOK, Checks: checks}
	for _, c := range checks {
		if c.Status != StatusOK {
			report.Status = ReportSetupMissing
			break
		}
	}

	var hints []string
	if !report.OK() {
		hints = append(hints,
			"No local gohealthcli setup found. Run 'gohealthcli init' to create the config and archive.")
	}

	return Result{Report: report, Hints: hints}
}

// fileCheck reports presence of a file path. The path itself is not secret and
// is included as a detail to help the user locate the missing resource.
func fileCheck(name, path string) Check {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Check{Name: name, Status: StatusMissing, Detail: fmt.Sprintf("not found at %s", path)}
		}
		return Check{Name: name, Status: StatusMissing, Detail: fmt.Sprintf("unreadable at %s: %v", path, err)}
	}
	return Check{Name: name, Status: StatusOK, Detail: path}
}

package cli

import (
	"fmt"
	"io"

	"github.com/BramVR/gohealthcli/internal/config"
	"github.com/BramVR/gohealthcli/internal/diagnostics"
	"github.com/BramVR/gohealthcli/internal/output"
)

// runDoctor runs the default local-only diagnostic and renders it in the chosen
// mode. Machine-readable output goes to stdout; the human hint goes to stderr
// regardless of mode. A setup-missing result is a diagnostic failure and exits
// non-zero so that scripts can gate on a healthy setup.
func runDoctor(gf globalFlags, mode output.Mode, stdout, stderr io.Writer) int {
	paths, err := config.Resolve(gf.configPath, gf.dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: resolving paths: %v\n", err)
		return exitUsage
	}

	result := diagnostics.Run(paths)

	switch mode {
	case output.JSON:
		if err := output.WriteJSON(stdout, result.Report); err != nil {
			fmt.Fprintf(stderr, "error: writing json: %v\n", err)
			return exitUsage
		}
	case output.Plain:
		if err := output.WritePlain(stdout, doctorPairs(result.Report)); err != nil {
			fmt.Fprintf(stderr, "error: writing output: %v\n", err)
			return exitUsage
		}
	default:
		writeDoctorHuman(stdout, result.Report)
	}

	// Human hints always belong on stderr, never on stdout.
	output.WriteHints(stderr, result.Hints)

	if !result.Report.OK() {
		return exitDiagnostic
	}
	return exitOK
}

// doctorPairs flattens a report into stable key/value lines for plain mode.
func doctorPairs(r diagnostics.Report) []output.Pair {
	pairs := []output.Pair{{Key: "status", Value: r.Status}}
	for _, c := range r.Checks {
		pairs = append(pairs, output.Pair{Key: c.Name, Value: string(c.Status)})
	}
	return pairs
}

// writeDoctorHuman renders readable status lines for interactive use.
func writeDoctorHuman(w io.Writer, r diagnostics.Report) {
	if r.OK() {
		fmt.Fprintln(w, "gohealthcli setup looks healthy.")
	} else {
		fmt.Fprintln(w, "gohealthcli setup is incomplete.")
	}
	for _, c := range r.Checks {
		if c.Detail != "" {
			fmt.Fprintf(w, "  %-8s %-8s %s\n", c.Name, c.Status, c.Detail)
			continue
		}
		fmt.Fprintf(w, "  %-8s %s\n", c.Name, c.Status)
	}
}

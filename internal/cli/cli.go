// Package cli is the thin command layer for gohealthcli.
//
// It parses global flags, selects an output mode, dispatches to a command, and
// routes machine-readable data to stdout and human hints to stderr. Business
// logic lives in the deeper packages it calls.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/BramVR/gohealthcli/internal/output"
	"github.com/BramVR/gohealthcli/internal/version"
)

// Process exit codes.
const (
	exitOK         = 0
	exitDiagnostic = 1
	exitUsage      = 2
)

const usage = `usage: gohealthcli [global flags] <command>

commands:
  doctor    check local setup (config and archive) without network access

global flags:
  --config PATH   config file path
  --db PATH       SQLite archive path
  --json          write stable JSON to stdout
  --plain         write simple key/value output to stdout
  --no-input      never prompt or wait for input
  --version       print version and exit`

// globalFlags holds the parsed global flag values shared by all commands.
type globalFlags struct {
	configPath  string
	dbPath      string
	json        bool
	plain       bool
	noInput     bool
	showVersion bool
}

// Run parses args and dispatches a command, writing machine-readable output to
// stdout and human hints/warnings to stderr. It returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gohealthcli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }

	var gf globalFlags
	fs.StringVar(&gf.configPath, "config", "", "config file path")
	fs.StringVar(&gf.dbPath, "db", "", "SQLite archive path")
	fs.BoolVar(&gf.json, "json", false, "write stable JSON to stdout")
	fs.BoolVar(&gf.plain, "plain", false, "write simple key/value output to stdout")
	fs.BoolVar(&gf.noInput, "no-input", false, "never prompt or wait for input")
	fs.BoolVar(&gf.showVersion, "version", false, "print version and exit")

	// Parse flags that appear before the command name.
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}

	// The command is the first non-flag token; re-parse to also accept flags
	// written after it (e.g. "doctor --json").
	var command string
	rest := fs.Args()
	if len(rest) > 0 {
		command = rest[0]
		if err := fs.Parse(rest[1:]); err != nil {
			return parseExit(err)
		}
		// No command in this slice accepts positional arguments; reject any
		// leftover tokens rather than silently ignoring them.
		if extra := fs.Args(); len(extra) > 0 {
			fmt.Fprintf(stderr, "error: unexpected arguments: %v\n", extra)
			fs.Usage()
			return exitUsage
		}
	}

	// --version short-circuits before any local setup is inspected.
	if gf.showVersion {
		fmt.Fprintln(stdout, version.String())
		return exitOK
	}

	if gf.json && gf.plain {
		fmt.Fprintln(stderr, "error: --json and --plain are mutually exclusive")
		return exitUsage
	}

	mode := output.Human
	switch {
	case gf.json:
		mode = output.JSON
	case gf.plain:
		mode = output.Plain
	}

	switch command {
	case "":
		fmt.Fprintln(stderr, "error: no command given")
		fs.Usage()
		return exitUsage
	case "doctor":
		return runDoctor(gf, mode, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", command)
		fs.Usage()
		return exitUsage
	}
}

// parseExit maps a flag parse error to an exit code. An explicit help request
// (-h/--help) is success; anything else is a usage error.
func parseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}

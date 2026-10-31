package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

// schemaDocument is the wire shape of `gohealthcli schema --json`. Field
// names and structure are part of the contract the Project Site's command
// reference generator depends on — bump commandSchemaVersion in commands.go
// when changing them.
type schemaDocument struct {
	Version  int          `json:"version"`
	Binary   string       `json:"binary"`
	Commands []commandDef `json:"commands"`
}

// runSchemaWithRegistry is the runner behind the hidden `schema`
// subcommand; the registry entry's Run adapter (wired in commands.go's
// init()) binds it to the package-level `commands` slice. Taking the
// registry as a parameter avoids the var-init cycle that a direct
// reference to `commands` would create — Go's var-init checker sees
// the cycle even though closures defer the actual call — and keeps the
// data and the adapter in lockstep.
func runSchemaWithRegistry(args []string, registry []commandDef, stdout, stderr io.Writer, observe flagSetObserver) int {
	flags := flag.NewFlagSet("schema", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", true, "emit the registry as JSON (default and currently the only output mode)")

	notifySubcommandFlagSetObserver(observe, flags)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		return ReportFailure(FailureReport{
			Command: "schema",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected schema argument: %s", flags.Arg(0)),
		}, stdout, stderr)
	}
	if !*jsonOutput {
		return ReportFailure(FailureReport{
			Command: "schema",
			Status:  StatusFlagInvalid,
			Message: "schema currently supports --json output only",
		}, stdout, stderr)
	}

	doc := schemaDocument{
		Version:  commandSchemaVersion,
		Binary:   "gohealthcli",
		Commands: registry,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return ReportFailure(FailureReport{
			Command: "schema",
			Status:  StatusArchiveUnwritable,
			Message: err.Error(),
		}, stdout, stderr)
	}
	return 0
}

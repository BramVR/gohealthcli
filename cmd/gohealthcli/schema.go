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

func runSchema(args []string, stdout, stderr io.Writer) int {
	return runSchemaWithRegistry(args, commands, stdout, stderr)
}

// runSchemaWithRegistry is the seam runSchema's adapter uses. Splitting
// the package-level `commands` lookup out of runSchema removes the
// initialisation cycle that arose when slice 6 added a `commands` entry
// whose Run adapter referenced runSchema — Go's var-init checker sees
// the cycle even though closures defer the actual call. Taking the
// registry as a parameter keeps the data and the adapter in lockstep
// without the cycle.
func runSchemaWithRegistry(args []string, registry []commandDef, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("schema", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", true, "emit the registry as JSON (default and currently the only output mode)")

	notifySubcommandFlagSetObserver(flags)
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

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
	flags := flag.NewFlagSet("schema", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", true, "emit the registry as JSON (default and currently the only output mode)")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected schema argument: %s\n", flags.Arg(0))
		return 1
	}
	if !*jsonOutput {
		fmt.Fprintln(stderr, "schema currently supports --json output only")
		return 1
	}

	doc := schemaDocument{
		Version:  commandSchemaVersion,
		Binary:   "gohealthcli",
		Commands: commands,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		fmt.Fprintf(stderr, "schema: %v\n", err)
		return 1
	}
	return 0
}

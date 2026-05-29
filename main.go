// Command gohealthcli is a local-first, read-only CLI for archiving personal
// health and fitness data available through the Google Health API.
package main

import (
	"os"

	"github.com/BramVR/gohealthcli/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}

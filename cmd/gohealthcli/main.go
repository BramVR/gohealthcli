package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

const setupMissingExitCode = 2
const version = "dev"

type doctorResult struct {
	Status      string `json:"status"`
	ConfigPath  string `json:"config_path"`
	ArchivePath string `json:"archive_path"`
	Message     string `json:"message"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gohealthcli", flag.ContinueOnError)
	flags.SetOutput(stderr)

	configPath := flags.String("config", defaultConfigPath(), "config file path")
	archivePath := flags.String("db", defaultArchivePath(), "SQLite Health Archive path")
	jsonOutput := flags.Bool("json", false, "write stable JSON to stdout")
	plainOutput := flags.Bool("plain", false, "write plain key/value output to stdout")
	flags.Bool("no-input", false, "never prompt, never wait for browser input")
	versionOutput := flags.Bool("version", false, "print version and exit")

	if err := flags.Parse(args); err != nil {
		return 1
	}

	if *versionOutput {
		fmt.Fprintf(stdout, "gohealthcli %s\n", version)
		return 0
	}

	if flags.NArg() == 0 {
		fmt.Fprintln(stderr, "missing command")
		return 1
	}

	switch flags.Arg(0) {
	case "doctor":
		return runDoctor(flags.Args()[1:], *configPath, *archivePath, outputMode{json: *jsonOutput, plain: *plainOutput}, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", flags.Arg(0))
		return 1
	}
}

type outputMode struct {
	json  bool
	plain bool
}

func runDoctor(args []string, configPath, archivePath string, mode outputMode, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)

	doctorConfigPath := flags.String("config", configPath, "config file path")
	doctorArchivePath := flags.String("db", archivePath, "SQLite Health Archive path")
	doctorJSONOutput := flags.Bool("json", mode.json, "write stable JSON to stdout")
	doctorPlainOutput := flags.Bool("plain", mode.plain, "write plain key/value output to stdout")
	flags.Bool("no-input", false, "never prompt, never wait for browser input")

	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected doctor argument: %s\n", flags.Arg(0))
		return 1
	}

	mode = outputMode{json: *doctorJSONOutput, plain: *doctorPlainOutput}
	result := doctorResult{
		Status:      "setup_missing",
		ConfigPath:  *doctorConfigPath,
		ArchivePath: *doctorArchivePath,
		Message:     "local gohealthcli setup not found",
	}

	if mode.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(stderr, "write JSON output: %v\n", err)
			return 1
		}
	} else if mode.plain {
		fmt.Fprintf(stdout, "status: %s\n", result.Status)
		fmt.Fprintf(stdout, "config_path: %s\n", result.ConfigPath)
		fmt.Fprintf(stdout, "archive_path: %s\n", result.ArchivePath)
		fmt.Fprintf(stdout, "message: %s\n", result.Message)
	} else {
		fmt.Fprintln(stdout, "Setup missing")
		fmt.Fprintf(stdout, "Config: %s\n", result.ConfigPath)
		fmt.Fprintf(stdout, "Health Archive: %s\n", result.ArchivePath)
	}

	fmt.Fprintln(stderr, "run `gohealthcli init` to create local config and Health Archive")
	return setupMissingExitCode
}

func defaultConfigPath() string {
	return "~/.config/gohealthcli/config.toml"
}

func defaultArchivePath() string {
	return "~/.local/share/gohealthcli/gohealthcli.sqlite"
}

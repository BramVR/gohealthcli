package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var completionShells = []string{"bash", "zsh", "fish", "powershell"}

func runCompletionWithRegistry(
	args []string,
	registry []commandDef,
	mode outputMode,
	stdout, stderr io.Writer,
	observe flagSetObserver,
) int {
	flags := flag.NewFlagSet("completion", flag.ContinueOnError)
	parseOutput := new(strings.Builder)
	flags.SetOutput(parseOutput)
	noDescriptions := flags.Bool("no-descriptions", false, "disable completion descriptions")

	notifySubcommandFlagSetObserver(observe, flags)
	if err := flags.Parse(completionFlagArgsFirst(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stderr, parseOutput.String())
			return 0
		}
		return ReportFailure(FailureReport{
			Command: "completion",
			Status:  StatusFlagInvalid,
			Message: err.Error(),
			Mode:    mode,
		}, stdout, stderr)
	}
	if flags.NArg() == 0 {
		return ReportFailure(FailureReport{
			Command: "completion",
			Status:  StatusFlagInvalid,
			Message: "shell is required; choose bash, zsh, fish, or powershell",
			Mode:    mode,
		}, stdout, stderr)
	}
	if flags.NArg() > 1 {
		return ReportFailure(FailureReport{
			Command: "completion",
			Status:  StatusUnexpectedArgument,
			Message: fmt.Sprintf("unexpected arguments after %s: %s", flags.Arg(0), strings.Join(flags.Args()[1:], " ")),
			Mode:    mode,
		}, stdout, stderr)
	}

	root, err := completionCommandTree(registry)
	if err != nil {
		return reportCompletionGenerationFailure(err, mode, stdout, stderr)
	}
	switch flags.Arg(0) {
	case "bash":
		err = root.GenBashCompletionV2(stdout, !*noDescriptions)
	case "zsh":
		if *noDescriptions {
			err = root.GenZshCompletionNoDesc(stdout)
		} else {
			err = root.GenZshCompletion(stdout)
		}
	case "fish":
		err = root.GenFishCompletion(stdout, !*noDescriptions)
	case "powershell":
		if *noDescriptions {
			err = root.GenPowerShellCompletion(stdout)
		} else {
			err = root.GenPowerShellCompletionWithDesc(stdout)
		}
	default:
		return ReportFailure(FailureReport{
			Command: "completion",
			Status:  StatusFlagInvalid,
			Message: fmt.Sprintf("unsupported shell %q; choose bash, zsh, fish, or powershell", flags.Arg(0)),
			Mode:    mode,
		}, stdout, stderr)
	}
	if err != nil {
		return reportCompletionGenerationFailure(err, mode, stdout, stderr)
	}
	return 0
}

// The standard flag package stops parsing at the first positional argument.
// Completion's documented shape permits --no-descriptions after the shell, so
// move flags ahead of positionals before handing the same tokens to FlagSet.
func completionFlagArgsFirst(args []string) []string {
	flagArgs := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	flagsEnded := false
	for _, arg := range args {
		if arg == "--" && !flagsEnded {
			flagsEnded = true
			continue
		}
		if !flagsEnded && strings.HasPrefix(arg, "-") && arg != "-" {
			flagArgs = append(flagArgs, arg)
			continue
		}
		positionals = append(positionals, arg)
	}
	if flagsEnded {
		flagArgs = append(flagArgs, "--")
	}
	return append(flagArgs, positionals...)
}

func reportCompletionGenerationFailure(err error, mode outputMode, stdout, stderr io.Writer) int {
	return ReportFailure(FailureReport{
		Command: "completion",
		Status:  StatusArchiveUnwritable,
		Message: fmt.Sprintf("write completion script: %v", err),
		Mode:    mode,
	}, stdout, stderr)
}

func completionCommandTree(registry []commandDef) (*cobra.Command, error) {
	root := &cobra.Command{
		Use:              "gohealthcli",
		Short:            "Local-first, read-only Google Health archive CLI.",
		SilenceErrors:    true,
		SilenceUsage:     true,
		TraverseChildren: true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
	}
	for _, spec := range commonFlagsSpec {
		if err := addCompletionFlag(root.Flags(), spec); err != nil {
			return nil, fmt.Errorf("project root flag --%s: %w", spec.Name, err)
		}
	}
	root.Flags().Bool("version", false, "print version and exit")

	for _, def := range registry {
		if def.Hidden {
			continue
		}
		use := def.Name
		if def.PositionalArgs != "" {
			use += " " + def.PositionalArgs
		}
		cmd := &cobra.Command{
			Use:   use,
			Short: def.Short,
			Long:  def.Long,
			Run:   func(*cobra.Command, []string) {},
		}
		for _, spec := range def.Flags {
			if err := addCompletionFlag(cmd.Flags(), spec); err != nil {
				return nil, fmt.Errorf("%s flag --%s: %w", def.Name, spec.Name, err)
			}
		}
		if def.Name == "completion" {
			cmd.ValidArgs = append([]string(nil), completionShells...)
		}
		root.AddCommand(cmd)
	}
	root.InitDefaultHelpCmd()
	return root, nil
}

type completionFlagSet interface {
	Bool(name string, value bool, usage string) *bool
	Int(name string, value int, usage string) *int
	String(name, value, usage string) *string
}

func addCompletionFlag(flags completionFlagSet, spec flagSpec) error {
	switch spec.Type {
	case "bool":
		value, err := strconv.ParseBool(spec.Default)
		if err != nil {
			return fmt.Errorf("invalid bool default %q", spec.Default)
		}
		flags.Bool(spec.Name, value, spec.Usage)
	case "int":
		value := 0
		if spec.Default != "" {
			var err error
			value, err = strconv.Atoi(spec.Default)
			if err != nil {
				return fmt.Errorf("invalid int default %q", spec.Default)
			}
		}
		flags.Int(spec.Name, value, spec.Usage)
	case "string":
		flags.String(spec.Name, spec.Default, spec.Usage)
	default:
		return fmt.Errorf("unsupported type %q", spec.Type)
	}
	return nil
}

func runCompletionProtocol(args []string, registry []commandDef, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || (args[0] != cobra.ShellCompRequestCmd && args[0] != cobra.ShellCompNoDescRequestCmd) {
		return false, 0
	}
	root, err := completionCommandTree(registry)
	if err != nil {
		fmt.Fprintf(stderr, "completion protocol: %v\n", err)
		return true, 1
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(stderr, "completion protocol: %v\n", err)
		return true, 1
	}
	return true, 0
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/versioninfo"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// errNoCommand reports that agentswarm ran without a command.
var errNoCommand = errors.New("a command is required")

// failure marks an error that happened while a command ran. Any other error
// comes from cobra before the command runs, such as an unknown command or a
// bad flag, and is a usage error.
type failure struct{ err error }

func (f failure) Error() string { return f.err.Error() }

func (f failure) Unwrap() error { return f.err }

// run executes one command and returns the process exit code. An interrupt
// cancels the command's context.
func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	root := newRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	var f failure
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &f):
		_, _ = fmt.Fprintf(stderr, "agentswarm: %v\n", err)
		return exitError
	default:
		_, _ = fmt.Fprintf(stderr, "agentswarm: %v\n\n%s", err, cmd.UsageString())
		return exitUsage
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "agentswarm",
		Short:         "Agent Swarm, a governance platform for AI agents",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(*cobra.Command, []string) error {
			return errNoCommand
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newVersionCommand(), newConcludeCommand(), newCheckCommand(), newRegistryCommand())
	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the build version as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeJSON(cmd.OutOrStdout(), versioninfo.Get())
		},
	}
}

// writeJSON writes v as indented JSON. A failed write is a failure of the command.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return failure{fmt.Errorf("write output: %w", err)}
	}
	return nil
}

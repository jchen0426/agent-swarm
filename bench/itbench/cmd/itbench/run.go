package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/bench/itbench/score"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

var errNoCommand = errors.New("a command is required")

// failure marks an error that happened while a command ran; any other error
// is a usage error from cobra.
type failure struct{ err error }

func (f failure) Error() string { return f.err.Error() }

func (f failure) Unwrap() error { return f.err }

// run executes one command and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	root := &cobra.Command{
		Use:           "itbench",
		Short:         "Prepare, serve and score ITBench-Lite scenarios for the mechanism check",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          func(*cobra.Command, []string) error { return errNoCommand },
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newFetchCommand(), newPrepareCommand(), newServeCommand(), newBaselineCommand(), newScoreCommand())
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(context.Background())
	var f failure
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &f):
		_, _ = fmt.Fprintf(stderr, "itbench: %v\n", err)
		return exitError
	default:
		_, _ = fmt.Fprintf(stderr, "itbench: %v\n\n%s", err, cmd.UsageString())
		return exitUsage
	}
}

// selected returns the set's scenarios, or only those named.
func selected(set *score.Set, only []string) ([]score.Scenario, error) {
	if len(only) == 0 {
		return set.Scenarios, nil
	}
	var out []score.Scenario
	for _, id := range only {
		i := slices.IndexFunc(set.Scenarios, func(s score.Scenario) bool { return s.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("scenario %s is not in the set", id)
		}
		out = append(out, set.Scenarios[i])
	}
	return out, nil
}

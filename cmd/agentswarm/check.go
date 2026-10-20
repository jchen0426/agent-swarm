package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/measurement/check"
	"github.com/jchen0426/agent-swarm/internal/runner"
	"github.com/jchen0426/agent-swarm/internal/versioninfo"
)

// checkFlags are the check command's flags.
type checkFlags struct {
	set, agents, out string
	only             []string
	opts             check.Options
	retries          int
}

func newCheckCommand() *cobra.Command {
	f := checkFlags{opts: check.Options{Turn: runner.Config{}}}
	cmd := &cobra.Command{
		Use:   "check --set FILE --agents FILE --out DIR",
		Short: "Run the retrospective check: the agents on each case of a set, aggregated by key and scored",
		Long: `Run the retrospective check (spec Sections 14.1 and 14.4). The agents work each case
of the set on their own, in one round, through the case's read-only MCP servers.
Their hypotheses are aggregated by key as in the independent run, and the
conclusion is scored against the case's labels and its baseline.

The model client reads ANTHROPIC_API_KEY. The records and report.json go to
--out. Run the same command again to retry the runs that ended in an error;
the finished runs are kept.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			report, err := runCheck(c.Context(), &f)
			if err != nil {
				return failure{err}
			}
			summary := struct {
				Model      string               `json:"model"`
				Trials     []check.TrialSummary `json:"trials"`
				Errors     int                  `json:"errors"`
				Violations int                  `json:"violations"`
				Report     string               `json:"report"`
			}{report.Model, report.Trials, report.Errors, report.Violations, filepath.Join(f.out, "report.json")}
			if err := writeJSON(c.OutOrStdout(), summary); err != nil {
				return err
			}
			switch {
			case report.Errors > 0:
				return failure{fmt.Errorf("%d runs ended in an infrastructure error; run the same command again to retry them", report.Errors)}
			case report.Violations > 0:
				return failure{fmt.Errorf("%d violations of the board's mechanics; see %s", report.Violations, summary.Report)}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.set, "set", "", "retrospective set (JSON)")
	fl.StringVar(&f.agents, "agents", "", "agents file (JSON): names and skill files")
	fl.StringVar(&f.out, "out", "", "directory for the records and the report")
	fl.StringSliceVar(&f.only, "only", nil, "run only these cases (comma-separated ids)")
	fl.IntVar(&f.opts.Trials, "trials", 3, "times each case is run")
	fl.StringVar(&f.opts.Turn.Model, "model", "claude-haiku-5-5", "model of every agent")
	fl.Int64Var(&f.opts.Turn.MaxTokens, "max-tokens", 4096, "output tokens per model response")
	fl.IntVar(&f.opts.Turn.MaxSteps, "max-steps", 50, "model calls per turn")
	fl.Int64Var(&f.opts.Turn.PromptLimit, "prompt-limit", 90000, "input tokens per model request, at most 100000")
	fl.Int64Var(&f.opts.RunBudget, "run-budget", 2000000, "cost-weighted tokens per run, split evenly among the agents; 0 for none")
	fl.DurationVar(&f.opts.RoundLimit, "round-limit", 15*time.Minute, "time limit of the run's one round")
	fl.DurationVar(&f.opts.ReadTimeout, "read-timeout", time.Minute, "time limit of one read")
	fl.IntVar(&f.opts.EntryLimit, "entry-limit", 5, "board entries per agent in the round")
	fl.IntVar(&f.opts.MaxOutput, "max-output", 40000, "bytes of one read's output that an agent sees; 0 for no cap")
	fl.IntVar(&f.opts.Concurrency, "concurrency", 1, "runs at once")
	fl.IntVar(&f.retries, "retries", 6, "retries of a failed model request")
	fl.BoolVar(&f.opts.Turn.Nudge, "nudge", false, "nudge a model that answers without calling a tool, once a turn, as agentswarm turn does; off, as Plan 3's check ran")
	for _, name := range []string{"set", "agents", "out"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// runCheck loads the inputs and runs the check with a model client.
func runCheck(ctx context.Context, f *checkFlags) (*check.Report, error) {
	set, err := check.LoadSet(f.set)
	if err != nil {
		return nil, err
	}
	if len(f.only) > 0 {
		if set.Cases, err = onlyCases(set.Cases, f.only); err != nil {
			return nil, err
		}
	}
	agents, err := check.LoadAgents(f.agents)
	if err != nil {
		return nil, err
	}
	client := anthropic.NewClient(option.WithMaxRetries(f.retries), option.WithRequestTimeout(3*time.Minute))
	turn := func(ctx context.Context, tools runner.Toolbox, cfg *runner.Config, prompt string) (runner.Result, error) {
		return runner.Run(ctx, &client, tools, cfg, prompt)
	}
	opts := f.opts
	opts.Out = f.out
	opts.Build = versioninfo.Get().Build()
	return check.Run(ctx, set, agents, turn, &opts)
}

// onlyCases keeps the named cases, in the set's order.
func onlyCases(cases []check.Case, ids []string) ([]check.Case, error) {
	for _, id := range ids {
		if !slices.ContainsFunc(cases, func(c check.Case) bool { return c.ID == id }) {
			return nil, fmt.Errorf("the set has no case %s", id)
		}
	}
	return slices.DeleteFunc(cases, func(c check.Case) bool { return !slices.Contains(ids, c.ID) }), nil
}

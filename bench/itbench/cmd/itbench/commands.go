package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/bench/itbench/baseline"
	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/fetch"
	"github.com/jchen0426/agent-swarm/bench/itbench/prepare"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
	"github.com/jchen0426/agent-swarm/bench/itbench/server"
)

func newFetchCommand() *cobra.Command {
	var setPath, dest string
	var only []string
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Download the set's scenarios at the pinned commit, checking every file's hash",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, scenarios, err := loadSelected(setPath, only)
			if err != nil {
				return failure{err}
			}
			src, err := fetch.ParseSource(set.Source, set.Revision)
			if err != nil {
				return failure{err}
			}
			for _, sc := range scenarios {
				n, err := src.Scenario(cmd.Context(), set.Prefix, sc.Dir, dest)
				if err != nil {
					return failure{fmt.Errorf("%s: %w", sc.ID, err)}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %d files\n", sc.ID, n)
			}
			return nil
		},
	}
	setFlag(cmd, &setPath, &only)
	cmd.Flags().StringVar(&dest, "dest", "", "directory to download into")
	_ = cmd.MarkFlagRequired("dest")
	return cmd
}

func newPrepareCommand() *cobra.Command {
	var setPath, rawDir, out string
	var only []string
	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Turn downloaded scenarios into datasets cut at the alert, without the benchmark's traces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, scenarios, err := loadSelected(setPath, only)
			if err != nil {
				return failure{err}
			}
			for _, sc := range scenarios {
				d, err := prepare.Run(filepath.Join(rawDir, sc.Dir), filepath.Join(out, sc.ID), set.Namespace)
				if err != nil {
					return failure{fmt.Errorf("%s: %w", sc.ID, err)}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: alert %s; %d changes, %d objects, %d events, %d logs, %d error spans (traces end %s), %d service and %d pod metric points\n",
					sc.ID, d.Meta.AlertTime.Format(time.RFC3339), len(d.Changes), len(d.Objects), len(d.Events), len(d.Logs), len(d.Spans),
					d.Meta.TracesUntil.Format(time.RFC3339), len(d.Services), len(d.Pods))
			}
			return nil
		},
	}
	setFlag(cmd, &setPath, &only)
	cmd.Flags().StringVar(&rawDir, "raw", "", "directory that fetch downloaded into")
	cmd.Flags().StringVar(&out, "out", "", "directory to write the datasets into, one per scenario id")
	_ = cmd.MarkFlagRequired("raw")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func newServeCommand() *cobra.Command {
	var dir, addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve one dataset through read-only MCP tools at /mcp",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dataset.Load(dir)
			if err != nil {
				return failure{err}
			}
			srv := &http.Server{Addr: addr, Handler: server.Handler(d), ReadHeaderTimeout: 10 * time.Second}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errc := make(chan error, 1)
			go func() { errc <- srv.ListenAndServe() }()
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "serving %s at http://%s/mcp\n", dir, addr)
			select {
			case err := <-errc:
				return failure{err}
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return srv.Shutdown(shutdown) //nolint:contextcheck // the command's context is already done.
			}
		},
	}
	cmd.Flags().StringVar(&dir, "dataset", "", "a dataset that prepare wrote")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "address to listen on")
	_ = cmd.MarkFlagRequired("dataset")
	return cmd
}

func newBaselineCommand() *cobra.Command {
	var setPath, datasets, scope string
	var only []string
	cmd := &cobra.Command{
		Use:   "baseline",
		Short: "Write the most-recent-change baseline's answers as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if scope != "namespace" && scope != "service" {
				return fmt.Errorf("--scope must be namespace or service, not %q", scope)
			}
			set, scenarios, err := loadSelected(setPath, only)
			if err != nil {
				return failure{err}
			}
			answers := score.Answers{}
			for _, sc := range scenarios {
				d, err := dataset.Load(filepath.Join(datasets, sc.ID))
				if err != nil {
					return failure{fmt.Errorf("%s: %w", sc.ID, err)}
				}
				service, namespace := baseline.Evaluate(d, set.FlagStore)
				if k := map[string]*score.Key{"service": service, "namespace": namespace}[scope]; k != nil {
					answers[sc.ID] = *k
				}
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(answers); err != nil {
				return failure{err}
			}
			return nil
		},
	}
	setFlag(cmd, &setPath, &only)
	cmd.Flags().StringVar(&datasets, "datasets", "", "directory that prepare wrote into")
	cmd.Flags().StringVar(&scope, "scope", "namespace", "namespace: the latest change anywhere; service: the latest change to an alerting service")
	_ = cmd.MarkFlagRequired("datasets")
	return cmd
}

func newScoreCommand() *cobra.Command {
	var setPath, answersPath, baselinePath string
	cmd := &cobra.Command{
		Use:   "score",
		Short: "Score answers against the set's keys, next to the baseline's",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, err := score.Load(setPath)
			if err != nil {
				return failure{err}
			}
			answers, err := readAnswers(set, answersPath)
			if err != nil {
				return failure{err}
			}
			base := score.Answers{}
			if baselinePath != "" {
				if base, err = readAnswers(set, baselinePath); err != nil {
					return failure{err}
				}
			}
			r := score.Compare(set, answers, base)
			return printReport(cmd, &r)
		},
	}
	cmd.Flags().StringVar(&setPath, "set", "scenarios.yaml", "the scenario set")
	cmd.Flags().StringVar(&answersPath, "answers", "", "JSON object mapping scenario ids to keys")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "the baseline's answers, in the same form")
	_ = cmd.MarkFlagRequired("answers")
	return cmd
}

func setFlag(cmd *cobra.Command, setPath *string, only *[]string) {
	cmd.Flags().StringVar(setPath, "set", "scenarios.yaml", "the scenario set")
	cmd.Flags().StringSliceVar(only, "only", nil, "only these scenario ids")
}

func loadSelected(path string, only []string) (*score.Set, []score.Scenario, error) {
	set, err := score.Load(path)
	if err != nil {
		return nil, nil, err
	}
	scenarios, err := selected(set, only)
	return set, scenarios, err
}

// readAnswers reads a JSON object of scenario ids to keys, and refuses ids
// that are not in the set.
func readAnswers(set *score.Set, path string) (score.Answers, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var answers score.Answers
	if err := json.Unmarshal(data, &answers); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for id := range answers {
		if _, err := selected(set, []string{id}); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return answers, nil
}

func printReport(cmd *cobra.Command, r *score.Report) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	yes := map[bool]string{true: "yes", false: "no"}
	_, _ = fmt.Fprintln(w, "scenario\tchange\tanswer\thit\tbaseline\thit")
	for _, row := range r.Rows {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", row.ID, yes[row.Change], row.Answer, yes[row.Hit], row.Baseline, yes[row.BaselineHit])
	}
	_, _ = fmt.Fprintf(w, "\nhits %d/%d (change scenarios %d/%d); baseline %d/%d (change scenarios %d/%d); only the answers right: %d; only the baseline right: %d\n",
		r.Hits, r.Scenarios, r.ChangeHits, r.ChangeScenarios, r.BaselineHits, r.Scenarios, r.ChangeBaselineHits, r.ChangeScenarios, r.OnlyAnswers, r.OnlyBaseline)
	if err := w.Flush(); err != nil {
		return failure{err}
	}
	return nil
}

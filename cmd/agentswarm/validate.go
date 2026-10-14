package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/registry/app"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/dirsource"
	"github.com/jchen0426/agent-swarm/internal/registry/gitsource"
)

// validation is the output of registry validate for a revision that keeps
// every rule.
type validation struct {
	Commit  string          `json:"commit,omitempty"`
	Digest  string          `json:"digest"`
	Objects []config.Object `json:"objects"`
}

func newValidateCommand() *cobra.Command {
	var dir, repo, ref string
	cmd := &cobra.Command{
		Use:   "validate (--dir DIR | --repo DIR [--ref REF])",
		Short: "Check a configuration repository against every loader rule and print its objects as JSON",
		Long: `Check a configuration repository against every loader rule of spec Section 3,
as the platform does before it loads a revision. With --dir, read a directory,
such as a checkout in a pull request check. With --repo, read the files of one
commit of a Git repository, never its working tree.

A revision that keeps every rule prints its commit, digest and objects with
their content hashes. A refused revision prints every problem found, and the
command exits with 1.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			v, err := validate(c.Context(), dir, repo, ref)
			var ps config.Problems
			switch {
			case errors.As(err, &ps):
				if err := writeJSON(c.OutOrStdout(), struct {
					Problems config.Problems `json:"problems"`
				}{ps}); err != nil {
					return err
				}
				return failure{fmt.Errorf("the configuration is refused: %d problems", len(ps))}
			case err != nil:
				return failure{err}
			}
			return writeJSON(c.OutOrStdout(), v)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&dir, "dir", "", "configuration directory")
	fl.StringVar(&repo, "repo", "", "Git repository of the configuration")
	fl.StringVar(&ref, "ref", "", "branch, tag or commit of --repo (default: HEAD)")
	cmd.MarkFlagsOneRequired("dir", "repo")
	cmd.MarkFlagsMutuallyExclusive("dir", "repo")
	cmd.MarkFlagsMutuallyExclusive("dir", "ref")
	return cmd
}

// validate reads the configuration and builds its revision.
func validate(ctx context.Context, dir, repo, ref string) (*validation, error) {
	var src app.Source = dirsource.New(dir)
	if repo != "" {
		s, err := gitsource.Open(repo)
		if err != nil {
			return nil, err
		}
		src = s
	}
	snap, err := src.Read(ctx, ref)
	if err != nil {
		return nil, err
	}
	conditions, err := celeval.New()
	if err != nil {
		return nil, err
	}
	rev, err := config.Build(snap.Files, snap.Commit, conditions)
	if err != nil {
		return nil, err
	}
	return &validation{Commit: rev.Commit(), Digest: rev.Digest(), Objects: rev.Objects()}, nil
}

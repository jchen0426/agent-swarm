package main

import (
	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/registry/mcptools"
)

func newRegistryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Check a configuration repository, and hash the tools a registration pins",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errNoCommand
		},
	}
	cmd.AddCommand(newHashToolsCommand())
	return cmd
}

func newHashToolsCommand() *cobra.Command {
	var endpoint string
	cmd := &cobra.Command{
		Use:   "hash-tools --endpoint URL",
		Short: "List an MCP server's tools with the schema hash a registration pins for each",
		Long: `Connect to an MCP server over streamable HTTP, list its tools, and print for
each its whole definition and the schemaHash that a registration records once a
reviewer has read it (spec Section 3.3). The hash covers exactly the printed
definition, so a reviewer pins only what they have read. The annotations' hints,
such as read-only, are shown, not trusted: a registration sets each tool's
effect itself.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			tools, err := mcptools.List(c.Context(), endpoint)
			if err != nil {
				return failure{err}
			}
			return writeJSON(c.OutOrStdout(), tools)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "the server's MCP endpoint URL")
	_ = cmd.MarkFlagRequired("endpoint")
	return cmd
}

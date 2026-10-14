package main

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"

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
	cmd.AddCommand(newValidateCommand(), newHashToolsCommand())
	return cmd
}

func newHashToolsCommand() *cobra.Command {
	var endpoint string
	cmd := &cobra.Command{
		Use:   "hash-tools --endpoint URL",
		Short: "List an MCP server's tools with the schema hash a registration pins for each",
		Long: `Connect to an MCP server over streamable HTTP, as the MCP gateway does, list
its tools, and print for each its whole definition and the schemaHash that a
registration records once a reviewer has read it (spec Section 3.3). The hash
covers exactly the printed definition, so a reviewer pins only what they have
read. Characters a reader could miss, such as zero-width, tag, bidi or variation
selector characters, are printed as \u escapes, and each tool lists the fields
that hold them, also inside its schemas, under "invisible"; the gateway never
lists such a tool to agents. A tool the gateway could not hash shows an "error"
instead of a hash, and cannot be pinned. The annotations' hints, such as
read-only, are shown, not trusted: a registration sets each tool's effect
itself.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			tools, err := mcptools.List(c.Context(), endpoint)
			if err != nil {
				// The error holds the server's own text, which reaches a
				// terminal. Quoted, no character in it can move the cursor,
				// erase a line or reorder text, so it cannot draw a listing.
				return failure{fmt.Errorf("the server's tools could not be listed: %s", strconv.QuoteToGraphic(err.Error()))}
			}
			return writeVisibleJSON(c.OutOrStdout(), tools)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "the server's MCP endpoint URL")
	_ = cmd.MarkFlagRequired("endpoint")
	return cmd
}

// writeVisibleJSON writes v as indented JSON in which every character that
// mcptools.Hidden reports is escaped as \uXXXX. Such characters stand only
// inside strings, so the output is the same JSON: its escapes decode to the
// text the schema hash covers, but nothing in it is invisible.
func writeVisibleJSON(w io.Writer, v any) error {
	var raw bytes.Buffer
	if err := writeJSON(&raw, v); err != nil {
		return err
	}
	var out strings.Builder
	for _, r := range raw.String() {
		switch {
		case !mcptools.Hidden(r):
			out.WriteRune(r)
		case r > 0xFFFF:
			hi, lo := utf16.EncodeRune(r)
			_, _ = fmt.Fprintf(&out, "\\u%04x\\u%04x", hi, lo)
		default:
			_, _ = fmt.Fprintf(&out, "\\u%04x", r)
		}
	}
	if _, err := io.WriteString(w, out.String()); err != nil {
		return failure{fmt.Errorf("write output: %w", err)}
	}
	return nil
}

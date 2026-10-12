package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jchen0426/agent-swarm/internal/versioninfo"
)

const usage = `Usage: agentswarm <command> [flags]

Commands:
  version    Print the build version as JSON.
`

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// run executes one command and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version":
		return report(stdout, stderr, versioninfo.Get(), nil)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return exitOK
	default:
		//nolint:gosec // G705 targets HTML responses; this is a terminal, and %q escapes control characters.
		_, _ = fmt.Fprintf(stderr, "agentswarm: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// report writes v as indented JSON, or err to stderr.
func report(stdout, stderr io.Writer, v any, err error) int {
	if err == nil {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(v)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agentswarm: %v\n", err)
		return exitError
	}
	return exitOK
}

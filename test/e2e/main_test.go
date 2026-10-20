//go:build e2e

// Package e2e drives the built agentswarm binary from outside, as a user does.
// It imports no platform package; a depguard rule enforces that.
package e2e

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// binary is the agentswarm binary under test: AGENTSWARM_BIN when set (the
// Makefile sets it to the artifact it built), otherwise a fresh build, which
// is then named in AGENTSWARM_BIN, where the harness looks for it.
var binary string

func TestMain(m *testing.M) {
	// The harness runs a process it starts under a copy of this binary, its
	// parent (R20), which does nothing else.
	if os.Getenv(harness.ParentEnv) != "" {
		harness.ParentMain()
	}
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	if b := os.Getenv(harness.BinaryEnv); b != "" {
		binary = b
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "agentswarm-e2e-")
	if err != nil {
		slog.Error("create a build directory", "error", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binary = filepath.Join(dir, "agentswarm")
	build := exec.Command("go", "build", "-o", binary, "github.com/jchen0426/agent-swarm/cmd/agentswarm")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		slog.Error("build agentswarm", "error", err)
		return 1
	}
	if err := os.Setenv(harness.BinaryEnv, binary); err != nil {
		slog.Error("name the binary to the harness", "error", err)
		return 1
	}
	return m.Run()
}

// agentswarm runs the binary and returns its output and exit code.
func agentswarm(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), errOut.String(), exit.ExitCode()
	}
	require.NoError(t, err)
	return out.String(), errOut.String(), 0
}

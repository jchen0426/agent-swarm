package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

func runCommand(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestVersion(t *testing.T) {
	out, _, code := runCommand("version")

	require.Equal(t, exitOK, code)
	var info map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	assert.Equal(t, "dev", info["version"])
}

func TestUsageErrors(t *testing.T) {
	_, errOut, code := runCommand()
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "a command is required")
	assert.Contains(t, errOut, "Usage:")

	_, errOut, code = runCommand("frobnicate")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, `unknown command "frobnicate"`)

	_, errOut, code = runCommand("version", "--bogus")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "unknown flag: --bogus")
}

func TestHelp(t *testing.T) {
	out, _, code := runCommand("--help")
	assert.Equal(t, exitOK, code)
	assert.Contains(t, out, "Available Commands:")
	assert.Contains(t, out, "version")

	out, _, code = runCommand("version", "--help")
	assert.Equal(t, exitOK, code)
	assert.Contains(t, out, "agentswarm version")
}

func TestConcludeNeedsABoard(t *testing.T) {
	_, errOut, code := runCommand("conclude")

	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, `required flag(s) "board" not set`)
}

func TestConclude(t *testing.T) {
	out, errOut, code := runCommand("conclude", "--board", "testdata/board.json")

	require.Equal(t, exitOK, code, errOut)
	var a board.Assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	assert.Equal(t, board.EntryID("h1"), a.Conclusion)
	require.Len(t, a.Hypotheses, 2)
	assert.True(t, a.Hypotheses[0].Converged)
	assert.Equal(t, []board.Source{"cd/get_history", "logs/search"}, a.Hypotheses[0].Sources)
}

func TestConcludeAsOfAMoment(t *testing.T) {
	out, errOut, code := runCommand("conclude", "--board", "testdata/board.json", "--at", "2026-10-12T10:06:00Z")

	require.Equal(t, exitOK, code, errOut)
	var a board.Assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	require.Len(t, a.Hypotheses, 1, "h2 came later")
	assert.False(t, a.Hypotheses[0].Converged)
	assert.Equal(t, board.EntryID("h1"), a.Conclusion, "the leading hypothesis")
}

func TestConcludeAcceptsACutoffWithAnOffset(t *testing.T) {
	// 12:06+02:00 is 10:06 UTC, the cutoff of TestConcludeAsOfAMoment.
	out, errOut, code := runCommand("conclude", "--board", "testdata/board.json", "--at", "2026-10-12T12:06:00+02:00")

	require.Equal(t, exitOK, code, errOut)
	var a board.Assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	assert.Len(t, a.Hypotheses, 1)
}

func TestConcludeReportsBadInput(t *testing.T) {
	_, errOut, code := runCommand("conclude", "--board", "testdata/missing.json")
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "read board file")

	_, errOut, code = runCommand("conclude", "--board", "testdata/board.json", "--at", "yesterday")
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "parse --at")
}

func TestConcludeRefusesABoardWithoutConvergenceSettings(t *testing.T) {
	_, errOut, code := runCommand("conclude", "--board", "testdata/noconv.json")

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "convergence settings")
}

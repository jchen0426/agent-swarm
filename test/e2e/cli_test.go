//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionPrintsBuildInfo(t *testing.T) {
	out, errOut, code := agentswarm(t, "version")

	require.Equal(t, 0, code, errOut)
	var info struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	assert.NotEmpty(t, info.Version)
}

func TestUnknownCommandExitsWithUsage(t *testing.T) {
	_, errOut, code := agentswarm(t, "frobnicate")

	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, `unknown command "frobnicate"`)
}

// assessment is the part of the conclude output these tests read.
type assessment struct {
	Conclusion string `json:"conclusion"`
	Hypotheses []struct {
		ID        string   `json:"id"`
		Converged bool     `json:"converged"`
		Sources   []string `json:"sources"`
	} `json:"hypotheses"`
}

func TestConcludeAssessesARecordedBoard(t *testing.T) {
	out, errOut, code := agentswarm(t, "conclude", "--board", "testdata/board.json")

	require.Equal(t, 0, code, errOut)
	var a assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	assert.Equal(t, "h1", a.Conclusion)
	require.Len(t, a.Hypotheses, 2)
	assert.True(t, a.Hypotheses[0].Converged)
	assert.Equal(t, []string{"cd/get_history", "logs/search"}, a.Hypotheses[0].Sources)
}

func TestConcludeAsOfACutoff(t *testing.T) {
	out, errOut, code := agentswarm(t, "conclude", "--board", "testdata/board.json", "--at", "2026-10-12T10:06:00Z")

	require.Equal(t, 0, code, errOut)
	var a assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	require.Len(t, a.Hypotheses, 1)
	assert.False(t, a.Hypotheses[0].Converged)
	assert.Equal(t, "h1", a.Conclusion)
}

func TestConcludeRejectsAnInconsistentBoard(t *testing.T) {
	_, errOut, code := agentswarm(t, "conclude", "--board", "testdata/inconsistent.json")

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "no such entry")
}

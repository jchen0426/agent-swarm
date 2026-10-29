package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// runCommand runs one command with nothing on its standard input.
func runCommand(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(""), &out, &errOut)
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

func TestConcludeChecksTheWholeBoardBeforeACutoff(t *testing.T) {
	// h2 is the last entry committed but is dated 10:03, before e1 (10:05).
	// Cutting at 10:04 first would drop h2 without noticing the bad order.
	_, errOut, code := runCommand("conclude", "--board", "testdata/skew.json", "--at", "2026-10-12T10:04:00Z")

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "out of commit order")
}

func TestConcludeRefusesTrailingData(t *testing.T) {
	_, errOut, code := runCommand("conclude", "--board", "testdata/trailing.json")

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "parse board file")
}

// boardWith writes testdata/board.json, with change applied to the body of
// its event at index i, to a file of its own, and returns the file's path.
func boardWith(t *testing.T, i int, change func(body map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/board.json")
	require.NoError(t, err)
	var f struct {
		Settings json.RawMessage  `json:"settings"`
		Events   []map[string]any `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	body, ok := f.Events[i]["body"].(map[string]any)
	require.True(t, ok)
	change(body)
	return writeBoard(t, f)
}

// boardWithEvent writes testdata/board.json, with event appended to its
// events, to a file of its own, and returns the file's path.
func boardWithEvent(t *testing.T, event string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/board.json")
	require.NoError(t, err)
	var f struct {
		Settings json.RawMessage   `json:"settings"`
		Events   []json.RawMessage `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	f.Events = append(f.Events, json.RawMessage(event))
	return writeBoard(t, f)
}

// writeBoard writes a board file, f as JSON, to a file of its own, and
// returns the file's path.
func writeBoard(t *testing.T, f any) string {
	t.Helper()
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

// conclude reads a board file strictly, its entries' bodies included: a field
// it does not know, as one misspelled, is refused, where dropping it without a
// word would change the assessment it prints.
func TestConcludeRefusesAnUnknownFieldInAnEntrysBody(t *testing.T) {
	for name, change := range map[string]func(body map[string]any){
		"in a body": func(body map[string]any) { body["verdict"] = "the release" },
		"in a body's evidence": func(body map[string]any) {
			evidence, ok := body["evidence"].(map[string]any)
			require.True(t, ok)
			evidence["stence"] = "refutes"
		},
	} {
		_, errOut, code := runCommand("conclude", "--board", boardWith(t, 0, change))

		assert.Equal(t, exitError, code, name)
		assert.Contains(t, errOut, "parse board file", name)
		assert.Contains(t, errOut, "unknown field", name)
	}
}

// conclude reads a board that holds a turn_unstarted notice with its reason
// (Decision 7), and prints the assessment it prints without the notice.
func TestConcludeReadsATurnUnstartedNotice(t *testing.T) {
	path := boardWithEvent(t, `{"seq": 6, "id": "n1", "run": "run-1", "round": 2,
		"author": {"kind": "platform", "name": "coordinator"}, "at": "2026-10-12T10:08:00Z",
		"body": {"notice": {"code": "turn_unstarted", "agent": "release-investigator", "reason": "engine"}}}`)
	want, errOut, code := runCommand("conclude", "--board", "testdata/board.json")
	require.Equal(t, exitOK, code, errOut)

	out, errOut, code := runCommand("conclude", "--board", path)

	require.Equal(t, exitOK, code, errOut)
	var a board.Assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	assert.Equal(t, board.EntryID("h1"), a.Conclusion)
	assert.JSONEq(t, want, out, "the notice changes nothing of the assessment")
}

// conclude refuses an entry of a later version as such, as a store does
// (Decision 10): a later build's entry is not this build's to assess.
func TestConcludeRefusesAnEntryOfALaterVersion(t *testing.T) {
	path := boardWith(t, 0, func(body map[string]any) { body["v"] = board.Version + 1 })

	_, errOut, code := runCommand("conclude", "--board", path)

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, board.ErrVersion.Error())
}

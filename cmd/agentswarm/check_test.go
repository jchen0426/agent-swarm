package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/measurement/check"
	"github.com/jchen0426/agent-swarm/internal/runner"
)

// messagesAPI answers every request with text and no tool call, so that
// every turn ends at once, and records the models asked for and the
// requests' bodies.
type messagesAPI struct {
	mu     sync.Mutex
	models []string
	bodies [][]byte
	output int // output tokens each reply reports
}

func (a *messagesAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)
	a.mu.Lock()
	a.models = append(a.models, req.Model)
	a.bodies = append(a.bodies, body)
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	output := a.output
	if output == 0 {
		output = 10
	}
	_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"Nothing found."}],`+
		`"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":%d}}`, req.Model, output)
}

func (a *messagesAPI) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.models...)
}

// lastTexts returns the text of each request's last message, in order.
func (a *messagesAPI) lastTexts(t *testing.T) []string {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	texts := make([]string, 0, len(a.bodies))
	for _, body := range a.bodies {
		var req struct {
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		last := req.Messages[len(req.Messages)-1].Content
		texts = append(texts, last[len(last)-1].Text)
	}
	return texts
}

// useMessagesAPI points the model client at a fake Messages API.
func useMessagesAPI(t *testing.T) *messagesAPI {
	t.Helper()
	api := &messagesAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	return api
}

// readServer is an MCP server with one read-only tool.
func readServer(t *testing.T) string {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "observability", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "get_changes", InputSchema: json.RawMessage(`{"type":"object"}`), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "no changes"}}}, nil
		})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	t.Cleanup(srv.Close)
	return srv.URL
}

// checkFiles writes a set of two cases on the server and an agents file, and
// returns their paths and an output directory.
func checkFiles(t *testing.T, server string) (set, agents, out string) {
	t.Helper()
	dir := t.TempDir()
	cases := ""
	for i, id := range []string{"S1", "S2"} {
		if i > 0 {
			cases += ","
		}
		cases += fmt.Sprintf(`{"id": %q, "task": {"servers": [%q], "alert": "Errors on cart", "services": ["cart"]},
			"change": true, "labels": [{"service": "cart", "failureMode": "config-change"}]}`, id, server)
	}
	set = filepath.Join(dir, "set.json")
	require.NoError(t, os.WriteFile(set, []byte(`{"failureModes": [{"name": "config-change", "meaning": "A configuration change broke it."}], "cases": [`+cases+`]}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("Look at the changes."), 0o600))
	agents = filepath.Join(dir, "agents.json")
	require.NoError(t, os.WriteFile(agents, []byte(`[{"name": "changes", "skill": "a.md"}]`), 0o600))
	return set, agents, filepath.Join(dir, "out")
}

type checkSummary struct {
	Model      string               `json:"model"`
	Trials     []check.TrialSummary `json:"trials"`
	Errors     int                  `json:"errors"`
	Violations int                  `json:"violations"`
	Report     string               `json:"report"`
}

func TestCheckRunsTheAgentsOnEveryCase(t *testing.T) {
	api := useMessagesAPI(t)
	set, agents, out := checkFiles(t, readServer(t))

	stdout, errOut, code := runCommand("check", "--set", set, "--agents", agents, "--out", out, "--trials", "1")

	require.Equal(t, exitOK, code, errOut)
	var s checkSummary
	require.NoError(t, json.Unmarshal([]byte(stdout), &s))
	assert.Equal(t, "claude-haiku-5-5", s.Model)
	require.Len(t, s.Trials, 1)
	assert.Equal(t, 2, s.Trials[0].Cases)
	assert.Equal(t, 2, s.Trials[0].NoConclusion)
	assert.Equal(t, filepath.Join(out, "report.json"), s.Report)
	assert.FileExists(t, s.Report)
	assert.Equal(t, []string{"claude-haiku-5-5", "claude-haiku-5-5"}, api.asked())
}

func TestCheckRunsOnlyTheNamedCases(t *testing.T) {
	useMessagesAPI(t)
	set, agents, out := checkFiles(t, readServer(t))

	stdout, errOut, code := runCommand("check", "--set", set, "--agents", agents, "--out", out, "--trials", "1", "--only", "S2")

	require.Equal(t, exitOK, code, errOut)
	var s checkSummary
	require.NoError(t, json.Unmarshal([]byte(stdout), &s))
	assert.Equal(t, 1, s.Trials[0].Cases)

	_, errOut, code = runCommand("check", "--set", set, "--agents", agents, "--out", t.TempDir(), "--only", "S9")
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "no case S9")
}

func TestCheckFailsWhenARunCouldNotFinish(t *testing.T) {
	useMessagesAPI(t)
	set, agents, out := checkFiles(t, "http://127.0.0.1:1/mcp")

	stdout, errOut, code := runCommand("check", "--set", set, "--agents", agents, "--out", out, "--trials", "1")

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "2 runs ended in an infrastructure error")
	assert.Contains(t, stdout, `"errors": 2`, "the summary is still written")
}

func TestCheckFailsWhenTheBoardsMechanicsBroke(t *testing.T) {
	api := useMessagesAPI(t)
	api.output = 1000 // a reply far longer than --max-tokens allows: 5,000 cost-weighted tokens
	set, agents, out := checkFiles(t, readServer(t))

	stdout, errOut, code := runCommand("check", "--set", set, "--agents", agents, "--out", out, "--trials", "1",
		"--max-tokens", "10", "--run-budget", "4000", "--only", "S1")

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "1 violations of the board's mechanics")
	assert.Contains(t, stdout, `"violations": 1`)
}

// The check runs its turns without the nudge, as Plan 3's ran, unless it is
// given --nudge (R21): the gate after Task 12 runs it so. Each turn's record
// tells a turn the nudge did not end apart from one it did not reach.
func TestCheckTakesTheNudgeOnRequest(t *testing.T) {
	for _, nudge := range []bool{false, true} {
		t.Run(fmt.Sprintf("nudge %t", nudge), func(t *testing.T) {
			api := useMessagesAPI(t)
			set, agents, out := checkFiles(t, readServer(t))
			args := []string{"check", "--set", set, "--agents", agents, "--out", out, "--trials", "1", "--only", "S1"}
			if nudge {
				args = append(args, "--nudge")
			}

			_, errOut, code := runCommand(args...)

			require.Equal(t, exitOK, code, errOut)
			raw, err := os.ReadFile(filepath.Join(out, "runs", "S1", "t1", "run.json"))
			require.NoError(t, err)
			var run struct {
				Turns []map[string]any `json:"turns"`
			}
			require.NoError(t, json.Unmarshal(raw, &run))
			require.Len(t, run.Turns, 1)
			turn := run.Turns[0]
			assert.Equal(t, string(runner.EndedByModel), turn["outcome"])
			inputs, err := os.ReadFile(filepath.Join(out, "inputs.json"))
			require.NoError(t, err)
			texts := api.lastTexts(t)
			if !nudge {
				assert.Len(t, texts, 1, "the first answer without a tool ends the turn")
				assert.NotContains(t, turn, "nudged", "a turn's record reads as Plan 3's did")
				assert.NotContains(t, string(inputs), "nudge", "the check's inputs read as Plan 3's did")
				return
			}
			require.Len(t, texts, 2, "the answer without a tool, then the one after the nudge")
			assert.Equal(t, runner.Nudge, texts[1])
			assert.Equal(t, true, turn["nudged"], "a second answer without a tool, after the nudge")
			assert.Contains(t, string(inputs), `"nudge": true`, "a check rerun into the directory runs with the nudge too")
		})
	}
}

func TestCheckNeedsItsInputs(t *testing.T) {
	_, errOut, code := runCommand("check")

	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, `required flag(s) "agents", "out", "set" not set`)
}

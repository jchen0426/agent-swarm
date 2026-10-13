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
)

// messagesAPI answers every request with text and no tool call, so that
// every turn ends at once, and records the models asked for.
type messagesAPI struct {
	mu     sync.Mutex
	models []string
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

func TestCheckNeedsItsInputs(t *testing.T) {
	_, errOut, code := runCommand("check")

	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, `required flag(s) "agents", "out", "set" not set`)
}

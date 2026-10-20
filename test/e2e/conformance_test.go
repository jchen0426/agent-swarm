//go:build e2e

package e2e

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// ADR-006's conformance tests of the runtime, agentswarm turn, run as a host
// process (Plan 7d-1, Decision 13): the scripted vendor stands in for the LLM
// gateway, and a plain scripted MCP server for the MCP gateway, each
// accepting the session tokens of the turn's spec. Criterion 4, egress, is
// Plan 7d-2's.

// nudge is the platform's one message to a model that answered without
// calling a tool (Decision 13). e2e imports no platform code, so it is
// written here again.
const nudge = "You answered without calling a tool. If you are done, call end_turn; otherwise, continue."

// The exit codes of agentswarm turn (C7).
const (
	exitEnded       = 10
	exitFinished    = 11
	exitInterrupted = 14
	exitUnavailable = 15
)

// turnSpec is a turn's spec (C6), which the runner writes on the runtime's
// standard input.
type turnSpec struct {
	Turn      string     `json:"turn"`
	Run       string     `json:"run"`
	Agent     string     `json:"agent"`
	Round     int        `json:"round"`
	MCP       gatewayRef `json:"mcp"`
	LLM       gatewayRef `json:"llm"`
	Model     string     `json:"model"`
	System    []string   `json:"system"`
	Prompt    string     `json:"prompt"`
	Deadline  time.Time  `json:"deadline"`
	MaxTokens int64      `json:"maxTokens"`
}

// gatewayRef is where a gateway is, and the session's token for it.
type gatewayRef struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// stand is the two gateways' stand-ins.
type stand struct {
	env *harness.Env
	mcp *harness.MCPServer
}

// The tools the MCP gateway's stand-in lists: a read, and the board's
// end_turn.
const (
	search  = "logs__search"
	endTurn = "end_turn"
)

func newStand(t *testing.T) *stand {
	t.Helper()
	env := harness.New(t, harness.Options{NoDatabase: true, Servers: []harness.ServerSpec{{
		Name:  "gateway",
		Plain: true,
		Tools: []harness.ToolSpec{
			{Name: search, Description: "Search the logs.", InputSchema: `{"type":"object","properties":{"q":{"type":"string"}}}`},
			{Name: endTurn, Description: "End your turn.", InputSchema: `{"type":"object","properties":{}}`},
		},
	}}})
	return &stand{env: env, mcp: env.Server("gateway")}
}

// spec returns the spec of a turn, whose requests the stand-ins hold under
// name, and whose tokens they accept.
func (s *stand) spec(t *testing.T, name, prompt string, deadline time.Time) *turnSpec {
	t.Helper()
	spec := s.refused(name, prompt, deadline)
	s.env.Vendor().AcceptKey(t, name, spec.LLM.Token)
	s.mcp.AcceptCredential(t, name, spec.MCP.Token)
	return spec
}

// refused returns a spec whose tokens neither stand-in accepts.
func (s *stand) refused(name, prompt string, deadline time.Time) *turnSpec {
	return &turnSpec{
		Turn: "turn-" + name, Run: "run-1", Agent: "alpha", Round: 1,
		MCP:   gatewayRef{URL: s.mcp.URL(), Token: "aswm_" + rand.Text()},
		LLM:   gatewayRef{URL: s.env.Vendor().URL(), Token: "aswl_" + rand.Text()},
		Model: "claude-sonnet-5-5", System: []string{"You are alpha.", "Read the logs before anything else."},
		Prompt: prompt, Deadline: deadline, MaxTokens: 4096,
	}
}

// inTurn answers each turn's requests with replies, in order, and with the
// last of them once they run out.
func inTurn(replies ...harness.VendorReply) func(*harness.VendorRequest) harness.VendorReply {
	asked := map[string]int{}
	return func(r *harness.VendorRequest) harness.VendorReply {
		i := min(asked[r.Instance], len(replies)-1)
		asked[r.Instance]++
		return replies[i]
	}
}

// searching is the model's call of the read.
func searching() harness.VendorReply { return harness.ToolUse(search, map[string]any{"q": "error"}) }

// host is what a turn's process runs in: its working directory, its home and
// the environment it gets besides PATH, HOME and TMPDIR.
type host struct {
	dir, home string
	env       []string
}

// played is how a turn's process ended.
type played struct {
	code           int
	stdout, stderr string
	ended          time.Time
}

// playTurn runs agentswarm turn as a host process, with spec on its standard
// input, which it holds open until the process exits, as the runner does.
// The process gets no environment of the test's but PATH.
func playTurn(t *testing.T, spec *turnSpec, h host) played {
	t.Helper()
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Until(spec.Deadline)+30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "turn") //nolint:gosec // G204: the binary under test.
	cmd.Dir = cmp.Or(h.dir, t.TempDir())
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + cmp.Or(h.home, t.TempDir()), "TMPDIR=" + t.TempDir()}, h.env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	// A process that exits before it reads its spec closes the pipe, and its
	// exit code says why.
	_, _ = stdin.Write(raw)
	err = cmd.Wait()
	ended := time.Now()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		require.NoError(t, err)
	}
	require.NoError(t, ctx.Err(), "the turn did not exit")
	return played{code: cmd.ProcessState.ExitCode(), stdout: out.String(), stderr: errOut.String(), ended: ended}
}

// rpcs counts a turn's MCP requests of each JSON-RPC method, and its calls of
// each tool.
func rpcs(requests []harness.MCPRequest) (methods, tools map[string]int) {
	methods, tools = map[string]int{}, map[string]int{}
	for i := range requests {
		methods[requests[i].RPC]++
		if requests[i].RPC == "tools/call" {
			tools[requests[i].Tool]++
		}
	}
	return methods, tools
}

// Criterion 1: a turn ends at end_turn or at the round's limit. A model that
// keeps calling tools is stopped by the spec's deadline: no call starts after
// it, and the process exits interrupted.
func TestConformanceRoundBasedTurns(t *testing.T) {
	s := newStand(t)
	s.env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply {
		return searching().After(100 * time.Millisecond)
	})
	deadline := time.Now().Add(3 * time.Second)

	run := playTurn(t, s.spec(t, "turn", "Find the cause.", deadline), host{})

	assert.Equal(t, exitInterrupted, run.code, run.stderr)
	assert.False(t, run.ended.Before(deadline), "it ran to its deadline")
	assert.WithinDuration(t, deadline, run.ended, 2*time.Second, "and exited at it")
	// A request the runtime sent just before the deadline may arrive a little
	// after it.
	late := deadline.Add(250 * time.Millisecond)
	asked := s.env.Vendor().Requests("turn")
	assert.Greater(t, len(asked), 5, "the model kept calling tools")
	for i, r := range asked {
		assert.False(t, r.At.After(late), "model request %d started after the deadline", i)
	}
	for i, r := range s.mcp.Requests("turn") {
		if r.RPC == "tools/call" {
			assert.False(t, r.At.After(late), "tool call %d started after the deadline", i)
		}
	}
}

// Criterion 2: the platform's gateways are the only endpoints. With the
// environment naming another model URL and key, a trap, the trap gets
// nothing: only the spec's two URLs see requests, each with its token.
func TestConformanceToolsOnlyThroughTheGateway(t *testing.T) {
	s := newStand(t)
	var trapped atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		trapped.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	s.env.Vendor().Script(inTurn(searching(), harness.EndTurn()))
	spec := s.spec(t, "turn", "Find the cause.", time.Now().Add(time.Minute))

	run := playTurn(t, spec, host{env: []string{
		"ANTHROPIC_BASE_URL=" + trap.URL, "ANTHROPIC_API_KEY=sk-ant-trap-key", "ANTHROPIC_AUTH_TOKEN=trap-token",
	}})

	require.Equal(t, exitEnded, run.code, run.stderr)
	assert.Zero(t, trapped.Load(), "the environment's model URL and key reach nothing")
	vendor := s.env.Vendor()
	require.Len(t, vendor.Requests("turn"), 2)
	assert.Len(t, vendor.All(), 2, "no model request without the spec's token")
	for _, r := range vendor.All() {
		assert.Equal(t, spec.LLM.Token, r.Header.Get("X-Api-Key"))
		assert.Empty(t, r.Header.Get("Authorization"))
	}
	require.NotEmpty(t, s.mcp.Requests("turn"))
	assert.Len(t, s.mcp.All(), len(s.mcp.Requests("turn")), "no MCP request without the spec's token")
	_, tools := rpcs(s.mcp.Requests("turn"))
	assert.Equal(t, map[string]int{search: 1, endTurn: 1}, tools)
}

// Criterion 3: no tool that reaches outside. Each request offers exactly the
// tools the MCP gateway listed, and no tool the vendor runs.
func TestConformanceNoBuiltInToolsReachOutside(t *testing.T) {
	s := newStand(t)
	s.env.Vendor().Script(inTurn(harness.Text("Let me look."), searching(), harness.EndTurn()))

	run := playTurn(t, s.spec(t, "turn", "Find the cause.", time.Now().Add(time.Minute)), host{})

	require.Equal(t, exitEnded, run.code, run.stderr)
	requests := s.env.Vendor().Requests("turn")
	require.Len(t, requests, 3)
	for i, r := range requests {
		assert.Equal(t, []string{search, endTurn}, r.Tools, "request %d", i)
		var body struct {
			Tools []map[string]any `json:"tools"`
		}
		require.NoError(t, json.Unmarshal(r.Body, &body))
		for _, tool := range body.Tools {
			assert.NotContains(t, tool, "type", "request %d: a tool the vendor runs has a type", i)
		}
	}
}

// Criterion 5: no host content. With memories in the home and working
// directories, an AGENTS.md, and marker values in the environment, no
// request holds any of them: the system prompt is the spec's skills, and the
// only text of the platform's in a request is the nudge.
func TestConformanceNoHostConfigurationInPrompts(t *testing.T) {
	s := newStand(t)
	home, dir := t.TempDir(), filepath.Join(t.TempDir(), "canary-workdir")
	files := map[string]string{
		filepath.Join(home, "CLAUDE.md"):                "CANARY-USER-MEMORY",
		filepath.Join(home, ".claude", "CLAUDE.md"):     "CANARY-USER-SETTINGS-MEMORY",
		filepath.Join(home, ".claude", "settings.json"): `{"env": {"CANARY": "CANARY-SETTINGS-VALUE"}}`,
		filepath.Join(dir, "CLAUDE.md"):                 "CANARY-PROJECT-MEMORY",
		filepath.Join(dir, "AGENTS.md"):                 "CANARY-AGENTS-FILE",
	}
	for path, content := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	canaries := []string{"CANARY-USER-MEMORY", "CANARY-USER-SETTINGS-MEMORY", "CANARY-SETTINGS-VALUE", "CANARY-PROJECT-MEMORY",
		"CANARY-AGENTS-FILE", "CANARY-ENV-VALUE", "CANARY-HEADER-VALUE", "canary-workdir"}
	s.env.Vendor().Script(inTurn(harness.Text("Let me look."), searching(), harness.EndTurn()))
	spec := s.spec(t, "turn", "Find the cause of the checkout errors.", time.Now().Add(time.Minute))

	run := playTurn(t, spec, host{dir: dir, home: home, env: []string{
		"AGENTSWARM_CANARY=CANARY-ENV-VALUE", "ANTHROPIC_CUSTOM_HEADERS=X-Canary: CANARY-HEADER-VALUE",
	}})

	require.Equal(t, exitEnded, run.code, run.stderr)
	requests := s.env.Vendor().Requests("turn")
	require.Len(t, requests, 3, "the answer without a tool was nudged")
	for i, r := range requests {
		whole := string(r.Body) + headerText(r.Header)
		for _, canary := range canaries {
			assert.NotContains(t, whole, canary, "model request %d", i)
		}
		var body struct {
			System []map[string]any `json:"system"`
		}
		require.NoError(t, json.Unmarshal(r.Body, &body))
		var system []string
		for _, block := range body.System {
			assert.Equal(t, "text", block["type"], "model request %d", i)
			text, _ := block["text"].(string)
			system = append(system, text)
		}
		assert.Equal(t, spec.System, system, "model request %d: the system prompt is the spec's skills, each its own block", i)
		for _, m := range r.Messages {
			if m.Role != "user" {
				continue
			}
			for _, b := range m.Blocks {
				if b.Type == "text" {
					assert.Contains(t, []string{spec.Prompt, nudge}, b.Text, "model request %d: a text of the platform's", i)
				}
			}
		}
	}
	assert.Equal(t, nudge, requests[1].Messages[len(requests[1].Messages)-1].Blocks[0].Text, "the nudge, after the answer without a tool")
	for i, r := range s.mcp.Requests("turn") {
		whole := string(r.Body) + headerText(r.Header)
		for _, canary := range canaries {
			assert.NotContains(t, whole, canary, "MCP request %d", i)
		}
	}
}

// headerText writes a request's headers as text, to search.
func headerText(h http.Header) string {
	var b strings.Builder
	for name, values := range h {
		b.WriteString(name + ": " + strings.Join(values, ", ") + "\n")
	}
	return b.String()
}

// Criterion 6: each turn starts from a fresh context. The second turn's first
// request holds its own prompt, and nothing of the first turn's.
func TestConformanceAFreshContextEachTurn(t *testing.T) {
	s := newStand(t)
	s.env.Vendor().Script(inTurn(harness.Text("FIRST-TURN-ANSWER"), searching(), harness.EndTurn()))
	s.mcp.Script(search, func(*harness.ToolCall) harness.ToolReply { return harness.Result("FIRST-TURN-ROWS") })
	first := s.spec(t, "turn-1", "Round 1: FIRST-TURN-PROMPT", time.Now().Add(time.Minute))
	require.Equal(t, exitEnded, playTurn(t, first, host{}).code)
	second := s.spec(t, "turn-2", "Round 2: find the cause again.", time.Now().Add(time.Minute))

	run := playTurn(t, second, host{})

	require.Equal(t, exitEnded, run.code, run.stderr)
	requests := s.env.Vendor().Requests("turn-2")
	require.NotEmpty(t, requests)
	opening := requests[0]
	require.Len(t, opening.Messages, 1)
	assert.Equal(t, "user", opening.Messages[0].Role)
	assert.Equal(t, []harness.VendorBlock{{Type: "text", Text: second.Prompt}}, opening.Messages[0].Blocks)
	for _, earlier := range []string{"FIRST-TURN-PROMPT", "FIRST-TURN-ANSWER", "FIRST-TURN-ROWS", first.LLM.Token, first.MCP.Token} {
		assert.NotContains(t, string(opening.Body), earlier)
	}
}

// Criterion 7: tools are listed once per turn, however many calls it makes.
func TestConformanceToolsListedOncePerTurn(t *testing.T) {
	s := newStand(t)
	s.env.Vendor().Script(inTurn(searching(), searching(), searching(), harness.EndTurn()))
	for _, name := range []string{"turn-1", "turn-2"} {
		run := playTurn(t, s.spec(t, name, "Find the cause.", time.Now().Add(time.Minute)), host{})

		require.Equal(t, exitEnded, run.code, run.stderr)
		methods, tools := rpcs(s.mcp.Requests(name))
		assert.Equal(t, 1, methods["tools/list"], name)
		assert.Equal(t, map[string]int{search: 3, endTurn: 1}, tools, name)
	}
}

// A conforming runtime calls end_turn only when its model asks for it: a
// turn whose model never names it makes no end_turn call, and leaves no
// turn end (Decision 9).
func TestConformanceEndTurnOnlyWhenTheModelAsks(t *testing.T) {
	s := newStand(t)
	s.env.Vendor().Script(inTurn(searching(), harness.Text("I am done."), harness.Text("Nothing more.")))

	run := playTurn(t, s.spec(t, "turn", "Find the cause.", time.Now().Add(time.Minute)), host{})

	assert.Equal(t, exitFinished, run.code, run.stderr)
	assert.Len(t, s.env.Vendor().Requests("turn"), 3, "the second answer without a tool came after the nudge")
	_, tools := rpcs(s.mcp.Requests("turn"))
	assert.Equal(t, map[string]int{search: 1}, tools, "no end_turn call")
}

// Neither of a turn's tokens appears in what the process writes, however the
// turn ends (Global Constraints, secrets).
func TestNoTokenAppearsInTheRuntimesOutput(t *testing.T) {
	s := newStand(t)
	s.env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
		switch r.Instance {
		case "unavailable":
			return harness.Status(http.StatusInternalServerError, `{"type":"error","error":{"type":"api_error","message":"the gateway failed"}}`)
		case "deadline":
			return harness.Hang()
		}
		return harness.EndTurn()
	})
	for _, c := range []struct {
		name string
		spec func() *turnSpec
		code int
	}{
		{name: "ended", code: exitEnded, spec: func() *turnSpec { return s.spec(t, "ended", "Find the cause.", time.Now().Add(time.Minute)) }},
		{name: "unavailable", code: exitUnavailable, spec: func() *turnSpec {
			return s.spec(t, "unavailable", "Find the cause.", time.Now().Add(time.Minute))
		}},
		{name: "deadline", code: exitInterrupted, spec: func() *turnSpec { return s.spec(t, "deadline", "Find the cause.", time.Now().Add(2*time.Second)) }},
		{name: "session gone", code: exitInterrupted, spec: func() *turnSpec {
			return s.refused("session-gone", "Find the cause.", time.Now().Add(time.Minute))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := c.spec()

			run := playTurn(t, spec, host{})

			assert.Equal(t, c.code, run.code, run.stderr)
			for _, tok := range []string{spec.LLM.Token, spec.MCP.Token} {
				assert.NotContains(t, run.stdout, tok)
				assert.NotContains(t, run.stderr, tok)
			}
			assert.False(t, slices.ContainsFunc([]string{run.stdout, run.stderr}, func(s string) bool { return strings.Contains(s, "aswl_") || strings.Contains(s, "aswm_") }),
				"no token's prefix either")
		})
	}
}

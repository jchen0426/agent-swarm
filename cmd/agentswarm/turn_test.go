package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/outcome"
	"github.com/jchen0426/agent-swarm/internal/runner"
)

// modelMode is how the Messages API's stand-in answers.
type modelMode int

const (
	modelEnds  modelMode = iota // the model calls end_turn
	modelHangs                  // every request is held until the client gives up
	modelReads                  // the model calls a read, again and again
	modelFails                  // every request fails, marked unavailable
)

// gateways stand in for the two a turn reaches: a Messages API whose model
// answers as mode says; and an MCP endpoint, without sessions as the
// platform's is, that lists a read and end_turn and answers them. Each keeps
// what it was sent.
type gateways struct {
	llm, mcp           *httptest.Server
	llmToken, mcpToken string
	mode               modelMode

	mu      sync.Mutex
	headers []http.Header // each model request's
	keys    []string      // the X-Api-Key of each model request
	bodies  [][]byte      // each model request's body
	bearers []string      // the Authorization of each MCP request
}

func newGateways(t *testing.T, mode modelMode) *gateways {
	t.Helper()
	g := &gateways{llmToken: "aswl_" + rand.Text(), mcpToken: "aswm_" + rand.Text(), mode: mode}
	g.llm = httptest.NewServer(http.HandlerFunc(g.answer))
	t.Cleanup(g.llm.Close)
	s := mcp.NewServer(&mcp.Implementation{Name: "agentswarm-gateway", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "end_turn", Description: "End your turn.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "source: board"}, &mcp.TextContent{Text: "your turn has ended"}}}, nil
		})
	s.AddTool(&mcp.Tool{Name: "logs__search", Description: "Search the logs.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "call_id: c-1"}, &mcp.TextContent{Text: "no errors"}}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	g.mcp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.bearers = append(g.bearers, r.Header.Get("Authorization"))
		g.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(g.mcp.Close)
	return g
}

func (g *gateways) answer(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.headers = append(g.headers, r.Header.Clone())
	g.keys = append(g.keys, r.Header.Get("X-Api-Key"))
	g.bodies = append(g.bodies, body)
	n := len(g.bodies)
	g.mu.Unlock()
	tool := "end_turn"
	switch g.mode {
	case modelHangs:
		<-r.Context().Done()
		return
	case modelFails:
		w.Header().Set(outcome.Header, outcome.Unavailable)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"the gateway cannot serve this call now"}}`)
		return
	case modelReads:
		tool = "logs__search"
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"id":"msg_%d","type":"message","role":"assistant","model":"claude-sonnet-5-5",`+
		`"content":[{"type":"tool_use","id":"tu_%d","name":%q,"input":{}}],"stop_reason":"tool_use","stop_sequence":null,`+
		`"usage":{"input_tokens":10,"output_tokens":5}}`, n, n, tool)
}

func (g *gateways) asked() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.bodies)
}

// spec is a turn's spec (C6) for the gateways, as the runner writes it.
func (g *gateways) spec(deadline time.Time) map[string]any {
	return map[string]any{
		"turn": "0193a5c8-0000-7000-8000-000000000001", "run": "0193a5c8-0000-7000-8000-000000000002", "agent": "alpha", "round": 2,
		"mcp":   map[string]string{"url": g.mcp.URL, "token": g.mcpToken},
		"llm":   map[string]string{"url": g.llm.URL, "token": g.llmToken},
		"model": "claude-sonnet-5-5", "system": []string{"You are alpha.", "Read the logs first."},
		"prompt": "Round 2: find the cause.", "deadline": deadline.Format(time.RFC3339Nano), "maxTokens": 4096,
	}
}

// sized returns the gateways' spec, encoded, with its skill grown so that it
// is n bytes long.
func (g *gateways) sized(t *testing.T, n int) []byte {
	t.Helper()
	s := g.spec(time.Now().Add(time.Minute))
	s["system"] = []string{""}
	short := len(encode(t, s))
	s["system"] = []string{strings.Repeat("x", n-short)}
	raw := encode(t, s)
	require.Len(t, raw, n)
	return raw
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

// playing is agentswarm turn running in the test's process.
type playing struct {
	input          *io.PipeWriter
	done           chan struct{}
	stdout, stderr bytes.Buffer
	code           int
}

// startTurn runs agentswarm turn with spec on its standard input, which
// stays open until the turn exits or the test closes it, as the runner
// holds it open for the turn's life.
func startTurn(t *testing.T, spec []byte) *playing {
	t.Helper()
	r, w := io.Pipe()
	p := &playing{input: w, done: make(chan struct{})}
	go func() { _, _ = w.Write(spec) }()
	go func() {
		defer close(p.done)
		p.code = run([]string{"turn"}, r, &p.stdout, &p.stderr)
	}()
	t.Cleanup(func() {
		_ = w.Close()
		<-p.done
	})
	return p
}

// wait returns the turn's exit code, failing the test if it does not exit
// within timeout.
func (p *playing) wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		return p.code
	case <-time.After(timeout):
		t.Fatalf("the turn did not exit within %v", timeout)
		return -1
	}
}

// The runtime takes everything it needs from the spec on its standard input:
// the gateways' URLs and tokens, the model, the skills, the prompt and
// max_tokens. Nothing of the SDK's environment reaches a request: not its
// model URL, its token or its headers (ADR-006, criteria 2 and 5). A spec it
// cannot take is refused before any request.
func TestTurnTakesItsSpecFromStandardInput(t *testing.T) {
	var trapped atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		trapped.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	// Without a key in the environment, the SDK's defaults would read its
	// token and its headers.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", trap.URL)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "trap-token")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Canary: from-the-environment")
	t.Run("a spec", func(t *testing.T) {
		g := newGateways(t, modelEnds)

		p := startTurn(t, encode(t, g.spec(time.Now().Add(time.Minute))))

		require.Equal(t, outcome.ExitEnded, p.wait(t, 10*time.Second), p.stderr.String())
		var res runner.Result
		require.NoError(t, json.Unmarshal(p.stdout.Bytes(), &res), p.stdout.String())
		assert.Equal(t, runner.EndedByTool, res.Outcome)
		g.mu.Lock()
		defer g.mu.Unlock()
		require.Len(t, g.bodies, 1)
		assert.Equal(t, []string{g.llmToken}, g.keys, "the spec's LLM token, as the key")
		assert.Empty(t, g.headers[0].Get("Authorization"), "no token of the environment's")
		assert.Empty(t, g.headers[0].Get("X-Canary"), "no header of the environment's")
		var req struct {
			Model     string `json:"model"`
			MaxTokens int64  `json:"max_tokens"`
			System    []struct {
				Text string `json:"text"`
			} `json:"system"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(g.bodies[0], &req))
		assert.Equal(t, "claude-sonnet-5-5", req.Model)
		assert.Equal(t, int64(4096), req.MaxTokens)
		require.Len(t, req.System, 2, "each skill its own system block")
		assert.Equal(t, "You are alpha.", req.System[0].Text)
		assert.Equal(t, "Read the logs first.", req.System[1].Text)
		require.Len(t, req.Messages, 1)
		assert.Equal(t, "Round 2: find the cause.", req.Messages[0].Content[0].Text)
		require.NotEmpty(t, g.bearers)
		for _, b := range g.bearers {
			assert.Equal(t, "Bearer "+g.mcpToken, b)
		}
		assert.Zero(t, trapped.Load(), "the environment's model URL and key reach nothing")
	})
	t.Run("a spec of 1 MiB, then a newline", func(t *testing.T) {
		g := newGateways(t, modelEnds)

		p := startTurn(t, append(g.sized(t, maxSpec), '\n'))

		assert.Equal(t, outcome.ExitEnded, p.wait(t, 10*time.Second), p.stderr.String())
	})
	for _, c := range []struct {
		name string
		spec func(t *testing.T, g *gateways) []byte
		// ends closes standard input after the spec, which ends a spec that
		// is cut off.
		ends bool
		says string
	}{
		{name: "a spec over 1 MiB", says: "larger than 1 MiB", spec: func(t *testing.T, g *gateways) []byte {
			s := g.spec(time.Now().Add(time.Minute))
			s["system"] = []string{strings.Repeat("x", 1<<20)}
			return encode(t, s)
		}},
		{name: "a spec of 1 MiB and one byte", says: "larger than 1 MiB", spec: func(t *testing.T, g *gateways) []byte { return g.sized(t, maxSpec+1) }},
		{name: "a spec cut off", says: "read the turn's spec", ends: true, spec: func(*testing.T, *gateways) []byte { return []byte(`{"turn": `) }},
		{name: "a spec that is not JSON", says: "read the turn's spec", spec: func(*testing.T, *gateways) []byte { return []byte(`turn`) }},
		{name: "a spec without the MCP gateway's token", says: "no token for the MCP gateway", spec: func(t *testing.T, g *gateways) []byte {
			s := g.spec(time.Now().Add(time.Minute))
			s["mcp"] = map[string]string{"url": g.mcp.URL}
			return encode(t, s)
		}},
		{name: "a spec without a deadline", says: "no deadline", spec: func(t *testing.T, g *gateways) []byte {
			s := g.spec(time.Now().Add(time.Minute))
			delete(s, "deadline")
			return encode(t, s)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newGateways(t, modelEnds)
			spec := c.spec(t, g)

			p := startTurn(t, spec)
			if c.ends {
				require.NoError(t, p.input.Close())
			}

			code := p.wait(t, 10*time.Second)
			assert.Equal(t, exitError, code, "a crash: no class of the table")
			assert.Contains(t, p.stderr.String(), c.says)
			assert.Zero(t, g.asked(), "no request")
			assert.Empty(t, g.bearers, "no request")
			assert.NotContains(t, p.stderr.String(), g.llmToken)
			assert.NotContains(t, p.stderr.String(), g.mcpToken)
		})
	}
}

// The runner holds the runtime's standard input open for the turn's life:
// its end, as when the runner or its parent dies, ends the turn at once,
// interrupted (Decision 12).
func TestTurnExitsWhenItsStandardInputCloses(t *testing.T) {
	g := newGateways(t, modelHangs)
	p := startTurn(t, encode(t, g.spec(time.Now().Add(time.Minute))))
	require.Eventually(t, func() bool { return g.asked() > 0 }, 10*time.Second, 10*time.Millisecond, "the turn asks the model")

	closed := time.Now()
	require.NoError(t, p.input.Close())
	code := p.wait(t, 5*time.Second)

	assert.Equal(t, outcome.ExitInterrupted, code, p.stderr.String())
	assert.Less(t, time.Since(closed), time.Second)
}

// validSpec is a spec the runtime takes, for tests of how it builds its
// clients.
func validSpec(deadline time.Time) *turnSpec {
	return &turnSpec{
		MCP: gatewaySpec{URL: "http://127.0.0.1:53001/", Token: "aswm_test"}, LLM: gatewaySpec{URL: "http://127.0.0.1:53002", Token: "aswl_test"},
		Model: "claude-sonnet-5-5", System: []string{"You are alpha."}, Prompt: "Find the cause.", Deadline: deadline, MaxTokens: 4096,
	}
}

// A spec that is printed, as in an error, prints no token.
func TestASpecPrintsNoToken(t *testing.T) {
	spec := validSpec(time.Now())

	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		printed := fmt.Sprintf(verb, spec)

		assert.NotContains(t, printed, string(spec.MCP.Token), verb)
		assert.NotContains(t, printed, string(spec.LLM.Token), verb)
	}
}

// The runtime reaches the spec's URLs directly: a proxy the environment
// names would see both gateways' tokens (Decision 12).
func TestTheRuntimesTransportIgnoresProxies(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")

	rt := newTurnRuntime(validSpec(time.Now().Add(time.Minute)), time.Now())

	assert.Nil(t, rt.transport.Proxy)
	assert.Same(t, rt.transport, rt.client.Transport, "both gateways' clients use the one transport")
}

// The model client's request timeout runs to the spec's deadline, which
// ends the turn's context too, so no answer is cut short of the round: a
// round longer than the SDK's own default of ten minutes keeps its length.
func TestTheClientsTimeoutRunsToTheDeadline(t *testing.T) {
	now := time.Now()
	spec := validSpec(now.Add(30 * time.Minute))

	rt := newTurnRuntime(spec, now)

	timeout, err := anthropic.CalculateNonStreamingTimeout(int(spec.MaxTokens), spec.Model, rt.model.Options)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, timeout, spec.Deadline.Sub(now))
}

// A gateway's redirect is not followed: it would carry a session's token to
// another server (Global Constraints, secrets). It is an answer that is not
// a success, so the turn ends unavailable.
func TestTheRuntimeFollowsNoRedirect(t *testing.T) {
	var trapped atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		trapped.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, trap.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	for _, which := range []string{"llm", "mcp"} {
		t.Run(which, func(t *testing.T) {
			g := newGateways(t, modelEnds)
			spec := g.spec(time.Now().Add(time.Minute))
			spec[which] = map[string]string{"url": redirect.URL, "token": map[string]string{"llm": g.llmToken, "mcp": g.mcpToken}[which]}

			p := startTurn(t, encode(t, spec))

			assert.Equal(t, outcome.ExitUnavailable, p.wait(t, 10*time.Second), p.stderr.String())
			assert.Zero(t, trapped.Load(), "nothing reached the redirect's target")
		})
	}
}

// The runtime tries a model call once: the LLM gateway retries the vendor
// itself (Plan 6b), and its answer that is not a success ends the turn.
func TestTheRuntimeTriesAModelCallOnce(t *testing.T) {
	g := newGateways(t, modelFails)

	p := startTurn(t, encode(t, g.spec(time.Now().Add(time.Minute))))

	assert.Equal(t, outcome.ExitUnavailable, p.wait(t, 10*time.Second), p.stderr.String())
	assert.Equal(t, 1, g.asked())
}

// The runtime's step limit is 50 model calls (R14).
func TestTheRuntimeStopsAtItsStepLimit(t *testing.T) {
	g := newGateways(t, modelReads)

	p := startTurn(t, encode(t, g.spec(time.Now().Add(time.Minute))))

	assert.Equal(t, outcome.ExitFinished, p.wait(t, 30*time.Second), p.stderr.String())
	assert.Equal(t, 50, g.asked())
	var res runner.Result
	require.NoError(t, json.Unmarshal(p.stdout.Bytes(), &res), p.stdout.String())
	assert.Equal(t, runner.EndedBySteps, res.Outcome)
}

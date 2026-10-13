// Package mcpgw stands in for the platform's MCP gateway (spec Section 6) in
// the runtime spike. It serves canned read tools and the board tools of the
// current round, and records every call.
package mcpgw

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Call is one tool call the gateway received.
type Call struct {
	Round int             `json:"round"`
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args,omitempty"`
}

// Options configure a Gateway.
type Options struct {
	// Outputs maps read tools to their canned output. Board tools answer "recorded".
	Outputs map[string]string
	// Delays makes tools slow, to exercise round time limits.
	Delays map[string]time.Duration
	// AddAfterFirstCall names a tool added right after the first call of round 1,
	// to check that runtimes keep their tool list until the round ends.
	AddAfterFirstCall string
	// Log, when set, receives every call as one JSON line.
	Log io.Writer
}

var boardTools = map[string]bool{"post_evidence": true, "post_hypothesis": true, "end_turn": true}

var descriptions = map[string]string{
	"get_alert":          "Return the alert that opened this item.",
	"get_recent_changes": "List release and configuration changes on a service before a time.",
	"query_logs":         "Search a service's logs in a time range.",
	"slow_read":          "A read that takes a long time.",
	"probe_extra":        "A tool that appears in the middle of a round.",
	"post_evidence":      "Post evidence to the board, with a verbatim quote of a read's output.",
	"post_hypothesis":    "Post a hypothesis to the board: a key and one line of rationale.",
	"end_turn":           "End your turn for this round.",
}

// Gateway is an http.Handler that serves MCP at /mcp and a health check at
// /healthz. It has no control endpoint: the sandbox can reach it, so rounds
// change only through SetRound, from outside the sandbox.
type Gateway struct {
	opts   Options
	server *mcp.Server
	mcp    http.Handler
	mu     sync.Mutex
	round  int
	tools  []string
	added  bool
	calls  []Call
}

// New returns a gateway with no tools; SetRound gives it some.
func New(opts Options) *Gateway {
	g := &Gateway{opts: opts}
	g.server = mcp.NewServer(&mcp.Implementation{Name: "spike-mcp-gateway", Version: "0"}, nil)
	g.mcp = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return g.server }, nil)
	return g
}

// SetRound starts round n with exactly the given tools.
func (g *Gateway) SetRound(n int, tools ...string) {
	g.mu.Lock()
	old := g.tools
	g.round, g.tools, g.added = n, tools, false
	g.mu.Unlock()
	g.server.RemoveTools(old...)
	for _, name := range tools {
		g.add(name)
	}
}

// Calls returns a copy of the calls received so far.
func (g *Gateway) Calls() []Call {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Call(nil), g.calls...)
}

func (g *Gateway) add(name string) {
	desc := descriptions[name]
	if desc == "" {
		desc = "Spike tool."
	}
	g.server.AddTool(&mcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return g.call(ctx, name, req.Params.Arguments)
	})
}

func (g *Gateway) call(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	g.mu.Lock()
	c := Call{Round: g.round, Tool: name, Args: args}
	g.calls = append(g.calls, c)
	addNow := g.round == 1 && !g.added && g.opts.AddAfterFirstCall != ""
	if addNow {
		g.added = true
	}
	g.mu.Unlock()
	if g.opts.Log != nil {
		if line, err := json.Marshal(c); err == nil {
			_, _ = g.opts.Log.Write(append(line, '\n'))
		}
	}
	if addNow {
		g.add(g.opts.AddAfterFirstCall)
	}
	if d := g.opts.Delays[name]; d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := "recorded"
	if !boardTools[name] {
		out = g.opts.Outputs[name]
		if out == "" {
			out = "no data"
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		_, _ = io.WriteString(w, "ok")
	case "/mcp":
		g.mcp.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

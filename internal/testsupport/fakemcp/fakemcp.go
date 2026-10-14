// Package fakemcp is a programmable MCP server for the gateway's contract
// tests (spec Section 12.1). It records every tool call it receives, and it
// can answer slowly, fail, drop the connection or change its tool list.
package fakemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Reply answers one tool call.
type Reply func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, error)

// Text is a Reply that answers with one text block.
func Text(text string) Reply {
	return func(context.Context, json.RawMessage) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	}
}

// Result is a Reply that answers with res.
func Result(res *mcp.CallToolResult) Reply {
	return func(context.Context, json.RawMessage) (*mcp.CallToolResult, error) { return res, nil }
}

// Call is one tool call the server received.
type Call struct {
	Tool string
	Args json.RawMessage
	// Authorization is the request's Authorization header.
	Authorization string
	// Session is the id of the MCP session the call came in.
	Session string
}

// Server is a running fake server. Its URL is the MCP endpoint.
type Server struct {
	URL string
	// Client is an HTTP client that trusts the server's certificate.
	Client *http.Client

	srv *mcp.Server

	mu       sync.Mutex
	calls    []Call
	onCall   func(Call)
	slow     map[string]time.Duration
	drop     int
	status   int
	statuses int
	token    string
	held     chan struct{} // while open, requests to end a session wait
}

// Start starts a server named name, over plain HTTP. It stops when the test
// ends.
func Start(t testing.TB, name string) *Server {
	t.Helper()
	return start(t, name, httptest.NewServer)
}

// StartTLS starts a server named name, over TLS. Its Client trusts it.
func StartTLS(t testing.TB, name string) *Server {
	t.Helper()
	return start(t, name, httptest.NewTLSServer)
}

func start(t testing.TB, name string, newServer func(http.Handler) *httptest.Server) *Server {
	s := &Server{
		srv:  mcp.NewServer(&mcp.Implementation{Name: name, Version: "1"}, nil),
		slow: map[string]time.Duration{},
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.srv }, nil)
	ts := newServer(s.dropping(h))
	t.Cleanup(ts.Close)
	s.URL, s.Client = ts.URL, ts.Client()
	return s
}

// Add adds a tool, or replaces the one with its name. Connected clients are
// told that the tool list changed.
func (s *Server) Add(tool *mcp.Tool, reply Reply) {
	s.srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c := Call{Tool: req.Params.Name, Args: req.Params.Arguments}
		if req.Session != nil {
			c.Session = req.Session.ID()
		}
		if req.Extra != nil && req.Extra.Header != nil {
			c.Authorization = req.Extra.Header.Get("Authorization")
		}
		s.mu.Lock()
		s.calls = append(s.calls, c)
		onCall, delay := s.onCall, s.slow[c.Tool]
		s.mu.Unlock()
		if onCall != nil {
			onCall(c)
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return reply(ctx, c.Args)
	})
}

// AddHandler adds a tool answered by an SDK handler, which can use the
// session, for instance to send the client a request. Its calls are not
// recorded.
func (s *Server) AddHandler(tool *mcp.Tool, h mcp.ToolHandler) { s.srv.AddTool(tool, h) }

// Remove removes tools. Connected clients are told that the tool list changed.
func (s *Server) Remove(names ...string) { s.srv.RemoveTools(names...) }

// Calls returns the tool calls received so far, in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// OnCall runs f on every tool call as it arrives, before the reply.
func (s *Server) OnCall(f func(Call)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCall = f
}

// Slow delays the replies of a tool.
func (s *Server) Slow(tool string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slow[tool] = d
}

// DropNext drops the connection of the next n tool calls without answering
// them. A dropped call is not recorded.
func (s *Server) DropNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drop = n
}

// AnswerNext answers the next n tool calls with an HTTP status and no body.
func (s *Server) AnswerNext(n, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses, s.status = n, status
}

// RequireToken answers 401 to every request without "Bearer token".
func (s *Server) RequireToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
}

// HoldDeletes makes requests to end a session wait until release is called
// or the client gives up, as a slow server may.
func (s *Server) HoldDeletes() (release func()) {
	held := make(chan struct{})
	s.mu.Lock()
	s.held = held
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(held) }) }
}

// waitHeld holds a request to end a session while HoldDeletes says to, and
// reports whether the request is still to be served.
func (s *Server) waitHeld(r *http.Request) bool {
	s.mu.Lock()
	held := s.held
	s.mu.Unlock()
	if held == nil {
		return true
	}
	select {
	case <-held:
		return true
	case <-r.Context().Done():
		return false
	}
}

// dropping wraps h so that requests are refused, answered with a status or
// dropped while the server is told to.
func (s *Server) dropping(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		token := s.token
		s.mu.Unlock()
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodDelete && !s.waitHeld(r) {
			return
		}
		if r.Method != http.MethodPost {
			h.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		s.mu.Lock()
		drop := msg.Method == "tools/call" && s.drop > 0
		if drop {
			s.drop--
		}
		status := 0
		if msg.Method == "tools/call" && s.statuses > 0 {
			s.statuses--
			status = s.status
		}
		s.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if !drop {
			h.ServeHTTP(w, r)
			return
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
}

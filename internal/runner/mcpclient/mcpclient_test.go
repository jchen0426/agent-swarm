package mcpclient_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/endpoint"
	"github.com/jchen0426/agent-swarm/internal/runner"
	"github.com/jchen0426/agent-swarm/internal/runner/mcpclient"
)

// gateway stands in for the MCP gateway behind its endpoint: it knows one
// session's token, until the session ends; it lists a read and the board's
// end_turn; and it answers a tool as answers says, and the read with an
// output otherwise.
type gateway struct {
	token string

	mu       sync.Mutex
	ended    bool
	listings int
	calls    []string
	answers  map[string]answer
}

// answer is how the stand-in answers a call.
type answer struct {
	resp *app.Response
	err  error
}

func newGateway() *gateway {
	return &gateway{token: "aswm_" + rand.Text(), answers: map[string]answer{}}
}

func (g *gateway) Authenticate(_ context.Context, token string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if token != g.token || g.ended {
		return app.ErrNoSession
	}
	return nil
}

func (g *gateway) Tools(context.Context, string) ([]app.Tool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.listings++
	return []app.Tool{
		{Name: "logs__search", Description: "Search the logs.", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)},
		{Name: "end_turn", Description: "End your turn.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}, nil
}

func (g *gateway) Call(_ context.Context, _, name string, args json.RawMessage) (*app.Response, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, name+" "+string(args))
	if a, ok := g.answers[name]; ok {
		return a.resp, a.err
	}
	return &app.Response{CallID: "call-1", Source: "logs/search", Status: app.StatusOK, Text: "10:02 checkout error: FLAG_X invalid"}, nil
}

func (g *gateway) set(name string, a answer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.answers[name] = a
}

func (g *gateway) end() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ended = true
}

func (g *gateway) listed() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.listings
}

func (g *gateway) called() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

// serve serves the endpoint in front of gw, behind front when it is set.
func serve(t *testing.T, gw *gateway, front func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	h := endpoint.New(gw)
	if front != nil {
		h = front(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// open returns a toolbox for the session of gw, through srv.
func open(t *testing.T, srv *httptest.Server, gw *gateway) *mcpclient.Toolbox {
	t.Helper()
	tb := mcpclient.New(srv.URL, gw.token, srv.Client())
	t.Cleanup(func() { _ = tb.Close() })
	return tb
}

// failingCalls answers the first n tools/call requests with status, unmarked,
// and lets every other request through to the endpoint.
func failingCalls(status, n int) func(http.Handler) http.Handler {
	var mu sync.Mutex
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			mu.Lock()
			fail := n > 0 && bytes.Contains(body, []byte(`"method":"tools/call"`))
			if fail {
				n--
			}
			mu.Unlock()
			if fail {
				http.Error(w, "scripted failure", status)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// breakingCalls answers every tools/call with the start of a success, then
// drops the connection, and lets every other request through to the
// endpoint.
func breakingCalls(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if !bytes.Contains(body, []byte(`"method":"tools/call"`)) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":3,"result":{"content":[`)
		_ = http.NewResponseController(w).Flush()
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			_ = conn.Close()
		}
	})
}

// unreadable answers every tools/call with a success whose body is not
// JSON-RPC, and lets every other request through to the endpoint.
func unreadable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if !bytes.Contains(body, []byte(`"method":"tools/call"`)) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"this is": "no answer"`)
	})
}

// answering answers every tools/call with a result as it is, and lets every
// other request through to the endpoint.
func answering(result string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(body, &msg) != nil || msg.Method != "tools/call" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, msg.ID, result)
		})
	}
}

func names(tools []runner.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

// The toolbox lists the gateway's tools once in its life, which is one
// turn's: a second listing, after calls, reads the same list again without
// asking (ADR-006, criterion 7).
func TestTheToolboxListsItsToolsOncePerTurn(t *testing.T) {
	gw := newGateway()
	tb := open(t, serve(t, gw, nil), gw)

	tools, err := tb.Tools(t.Context())
	require.NoError(t, err)
	for range 3 {
		_, _, err := tb.Call(t.Context(), "logs__search", json.RawMessage(`{"q":"error"}`))
		require.NoError(t, err)
	}
	again, err := tb.Tools(t.Context())
	require.NoError(t, err)

	assert.Equal(t, []string{"logs__search", "end_turn"}, names(tools))
	assert.Equal(t, "Search the logs.", tools[0].Description)
	assert.JSONEq(t, `{"type":"object","properties":{"q":{"type":"string"}}}`, string(tools[0].InputSchema))
	assert.Equal(t, tools, again)
	assert.Equal(t, 1, gw.listed(), "one listing in the turn, whatever it calls")
	assert.Len(t, gw.called(), 3)
}

// Each of the endpoint's answers has its class (Decision 13): a session gone
// (-32001, and HTTP 401), the gateway unavailable (-32002, any other answer
// that is not a success, and no answer at all), and arguments refused (HTTP
// 413); a tool's own error is a result, with isError, each text block on its
// own.
func TestTheToolboxMapsTheEndpointsAnswers(t *testing.T) {
	big := json.RawMessage(`{"q":"` + strings.Repeat("x", endpoint.MaxBodyBytes) + `"}`)
	for _, c := range []struct {
		name    string
		tool    string
		args    json.RawMessage
		answer  *answer
		end     bool // the session ends after the listing
		front   func(http.Handler) http.Handler
		refused bool // nothing listens at the endpoint's address
		want    error
		crash   bool     // the call fails with no class of the table: an answer the runtime cannot read
		text    string   // what the error says, when it says the gateway's words
		blocks  []string // the result's, when the call answers one
		isError bool
	}{
		{name: "-32001: the session gone at a call", tool: "logs__search", answer: &answer{err: app.ErrNoSession},
			want: runner.ErrSessionGone, text: "the session has ended"},
		{name: "-32001: a board call its round's end refused", tool: "end_turn",
			answer: &answer{resp: &app.Response{Source: "board", Status: app.StatusRefused, Text: "the round has ended: your turn is over", TurnOver: true}},
			want:   runner.ErrSessionGone, text: "the round has ended: your turn is over"},
		{name: "HTTP 401: a token with no live session", tool: "logs__search", end: true, want: runner.ErrSessionGone},
		{name: "-32002: the gateway unavailable", tool: "end_turn", answer: &answer{err: fmt.Errorf("%w: the board could not be reached", app.ErrUnavailable)},
			want: runner.ErrUnavailable, text: "the gateway cannot serve this call now; try again"},
		{name: "another answer that is not a success: 500", tool: "logs__search", front: failingCalls(http.StatusInternalServerError, 1), want: runner.ErrUnavailable},
		{name: "another answer that is not a success: 404", tool: "logs__search", front: failingCalls(http.StatusNotFound, 1), want: runner.ErrUnavailable},
		{name: "a refused connection", tool: "logs__search", refused: true, want: runner.ErrUnavailable},
		{name: "an answer that breaks off", tool: "logs__search", front: breakingCalls, want: runner.ErrUnavailable},
		{name: "HTTP 413: arguments over the endpoint's cap", tool: "logs__search", args: big, want: runner.ErrRefused},
		{name: "a tool's own error", tool: "logs__search",
			answer:  &answer{resp: &app.Response{CallID: "call-2", Source: "logs/search", Status: app.StatusReadError, Text: "logs gave no answer in time"}},
			blocks:  []string{"call_id: call-2\nsource: logs/search\nread error: this output cannot be cited", "logs gave no answer in time"},
			isError: true},
		{name: "a result", tool: "logs__search",
			blocks: []string{"call_id: call-1\nsource: logs/search", "10:02 checkout error: FLAG_X invalid"}},
		{name: "a success that is not an answer", tool: "logs__search", front: unreadable, crash: true},
		{name: "a call the client cannot send", tool: "logs__search", args: json.RawMessage(`{"q": `), crash: true},
		{name: "a result with a block that is not text", tool: "logs__search", crash: true,
			front: answering(`{"content":[{"type":"text","text":"call_id: call-1"},{"type":"image","data":"aGk=","mimeType":"image/png"}]}`)},
	} {
		t.Run(c.name, func(t *testing.T) {
			gw := newGateway()
			if c.answer != nil {
				gw.set(c.tool, *c.answer)
			}
			srv := serve(t, gw, c.front)
			url := srv.URL
			if c.refused {
				url = closedAddress(t)
			}
			tb := mcpclient.New(url, gw.token, srv.Client())
			t.Cleanup(func() { _ = tb.Close() })
			if !c.refused {
				_, err := tb.Tools(t.Context())
				require.NoError(t, err)
			}
			if c.end {
				gw.end()
			}
			args := c.args
			if args == nil {
				args = json.RawMessage(`{"q":"error"}`)
			}

			blocks, isError, err := tb.Call(t.Context(), c.tool, args)

			if c.crash {
				require.Error(t, err)
				for _, class := range []error{runner.ErrSessionGone, runner.ErrUnavailable, runner.ErrRefused, runner.ErrVendor, runner.ErrCut} {
					require.NotErrorIs(t, err, class)
				}
				return
			}
			if c.want != nil {
				require.ErrorIs(t, err, c.want)
				assert.Contains(t, err.Error(), c.text)
				assert.NotContains(t, err.Error(), gw.token)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.blocks, blocks)
			assert.Equal(t, c.isError, isError)
		})
	}
}

// A call the gateway could not serve leaves the toolbox able to call again,
// as end_turn's second try needs (Decision 13), even after an answer that
// ends the client's MCP session.
func TestTheToolboxCanCallAgainAfterAFailure(t *testing.T) {
	for _, c := range []struct {
		name  string
		front func(http.Handler) http.Handler
		set   func(*gateway)
	}{
		{name: "-32002", set: func(g *gateway) {
			g.set("end_turn", answer{err: fmt.Errorf("%w: the board could not be reached", app.ErrUnavailable)})
		}},
		{name: "503", front: failingCalls(http.StatusServiceUnavailable, 1)},
		{name: "an unmarked 400", front: failingCalls(http.StatusBadRequest, 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			gw := newGateway()
			if c.set != nil {
				c.set(gw)
			}
			tb := open(t, serve(t, gw, c.front), gw)
			_, err := tb.Tools(t.Context())
			require.NoError(t, err)

			_, _, err = tb.Call(t.Context(), "end_turn", json.RawMessage(`{}`))
			require.ErrorIs(t, err, runner.ErrUnavailable)
			gw.set("end_turn", answer{resp: &app.Response{Source: "board", Status: app.StatusOK, Text: "your turn has ended"}})
			blocks, isError, err := tb.Call(t.Context(), "end_turn", json.RawMessage(`{}`))

			require.NoError(t, err)
			assert.False(t, isError)
			assert.Equal(t, []string{"source: board", "your turn has ended"}, blocks)
			assert.Equal(t, 1, gw.listed(), "a second try lists nothing again")
		})
	}
}

// request is what the endpoint received of one request.
type request struct {
	url    string
	header http.Header
	body   []byte
}

// The session's token travels in the Authorization header of every request,
// as a bearer, and nowhere else: not in the URL, the body or another header
// (Global Constraints, secrets).
func TestTheTokenTravelsOnlyInTheAuthorizationHeader(t *testing.T) {
	gw := newGateway()
	var (
		mu   sync.Mutex
		seen []request
	)
	keep := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			seen = append(seen, request{url: r.URL.String(), header: r.Header.Clone(), body: body})
			mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
	srv := serve(t, gw, keep)
	tb := mcpclient.New(srv.URL, gw.token, srv.Client())

	_, err := tb.Tools(t.Context())
	require.NoError(t, err)
	_, _, err = tb.Call(t.Context(), "logs__search", json.RawMessage(`{"q":"error"}`))
	require.NoError(t, err)
	require.NoError(t, tb.Close())

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, seen)
	for i, r := range seen {
		assert.Equal(t, "Bearer "+gw.token, r.header.Get("Authorization"), "request %d", i)
		assert.NotContains(t, r.url, gw.token, "request %d", i)
		assert.NotContains(t, string(r.body), gw.token, "request %d", i)
		for name, values := range r.header {
			if name != "Authorization" {
				assert.NotContains(t, strings.Join(values, " "), gw.token, "request %d, header %s", i, name)
			}
		}
	}
}

// The toolbox follows no redirect, whatever client it is given: the bearer
// would go with it to another server.
func TestTheToolboxFollowsNoRedirect(t *testing.T) {
	gw := newGateway()
	var trapped atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		trapped.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	redirecting := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"method":"tools/call"`)) {
				http.Redirect(w, r, trap.URL, http.StatusTemporaryRedirect)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	srv := serve(t, gw, redirecting)
	tb := mcpclient.New(srv.URL, gw.token, &http.Client{})
	t.Cleanup(func() { _ = tb.Close() })
	_, err := tb.Tools(t.Context())
	require.NoError(t, err)

	_, _, err = tb.Call(t.Context(), "logs__search", json.RawMessage(`{"q":"error"}`))

	require.ErrorIs(t, err, runner.ErrUnavailable, "an answer that is not a success, unmarked")
	assert.Zero(t, trapped.Load(), "nothing reached the redirect's target")
}

// closedAddress returns the URL of a loopback port nothing listens on.
func closedAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return "http://" + addr + "/"
}

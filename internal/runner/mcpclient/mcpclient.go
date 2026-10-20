// Package mcpclient is the runtime's toolbox (Plan 7d-1, Decision 13): it
// speaks MCP to the MCP gateway's endpoint, with the session's token as its
// bearer, lists the tools once, and gives each failure of the endpoint's its
// class, as runner.ErrSessionGone, runner.ErrUnavailable or
// runner.ErrRefused.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jchen0426/agent-swarm/internal/outcome"
	"github.com/jchen0426/agent-swarm/internal/runner"
)

// Toolbox is a turn's tools, at the MCP gateway's endpoint. It lists them
// once in its life, a turn's, and serves one call at a time, as the runner
// makes them.
type Toolbox struct {
	url    string
	client *http.Client
	bearer *bearer

	session *mcp.ClientSession
	tools   []runner.Tool
	listed  bool
}

// errRedirect refuses a redirect, which would carry the token to another
// server.
var errRedirect = errors.New("mcpclient: the endpoint answered with a redirect, which the toolbox does not follow")

// New returns a Toolbox that speaks MCP to url, with token as its bearer.
// The token goes in every request's Authorization header and nowhere else,
// and the toolbox follows no redirect. client's transport carries the
// requests; nil means http.DefaultClient's.
func New(url, token string, client *http.Client) *Toolbox {
	c := http.Client{}
	if client != nil {
		c = *client
	}
	b := &bearer{token: token, next: c.Transport}
	if b.next == nil {
		b.next = http.DefaultTransport
	}
	c.Transport = b
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirect }
	return &Toolbox{url: url, client: &c, bearer: b}
}

// Tools lists the gateway's tools, the first time it is called, and gives
// the same list again after that: the tools a turn sees change only at its
// round's end (ADR-006, criterion 7).
func (tb *Toolbox) Tools(ctx context.Context) ([]runner.Tool, error) {
	if tb.listed {
		return tb.tools, nil
	}
	cs, err := tb.connect(ctx)
	if err != nil {
		return nil, err
	}
	tb.bearer.reset()
	var tools []runner.Tool
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, tb.failed(err)
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s: input schema: %w", tool.Name, err)
		}
		tools = append(tools, runner.Tool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	tb.tools, tb.listed = tools, true
	return tools, nil
}

// Call calls a tool and returns its result's content, each text block on its
// own, and whether the result is a tool's error. A result with a block that
// is not text is one the runtime cannot read: the gateway sends text alone.
func (tb *Toolbox) Call(ctx context.Context, name string, input json.RawMessage) (blocks []string, isError bool, err error) {
	cs, err := tb.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	tb.bearer.reset()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		return nil, false, tb.failed(err)
	}
	for i, c := range res.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok {
			return nil, false, fmt.Errorf("block %d of the result is not text, which the runtime cannot read", i)
		}
		blocks = append(blocks, text.Text)
	}
	return blocks, res.IsError, nil
}

// Close ends the toolbox's MCP session, if it has one.
func (tb *Toolbox) Close() error {
	if tb.session == nil {
		return nil
	}
	err := tb.session.Close()
	tb.session = nil
	return err
}

// connect returns the toolbox's MCP session, opening one when it has none.
// The client offers the endpoint nothing of its host, no roots, and listens
// for nothing the endpoint might send: no list of tools changes in a turn.
func (tb *Toolbox) connect(ctx context.Context) (*mcp.ClientSession, error) {
	if tb.session != nil {
		return tb.session, nil
	}
	tb.bearer.reset()
	client := mcp.NewClient(&mcp.Implementation{Name: "agentswarm-turn", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: tb.url, HTTPClient: tb.client, MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", tb.failed(err))
	}
	tb.session = cs
	return cs, nil
}

// failed gives an exchange with the endpoint that failed its class
// (Decision 13), and drops the MCP session when the failure was the HTTP
// answer's, which may have ended the client's session, so that a next call,
// end_turn's second try, opens another.
//
// The HTTP answer decides first: HTTP 401 without a marker is a session
// gone; any other answer that is not a success goes by its marker, refused
// for the endpoint's 413, and unavailable without one; no whole answer at
// all is unavailable. A JSON-RPC error in an answer that is a success decides
// next: CodeSessionGone is a session gone, with the gateway's words, and
// CodeUnavailable unavailable. Any other failure, a request that was never
// sent and an answer that cannot be read among them, is returned as it is:
// a crash.
func (tb *Toolbox) failed(err error) error {
	ex := tb.bearer.answered()
	// No answer at all has the status 0.
	httpFailure := ex.sent && (ex.broken || ex.status < 200 || ex.status > 299)
	if httpFailure {
		_ = tb.Close()
	}
	var rpc *jsonrpc.Error
	switch {
	case !ex.sent:
		return err
	case ex.status == 0 || ex.broken:
		return fmt.Errorf("%w: %w", runner.ErrUnavailable, err)
	case ex.status == http.StatusUnauthorized && ex.marker == "":
		return fmt.Errorf("%w: %w", runner.ErrSessionGone, err)
	case httpFailure:
		return fmt.Errorf("%w: %w", runner.ByMarker(ex.marker), err)
	case !errors.As(err, &rpc):
		return err
	case rpc.Code == outcome.CodeSessionGone:
		return fmt.Errorf("%w: %s", runner.ErrSessionGone, rpc.Message)
	case rpc.Code == outcome.CodeUnavailable:
		return fmt.Errorf("%w: %s", runner.ErrUnavailable, rpc.Message)
	}
	return err
}

// bearer is the toolbox's transport. It sends each request with the
// session's token as its bearer, and keeps what the endpoint answered the
// last request.
type bearer struct {
	token string
	next  http.RoundTripper

	mu   sync.Mutex
	last *exchange
}

// exchange is what became of a request.
type exchange struct {
	sent   bool
	status int    // the answer's; 0 when none came
	marker string // the answer's outcome.Header
	broken bool   // the answer's body broke off
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	ex := &exchange{sent: true}
	b.mu.Lock()
	b.last = ex
	b.mu.Unlock()
	resp, err := b.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ex.status, ex.marker = resp.StatusCode, resp.Header.Get(outcome.Header)
	resp.Body = &answerBody{ReadCloser: resp.Body, b: b, ex: ex}
	return resp, nil
}

// reset forgets the last exchange, before a request of the toolbox's.
func (b *bearer) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last = nil
}

// answered returns what became of the last request since the reset: an
// exchange that was not sent when there was none.
func (b *bearer) answered() exchange {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last == nil {
		return exchange{}
	}
	return *b.last
}

// answerBody is an answer's body, which marks its exchange broken when it
// breaks off.
type answerBody struct {
	io.ReadCloser
	b  *bearer
	ex *exchange
}

func (a *answerBody) Read(p []byte) (int, error) {
	n, err := a.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		a.b.mu.Lock()
		a.ex.broken = true
		a.b.mu.Unlock()
	}
	return n, err
}

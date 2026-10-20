package endpoint_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/endpoint"
	"github.com/jchen0426/agent-swarm/internal/outcome"
)

// gateway is a stand-in that knows one token and records the calls it gets.
// It answers a tool answers names as answers says, and fails every listing
// with listErr when that is set.
type gateway struct {
	mu      sync.Mutex
	calls   []string
	answers map[string]answer
	listErr error
}

// answer is how the stand-in answers a call.
type answer struct {
	resp *app.Response
	err  error
}

func (g *gateway) Authenticate(_ context.Context, token string) error {
	if token != "tok" {
		return app.ErrNoSession
	}
	return nil
}

func (g *gateway) Tools(context.Context, string) ([]app.Tool, error) {
	if g.listErr != nil {
		return nil, g.listErr
	}
	return []app.Tool{{
		Name: "flags__get_flag", Title: "Flag", Description: "Read a flag.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"flag":{"type":"string"}}}`),
		Annotations: json.RawMessage(`{"title":"","readOnlyHint":true,"destructiveHint":null,"idempotentHint":false,"openWorldHint":null}`),
	}}, nil
}

func (g *gateway) Call(_ context.Context, token, name string, args json.RawMessage) (*app.Response, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, token+" "+name+" "+string(args))
	if a, ok := g.answers[name]; ok {
		return a.resp, a.err
	}
	if name != "flags__get_flag" {
		return &app.Response{CallID: "call-2", Status: app.StatusRefused, Text: "there is no tool by that name in your list"}, nil
	}
	return &app.Response{CallID: "call-1", Source: "flags/get_flag", Status: app.StatusOK, Text: `{"on":true}`, Notes: []string{"image content was left out"}}, nil
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func serve(t *testing.T) (*httptest.Server, *gateway) {
	t.Helper()
	return serveFor(t, &gateway{})
}

// serveFor serves the endpoint in front of gw.
func serveFor(t *testing.T, gw *gateway) (*httptest.Server, *gateway) {
	t.Helper()
	srv := httptest.NewServer(endpoint.New(gw))
	t.Cleanup(srv.Close)
	return srv, gw
}

func connect(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil)
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token}}, MaxRetries: -1}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestAnAgentListsAndCallsToolsThroughTheEndpoint(t *testing.T) {
	srv, gw := serve(t)
	cs := connect(t, srv.URL, "tok")

	list, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, list.Tools, 1)
	assert.Equal(t, "flags__get_flag", list.Tools[0].Name)
	assert.True(t, list.Tools[0].Annotations.ReadOnlyHint)
	assert.Nil(t, list.Tools[0].OutputSchema)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "flags__get_flag", Arguments: map[string]any{"flag": "x"}})
	require.NoError(t, err)
	assert.False(t, res.IsError)
	require.Len(t, res.Content, 2)
	assert.Equal(t, "call_id: call-1\nsource: flags/get_flag\nnote: image content was left out", res.Content[0].(*mcp.TextContent).Text)
	assert.JSONEq(t, `{"on":true}`, res.Content[1].(*mcp.TextContent).Text)
	assert.Equal(t, []string{`tok flags__get_flag {"flag":"x"}`}, gw.calls)
}

func TestTheSessionAdvertisesToolsAndNothingElse(t *testing.T) {
	srv, _ := serve(t)
	cs := connect(t, srv.URL, "tok")

	caps := cs.InitializeResult().Capabilities

	require.NotNil(t, caps.Tools)
	assert.False(t, caps.Tools.ListChanged)
	assert.Nil(t, caps.Resources)
	assert.Nil(t, caps.Prompts)
}

func TestACallToAToolTheAgentCannotSeeReachesTheGateway(t *testing.T) {
	srv, gw := serve(t)
	cs := connect(t, srv.URL, "tok")

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "flags__set_flag", Arguments: map[string]any{}})

	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "refused")
	assert.Equal(t, []string{"tok flags__set_flag {}"}, gw.calls, "the gateway records the attempt")
}

func TestARequestWithoutALiveTokenIsRefused(t *testing.T) {
	srv, gw := serve(t)
	for _, token := range []string{"", "stolen"} {
		c := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil)
		_, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer{token}}, MaxRetries: -1}, nil)

		require.Error(t, err, token)
	}
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Empty(t, gw.calls)
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	return postAs(t, url, "tok", body)
}

// postAs posts body with the bearer token tok.
func postAs(t *testing.T, url, tok, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestBatchesAndLargeRequestsAreRefused(t *testing.T) {
	srv, gw := serve(t)

	batch := post(t, srv.URL, ` [{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`)
	large := post(t, srv.URL, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"a":"`+strings.Repeat("x", endpoint.MaxBodyBytes)+`"}}}`)

	assert.Equal(t, http.StatusBadRequest, batch.StatusCode)
	assert.Equal(t, http.StatusRequestEntityTooLarge, large.StatusCode)
	assert.Empty(t, gw.calls)
}

func TestEveryOtherMethodIsRefused(t *testing.T) {
	srv, _ := serve(t)
	for _, method := range []string{"resources/list", "prompts/list", "completion/complete", "logging/setLevel", "resources/read"} {
		resp := post(t, srv.URL, `{"jsonrpc":"2.0","id":7,"method":"`+method+`","params":{}}`)
		var body struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body), method)

		require.NotNil(t, body.Error, method)
		assert.Equal(t, -32601, body.Error.Code, method)
	}
}

// rpcError is a JSON-RPC answer's error, if it has one.
type rpcError struct {
	Error *struct {
		Code    int64  `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// callOf is a tools/call request of name.
func callOf(name string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
}

// The endpoint answers each of the gateway's failures with its code (Plan
// 7d-1, Decision 13; R8): a session gone, the round's end of a board call
// among them, with CodeSessionGone; the gateway's own failures, a holding's
// refusal and a board call's platform failure with CodeUnavailable. A dead
// token gets HTTP 401, and arguments over the cap HTTP 413, marked refused.
// No answer repeats the gateway's error.
func TestTheMCPEndpointAnswersWithItsCodes(t *testing.T) {
	roundEnded := &app.Response{Source: "board", Status: app.StatusRefused, Text: "the round has ended: your turn is over", TurnOver: true}
	for _, c := range []struct {
		name   string
		gw     *gateway
		token  string
		body   string
		status int
		code   int64
		marker string
	}{
		{name: "a session gone at a call", gw: &gateway{answers: map[string]answer{"flags__get_flag": {err: app.ErrNoSession}}},
			body: callOf("flags__get_flag"), status: http.StatusOK, code: outcome.CodeSessionGone},
		{name: "a session gone at a listing", gw: &gateway{listErr: app.ErrNoSession},
			body: rpcList, status: http.StatusOK, code: outcome.CodeSessionGone},
		{name: "a board call the round's end refused", gw: &gateway{answers: map[string]answer{"end_turn": {resp: roundEnded}}},
			body: callOf("end_turn"), status: http.StatusOK, code: outcome.CodeSessionGone},
		{name: "the gateway unable to serve a call", gw: &gateway{answers: map[string]answer{"flags__get_flag": {err: fmt.Errorf("%w: revision c0ffee: the registry is down", app.ErrUnavailable)}}},
			body: callOf("flags__get_flag"), status: http.StatusOK, code: outcome.CodeUnavailable},
		{name: "a holding's refusal", gw: &gateway{answers: map[string]answer{"flags__get_flag": {err: fmt.Errorf("%w: the holding of the lease does not hold", app.ErrUnavailable)}}},
			body: callOf("flags__get_flag"), status: http.StatusOK, code: outcome.CodeUnavailable},
		{name: "a board call's platform failure", gw: &gateway{answers: map[string]answer{"end_turn": {err: fmt.Errorf("%w: the board could not be reached: database down", app.ErrUnavailable)}}},
			body: callOf("end_turn"), status: http.StatusOK, code: outcome.CodeUnavailable},
		{name: "the gateway unable to serve a listing", gw: &gateway{listErr: fmt.Errorf("%w: the board of run run-1: database down", app.ErrUnavailable)},
			body: rpcList, status: http.StatusOK, code: outcome.CodeUnavailable},
		{name: "a dead token", gw: &gateway{}, token: "stolen", body: callOf("flags__get_flag"), status: http.StatusUnauthorized},
		{name: "arguments over the cap", gw: &gateway{},
			body:   `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"a":"` + strings.Repeat("x", endpoint.MaxBodyBytes) + `"}}}`,
			status: http.StatusRequestEntityTooLarge, marker: outcome.Refused},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := serveFor(t, c.gw)
			resp := postAs(t, srv.URL, cmp.Or(c.token, "tok"), c.body)

			require.Equal(t, c.status, resp.StatusCode)
			assert.Equal(t, c.marker, resp.Header.Get(outcome.Header))
			if c.code == 0 {
				return
			}
			var answered rpcError
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&answered))
			require.NotNil(t, answered.Error)
			assert.Equal(t, c.code, answered.Error.Code)
			assert.NotContains(t, answered.Error.Message, "down", "the gateway's own words, never its error")
		})
	}
}

// rpcList is a tools/list request.
const rpcList = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

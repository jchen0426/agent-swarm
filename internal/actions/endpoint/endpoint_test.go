package endpoint_test

import (
	"context"
	"encoding/json"
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
)

// gateway is a stand-in that knows one token and records the calls it gets.
type gateway struct {
	mu    sync.Mutex
	calls []string
}

func (g *gateway) Authenticate(_ context.Context, token string) error {
	if token != "tok" {
		return app.ErrNoSession
	}
	return nil
}

func (g *gateway) Tools(context.Context, string) ([]app.Tool, error) {
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
	gw := &gateway{}
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")
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

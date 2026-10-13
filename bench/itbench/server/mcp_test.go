package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/server"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientSide, serverSide := mcp.NewInMemoryTransports()
	_, err := server.New(fixture()).Connect(ctx, serverSide, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientSide, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestTheServerOffersOnlyReadOnlyTools(t *testing.T) {
	cs := connect(t)

	res, err := cs.ListTools(context.Background(), nil)

	require.NoError(t, err)
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		assert.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
	}
	slices.Sort(names)
	assert.Equal(t, []string{"get_changes", "get_error_spans", "get_events", "get_object", "get_pod_metrics",
		"get_service_metrics", "list_alerts", "list_objects", "list_services", "query_logs"}, names)
	assert.Contains(t, cs.InitializeResult().Instructions, "2025-12-15T17:57:59Z")
}

func TestAToolCallReturnsJSONText(t *testing.T) {
	cs := connect(t)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_changes", Arguments: map[string]any{"service": "shipping"}})

	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, `"to":"quote:0000"`)
}

func TestABadCallIsAToolError(t *testing.T) {
	cs := connect(t)

	missing, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "query_logs", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.True(t, missing.IsError)

	unknown, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_changes", Arguments: map[string]any{"servise": "shipping"}})
	require.NoError(t, err)
	assert.True(t, unknown.IsError, "an unknown argument is rejected, not ignored")
}

func TestTheHandlerServesMCPAndHealth(t *testing.T) {
	srv := httptest.NewServer(server.Handler(fixture()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	resp, err = http.Get(srv.URL + "/other")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_alerts"})
	require.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "RequestErrorRate")
}

// Every tool must answer over MCP, not only when called directly: the SDK
// checks each answer against the schema it derives from the output type.
func TestEveryToolAnswersOverMCP(t *testing.T) {
	cs := connect(t)
	for tool, args := range map[string]map[string]any{
		"list_alerts":         nil,
		"list_services":       nil,
		"get_changes":         nil,
		"get_events":          nil,
		"query_logs":          {"service": "shipping"},
		"get_error_spans":     nil,
		"get_service_metrics": {"service": "shipping"},
		"get_pod_metrics":     {"service": "shipping"},
		"list_objects":        {"kind": "Pod"},
		"get_object":          {"kind": "Deployment", "name": "shipping"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err, tool)
		assert.False(t, res.IsError, tool)
	}
}

// Agents see the server's name and instructions. Neither may say that the
// data comes from a benchmark.
func TestTheServerDoesNotNameTheBenchmark(t *testing.T) {
	cs := connect(t)

	init := cs.InitializeResult()
	for _, text := range []string{init.ServerInfo.Name, init.Instructions} {
		assert.NotContains(t, strings.ToLower(text), "bench", text)
	}
}

func TestHandlersServeEachDatasetAtItsOwnPath(t *testing.T) {
	other := fixture()
	other.Meta.Namespace = "elsewhere"
	srv := httptest.NewServer(server.Handlers(map[string]*dataset.Dataset{"S1": fixture(), "S2": other}))
	t.Cleanup(srv.Close)

	for id, namespace := range map[string]string{"S1": "otel-demo", "S2": "elsewhere"} {
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
			Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/" + id + "/mcp"}, nil)
		require.NoError(t, err, id)
		assert.Contains(t, cs.InitializeResult().Instructions, "namespace "+namespace, id)
		require.NoError(t, cs.Close())
	}
	for path, status := range map[string]int{"/healthz": http.StatusOK, "/S3/mcp": http.StatusNotFound, "/mcp": http.StatusNotFound} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		assert.Equal(t, status, resp.StatusCode, path)
		require.NoError(t, resp.Body.Close())
	}
}

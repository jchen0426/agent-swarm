package mcpgw_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/spike/runtime/mcpgw"
)

func connect(t *testing.T, url string, onChange func()) *mcp.ClientSession {
	t.Helper()
	opts := &mcp.ClientOptions{}
	if onChange != nil {
		opts.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) { onChange() }
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, opts)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url + "/mcp"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func names(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var out []string
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}
	slices.Sort(out)
	return out
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	return text.Text
}

func TestToolsFollowTheRound(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)

	gw.SetRound(1, "get_alert", "end_turn")
	assert.Equal(t, []string{"end_turn", "get_alert"}, names(t, connect(t, srv.URL, nil)))

	gw.SetRound(2, "query_logs", "end_turn")
	assert.Equal(t, []string{"end_turn", "query_logs"}, names(t, connect(t, srv.URL, nil)))
}

func TestCallsAreRecordedWithTheirRound(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{Outputs: map[string]string{"get_alert": "checkout error rate 12%"}})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	gw.SetRound(3, "get_alert")

	out := call(t, connect(t, srv.URL, nil), "get_alert", map[string]any{"id": "a1"})

	assert.Equal(t, "checkout error rate 12%", out)
	calls := gw.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, 3, calls[0].Round)
	assert.Equal(t, "get_alert", calls[0].Tool)
	assert.JSONEq(t, `{"id":"a1"}`, string(calls[0].Args))
}

func TestBoardToolsAnswerRecorded(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	gw.SetRound(1, "post_hypothesis", "end_turn")
	cs := connect(t, srv.URL, nil)

	assert.Equal(t, "recorded", call(t, cs, "post_hypothesis", map[string]any{"key": "bad-release/cart"}))
	assert.Equal(t, "recorded", call(t, cs, "end_turn", nil))
}

func TestAToolAppearsAfterTheFirstCallOfRoundOne(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{AddAfterFirstCall: "probe_extra"})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	gw.SetRound(1, "get_alert", "end_turn")
	changed := make(chan struct{}, 1)
	cs := connect(t, srv.URL, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	assert.NotContains(t, names(t, cs), "probe_extra")

	call(t, cs, "get_alert", nil)

	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "no tools/list_changed notification")
	}
	assert.Contains(t, names(t, cs), "probe_extra")
}

func TestDelaysSlowATool(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{Delays: map[string]time.Duration{"slow_read": 300 * time.Millisecond}})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	gw.SetRound(1, "slow_read")
	cs := connect(t, srv.URL, nil)

	start := time.Now()
	call(t, cs, "slow_read", nil)
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond)
}

// The sandbox can reach the stand-in, so it offers no way to change a round's
// tools or to read the calls: rounds change only through SetRound, which the
// suite drives from outside the sandbox.
func TestTheGatewayHasNoControlEndpoints(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	gw.SetRound(1, "get_alert", "end_turn")

	round, err := http.Post(srv.URL+"/admin/round?n=2&tools=probe_extra", "text/plain", nil)
	require.NoError(t, err)
	require.NoError(t, round.Body.Close())
	calls, err := http.Get(srv.URL + "/admin/calls")
	require.NoError(t, err)
	require.NoError(t, calls.Body.Close())

	assert.Equal(t, http.StatusNotFound, round.StatusCode)
	assert.Equal(t, http.StatusNotFound, calls.StatusCode)
	assert.Equal(t, []string{"end_turn", "get_alert"}, names(t, connect(t, srv.URL, nil)))
}

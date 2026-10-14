package fakemcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/fakemcp"
)

var object = json.RawMessage(`{"type":"object"}`)

// bearer adds an Authorization header to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, url string, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, opts)
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: url, MaxRetries: -1, HTTPClient: &http.Client{Transport: bearer{"t0ken"}},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestTheServerRecordsEachCallWithItsAuthorization(t *testing.T) {
	s := fakemcp.Start(t, "flags")
	s.Add(&mcp.Tool{Name: "get_flag", InputSchema: object}, fakemcp.Text("on"))
	var seen []fakemcp.Call
	s.OnCall(func(c fakemcp.Call) { seen = append(seen, c) })
	cs := connect(t, s.URL, nil)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_flag", Arguments: json.RawMessage(`{"flag":"x"}`)})

	require.NoError(t, err)
	assert.Equal(t, "on", res.Content[0].(*mcp.TextContent).Text)
	want := []fakemcp.Call{{Tool: "get_flag", Args: json.RawMessage(`{"flag":"x"}`), Authorization: "Bearer t0ken", Session: cs.ID()}}
	assert.Equal(t, want, s.Calls())
	assert.Equal(t, want, seen)
}

func TestADroppedCallFailsWithoutAnAnswer(t *testing.T) {
	s := fakemcp.Start(t, "flags")
	s.Add(&mcp.Tool{Name: "get_flag", InputSchema: object}, fakemcp.Text("on"))
	cs := connect(t, s.URL, nil)
	s.DropNext(1)

	_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_flag", Arguments: object})

	require.Error(t, err)
	assert.Empty(t, s.Calls())
}

func TestASlowToolAnswersLate(t *testing.T) {
	s := fakemcp.Start(t, "flags")
	s.Add(&mcp.Tool{Name: "get_flag", InputSchema: object}, fakemcp.Text("on"))
	s.Slow("get_flag", time.Second)
	cs := connect(t, s.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_flag", Arguments: object})

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestAChangedToolListIsAnnounced(t *testing.T) {
	s := fakemcp.Start(t, "flags")
	s.Add(&mcp.Tool{Name: "get_flag", InputSchema: object}, fakemcp.Text("on"))
	changed := make(chan struct{}, 1)
	cs := connect(t, s.URL, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	})
	_, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)

	s.Add(&mcp.Tool{Name: "get_flag", Description: "Changed.", InputSchema: object}, fakemcp.Text("on"))

	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no tools/list_changed notification")
	}
	list, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, "Changed.", list.Tools[0].Description)
}

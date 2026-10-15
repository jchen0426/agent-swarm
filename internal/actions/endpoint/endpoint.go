// Package endpoint is the MCP gateway's agent-facing endpoint (spec Section
// 6.1), the only MCP endpoint agents see. It speaks streamable HTTP without
// sessions, so every request stands alone and carries the session's bearer
// token: the token names the run and the agent, and nothing in a request's
// content does. It answers initialize, ping, tools/list and tools/call, and
// refuses everything else. Tool lists and calls come from the gateway, so an
// attempt on a tool the agent cannot see is the gateway's to refuse and
// record.
package endpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
)

// MaxBodyBytes caps one request.
const MaxBodyBytes = 128 << 10

// Gateway is what the endpoint serves.
type Gateway interface {
	Authenticate(ctx context.Context, token string) error
	Tools(ctx context.Context, token string) ([]app.Tool, error)
	Call(ctx context.Context, token, name string, args json.RawMessage) (*app.Response, error)
}

// errSession answers a request whose session ended while it was served.
var errSession = &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "the session has ended"}

// New returns the endpoint's HTTP handler.
func New(gw Gateway) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "agentswarm-gateway", Version: "1"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	server.AddReceivingMiddleware(answer(gw))
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: MaxBodyBytes,
	})
	return authenticate(gw, h)
}

// authenticate lets through only requests with a live session's token, of
// at most MaxBodyBytes, that are not JSON-RPC batches.
func authenticate(gw Gateway, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" || gw.Authenticate(r.Context(), token) != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "a valid gateway token is required", http.StatusUnauthorized)
			return
		}
		if r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
			switch {
			case err != nil:
				http.Error(w, "the request could not be read", http.StatusBadRequest)
				return
			case len(body) > MaxBodyBytes:
				http.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
				return
			case bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("[")):
				http.Error(w, "batches are not accepted", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	})
}

// answer answers tools/list and tools/call from the gateway, lets the
// protocol's own initialize and ping through, ignores notifications, and
// refuses every other method.
func answer(gw Gateway) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "initialize", "ping":
				return next(ctx, method, req)
			case "tools/list":
				tools, err := gw.Tools(ctx, token(req))
				if err != nil {
					return nil, errSession
				}
				return listed(tools), nil
			case "tools/call":
				p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
				if !ok {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "a call names a tool"}
				}
				resp, err := gw.Call(ctx, token(req), p.Name, p.Arguments)
				if err != nil {
					return nil, errSession
				}
				return called(resp), nil
			}
			if strings.HasPrefix(method, "notifications/") {
				return nil, nil
			}
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "the gateway answers only tools/list and tools/call"}
		}
	}
}

// token returns the bearer token of a request the HTTP layer authenticated.
func token(req mcp.Request) string {
	extra := req.GetExtra()
	if extra == nil || extra.Header == nil {
		return ""
	}
	t, _ := strings.CutPrefix(extra.Header.Get("Authorization"), "Bearer ")
	return t
}

// listed builds the tool list from the gateway's tools: the verified
// definition's fields, under the agent's name for each tool.
func listed(tools []app.Tool) *mcp.ListToolsResult {
	res := &mcp.ListToolsResult{Tools: []*mcp.Tool{}}
	for i := range tools {
		t := &tools[i]
		tool := &mcp.Tool{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: t.InputSchema}
		if len(t.Annotations) > 0 {
			var a mcp.ToolAnnotations
			if json.Unmarshal(t.Annotations, &a) == nil {
				tool.Annotations = &a
			}
		}
		res.Tools = append(res.Tools, tool)
	}
	return res
}

// called builds a call's result: first a block that names the call, says
// where the output came from and what was left out, which is never stored and
// cannot be quoted; then the output itself, as stored. A board tool's call
// has no call id, and its source is the board.
func called(resp *app.Response) *mcp.CallToolResult {
	var head []string
	if resp.CallID != "" {
		head = append(head, "call_id: "+resp.CallID)
	}
	if resp.Source != "" {
		head = append(head, "source: "+resp.Source)
	}
	switch resp.Status {
	case app.StatusRefused:
		head = append(head, "refused: this call was not made")
	case app.StatusReadError:
		head = append(head, "read error: this output cannot be cited")
	case app.StatusRecorded:
		head = append(head, "recorded: a proposal for a person; nothing was sent to the server")
	}
	for _, n := range resp.Notes {
		head = append(head, "note: "+n)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(head, "\n")}, &mcp.TextContent{Text: resp.Text}},
		IsError: resp.Status == app.StatusRefused || resp.Status == app.StatusReadError,
	}
}

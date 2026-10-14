// Package mcptools reaches MCP servers the way the whole platform does, and
// lists and hashes their tools the way a registration pins them (spec
// Section 3.3). The registry command and the MCP gateway use the same client
// and the same hash, so a server answers both alike and a tool hashes alike
// in both.
package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// Limits on what a server may list.
const (
	MaxPages = 50
	MaxTools = 1000
	// MaxEventBytes caps one message a server streams.
	MaxEventBytes = 8 << 20
)

// errRefused answers a request a server sends to the platform's client.
var errRefused = errors.New("mcptools: the platform answers no requests from servers")

// Client returns the MCP client the platform reaches servers with. It
// advertises no capabilities, so a server can ask it for nothing: no roots,
// no sampling, no elicitation. It answers a server's ping and refuses every
// other request a server sends, it ignores notifications, and it never
// repeats a request because a server asks it to.
func Client() *mcp.Client {
	c := mcp.NewClient(&mcp.Implementation{Name: "agentswarm", Version: "1"}, &mcp.ClientOptions{
		Capabilities:   &mcp.ClientCapabilities{},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	c.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch {
			case method == "ping":
				return next(ctx, method, req)
			case strings.HasPrefix(method, "notifications/"):
				return nil, nil
			default:
				return nil, errRefused
			}
		}
	})
	return c
}

// Transport returns the streamable HTTP transport to endpoint through
// httpClient: it never retries on its own, opens no stream for messages the
// server would send unasked, and takes events of at most MaxEventBytes.
func Transport(endpoint string, httpClient *http.Client) *mcp.StreamableClientTransport {
	return &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: httpClient, MaxRetries: -1,
		DisableStandaloneSSE: true, MaxEventSize: MaxEventBytes,
	}
}

// NoRedirects is an HTTP client that follows no redirect.
func NoRedirects(base http.RoundTripper) *http.Client {
	return &http.Client{
		Transport:     base,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Listed is one tool a server lists: its definition as the client decodes it,
// or why it has none.
type Listed struct {
	Def config.ToolDefinition
	Err error
}

// Offered lists the tools on session, by name, in at most MaxPages pages and
// MaxTools tools. A tool listed twice, or whose definition cannot be read,
// has an error, and the listing goes on, so that one such tool cannot hide a
// change to another. A page the MCP client cannot decode at all, such as one
// whose description is a number, fails the whole listing.
func Offered(ctx context.Context, session *mcp.ClientSession) (map[string]Listed, error) {
	out := map[string]Listed{}
	n, cursor := 0, ""
	for page := 0; ; page++ {
		if page == MaxPages {
			return nil, fmt.Errorf("mcptools: the server lists more than %d pages of tools", MaxPages)
		}
		res, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcptools: list tools: %w", err)
		}
		for _, t := range res.Tools {
			n++
			if n > MaxTools {
				return nil, fmt.Errorf("mcptools: the server lists more than %d tools", MaxTools)
			}
			def, err := Definition(t)
			if _, twice := out[t.Name]; twice {
				def, err = config.ToolDefinition{Name: t.Name}, errors.New("is listed twice")
			}
			out[t.Name] = Listed{Def: def, Err: err}
		}
		if res.NextCursor == "" {
			return out, nil
		}
		cursor = res.NextCursor
	}
}

// Tool is one listed tool: the definition a registration pins, as a reviewer
// reads it, and its hash. SchemaHash is config.ToolSchemaHash of exactly
// these fields, so a reviewer pins only what they have read.
type Tool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	SchemaHash   string          `json:"schemaHash,omitempty"`
	// Invisible names the fields that hold characters a reader could miss
	// (config.Hidden), decoded. A model reads them; a reviewer must look
	// closely.
	Invisible []string `json:"invisible,omitempty"`
	// Error says why the tool has no hash. A registration cannot pin it.
	Error string `json:"error,omitempty"`
}

// Hidden reports whether a reader could miss r: config.Hidden.
func Hidden(r rune) bool { return config.Hidden(r) }

// Definition returns a tool as the MCP client decodes it: its name, title,
// description, schemas and annotations. The annotations take a fixed shape,
// every field present and an unset hint null, so that the definition does
// not follow the SDK's choice of which fields to omit. What the gateway lists
// to an agent must be built from this definition, never from the server's
// raw bytes, so that the agent sees what was hashed.
func Definition(t *mcp.Tool) (config.ToolDefinition, error) {
	in, err := json.Marshal(t.InputSchema)
	if err != nil {
		return config.ToolDefinition{}, fmt.Errorf("mcptools: tool %s: input schema: %w", t.Name, err)
	}
	def := config.ToolDefinition{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: in}
	if t.OutputSchema != nil {
		if def.OutputSchema, err = json.Marshal(t.OutputSchema); err != nil {
			return config.ToolDefinition{}, fmt.Errorf("mcptools: tool %s: output schema: %w", t.Name, err)
		}
	}
	if a := t.Annotations; a != nil {
		if def.Annotations, err = json.Marshal(map[string]any{
			"title": a.Title, "readOnlyHint": a.ReadOnlyHint, "destructiveHint": a.DestructiveHint,
			"idempotentHint": a.IdempotentHint, "openWorldHint": a.OpenWorldHint,
		}); err != nil {
			return config.ToolDefinition{}, fmt.Errorf("mcptools: tool %s: annotations: %w", t.Name, err)
		}
	}
	return def, nil
}

// SchemaHash returns the hash a registration pins for t: config.ToolSchemaHash
// over its Definition.
func SchemaHash(t *mcp.Tool) (string, error) {
	def, err := Definition(t)
	if err != nil {
		return "", err
	}
	return config.ToolSchemaHash(&def)
}

// List connects to the server at endpoint over streamable HTTP, as the
// gateway does, and returns its tools sorted by name: each tool's whole
// definition and the schema hash that covers it, or why it has none. The
// annotations' hints, such as read-only, are shown, not trusted: a
// registration sets each tool's effect itself.
func List(ctx context.Context, endpoint string) ([]Tool, error) {
	session, err := Client().Connect(ctx, Transport(endpoint, NoRedirects(http.DefaultTransport)), nil)
	if err != nil {
		return nil, fmt.Errorf("mcptools: connect to %s: %w", endpoint, err)
	}
	defer func() { _ = session.Close() }()
	offered, err := Offered(ctx, session)
	if err != nil {
		return nil, err
	}
	tools := make([]Tool, 0, len(offered))
	for name := range offered {
		if err := offered[name].Err; err != nil {
			tools = append(tools, Tool{Name: name, Error: err.Error()})
			continue
		}
		def := offered[name].Def
		h, err := config.ToolSchemaHash(&def)
		t := Tool{
			Name: def.Name, Title: def.Title, Description: def.Description, InputSchema: def.InputSchema,
			OutputSchema: def.OutputSchema, Annotations: def.Annotations, Invisible: def.HiddenFields(),
		}
		if err != nil {
			t.Error = err.Error()
		} else {
			t.SchemaHash = h
		}
		tools = append(tools, t)
	}
	slices.SortFunc(tools, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })
	return tools, nil
}

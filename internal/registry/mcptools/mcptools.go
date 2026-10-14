// Package mcptools lists an MCP server's tools and hashes them the way a
// registration pins them (spec Section 3.3). The registry command and the MCP
// gateway use the same hash, so a tool hashes alike in both.
package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// Tool is one listed tool: the definition a registration pins, as a reviewer
// reads it, and its hash. SchemaHash is config.ToolSchemaHash of exactly
// these fields, so a reviewer pins only what they have read.
type Tool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	SchemaHash   string          `json:"schemaHash"`
}

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

// List connects to the server at endpoint over streamable HTTP and returns
// its tools, sorted by name: each tool's whole definition and the schema hash
// that covers it. The annotations' hints, such as read-only, are shown, not
// trusted: a registration sets each tool's effect itself.
func List(ctx context.Context, endpoint string) ([]Tool, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "agentswarm-registry", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, MaxRetries: -1}, nil)
	if err != nil {
		return nil, fmt.Errorf("mcptools: connect to %s: %w", endpoint, err)
	}
	defer func() { _ = session.Close() }()
	var tools []Tool
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("mcptools: list the tools of %s: %w", endpoint, err)
		}
		def, err := Definition(t)
		if err != nil {
			return nil, err
		}
		h, err := config.ToolSchemaHash(&def)
		if err != nil {
			return nil, err
		}
		tools = append(tools, Tool{
			Name: def.Name, Title: def.Title, Description: def.Description, InputSchema: def.InputSchema,
			OutputSchema: def.OutputSchema, Annotations: def.Annotations, SchemaHash: h,
		})
	}
	slices.SortFunc(tools, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })
	return tools, nil
}

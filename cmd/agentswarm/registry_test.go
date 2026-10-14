package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

func TestHashToolsPrintsEachToolsSchemaHash(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "flags", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "get_flag", Description: "A flag's state.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	t.Cleanup(srv.Close)

	stdout, errOut, code := runCommand("registry", "hash-tools", "--endpoint", srv.URL)

	require.Equal(t, exitOK, code, errOut)
	var tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		SchemaHash  string `json:"schemaHash"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &tools))
	require.Len(t, tools, 1)
	want, err := config.ToolSchemaHash(&config.ToolDefinition{Name: "get_flag", Description: "A flag's state.", InputSchema: []byte(`{"type":"object"}`)})
	require.NoError(t, err)
	assert.Equal(t, want, tools[0].SchemaHash)
}

func TestRegistryNeedsASubcommand(t *testing.T) {
	_, errOut, code := runCommand("registry")

	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "a command is required")
}

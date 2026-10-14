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

func TestHashToolsShowsHiddenCharactersEscapedAndFlagged(t *testing.T) {
	hidden := "A flag's state.\U000E0049\u200b\u202e"
	s := mcp.NewServer(&mcp.Implementation{Name: "flags", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "get_flag", Description: hidden, InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	t.Cleanup(srv.Close)

	stdout, errOut, code := runCommand("registry", "hash-tools", "--endpoint", srv.URL)

	require.Equal(t, exitOK, code, errOut)
	for _, r := range []string{"\U000E0049", "\u200b", "\u202e"} {
		assert.NotContains(t, stdout, r, "a hidden character is never printed as itself")
	}
	assert.Contains(t, stdout, `\udb40\udc49\u200b\u202e`)
	var tools []struct {
		Description string   `json:"description"`
		Invisible   []string `json:"invisible"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &tools))
	require.Len(t, tools, 1)
	assert.Equal(t, hidden, tools[0].Description, "the escapes decode to the text the hash covers")
	assert.Equal(t, []string{"description"}, tools[0].Invisible)
}

package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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

// writeCA writes cert as a PEM file in a temporary directory, as a deployment
// keeps its CA bundle, and returns the file's path.
func writeCA(t *testing.T, cert *x509.Certificate) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600))
	return path
}

func TestHashToolsTakesACABundle(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "flags", Version: "1"}, nil)
	for _, name := range []string{"get_flag", "set_flag"} {
		s.AddTool(&mcp.Tool{Name: name, Description: "A flag.", InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
	}
	srv := httptest.NewUnstartedServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the command that refuses the certificate is what the test provokes
	srv.StartTLS()
	t.Cleanup(srv.Close)

	_, errOut, code := runCommand("registry", "hash-tools", "--endpoint", srv.URL)

	assert.Equal(t, exitError, code, "no root vouches for the server's certificate")
	assert.Contains(t, errOut, "certificate signed by unknown authority")

	stdout, errOut, code := runCommand("registry", "hash-tools", "--endpoint", srv.URL, "--ca", writeCA(t, srv.Certificate()))

	require.Equal(t, exitOK, code, errOut)
	var tools []struct {
		Name       string `json:"name"`
		SchemaHash string `json:"schemaHash"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &tools))
	require.Len(t, tools, 2)
	for i, name := range []string{"get_flag", "set_flag"} {
		assert.Equal(t, name, tools[i].Name)
		assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, tools[i].SchemaHash)
	}
}

// A bundle that cannot be used must not leave the command trusting the system
// roots alone without a word.
func TestHashToolsRefusesACABundleItCannotUse(t *testing.T) {
	notPEM := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(notPEM, []byte("not a certificate\n"), 0o600))
	for name, ca := range map[string]string{
		"a file that is not there":     filepath.Join(t.TempDir(), "missing.pem"),
		"a file without a certificate": notPEM,
	} {
		t.Run(name, func(t *testing.T) {
			_, errOut, code := runCommand("registry", "hash-tools", "--endpoint", "https://127.0.0.1:1/mcp", "--ca", ca)

			assert.Equal(t, exitError, code)
			assert.Contains(t, errOut, "CA bundle")
			assert.NotContains(t, errOut, "could not be listed", "the server is not asked")
		})
	}
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

// A server's error reaches the reviewer's terminal. Control characters in it
// could erase the line and draw a listing the server made up, whose hash the
// reviewer would then pin, so they are shown as escapes.
func TestAServersErrorCannotDrawOnTheTerminal(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "flags", Version: "1"}, nil)
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				return nil, &jsonrpc.Error{Code: -32000, Message: "\r\x1b[2K[{\"name \":\"get_flag\"}]\x1b[8m\u202e"}
			}
			return next(ctx, method, req)
		}
	})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	t.Cleanup(srv.Close)

	_, errOut, code := runCommand("registry", "hash-tools", "--endpoint", srv.URL)

	assert.Equal(t, exitError, code)
	assert.NotContains(t, errOut, "\x1b")
	assert.NotContains(t, errOut, "\r")
	assert.NotContains(t, errOut, "\u202e")
	assert.Contains(t, errOut, "could not be listed")
}

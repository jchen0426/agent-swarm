package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

func TestAToolSchemaHashIgnoresKeyOrderAndWhiteSpace(t *testing.T) {
	a, err := config.ToolSchemaHash("get_release", "Current release of a service.",
		[]byte(`{"type":"object","properties":{"service":{"type":"string"}},"required":["service"]}`), nil)
	require.NoError(t, err)
	b, err := config.ToolSchemaHash("get_release", "Current release of a service.",
		[]byte("{\n  \"required\": [\"service\"],\n  \"properties\": {\"service\": {\"type\": \"string\"}},\n  \"type\": \"object\"\n}"), []byte("  "))
	require.NoError(t, err)

	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, a)
	assert.Equal(t, a, b)
}

func TestAToolSchemaHashChangesWithAnyPart(t *testing.T) {
	in := []byte(`{"type":"object","properties":{"limit":{"type":"integer","maximum":100}}}`)
	base, err := config.ToolSchemaHash("query", "Run a query.", in, nil)
	require.NoError(t, err)

	for name, args := range map[string][4]string{
		"name":          {"query2", "Run a query.", string(in), ""},
		"description":   {"query", "Run any query.", string(in), ""},
		"input schema":  {"query", "Run a query.", `{"type":"object","properties":{"limit":{"type":"integer","maximum":1000}}}`, ""},
		"output schema": {"query", "Run a query.", string(in), `{"type":"object"}`},
		"number text":   {"query", "Run a query.", `{"type":"object","properties":{"limit":{"type":"integer","maximum":100.0}}}`, ""},
	} {
		h, err := config.ToolSchemaHash(args[0], args[1], []byte(args[2]), []byte(args[3]))
		require.NoError(t, err)
		assert.NotEqual(t, base, h, name)
	}
}

func TestAToolSchemaHashNeedsValidJSON(t *testing.T) {
	_, err := config.ToolSchemaHash("q", "", []byte(`{"type":`), nil)
	require.ErrorContains(t, err, "input schema")
	_, err = config.ToolSchemaHash("q", "", []byte(`{}`), []byte(`{} {}`))
	require.ErrorContains(t, err, "output schema")
}

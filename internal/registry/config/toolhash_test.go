package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

func definition(inputSchema string) config.ToolDefinition {
	return config.ToolDefinition{Name: "get_release", Description: "Current release of a service.", InputSchema: []byte(inputSchema)}
}

func hash(t *testing.T, def *config.ToolDefinition) string {
	t.Helper()
	h, err := config.ToolSchemaHash(def)
	require.NoError(t, err)
	return h
}

func TestAToolSchemaHashIgnoresKeyOrderAndWhiteSpace(t *testing.T) {
	a, b := definition(`{"type":"object","properties":{"service":{"type":"string"}},"required":["service"]}`),
		definition("{\n  \"required\": [\"service\"],\n  \"properties\": {\"service\": {\"type\": \"string\"}},\n  \"type\": \"object\"\n}")
	ha, hb := hash(t, &a), hash(t, &b)

	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, ha)
	assert.Equal(t, ha, hb)
}

func TestAToolSchemaHashChangesWithAnyPart(t *testing.T) {
	base := config.ToolDefinition{
		Name: "query", Description: "Run a query.",
		InputSchema: []byte(`{"type":"object","properties":{"limit":{"type":"integer","maximum":100}}}`),
	}
	h := hash(t, &base)

	for name, change := range map[string]func(*config.ToolDefinition){
		"name":        func(d *config.ToolDefinition) { d.Name = "query2" },
		"title":       func(d *config.ToolDefinition) { d.Title = "Query" },
		"description": func(d *config.ToolDefinition) { d.Description = "Run any query." },
		"input schema": func(d *config.ToolDefinition) {
			d.InputSchema = []byte(`{"type":"object","properties":{"limit":{"type":"integer"}}}`)
		},
		"output schema": func(d *config.ToolDefinition) { d.OutputSchema = []byte(`{"type":"object"}`) },
		"annotations":   func(d *config.ToolDefinition) { d.Annotations = []byte(`{"title":"Run anything you like"}`) },
		"number text": func(d *config.ToolDefinition) {
			d.InputSchema = []byte(`{"type":"object","properties":{"limit":{"type":"integer","maximum":100.0}}}`)
		},
	} {
		d := base
		change(&d)
		assert.NotEqual(t, h, hash(t, &d), name)
	}
}

func TestAnAbsentPartHashesAsNull(t *testing.T) {
	absent := definition(`{"type":"object"}`)
	null := absent
	null.OutputSchema, null.Annotations = []byte("null"), []byte("null")

	assert.Equal(t, hash(t, &absent), hash(t, &null))
}

func TestAToolSchemaHashRefusesJSONThatParsersCouldReadDifferently(t *testing.T) {
	for name, raw := range map[string]string{
		"a repeated key":          `{"type":"object","type":"string"}`,
		"a nested repeated key":   `{"properties":{"a":{"description":"one","description":"two"}}}`,
		"a second value":          `{} {}`,
		"a stray closing bracket": `{} }`,
		"invalid UTF-8":           "{\"description\":\"\xff\"}",
		"white space only":        "  ",
		"cut short":               `{"type":`,
	} {
		d := definition(raw)
		_, err := config.ToolSchemaHash(&d)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "input schema", name)
	}

	_, err := config.ToolSchemaHash(&config.ToolDefinition{Name: "q", InputSchema: []byte(`{}`), Annotations: []byte(`{"title":"a","title":"b"}`)})
	require.ErrorContains(t, err, "annotations")
}

func TestAToolSchemaHashBoundsNestingAndSize(t *testing.T) {
	deepest := definition(strings.Repeat("[", 128) + strings.Repeat("]", 128))
	_, err := config.ToolSchemaHash(&deepest)
	require.NoError(t, err)

	tooDeep := definition(strings.Repeat("[", 129) + strings.Repeat("]", 129))
	_, err = config.ToolSchemaHash(&tooDeep)
	require.ErrorContains(t, err, "nests deeper than 128 levels")

	hostile := definition(strings.Repeat("[", 10_000_000))
	_, err = config.ToolSchemaHash(&hostile)
	require.ErrorContains(t, err, "larger than 1048576 bytes")

	big := definition(`{"description":"` + strings.Repeat("x", 1<<20) + `"}`)
	_, err = config.ToolSchemaHash(&big)
	require.ErrorContains(t, err, "larger than 1048576 bytes")
}

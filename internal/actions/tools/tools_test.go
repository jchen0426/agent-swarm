package tools_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/tools"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

const schema = `{"type":"object","properties":{"service":{"type":"string"},"window":{"type":"string"},"limit":{"type":"integer"},"ratio":{"type":"number"},"filter":{"type":"object"}},"required":["service"]}`

func compile(t *testing.T, raw string) *tools.Schema {
	t.Helper()
	s, err := tools.CompileSchema([]byte(raw))
	require.NoError(t, err)
	return s
}

func read(t *testing.T, raw string) *tools.Args {
	t.Helper()
	a, err := compile(t, schema).Read(json.RawMessage(raw), 1024)
	require.NoError(t, err, raw)
	return a
}

func refused(t *testing.T, raw, why string) {
	t.Helper()
	_, err := compile(t, schema).Read(json.RawMessage(raw), 1024)
	require.ErrorIs(t, err, tools.ErrArguments, raw)
	assert.ErrorContains(t, err, why, raw)
}

func TestTheArgumentsThatAreCheckedAreTheBytesThatAreSent(t *testing.T) {
	a := read(t, "{ \"service\" : \"cart\", \"limit\": 1.50e1, \"filter\": {\"b\": 1, \"a\": \"<x>\"} }")

	if got := string(a.Raw); got != `{"filter":{"a":"<x>","b":1},"limit":1.50e1,"service":"cart"}` {
		t.Errorf("the canonical arguments are %s", got)
	}
}

func TestAbsentArgumentsAreTheEmptyObject(t *testing.T) {
	s := compile(t, `{"type":"object"}`)
	for _, raw := range []string{"", "null", " {} "} {
		a, err := s.Read(json.RawMessage(raw), 1024)
		require.NoError(t, err)
		assert.Equal(t, "{}", string(a.Raw))
	}
}

// Parsers disagree on repeated keys and on keys in two cases, so a server
// could read a call that the gateway never checked.
func TestArgumentsThatParsersCouldReadDifferentlyAreRefused(t *testing.T) {
	refused(t, `{"service":"cart","service":"admin"}`, "appears twice")
	refused(t, `{"service":"cart","Service":"admin"}`, "appears twice, or in two cases")
	refused(t, `{"service":"cart","filter":{"k":1,"K":2}}`, "in two cases")
	refused(t, "{\"service\":\"cart\\u0000\"}", "NUL")
	refused(t, `{"service":"cart"} {"service":"admin"}`, "more than one JSON value")
	refused(t, `{"service":`, "not JSON")
	refused(t, `["cart"]`, "must be a JSON object")
	refused(t, `{"service":"`+strings.Repeat("x", 1024)+`"}`, "larger than 1024 bytes")
	refused(t, `{"service":"cart","filter":`+strings.Repeat(`{"a":`, 40)+`1`+strings.Repeat(`}`, 41), "nest deeper")
}

func TestOnlyDeclaredArgumentsPass(t *testing.T) {
	refused(t, `{"service":"cart","since":"30d"}`, `"since" is not an argument of this tool`)
}

func TestThePinnedSchemaChecksTheArguments(t *testing.T) {
	refused(t, `{"service":3}`, "")
	refused(t, `{"limit":5}`, "")
	refused(t, `{"service":"cart","limit":2.5}`, "")
}

func TestASchemaTheGatewayCannotCheckIsRefused(t *testing.T) {
	for raw, why := range map[string]string{
		`{"type":"object","properties":{"a":{"$ref":"https://schemas.example.com/a.json"}}}`: "",
		`{"type":"array"}`: `root must have type "object"`,
		`{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"}}}`: "HTTP headers",
	} {
		_, err := tools.CompileSchema([]byte(raw))
		require.Error(t, err, raw)
		assert.ErrorContains(t, err, why)
	}
}

func TestEveryBoundedArgumentIsPresentPlainAndWithinItsCap(t *testing.T) {
	s := compile(t, schema)
	bounds := map[string]config.Bound{"window": {Max: "6h"}, "limit": {Max: "1000"}, "ratio": {Max: "0.5"}}
	ok := func(raw string) *tools.Args {
		a, err := s.Read(json.RawMessage(raw), 1024)
		require.NoError(t, err, raw)
		return a
	}
	require.NoError(t, s.CheckBounds(ok(`{"service":"x","window":"6h","limit":1000,"ratio":0.5}`), bounds))
	require.NoError(t, s.CheckBounds(ok(`{"service":"x","window":"90m","limit":1,"ratio":0.25}`), bounds))

	for _, raw := range []string{
		`{"service":"x","limit":10,"ratio":0.1}`,
		`{"service":"x","window":"7h","limit":10,"ratio":0.1}`,
		`{"service":"x","window":"0s","limit":10,"ratio":0.1}`,
		`{"service":"x","window":"0.9999h","limit":10,"ratio":0.1}`,
		`{"service":"x","window":"1h1h1h","limit":10,"ratio":0.1}`,
		`{"service":"x","window":"+6h","limit":10,"ratio":0.1}`,
		`{"service":"x","window":"1d","limit":10,"ratio":0.1}`,
		`{"service":"x","window":"6h","ratio":0.1}`,
		`{"service":"x","window":"6h","limit":1001,"ratio":0.1}`,
		`{"service":"x","window":"6h","limit":0,"ratio":0.1}`,
		`{"service":"x","window":"6h","limit":1e2,"ratio":0.1}`,
		`{"service":"x","window":"6h","limit":10,"ratio":0.6}`,
		`{"service":"x","window":"6h","limit":10,"ratio":-0.1}`,
	} {
		a, err := s.Read(json.RawMessage(raw), 1024)
		if err != nil {
			continue // refused before the bounds, which is as good
		}
		require.ErrorIs(t, s.CheckBounds(a, bounds), tools.ErrArguments, raw)
	}
}

func TestABoundMustNameADeclaredArgument(t *testing.T) {
	s := compile(t, schema)

	assert.Empty(t, s.MissingBounds(map[string]config.Bound{"window": {Max: "6h"}}))
	assert.Equal(t, []string{"range"}, s.MissingBounds(map[string]config.Bound{"range": {Max: "6h"}, "window": {Max: "6h"}}))
	allOf := compile(t, `{"type":"object","allOf":[{"properties":{"since":{"type":"string"}}}]}`)
	assert.Empty(t, allOf.MissingBounds(map[string]config.Bound{"since": {Max: "6h"}}))
}

func TestCoerceTurnsTextIntoTheDeclaredTypes(t *testing.T) {
	s := compile(t, `{"type":"object","properties":{"n":{"type":"integer"},"on":{"type":"boolean"},"s":{"type":"string"}}}`)

	got := s.Coerce(map[string]string{"n": "42", "on": "true", "s": "42", "x": "y"})

	assert.Equal(t, map[string]any{"n": json.Number("42"), "on": true, "s": "42", "x": "y"}, got)
}

func TestHiddenCharactersAnywhereInADefinitionAreFound(t *testing.T) {
	plain := config.ToolDefinition{Name: "get", Description: "Reads a flag.\nTabs\tare fine.", InputSchema: []byte(`{"type":"object"}`)}
	assert.False(t, tools.HiddenCharacters(&plain))

	for _, def := range []config.ToolDefinition{
		{Name: "get", Description: "Reads a flag.\u200b"},
		{Name: "get", Title: "Flag\u202e"},
		{Name: "get", InputSchema: []byte(`{"description":"\udb40\udc49gnore"}`)},
		{Name: "get", InputSchema: []byte(`{"properties":{"a\u0007":{}}}`)},
		{Name: "get", Annotations: []byte(`{"title":"\u2066x"}`)},
	} {
		assert.True(t, tools.HiddenCharacters(&def), "%+v", def)
	}
}

// Telling keys apart in any case compares each key with the others, so an
// object's members are capped: within the size cap, a flood of tiny keys
// would cost a server's worth of time.
func TestAnObjectWithTooManyMembersIsRefused(t *testing.T) {
	s := compile(t, schema)
	var b strings.Builder
	b.WriteString(`{"service":"x","filter":{`)
	for i := range 1001 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"k` + strconv.Itoa(i) + `":0`)
	}
	b.WriteString(`}}`)

	_, err := s.Read(json.RawMessage(b.String()), 64<<10)

	require.ErrorIs(t, err, tools.ErrArguments)
	assert.ErrorContains(t, err, "more than 1000 members")
}

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

// A server that does not know a unit may read the number in another one:
// 9999999ms passes a 6h cap, and read as seconds it is 115 days. Durations
// therefore take only s, m and h, which every server knows.
func TestADurationTakesOnlyUnitsEveryServerReads(t *testing.T) {
	s := compile(t, schema)
	a, err := s.Read(json.RawMessage(`{"service":"x","window":"9999999ms"}`), 1024)
	require.NoError(t, err)

	require.ErrorIs(t, s.CheckBounds(a, map[string]config.Bound{"window": {Max: "6h"}}), tools.ErrArguments)
}

// Validation would follow a cycle of references that never steps into the
// value forever, and the stack overflow ends the whole process; such a
// schema is refused when it is compiled.
func TestAReferenceCycleIsRefused(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","allOf":[{"$ref":"#"}]}`,
		`{"type":"object","properties":{"a":{"$ref":"#/$defs/x"}},"$defs":{"x":{"$ref":"#/$defs/x"}}}`,
		`{"type":"object","properties":{"a":{"$ref":"#/$defs/x"}},"$defs":{"x":{"anyOf":[{"not":{"$ref":"#/$defs/y"}}]},"y":{"if":{"$ref":"#/%24defs/x"}}}}`,
		`{"type":"object","properties":{"a":{"type":"object","dependentSchemas":{"b":{"$ref":"#/properties/a"}}}}}`,
	} {
		_, err := tools.CompileSchema([]byte(raw))

		require.Error(t, err, raw)
	}
}

func TestReferencesThatCannotBeResolvedHereAreRefused(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","properties":{"a":{"$ref":"#a"}},"$defs":{"x":{"$anchor":"a"}}}`,
		`{"type":"object","properties":{"a":{"$dynamicRef":"#/$defs/x"}},"$defs":{"x":{"type":"string"}}}`,
		`{"type":"object","properties":{"a":{"$id":"https://example.com/a","$ref":"#"}}}`,
	} {
		_, err := tools.CompileSchema([]byte(raw))

		require.Error(t, err, raw)
	}
}

func TestARecursiveSchemaThatStepsIntoTheValueIsAccepted(t *testing.T) {
	s := compile(t, `{"type":"object","properties":{"tree":{"$ref":"#/$defs/node"}},"$defs":{"node":{"type":"object","properties":{"children":{"type":"array","items":{"$ref":"#/$defs/node"}}}}}}`)

	_, err := s.Read(json.RawMessage(`{"tree":{"children":[{"children":[]}]}}`), 1<<10)

	require.NoError(t, err)
}

// The validator reads keywords in any case, so the check follows references
// as the validator reads them, not as they are written.
func TestReferencesAreCheckedAsTheValidatorReadsThem(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","$REF":"#"}`,
		`{"type":"object","ALLOF":[{"$ref":"#"}]}`,
		`{"type":"object","allOf":[{"type":"object"}],"AllOf":[{"$ref":"#"}]}`,
		`{"type":"object","$defs":{"x":{"$Ref":"#/$defs/x"}},"allOf":[{"$ref":"#/$defs/x"}]}`,
		`{"type":"object","properties":{"a":{"$DynamicRef":"#/$defs/x"}},"$defs":{"x":{"type":"string"}}}`,
	} {
		_, err := tools.CompileSchema([]byte(raw))

		require.Error(t, err, raw)
	}
}

// A write call's _hypothesis is split off before the pinned schema reads the
// rest: the schema never declares it, and no server receives it (spec
// Section 5.3). The split keeps every strict rule of the arguments.
func TestThePlatformsArgumentIsSplitOffBeforeTheSchemaReadsTheRest(t *testing.T) {
	s := compile(t, `{"type":"object","properties":{"flag":{"type":"string"}},"additionalProperties":false}`)

	args, h, err := s.ReadWithout(json.RawMessage(`{"flag":"x","_hypothesis":"h1"}`), 1024, "_hypothesis")
	require.NoError(t, err)
	assert.Equal(t, "h1", h)
	if got := string(args.Raw); got != `{"flag":"x"}` {
		t.Errorf("the arguments without _hypothesis are %s", got)
	}
	_, h, err = s.ReadWithout(json.RawMessage(`{"flag":"x"}`), 1024, "_hypothesis")
	require.NoError(t, err)
	assert.Nil(t, h)

	for _, raw := range []string{
		`{"flag":"x","_hypothesis":"h1","_Hypothesis":"h2"}`,
		`{"flag":"x","_HYPOTHESIS":"h1"}`,
		`{"flag":"x","_hypothesis":"h1","_hypothesis":"h2"}`,
		`[{"_hypothesis":"h1"}]`,
		`{"flag":"x","_hypothesis":"` + strings.Repeat("h", 2000) + `"}`,
	} {
		_, _, err := s.ReadWithout(json.RawMessage(raw), 1024, "_hypothesis")
		require.ErrorIs(t, err, tools.ErrArguments, raw)
	}
}

// A server that declares the platform's argument itself, in any case, would
// be told what the platform keeps from servers; the gateway hides its tool.
func TestASchemaThatDeclaresTheArgumentInAnyCaseIsToldApart(t *testing.T) {
	assert.True(t, compile(t, `{"type":"object","properties":{"_HYPOTHESIS":{"type":"string"}}}`).Declares("_hypothesis"))
	assert.False(t, compile(t, schema).Declares("_hypothesis"))
}

// A server whose schema requires the platform's argument, in any case,
// without declaring it would refuse every call once the gateway splits the
// argument off; the gateway hides its tool.
func TestASchemaThatRequiresTheArgumentInAnyCaseIsToldApart(t *testing.T) {
	assert.True(t, compile(t, `{"type":"object","properties":{"flag":{"type":"string"}},"required":["flag","_Hypothesis"]}`).Requires("_hypothesis"))
	assert.False(t, compile(t, schema).Requires("_hypothesis"))
}

// The schema a write tool is listed with is the pinned one plus the
// platform's required argument; numbers stay as the reviewer saw them.
func TestAListedSchemaGainsTheRequiredArgument(t *testing.T) {
	got, err := tools.WithArgument(json.RawMessage(`{"type":"object","properties":{"limit":{"type":"number","maximum":1.50}},"required":["limit"]}`),
		"_hypothesis", map[string]any{"type": "string"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object","properties":{"_hypothesis":{"type":"string"},"limit":{"type":"number","maximum":1.5}},"required":["limit","_hypothesis"]}`, string(got))
	assert.Contains(t, string(got), "1.50")

	got, err = tools.WithArgument(json.RawMessage(`{"type":"object"}`), "_hypothesis", map[string]any{"type": "string"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object","properties":{"_hypothesis":{"type":"string"}},"required":["_hypothesis"]}`, string(got))
}

// Splitting the platform's argument off reads any bytes without failing,
// and what it accepts holds the argument no more.
func FuzzReadWithoutNeverFails(f *testing.F) {
	s, err := tools.CompileSchema([]byte(`{"type":"object","properties":{"flag":{"type":"string"},"n":{}}}`))
	require.NoError(f, err)
	for _, seed := range []string{
		`{"flag":"x","_hypothesis":"h1"}`, `{"_hypothesis":{"_hypothesis":1}}`, `{"flag":"x","_HYPOTHESIS":"h"}`,
		`{"n":1e400,"_hypothesis":"h"}`, `[`, `{"flag":"\u0000"}`, `{"a":1}{"b":2}`, "",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		args, _, err := s.ReadWithout(raw, 1024, "_hypothesis")
		if err != nil {
			return
		}
		var top map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(args.Raw, &top))
		assert.NotContains(t, top, "_hypothesis")
	})
}

// Extending a schema reads any bytes without failing.
func FuzzWithArgumentNeverFails(f *testing.F) {
	for _, seed := range []string{`{"type":"object"}`, `{"properties":1,"required":"x"}`, `[]`, `null`, `{"required":[1,{}]}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out, err := tools.WithArgument(raw, "_hypothesis", map[string]any{"type": "string"})
		if err == nil {
			assert.True(t, json.Valid(out))
		}
	})
}

// Checking a schema's root reads any bytes without failing.
func FuzzOpenRootNeverFails(f *testing.F) {
	for _, seed := range []string{`{"type":"object"}`, `{"x-a":1,"ALLOF":[]}`, `true`, `false`, `[]`, `{"properties":{"a":{"$ref":"#"}}}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if tools.OpenRoot(raw) {
			assert.True(t, json.Valid(raw))
		}
	})
}

// A root that holds only keywords that describe the object and its declared
// properties, and vendor extensions, lets an added argument through; any
// other keyword, in any case or draft, and any the gateway does not know,
// could refuse it, or the object as a whole.
func TestAnOpenRootHoldsOnlyKeywordsThatDescribeTheObject(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object"}`,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","title":"t","description":"d","properties":{"flag":{"type":"string"}},"required":["flag"],"additionalProperties":false,"minProperties":1,"$defs":{"x":{}},"x-vendor":true,"X-Other":1}`,
	} {
		assert.True(t, tools.OpenRoot([]byte(raw)), raw)
	}
	for _, raw := range []string{
		`{"type":"object","allOf":[{}]}`, `{"type":"object","NOT":{"required":["_hypothesis"]}}`, `{"type":"object","enum":[{}]}`,
		`{"type":"object","const":{}}`, `{"type":"object","dependencies":{"_hypothesis":["x"]}}`, `{"type":"object","$ref":"#/$defs/x","$defs":{"x":{}}}`,
		`{"type":"object","maxProperties":1}`, `{"type":"object","propertyNames":{"maxLength":4}}`, `{"type":"object","patternProperties":{"^_":false}}`,
		`{"type":"object","unevaluatedProperties":false}`, `{"type":"object","if":{},"then":{}}`, `not json`,
		`{"type":"object","disallow":["object"]}`, `{"type":"object","extends":{"required":["y"]}}`,
	} {
		assert.False(t, tools.OpenRoot([]byte(raw)), raw)
	}
}

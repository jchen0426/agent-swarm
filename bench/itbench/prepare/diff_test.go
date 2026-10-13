package prepare

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal([]byte(s), &v))
	return v
}

func str(s string) *string { return &s }

func TestDiffMatchesNamedListItemsByName(t *testing.T) {
	a := decode(t, `{"spec":{"containers":[{"name":"shipping","env":[{"name":"A","value":"1"},{"name":"QUOTE_ADDR","value":"quote:8080"}]}]}}`)
	b := decode(t, `{"spec":{"containers":[{"name":"shipping","env":[{"name":"QUOTE_ADDR","value":"quote:0000"},{"name":"A","value":"1"}]}]}}`)

	got := diffTrees("", a, b)

	assert.Equal(t, []dataset.Field{{Path: "spec.containers[shipping].env[QUOTE_ADDR].value", From: str("quote:8080"), To: str("quote:0000")}}, got)
}

func TestDiffComparesJSONStringsByContent(t *testing.T) {
	a := decode(t, `{"data":{"demo.flagd.json":"{\n  \"flags\": {\n    \"x\": {\"defaultVariant\": \"off\", \"state\": \"ENABLED\"}\n  }\n}"}}`)
	same := decode(t, `{"data":{"demo.flagd.json":"{\"flags\":{\"x\":{\"state\":\"ENABLED\",\"defaultVariant\":\"off\"}}}"}}`)
	b := decode(t, `{"data":{"demo.flagd.json":"{\"flags\":{\"x\":{\"state\":\"ENABLED\",\"defaultVariant\":\"on\"}}}"}}`)

	assert.Empty(t, diffTrees("", a, same))
	assert.Equal(t, []dataset.Field{{Path: `data["demo.flagd.json"].flags.x.defaultVariant`, From: str("off"), To: str("on")}}, diffTrees("", a, b))
}

func TestDiffReportsAddedAndRemovedValues(t *testing.T) {
	a := decode(t, `{"spec":{"replicas":1,"args":["a","b"]}}`)
	b := decode(t, `{"spec":{"nodeSelector":{"disk":"ssd"},"args":["a"]}}`)

	got := diffTrees("", a, b)

	assert.Equal(t, []dataset.Field{
		{Path: "spec.args[1]", From: str("b")},
		{Path: "spec.nodeSelector", To: str(`{"disk":"ssd"}`)},
		{Path: "spec.replicas", From: str("1")},
	}, got)
}

func TestDiffTruncatesLongValues(t *testing.T) {
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	got := diffTrees("", decode(t, `{"a":"`+string(long)+`"}`), decode(t, `{"a":"y"}`))

	require.Len(t, got, 1)
	assert.Len(t, []rune(*got[0].From), maxValue+1)
	assert.Equal(t, "y", *got[0].To)
}

package selector_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

func TestParseAcceptsQueriesWithoutFunctions(t *testing.T) {
	for _, q := range []string{"$", "$[*].name", "$.events[*]", "$..env[*].value", "$.items[?@.kind == 'flag'].id", "$['a b']"} {
		s, err := selector.Parse(q)
		require.NoError(t, err, q)
		assert.Equal(t, q, s.String())
	}
}

func TestParseRefusesFunctionsAndBadQueries(t *testing.T) {
	for _, q := range []string{"", "name", "$[", "$[?length(@.a) > 1]", "$[?match(@.a, 'x.*')]", "$[?count(@.*) == 1]"} {
		_, err := selector.Parse(q)
		require.ErrorIs(t, err, selector.ErrSyntax, q)
	}
}

func TestSelectPicksValuesInDocumentOrder(t *testing.T) {
	doc := []any{
		map[string]any{"name": "cart", "kind": "service"},
		map[string]any{"name": "checkout", "kind": "service"},
		map[string]any{"name": "db", "kind": "store"},
	}

	assert.Equal(t, []any{"cart", "checkout", "db"}, mustParse(t, "$[*].name").Select(doc))
	assert.Equal(t, []any{"cart", "checkout"}, mustParse(t, "$[?@.kind == 'service'].name").Select(doc))
	assert.Empty(t, mustParse(t, "$[*].owner").Select(doc))
	assert.Empty(t, selector.Selector{}.Select(doc), "the zero value selects nothing")
}

func TestSelectJSONDecodesTheDocument(t *testing.T) {
	got, err := mustParse(t, "$.current").SelectJSON([]byte(`{"current": "v42", "previous": "v41"}`))
	require.NoError(t, err)
	assert.Equal(t, []any{"v42"}, got)

	_, err = mustParse(t, "$").SelectJSON([]byte(`{"current": `))
	require.Error(t, err)
}

func mustParse(t *testing.T, q string) selector.Selector {
	t.Helper()
	s, err := selector.Parse(q)
	require.NoError(t, err)
	return s
}

func TestReplaceReplacesEverySelectedValueInPlace(t *testing.T) {
	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{"env":[{"name":"A","value":"s3cret"},{"name":"B","value":{"nested":1}}],"keep":"x"}`), &doc))
	s, err := selector.Parse("$.env[*].value")
	require.NoError(t, err)

	got := s.Replace(doc, func(any) any { return "[redacted]" })

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, `{"env":[{"name":"A","value":"[redacted]"},{"name":"B","value":"[redacted]"}],"keep":"x"}`, string(raw))
}

func TestReplaceOfTheRootReplacesTheDocument(t *testing.T) {
	s, err := selector.Parse("$")
	require.NoError(t, err)

	assert.Equal(t, "[redacted]", s.Replace(map[string]any{"a": "b"}, func(any) any { return "[redacted]" }))
	assert.Equal(t, "doc", selector.Selector{}.Replace("doc", func(any) any { return "x" }), "the zero selector replaces nothing")
}

func TestReplaceSkipsAValueAnEarlierReplacementRemoved(t *testing.T) {
	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{"a":{"b":{"c":"deep"}}}`), &doc))
	s, err := selector.Parse("$..*")
	require.NoError(t, err)

	got := s.Replace(doc, func(any) any { return "x" })

	assert.Equal(t, map[string]any{"a": "x"}, got)
}

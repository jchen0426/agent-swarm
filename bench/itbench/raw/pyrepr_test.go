package raw_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

func TestParsePythonDict(t *testing.T) {
	got, err := raw.ParsePython(`{'k8s.pod.name': 'cart-1', 'count': 3, 'ratio': 0.5, 'ok': True, 'none': None, ` +
		`'list': [1, 'two'], 'nested': {'a': "b"}, 'neg': -2, 'exp': 1.5e+00}`)

	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"k8s.pod.name": "cart-1", "count": int64(3), "ratio": 0.5, "ok": true, "none": nil,
		"list": []any{int64(1), "two"}, "nested": map[string]any{"a": "b"}, "neg": int64(-2), "exp": 1.5,
	}, got)
}

func TestParsePythonStringEscapes(t *testing.T) {
	got, err := raw.ParsePython(`{'q': 'it\'s', 'dq': "say \"hi\"", 'nl': 'a\nb', 'tab': 'a\tb', 'bs': 'a\\b', 'hex': '\x41', 'uni': 'é'}`)

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"q": "it's", "dq": `say "hi"`, "nl": "a\nb", "tab": "a\tb", "bs": `a\b`, "hex": "A", "uni": "é"}, got)
}

func TestParsePythonEmptyAndTuple(t *testing.T) {
	got, err := raw.ParsePython(`{'empty': {}, 'list': [], 'tuple': (1, 2), 'one': (3,)}`)

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"empty": map[string]any{}, "list": []any{}, "tuple": []any{int64(1), int64(2)}, "one": []any{int64(3)}}, got)
}

func TestParsePythonCallsBecomeTheirArgumentText(t *testing.T) {
	got, err := raw.ParsePython(`[np.datetime64('2025-12-15T17:18:15.800000000'), 1]`)

	require.NoError(t, err)
	assert.Equal(t, []any{"2025-12-15T17:18:15.800000000", int64(1)}, got)
}

func TestParsePythonRejectsGarbage(t *testing.T) {
	for _, in := range []string{`{'a': }`, `{'a' 1}`, `'unterminated`, `{'a': 1} trailing`, ``} {
		_, err := raw.ParsePython(in)
		assert.Error(t, err, in)
	}
}

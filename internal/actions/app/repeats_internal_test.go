package app

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

// hideAll hides every string, key and number of the arguments.
var hideAll = redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`.+`)}, Replacement: "[hidden]"})

// At most 32 values an output is checked for; more refuse the call, which
// bounds the work redaction does on the output.
func TestRepeatsAreBounded(t *testing.T) {
	values := map[string]any{}
	for i := range maxRepeats {
		values[fmt.Sprintf("arg%d", i)] = "x"
	}
	_, err := repeats(redact.New(), values)
	require.NoError(t, err, "nothing changed, nothing to check")

	values = map[string]any{"terms": []any{}}
	for i := range maxRepeats / 2 {
		values["terms"] = append(values["terms"].([]any), fmt.Sprintf("term %d", i))
	}
	_, err = repeats(hideAll, values)
	require.NoError(t, err, "16 strings and their key")

	for i := range maxRepeats {
		values[fmt.Sprintf("more-%d", i)] = "a value"
	}
	_, err = repeats(hideAll, values)
	require.ErrorContains(t, err, "narrow the call")
}

// A value of fewer than four bytes is left alone; two characters beyond
// ASCII are enough to carry a word.
func TestShortValuesAreMeasuredInBytes(t *testing.T) {
	paths, err := selector.Parse("$.*")
	require.NoError(t, err)
	hidden := redact.New(redact.Rule{Paths: []selector.Selector{paths}, Replacement: "[hidden]"})

	red, err := repeats(hidden, map[string]any{"a": "abc", "b": "漢字"})

	require.NoError(t, err)
	assert.Equal(t, "abc [hidden]", red.Text("abc 漢字"))
}

// The length that counts is the folded one: a value that folds to fewer than
// four bytes, as "o" and the Kelvin sign fold to "ok", is left alone.
func TestShortValuesAreMeasuredFolded(t *testing.T) {
	paths, err := selector.Parse("$.*")
	require.NoError(t, err)
	hidden := redact.New(redact.Rule{Paths: []selector.Selector{paths}, Replacement: "[hidden]"})

	red, err := repeats(hidden, map[string]any{"a": "o\u212a"})

	require.NoError(t, err)
	assert.Equal(t, "ok, broken", red.Text("ok, broken"))
}

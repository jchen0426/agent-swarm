package output_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

var keys = redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`AKIA[0-9A-Z]{16}`)}, Replacement: "[key]"})

func text(s string) output.Block { return output.Block{Kind: output.Text, Text: s} }

func show(t *testing.T, res *output.Result, structured bool, red *redact.Redactor) string {
	t.Helper()
	c, err := output.Read(res, structured)
	require.NoError(t, err)
	s, err := c.Show(red)
	require.NoError(t, err)
	return s
}

func TestTextBlocksAreJoinedAndJSONIsWrittenAgainCompactly(t *testing.T) {
	res := &output.Result{Blocks: []output.Block{text("line one"), text("{\n  \"b\": 1,\n  \"a\": \"x<y\"\n}")}}

	got := show(t, res, false, keys)

	assert.Equal(t, []string{"line one", `{"a":"x<y","b":1}`}, strings.Split(got, "\n"))
}

func TestAnEscapeCannotHideASecret(t *testing.T) {
	res := &output.Result{Blocks: []output.Block{text(`{"key":"\u0041KIAABCDEFGHIJKLMNOP"}`)}}

	assert.Equal(t, []string{`{"key":"[key]"}`}, strings.Split(show(t, res, false, keys), "\n"))
}

func TestAnEscapedSecretInPlainTextIsAReadError(t *testing.T) {
	res := &output.Result{Blocks: []output.Block{text(`payload={"key":"\u0041KIAABCDEFGHIJKLMNOP"}`)}}
	c, err := output.Read(res, false)
	require.NoError(t, err)

	_, err = c.Show(keys)

	require.ErrorIs(t, err, output.ErrForm)
}

func TestTextThatLooksLikeJSONMustBeJSON(t *testing.T) {
	for _, s := range []string{`{"rows":[1,2`, `[1,2] trailing`, `{"a":1} {"b":2}`} {
		_, err := output.Read(&output.Result{Blocks: []output.Block{text(s)}}, false)

		require.ErrorIs(t, err, output.ErrForm, s)
	}
}

func TestContentThatCannotBeRedactedIsLeftOutWithANote(t *testing.T) {
	res := &output.Result{Blocks: []output.Block{
		{Kind: output.Image}, text("visible"), {Kind: output.Resource, Binary: true},
		{Kind: output.Resource, Text: "notes AKIAABCDEFGHIJKLMNOP"}, {Kind: output.Link},
	}}

	c, err := output.Read(res, false)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"image content was left out: the gateway cannot redact it",
		"resource content was left out: the gateway cannot redact it",
		"resource_link content was left out: the gateway cannot redact it",
	}, c.Notes)
	s, err := c.Show(keys)
	require.NoError(t, err)
	assert.Equal(t, "visible\nnotes [key]", s)
}

func TestADeclaredOutputSchemaMakesTheStructuredContentCanonical(t *testing.T) {
	res := &output.Result{
		Structured: map[string]any{"key": "AKIAABCDEFGHIJKLMNOP", "n": 3.0, "truncated": false},
		Blocks:     []output.Block{text("a summary the server cut short")},
	}

	c, err := output.Read(res, true)
	require.NoError(t, err)

	assert.True(t, c.JSON)
	plain, ok := c.Plain()
	require.True(t, ok)
	assert.Equal(t, map[string]any{"key": "AKIAABCDEFGHIJKLMNOP", "n": 3.0, "truncated": false}, plain)
	s, err := c.Show(keys)
	require.NoError(t, err)
	assert.JSONEq(t, `{"key":"[key]","n":3,"truncated":false}`, s)
	assert.Equal(t, "AKIAABCDEFGHIJKLMNOP", res.Structured.(map[string]any)["key"], "the result itself is not changed")
}

func TestStructuredOutputThatIsMissingOrRoundedIsAReadError(t *testing.T) {
	_, err := output.Read(&output.Result{Blocks: []output.Block{text("{}")}}, true)
	require.ErrorIs(t, err, output.ErrForm)

	_, err = output.Read(&output.Result{Structured: map[string]any{"trace": 1.2345678901234567e19}}, true)
	require.ErrorIs(t, err, output.ErrForm)
}

func TestLargeNumbersInTextKeepEveryDigit(t *testing.T) {
	res := &output.Result{Blocks: []output.Block{text(`{"trace":12345678901234567890}`)}}

	assert.Equal(t, []string{`{"trace":12345678901234567890}`}, strings.Split(show(t, res, false, keys), "\n"))
}

func TestNulCharactersAndBrokenUTF8AreReplaced(t *testing.T) {
	res := &output.Result{Blocks: []output.Block{text("a" + string([]byte{0, 0xff}) + "b")}}

	assert.Equal(t, "a\uFFFD\uFFFDb", show(t, res, false, keys))
}

func TestPathsNeedJSON(t *testing.T) {
	sel, err := selector.Parse("$..password")
	require.NoError(t, err)
	paths := redact.New(redact.Rule{Paths: []selector.Selector{sel}, Replacement: "[pw]"})
	c, err := output.Read(&output.Result{Blocks: []output.Block{text("password: hunter2")}}, false)
	require.NoError(t, err)

	_, err = c.Show(paths)

	require.ErrorIs(t, err, output.ErrForm)
}

func TestPlainIsOnlyForJSON(t *testing.T) {
	c, err := output.Read(&output.Result{Blocks: []output.Block{text("no rows")}}, false)
	require.NoError(t, err)

	_, ok := c.Plain()

	assert.False(t, ok)
}

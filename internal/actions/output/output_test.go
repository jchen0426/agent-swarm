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
	res := &output.Result{Blocks: []output.Block{text(`payload=\u0041KIAABCDEFGHIJKLMNOP`)}}
	c, err := output.Read(res, false)
	require.NoError(t, err)

	_, err = c.Show(keys)

	require.ErrorIs(t, err, output.ErrForm)
}

func TestAnEscapedSecretInAMemberWrittenInTextIsRedacted(t *testing.T) {
	res := &output.Result{Blocks: []output.Block{text(`payload={"key":"\u0041KIAABCDEFGHIJKLMNOP"}`)}}

	assert.Equal(t, `payload={"key":"[key]"}`, show(t, res, false, keys))
}

var secrets = redact.New(redact.Rule{Replacement: "[secret]", Patterns: []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key)\s*[:=]\s*[^\s,;]{8,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`(?i)\b[a-z]+://[^\s:/@]+:[^\s@/]+@`),
}})

// A block that is not one JSON document, such as a log line in brackets, a
// JSON line or JSON cut short, is plain text, and members written in it are
// still found.
func TestTextThatLooksLikeJSONButIsNotIsPlainText(t *testing.T) {
	for in, want := range map[string]string{
		`[2026-10-14 10:00:00] ERROR login failed for ana`: `[2026-10-14 10:00:00] ERROR login failed for ana`,
		`{"rows":[{"password":"hunter2hunter2"},{"x`:       `{"rows":[{"password":"[secret]"},{"x`,
		`{"a":1} {"password":"hunter2hunter2"}`:            `{"a":1} {"password":"[secret]"}`,
	} {
		c, err := output.Read(&output.Result{Blocks: []output.Block{text(in)}}, false)
		require.NoError(t, err, in)

		assert.False(t, c.JSON, in)
		s, err := c.Show(secrets)
		require.NoError(t, err, in)
		assert.Equal(t, want, s, in)
	}
}

// Each block is redacted in its own form, so a JSON block among others is
// still redacted value by value.
func TestEachBlockIsRedactedInItsOwnForm(t *testing.T) {
	res := &output.Result{Blocks: []output.Block{
		text("Found 1 user:"), text(`{"user":"ana","password":"hunter2hunter2"}`), text(`["line1\nAKIAABCDEFGHIJKLMNOP"]`),
	}}

	got := show(t, res, false, secrets)

	assert.Equal(t, []string{"Found 1 user:", `{"password":"[secret]","user":"ana"}`, `["line1\n[secret]"]`}, strings.Split(got, "\n"))
}

// JSON written inside a string value, as log tools return it, is redacted
// too, members included.
func TestJSONInsideAStringIsRedacted(t *testing.T) {
	res := &output.Result{Blocks: []output.Block{text(`{"logs":["{\"msg\":\"loaded key\\nAKIAABCDEFGHIJKLMNOP\"}","{\"password\":\"hunter2hunter2\"}"]}`)}}

	assert.JSONEq(t, `{"logs":["{\"msg\":\"loaded key\\n[secret]\"}","{\"password\":\"[secret]\"}"]}`, show(t, res, false, secrets))
}

// A member under a key that holds a JSON escape, as a Windows path does, is
// shown redacted, as it was before the member check looked at escaped keys:
// the escape hides no match, so the output is not refused.
func TestAMemberUnderAnEscapedKeyIsShownRedacted(t *testing.T) {
	red := redact.New(redact.Rule{Replacement: "[secret]", Patterns: []*regexp.Regexp{regexp.MustCompile(`(?i)password\s*[=:]\s*\S+`)}})
	res := &output.Result{Blocks: []output.Block{text(`{"C:\\temp\\password":"hunter2hunter2"}`)}}
	c, err := output.Read(res, false)
	require.NoError(t, err)

	s, err := c.Show(red)

	require.NoError(t, err)
	assert.JSONEq(t, `{"C:\\temp\\password":"[secret]"}`, s)
}

// A key that opens a written member before an escaped keyword is not shown in
// clear: the member is refused, as any member an escape hides a match in is.
func TestAMemberBehindADecoyPrefixIsARefusal(t *testing.T) {
	red := redact.New(redact.Rule{Replacement: "[secret]", Patterns: []*regexp.Regexp{regexp.MustCompile(`(?i)password\s*[=:]\s*\S+`)}})
	c, err := output.Read(&output.Result{Blocks: []output.Block{text(`{"\"x\":\"pass\\u0077ord":"hunter2hunter2"}`)}}, false)
	require.NoError(t, err)

	_, err = c.Show(red)

	require.ErrorIs(t, err, output.ErrForm)
}

func TestAnEscapedMatchInsideJSONIsAReadError(t *testing.T) {
	c, err := output.Read(&output.Result{Blocks: []output.Block{text(`{"lines":["postgres:\\/\\/admin:hunter2@db"]}`)}}, false)
	require.NoError(t, err)

	_, err = c.Show(secrets)

	require.ErrorIs(t, err, output.ErrForm)
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

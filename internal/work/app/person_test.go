package app_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// ruleOf is a redactor of one rule, which replaces what pattern matches with
// [redacted:name].
func ruleOf(name, pattern string) *redact.Redactor {
	return redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(pattern)}, Replacement: "[redacted:" + name + "]"})
}

// quotesText reports whether msg holds any four characters of text in a row.
func quotesText(msg, text string) bool {
	r := []rune(text)
	for i := 0; i+4 <= len(r); i++ {
		if strings.Contains(msg, string(r[i:i+4])) {
			return true
		}
	}
	return false
}

// A person's text whose JSON escape hides what a rule that applies always
// removes is refused, as an agent's is (Decision 9; agentText): the error
// wraps ErrInvalid, says why, and holds no part of the text. The same text
// written plainly is not refused but redacted (R1).
func TestAPersonsTextThatHidesAMatchBehindAnEscapeIsRefusedAndNotQuoted(t *testing.T) {
	red := ruleOf("api-keys", `sk-[a-z0-9]{8}`)
	// The JSON escape \u0061 decodes to "a": the text decodes to sk-abcdefgh.
	const escaped = `sk-\u0061bcdefgh`

	_, err := app.PersonText(red, "text", escaped)
	plain, plainErr := app.PersonText(red, "text", "sk-abcdefgh")

	require.ErrorIs(t, err, app.ErrInvalid)
	assert.Contains(t, err.Error(), "escape", "it says why")
	assert.False(t, quotesText(err.Error(), escaped), "it holds no part of the text")
	require.NoError(t, plainErr)
	assert.Equal(t, "[redacted:api-keys]", plain)
}

// A person's text whose member hides what a rule that applies always removes
// is refused, though the text alone hides nothing (Decision 9): the board's
// snapshot and the records hold the text as the member text=<text>, which the
// LLM gateway reads as one (redact.EscapedValue). Here the text, its escape
// decoded, is xxx, which the rule text=xxx does not match; its member decodes
// to text=xxx, which it does. The text written plainly is taken unchanged.
func TestAPersonsTextWhoseMemberHidesAMatchIsRefused(t *testing.T) {
	red := ruleOf("member", `text=xxx`)
	// The JSON escape \u0078 decodes to "x": the text decodes to xxx.
	const escaped = `\u0078xx`

	_, err := app.PersonText(red, "text", escaped)
	plain, plainErr := app.PersonText(red, "text", "xxx")

	require.ErrorIs(t, err, app.ErrInvalid)
	assert.Contains(t, err.Error(), "escape", "it says why")
	assert.False(t, quotesText(err.Error(), escaped), "it holds no part of the text")
	require.NoError(t, plainErr)
	assert.Equal(t, "xxx", plain, "taken unchanged")
}

// A person's text with a character a reader could miss, such as a zero-width
// space, is refused (Decision 9; config.Hidden), and not quoted.
func TestAPersonsTextWithAHiddenCharacterIsRefused(t *testing.T) {
	red := ruleOf("api-keys", `sk-[a-z0-9]{8}`)
	text := "tracked in item-42\u200b"

	_, err := app.PersonText(red, "text", text)

	require.ErrorIs(t, err, app.ErrInvalid)
	assert.Contains(t, err.Error(), "reader could miss", "it says why")
	assert.False(t, quotesText(err.Error(), text), "it holds no part of the text")
}

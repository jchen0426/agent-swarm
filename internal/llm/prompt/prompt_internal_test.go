package prompt

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
)

// redactContent refuses a block of a kind it does not name, and the refusal
// names the block, never its text: a kind the allowlist admitted without a
// case there would otherwise reach the vendor unredacted. Thinking blocks are
// named, and pass unchanged, as the vendor needs them back.
func TestRedactContentRefusesABlockOfAKindItDoesNotName(t *testing.T) {
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	for _, typ := range []any{"image", "document", "search_result", "server_tool_use", "Text", nil, 7} {
		content := []any{map[string]any{"type": "text", "text": "x"}, map[string]any{"type": typ, "text": "sk-aaaaaaaa"}}

		_, err := redactContent(content, red, "message 3")

		var refusal *Refusal
		if assert.ErrorAs(t, err, &refusal, typ) {
			assert.Contains(t, err.Error(), "block 1 of message 3", typ)
			assert.NotContains(t, err.Error(), "sk-aaaaaaaa", typ)
		}
	}
	thinking := func() []any {
		return []any{
			map[string]any{"type": "thinking", "thinking": "the key sk-iiiiiiii", "signature": "EqQB"},
			map[string]any{"type": "redacted_thinking", "data": "sk-jjjjjjjj"},
		}
	}

	got, err := redactContent(thinking(), red, "message 1")

	require.NoError(t, err)
	assert.Equal(t, thinking(), got, "thinking blocks pass unchanged")
}

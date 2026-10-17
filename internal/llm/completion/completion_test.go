package completion_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/llm/completion"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
)

var red = redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})

// answer is an answer as the vendor sends one: a thinking block, a text
// and a tool call, with the usage the vendor reports.
const answer = `{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
 "content": [{"type": "thinking", "thinking": "", "signature": "EqQBCkgI"},
  {"type": "text", "text": "The key sk-aaaaaaaa leaked."},
  {"type": "tool_use", "id": "toolu_2", "name": "metrics__query", "input": {"q": "sk-bbbbbbbb"}, "caller": {"type": "direct"}}],
 "stop_reason": "tool_use", "stop_sequence": null, "container": null,
 "usage": {"input_tokens": 120, "cache_creation_input_tokens": 500, "cache_read_input_tokens": 9000, "output_tokens": 80,
  "cache_creation": {"ephemeral_5m_input_tokens": 400, "ephemeral_1h_input_tokens": 100},
  "output_tokens_details": {"thinking_tokens": 24}, "service_tier": "standard", "inference_geo": "global"}}`

// An answer gives the tokens it used, its content redacted for the record,
// the hashes of its thinking blocks, and how the vendor served it.
func TestAnAnswerGivesItsTokensRecordAndThinking(t *testing.T) {
	c, err := completion.Read([]byte(answer), red)

	require.NoError(t, err)
	assert.Equal(t, cost.Usage{Input: 120, CacheWrite5m: 400, CacheWrite1h: 100, CacheRead: 9000, Output: 80}, c.Usage)
	assert.NotContains(t, c.Record, "sk-")
	assert.Contains(t, c.Record, `"stop_reason":"tool_use"`)
	assert.Contains(t, c.Record, `The key [redacted:keys] leaked.`)
	var block map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"type": "thinking", "thinking": "", "signature": "EqQBCkgI"}`), &block))
	h, _ := prompt.ThinkingHash(block)
	assert.Equal(t, [][32]byte{h}, c.Thinking)
	assert.JSONEq(t, `{"id": "msg_1", "model": "claude-haiku-5-5", "service_tier": "standard", "inference_geo": "global",
		"thinking_tokens": 24, "server_tool_use": null}`, c.Details)
}

// A thinking block is known by the fields a request sends back, so that a
// field the vendor adds to its answers does not keep the block from going
// back.
func TestAThinkingBlockIsKnownByWhatGoesBack(t *testing.T) {
	c, err := completion.Read([]byte(`{"content": [{"type": "thinking", "thinking": "", "signature": "EqQBCkgI", "added": 1}],
 "usage": {"input_tokens": 1, "output_tokens": 2}}`), red)

	require.NoError(t, err)
	var block map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"type": "thinking", "thinking": "", "signature": "EqQBCkgI"}`), &block))
	h, _ := prompt.ThinkingHash(block)
	assert.Equal(t, [][32]byte{h}, c.Thinking)
}

// Cache writes never count below what the vendor reports in all: without
// a breakdown, or beyond it, they count as one-hour writes, the dearer.
func TestCacheWritesNeverCountBelowTheTotal(t *testing.T) {
	for usage, want := range map[string]cost.Usage{
		`"cache_creation_input_tokens": 50`: {Input: 1, CacheWrite1h: 50, Output: 2},
		`"cache_creation_input_tokens": 9, "cache_creation": {"ephemeral_5m_input_tokens": 2, "ephemeral_1h_input_tokens": 3}`: {
			Input: 1, CacheWrite5m: 2, CacheWrite1h: 7, Output: 2},
		`"cache_creation_input_tokens": 9, "cache_creation": {"ephemeral_5m_input_tokens": 5, "ephemeral_1h_input_tokens": 5}`: {
			Input: 1, CacheWrite5m: 5, CacheWrite1h: 5, Output: 2},
	} {
		c, err := completion.Read([]byte(`{"content": [], "usage": {"input_tokens": 1, "output_tokens": 2, `+usage+`}}`), red)

		require.NoError(t, err)
		assert.Equal(t, want, c.Usage, usage)
	}
}

// An answer whose tokens cannot be read is an error: the budget would
// otherwise count nothing for it.
func TestAnAnswerWithoutUsageIsAnError(t *testing.T) {
	for _, body := range []string{
		`{"content": [], "stop_reason": "end_turn"}`,
		`{"content": [], "usage": {"output_tokens": 1}}`,
		`{"content": [], "usage": {"input_tokens": 1}}`,
		`{"content": [], "usage": {"input_tokens": -1, "output_tokens": 0}}`,
		`{"content": [], "usage": {"input_tokens": "1", "output_tokens": 0}}`,
		`{"content": [], "usage": {"input_tokens": 1e13, "output_tokens": 0}}`,
		`{"content": [], "usage": {"input_tokens": 100000001, "output_tokens": 0}}`,
		`not json`,
	} {
		_, err := completion.Read([]byte(body), red)

		require.Error(t, err, body)
	}
}

// A failure the vendor answered is recorded redacted, and cut short.
func TestAFailureIsRecordedRedacted(t *testing.T) {
	got := completion.Failure([]byte(`{"type": "error", "error": {"type": "invalid_request_error", "message": "bad sk-cccccccc"}}`), red)

	assert.JSONEq(t, `{"type": "error", "error": {"type": "invalid_request_error", "message": "bad [redacted:keys]"}}`, got)
	assert.Equal(t, "upstream sk [redacted:keys]", completion.Failure([]byte("upstream sk sk-dddddddd"), red))
	assert.LessOrEqual(t, len(completion.Failure([]byte(strings.Repeat("x", 10000)), red)), 4096+3)
	assert.Empty(t, completion.Failure([]byte("\xff\xfe"), red))
}

// An answer whose content holds a match an escape hides is read for its
// usage, and given no record: redaction cannot change such a match, and no
// record holds one.
func TestAnAnswerHidingAMatchHasNoRecord(t *testing.T) {
	c, err := completion.Read([]byte(`{"content": [{"type": "text", "text": "s\\u006b-aaaaaaaa"}], "stop_reason": "end_turn", "usage": {"input_tokens": 1, "output_tokens": 2}}`), red)

	require.NoError(t, err)
	assert.Equal(t, cost.Usage{Input: 1, Output: 2}, c.Usage)
	assert.Empty(t, c.Record)
}

// A failure whose body holds a match an escape hides is recorded as nothing,
// JSON or not, and however long it is. A failure with a plain match is still
// redacted.
func TestAFailureHidingAMatchIsNotRecorded(t *testing.T) {
	assert.Empty(t, completion.Failure([]byte(`{"type": "error", "error": {"type": "api_error", "message": "s\\u006b-aaaaaaaa"}}`), red), "JSON")
	assert.Empty(t, completion.Failure([]byte(`upstream s\\u006b-aaaaaaaa failed`), red), "text")
	assert.Empty(t, completion.Failure([]byte(strings.Repeat("x", 5000)+` s\\u006b-aaaaaaaa`), red), "a match past the cut")
	assert.JSONEq(t, `{"error": "[redacted:keys]"}`, completion.Failure([]byte(`{"error": "sk-eeeeeeee"}`), red), "a plain match in JSON")
	assert.Equal(t, "upstream [redacted:keys] failed", completion.Failure([]byte("upstream sk-eeeeeeee failed"), red), "a plain match in text")
}

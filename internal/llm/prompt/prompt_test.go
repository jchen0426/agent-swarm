package prompt_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
)

const model = "claude-haiku-5-5"

// thinking is a thinking block as the model's answers hold one: no text,
// and a signature.
const thinking = `{"type": "thinking", "thinking": "", "signature": "EqQBCkgIBRABGAIiQL"}`

// answered holds the hashes of the thinking blocks the gateway gave the
// session.
func answered(t testing.TB, blocks ...string) func([32]byte) bool {
	t.Helper()
	known := map[[32]byte]bool{}
	for _, b := range blocks {
		var block map[string]any
		require.NoError(t, json.Unmarshal([]byte(b), &block))
		h, ok := prompt.ThinkingHash(block)
		require.True(t, ok, b)
		known[h] = true
	}
	return func(h [32]byte) bool { return known[h] }
}

func policy(t testing.TB) *prompt.Policy {
	t.Helper()
	return &prompt.Policy{Model: model, MaxTokens: 16384, Version: "2023-06-01", Answered: answered(t, thinking)}
}

// headers are what anthropic-sdk-go sends with a request.
func headers() map[string][]string {
	return map[string][]string{
		"Anthropic-Version": {"2023-06-01"}, "Content-Type": {"application/json"}, "X-Api-Key": {"session-token"},
		"X-Stainless-Os": {"Linux"}, "X-Stainless-Runtime-Version": {"go1.26.0"}, "User-Agent": {"Anthropic/Go 1.80.0"},
	}
}

// turn is a request as the runner sends one mid-turn: a cached system
// prompt, a tool, and an answer echoed back, its thinking block and the
// caller of its tool call included, with the tool's result.
const turn = `{
 "model": "claude-haiku-5-5",
 "max_tokens": 4096,
 "system": [{"type": "text", "text": "You investigate incidents.", "cache_control": {"type": "ephemeral"}}],
 "tools": [{"name": "metrics__query", "description": "Query metrics.", "input_schema": {"type": "object", "properties": {"q": {"type": "string"}}}}],
 "messages": [
  {"role": "user", "content": "Find why checkout fails."},
  {"role": "assistant", "content": [` + thinking + `, {"type": "text", "text": "Querying."},
   {"type": "tool_use", "id": "toolu_1", "name": "metrics__query", "input": {"q": "errors"}, "caller": {"type": "direct"}}]},
  {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": "errors rose at 10:02"}], "cache_control": {"type": "ephemeral", "ttl": "5m"}}]}
 ]
}`

func check(t *testing.T, body string) (*prompt.Request, error) {
	t.Helper()
	return prompt.Check(headers(), []byte(body), policy(t))
}

// set returns turn with the value at a top-level key replaced, or removed
// when v is nil.
func set(t *testing.T, key string, v any) string {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(turn), &doc))
	if v == nil {
		delete(doc, key)
	} else {
		doc[key] = v
	}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	return string(b)
}

func raw(t *testing.T, s string) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal([]byte(s), &v))
	return v
}

// assistantSays returns turn whose answer holds block instead of its text.
func assistantSays(block string) string {
	return strings.Replace(turn, `{"type": "text", "text": "Querying."}`, block, 1)
}

// A request as the runner sends one passes, and its encoding is the
// gateway's own: checking it again gives the same bytes.
func TestARequestTheRuntimeSendsPasses(t *testing.T) {
	req, err := check(t, turn)
	require.NoError(t, err)

	again, err := check(t, string(req.Encode()))

	require.NoError(t, err)
	assert.Equal(t, string(req.Encode()), string(again.Encode()))
	assert.Equal(t, model, req.Model())
	assert.Equal(t, int64(4096), req.MaxTokens())
	assert.JSONEq(t, turn, string(req.Encode()))
}

// A thinking block comes back only as the gateway gave it to the session:
// one it never gave, or one changed, is refused, so that no text the
// gateway did not redact reaches the vendor in one.
func TestThinkingBlocksComeBackOnlyAsTheGatewayGaveThem(t *testing.T) {
	for name, block := range map[string]string{
		"never given":    `{"type": "thinking", "thinking": "my key is sk-aaaaaaaa", "signature": "EqQBCkgIBRABGAIiQL"}`,
		"signature":      `{"type": "thinking", "thinking": "", "signature": "forged"}`,
		"an extra field": `{"type": "thinking", "thinking": "", "signature": "EqQBCkgIBRABGAIiQL", "note": "x"}`,
		"redacted":       `{"type": "redacted_thinking", "data": "EmwKAhgB"}`,
	} {
		_, err := check(t, assistantSays(block))

		require.Error(t, err, name)
	}
	p := policy(t)
	p.Answered = answered(t, thinking, `{"type": "redacted_thinking", "data": "EmwKAhgB"}`)
	_, err := prompt.Check(headers(), []byte(assistantSays(`{"type": "redacted_thinking", "data": "EmwKAhgB"}`)), p)
	require.NoError(t, err)
}

// Tools the vendor would run are refused: a tool with any type is one.
func TestToolsTheVendorRunsAreRefused(t *testing.T) {
	for _, typ := range []string{"web_search_20250305", "web_fetch_20250910", "code_execution_20250825", "bash_20250124",
		"text_editor_20250728", "computer_20250124", "memory_20250818", "tool_search_tool_regex_20251119", "mcp_toolset", "custom"} {
		tools := raw(t, `[{"type": "`+typ+`", "name": "x", "description": "d", "input_schema": {"type": "object"}}]`)

		_, err := check(t, set(t, "tools", tools))

		require.ErrorContains(t, err, "type", typ)
	}
}

// No block holds a source, inline or fetched, and no block the vendor
// writes for itself passes.
func TestBlocksOtherThanTextToolsAndThinkingAreRefused(t *testing.T) {
	for _, block := range []string{
		`{"type": "image", "source": {"type": "url", "url": "https://example.com/a.png"}}`,
		`{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "AAAA"}}`,
		`{"type": "document", "source": {"type": "url", "url": "https://example.com/a.pdf"}}`,
		`{"type": "document", "source": {"type": "file", "file_id": "file_1"}}`,
		`{"type": "search_result", "source": "https://example.com", "title": "t", "content": []}`,
		`{"type": "container_upload", "file_id": "file_1"}`,
		`{"type": "server_tool_use", "id": "srvtoolu_1", "name": "web_search", "input": {}}`,
		`{"type": "mcp_tool_use", "id": "mcptoolu_1", "name": "x", "server_name": "s", "input": {}}`,
		`{"type": "text", "text": "x", "citations": [{"type": "web_search_result_location", "url": "https://example.com"}]}`,
		`{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "image", "source": {"type": "url", "url": "https://example.com/a.png"}}]}`,
	} {
		messages := raw(t, `[{"role": "user", "content": [`+block+`]}]`)

		_, err := check(t, set(t, "messages", messages))

		require.Error(t, err, block)
	}
}

// A tool call's caller is the model itself, never code the vendor runs.
func TestTheCallerIsTheModelItself(t *testing.T) {
	for _, caller := range []string{`{"type": "code_execution_20250825"}`, `{"type": "direct", "tool_id": "x"}`, `"direct"`} {
		body := strings.Replace(turn, `"caller": {"type": "direct"}`, `"caller": `+caller, 1)

		_, err := check(t, body)

		require.Error(t, err, caller)
	}
}

// Ids and names are letters, digits, _ and -, never a channel for text.
func TestIdsAndNamesAreNotText(t *testing.T) {
	for name, body := range map[string]string{
		"tool use id":   strings.Replace(turn, `"id": "toolu_1"`, `"id": "toolu 1 my key is sk"`, 1),
		"tool use name": strings.Replace(turn, `"name": "metrics__query", "input"`, `"name": "metrics/query", "input"`, 1),
		"result id":     strings.Replace(turn, `"tool_use_id": "toolu_1"`, `"tool_use_id": "`+strings.Repeat("x", 129)+`"`, 1),
		"tool name":     strings.Replace(turn, `"name": "metrics__query", "description"`, `"name": "metrics query", "description"`, 1),
	} {
		_, err := check(t, body)

		require.Error(t, err, name)
	}
}

// Fields outside the list are refused, whatever their value: the runtime
// sends model, messages, system, max_tokens and tools.
func TestFieldsOutsideTheListAreRefused(t *testing.T) {
	for key, v := range map[string]any{
		"metadata": raw(t, `{"user_id": "x"}`), "thinking": raw(t, `{"type": "enabled", "budget_tokens": 1024}`),
		"container": "c", "mcp_servers": raw(t, `[]`), "service_tier": "auto", "context_management": raw(t, `{}`),
		"stream": false, "Model": model, "inference_geo": "us", "output_config": raw(t, `{}`), "temperature": 0.5,
		"top_p": 0.9, "top_k": 5, "stop_sequences": raw(t, `["sk-aaaaaaaa"]`), "tool_choice": raw(t, `{"type": "auto"}`),
	} {
		_, err := check(t, set(t, key, v))

		require.Error(t, err, key)
	}
}

// What two JSON readers could read in two ways is refused: a key twice in
// one object, a key in another case, data after the request, invalid
// UTF-8, nesting too deep to read safely.
func TestAmbiguousJSONIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"key twice":          `{"model": "claude-haiku-5-5", "model": "claude-opus-5-5", "max_tokens": 1, "messages": []}`,
		"key twice in input": strings.Replace(turn, `"input": {"q": "errors"}`, `"input": {"q": "errors", "q": "logs"}`, 1),
		"key in other case":  strings.Replace(turn, `"tools"`, `"Tools"`, 1),
		"block key in case":  strings.Replace(turn, `{"type": "text", "text": "Querying."}`, `{"type": "text", "Text": "Querying."}`, 1),
		"data after":         turn + `{}`,
		"invalid UTF-8":      strings.Replace(turn, "Querying.", "Query\xffing.", 1),
		"not an object":      `[` + turn + `]`,
		"too deep":           strings.Replace(turn, `{"q": "errors"}`, `{"q": `+strings.Repeat(`[`, 200)+strings.Repeat(`]`, 200)+`}`, 1),
	} {
		_, err := check(t, body)

		require.Error(t, err, name)
	}
	_, err := check(t, strings.Replace(turn, `{"q": "errors"}`, `{"q": `+strings.Repeat(`[`, 50)+strings.Repeat(`]`, 50)+`}`, 1))
	require.NoError(t, err, "fifty levels are deep enough for any tool's input")
}

// The model is the agent's own, exactly.
func TestTheModelIsTheAgents(t *testing.T) {
	for _, m := range []any{"claude-opus-5-5", "Claude-Haiku-5-5", "claude-haiku-5-5 ", 5, nil} {
		_, err := check(t, set(t, "model", m))

		require.Error(t, err, m)
	}
}

// max_tokens is a whole number from 1 to the cap, written as one.
func TestMaxTokensIsAWholeNumberWithinTheCap(t *testing.T) {
	for _, v := range []string{`0`, `-1`, `1.5`, `1e3`, `16385`, `"100"`, `null`} {
		_, err := check(t, set(t, "max_tokens", json.RawMessage(v)))

		require.Error(t, err, v)
	}
	_, err := check(t, set(t, "max_tokens", nil))
	require.Error(t, err, "max_tokens is required")
	_, err = check(t, set(t, "max_tokens", json.RawMessage(`16384`)))
	require.NoError(t, err)
}

// A cache entry lives five minutes, as the runtime's do.
func TestCacheControlIsFiveMinutesOnly(t *testing.T) {
	for _, cc := range []string{`{"type": "ephemeral", "ttl": "1h"}`, `{"type": "persistent"}`, `{"type": "ephemeral", "scope": "x"}`} {
		system := raw(t, `[{"type": "text", "text": "s", "cache_control": `+cc+`}]`)

		_, err := check(t, set(t, "system", system))

		require.Error(t, err, cc)
	}
}

// The client's anthropic-version must be the gateway's, no beta header
// passes, and a body is sent as it is; every other header is the
// gateway's to drop.
func TestHeaders(t *testing.T) {
	for name, header := range map[string][2]string{
		"version":  {"Anthropic-Version", "2024-01-01"},
		"beta":     {"Anthropic-Beta", "context-1m-2025-08-07"},
		"encoding": {"Content-Encoding", "gzip"},
	} {
		h := headers()
		h[header[0]] = []string{header[1]}

		_, err := prompt.Check(h, []byte(turn), policy(t))

		require.ErrorContains(t, err, header[1], name)
	}
	h := headers()
	h["Anthropic-Workspace-Id"] = []string{"wrkspc_other"}
	_, err := prompt.Check(h, []byte(turn), policy(t))
	require.NoError(t, err, "the gateway drops it; it never reaches the vendor")
}

// A refusal quotes a short, plain value of the client's whole.
func TestARefusalQuotesAShortPlainValueWhole(t *testing.T) {
	for name, body := range map[string]string{
		"a key":   set(t, "sk-bbbbbbbb", 1),
		"a model": set(t, "model", "sk-bbbbbbbb"),
	} {
		_, err := check(t, body)

		require.ErrorContains(t, err, `"sk-bbbbbbbb"`, name)
	}
	h := headers()
	h["Anthropic-Beta"] = []string{"sk-bbbbbbbb"}
	_, err := prompt.Check(h, []byte(turn), policy(t))
	require.ErrorContains(t, err, `"sk-bbbbbbbb"`, "a header")
}

// A refusal of a long value, or of one that needs an escape, says only how
// long the value is and quotes none of it: a cut or escaped copy could hide
// a secret from the redaction that a stored refusal gets.
func TestARefusalSaysOnlyTheLengthOfALongOrEscapedValue(t *testing.T) {
	long := strings.Repeat("x", 56) + "sk-aaaaaaaa"
	for name, c := range map[string]struct{ body, says string }{
		"a long key":       {set(t, long, 1), "a value of 67 bytes"},
		"an escaped key":   {set(t, "sk-cccccccc\n", 1), "a value of 12 bytes"},
		"a long model":     {set(t, "model", long), "a value of 67 bytes"},
		"an escaped model": {set(t, "model", "sk-cccccccc\n"), "a value of 12 bytes"},
	} {
		_, err := check(t, c.body)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.says, name)
		assert.NotContains(t, err.Error(), "sk-", name)
	}
	for name, c := range map[string]struct{ value, says string }{
		"a long header":     {long, "a value of 67 bytes"},
		"an escaped header": {"sk-cccccccc\n", "a value of 12 bytes"},
	} {
		h := headers()
		h["Anthropic-Beta"] = []string{c.value}
		_, err := prompt.Check(h, []byte(turn), policy(t))

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.says, name)
		assert.NotContains(t, err.Error(), "sk-", name)
	}
}

// Redaction changes everything the model reads, each part on its own, and
// nothing that makes the request work: ids, names, types, cache control,
// numbers and thinking blocks stay as written.
func TestRedactionChangesWhatTheModelReadsOnly(t *testing.T) {
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	thought := `{"type": "thinking", "thinking": "the key sk-iiiiiiii", "signature": "EqQBCkgIBRABGAIiQL"}`
	body := strings.NewReplacer(
		"You investigate incidents.", "Use sk-aaaaaaaa.",
		"Find why checkout fails.", "Key sk-bbbbbbbb leaked?",
		`"input": {"q": "errors"}`, `"input": {"q": "sk-cccccccc", "sk-dddddddd": 1}`,
		"errors rose at 10:02", "token sk-eeeeeeee",
		`"description": "Query metrics."`, `"description": "Query sk-ffffffff."`,
		`"properties": {"q": {"type": "string"}}`, `"properties": {"q": {"type": "string", "default": "sk-gggggggg"}}`,
		thinking, thought,
	).Replace(turn)
	p := policy(t)
	p.Answered = answered(t, thought)
	req, err := prompt.Check(headers(), []byte(body), p)
	require.NoError(t, err)

	require.NoError(t, req.Redact(red))
	got := string(req.Encode())

	for _, secret := range []string{"sk-aaaaaaaa", "sk-bbbbbbbb", "sk-cccccccc", "sk-dddddddd", "sk-eeeeeeee", "sk-ffffffff", "sk-gggggggg"} {
		assert.NotContains(t, got, secret)
	}
	assert.Contains(t, got, `"thinking":"the key sk-iiiiiiii"`, "a thinking block goes back as the gateway gave it")
	assert.Contains(t, got, `"id":"toolu_1"`)
	assert.Contains(t, got, `"name":"metrics__query"`)
	assert.Contains(t, got, `"tool_use_id":"toolu_1"`)
	assert.Contains(t, got, `"cache_control":{"ttl":"5m","type":"ephemeral"}`)
	assert.Contains(t, got, `"max_tokens":4096`)
}

// An id or a name cannot be redacted without breaking the conversation, so
// a request whose id or name holds what a rule hides, the session's token
// among them, is refused, and the refusal does not repeat it.
func TestAnIdOrANameThatHoldsASecretIsRefused(t *testing.T) {
	red := redact.New(redact.Rule{Literals: []string{"session-token-123456"}, Replacement: "[redacted:session-token]"},
		redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	for name, body := range map[string]string{
		"a tool's name":      strings.Replace(turn, `"tools": [{"name": "metrics__query"`, `"tools": [{"name": "sk-abcdefgh"`, 1),
		"a tool call's id":   strings.Replace(turn, `"id": "toolu_1"`, `"id": "session-token-123456"`, 1),
		"a tool call's name": strings.Replace(turn, `"id": "toolu_1", "name": "metrics__query"`, `"id": "toolu_1", "name": "sk-abcdefgh"`, 1),
		"a result's id":      strings.Replace(turn, `"tool_use_id": "toolu_1"`, `"tool_use_id": "sk-abcdefgh"`, 1),
	} {
		req, err := check(t, body)
		require.NoError(t, err, name)

		err = req.Redact(red)

		var refusal *prompt.Refusal
		require.ErrorAs(t, err, &refusal, name)
		assert.Contains(t, err.Error(), "redaction rule", name)
		assert.NotContains(t, err.Error(), "sk-abcdefgh", name)
		assert.NotContains(t, err.Error(), "session-token-123456", name)
	}
}

// Text that an escape hides a match in is refused wherever the model reads
// it, and the refusal names the part but never its text: redaction cannot
// change such text safely. The JSON below writes each escape with a double
// backslash, so that the text holds it literally: the text sk is one a
// pattern does not match, and the model reads as sk.
func TestAnEscapeThatHidesAMatchIsRefused(t *testing.T) {
	red := redact.New(redact.Rule{Literals: []string{"session-token-123456"}, Replacement: "[redacted:session-token]"},
		redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	for name, c := range map[string]struct{ body, part string }{
		"a text":           {strings.Replace(turn, `"Find why checkout fails."`, `"Key s\\u006b-aaaaaaaa."`, 1), "message 0"},
		"a system prompt":  {strings.Replace(turn, `"You investigate incidents."`, `"Use s\\u006b-aaaaaaaa."`, 1), "the system prompt"},
		"a tool result":    {strings.Replace(turn, `"errors rose at 10:02"`, `"token s\\u006b-aaaaaaaa"`, 1), "message 2"},
		"a description":    {strings.Replace(turn, `"Query metrics."`, `"Query s\\u006b-aaaaaaaa."`, 1), "the description of tool 0"},
		"a tool input":     {strings.Replace(turn, `"input": {"q": "errors"}`, `"input": {"q": "s\\u006b-aaaaaaaa"}`, 1), "block 2 of message 1"},
		"a tool input key": {strings.Replace(turn, `"input": {"q": "errors"}`, `"input": {"s\\u006b-aaaaaaaa": 1}`, 1), "block 2 of message 1"},
		"an input schema":  {strings.Replace(turn, `"properties": {"q": {"type": "string"}}`, `"properties": {"q": {"type": "string", "default": "s\\u006b-aaaaaaaa"}}`, 1), "the input schema of tool 0"},
		"a session token":  {strings.Replace(turn, `"Find why checkout fails."`, `"Token session\\u002dtoken-123456."`, 1), "message 0"},
	} {
		req, err := check(t, c.body)
		require.NoError(t, err, name)

		err = req.Redact(red)

		var refusal *prompt.Refusal
		if assert.ErrorAs(t, err, &refusal, name) {
			assert.Contains(t, err.Error(), c.part, name)
			assert.NotContains(t, err.Error(), "aaaaaaaa", name)
			assert.NotContains(t, err.Error(), "123456", name)
			assert.NotContains(t, err.Error(), `\u`, name)
		}
	}
}

// A member rule matches a key and its value together, so an escape in a tool
// call's input key can hide the match: the input is refused, and the refusal
// names its block, never its text.
func TestAToolInputWhoseKeyHidesAMemberRuleIsRefused(t *testing.T) {
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`(?i)password\s*[=:]\s*\S+`)}, Replacement: "[redacted:password]"})
	req, err := check(t, strings.Replace(turn, `"input": {"q": "errors"}`, `"input": {"pass\\u0077ord": "hunter2"}`, 1))
	require.NoError(t, err)

	err = req.Redact(red)

	var refusal *prompt.Refusal
	if assert.ErrorAs(t, err, &refusal) {
		assert.Contains(t, err.Error(), "block 2 of message 1")
		assert.NotContains(t, err.Error(), "hunter2")
		assert.NotContains(t, err.Error(), `\u`)
	}
}

// A refused redaction leaves the request as it was: the text before the
// refused part is not redacted, so a request cannot be sent half redacted.
func TestARefusedRedactionLeavesTheRequestAsItWas(t *testing.T) {
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	body := strings.NewReplacer(
		`"Find why checkout fails."`, `"Key sk-bbbbbbbb."`,
		`"Query metrics."`, `"Query s\\u006b-aaaaaaaa."`,
	).Replace(turn)
	req, err := check(t, body)
	require.NoError(t, err)
	before := string(req.Encode())

	require.Error(t, req.Redact(red))

	assert.Equal(t, before, string(req.Encode()))
}

// The gateway pins the service tier, and the inference region it is given,
// so that every call is billed as its card prices it.
func TestTheGatewayPinsHowACallIsBilled(t *testing.T) {
	req, err := check(t, turn)
	require.NoError(t, err)

	req.Pin("standard_only", "global")

	assert.Contains(t, string(req.Encode()), `"inference_geo":"global"`)
	assert.Contains(t, string(req.Encode()), `"service_tier":"standard_only"`)
}

// A refused request is recorded with every string, key and number redacted,
// or not at all when it cannot be read.
func TestARefusedRequestIsRecordedRedacted(t *testing.T) {
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})

	got := prompt.Redacted([]byte(`{"metadata": {"sk-aaaaaaaa": "sk-bbbbbbbb"}, "x": [1, "sk-cccccccc"]}`), red)

	assert.JSONEq(t, `{"metadata": {"[redacted:keys]": "[redacted:keys]"}, "x": [1, "[redacted:keys]"]}`, got)
	assert.Empty(t, prompt.Redacted([]byte(`{"a": 1`), red))
	assert.Empty(t, prompt.Redacted([]byte("\xff"), red))
}

// A record holds no match that an escape hides: where one is, the record is
// empty, as it is for a body that cannot be read.
func TestARecordHoldsNoMatchAnEscapeHides(t *testing.T) {
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	for name, body := range map[string]string{
		"a text":  `{"messages": [{"role": "user", "content": "s\\u006b-aaaaaaaa"}]}`,
		"a key":   `{"metadata": {"s\\u006b-aaaaaaaa": 1}}`,
		"a value": `{"x": [1, "token s\\u006b-aaaaaaaa"]}`,
	} {
		assert.Empty(t, prompt.Redacted([]byte(body), red), name)
	}
}

// Checking any input never fails, what passes encodes to bytes that pass
// again unchanged, and what passes, redacted, passes again.
func FuzzCheckNeverFails(f *testing.F) {
	f.Add([]byte(turn))
	f.Add([]byte(`{"model": "claude-haiku-5-5", "max_tokens": 1, "messages": [{"role": "user", "content": "hi"}]}`))
	f.Add([]byte(`{"a": [[[[{}]]]]}`))
	p := policy(f)
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := prompt.Check(headers(), body, p)
		if err != nil {
			return
		}
		again, err := prompt.Check(headers(), req.Encode(), p)
		if err != nil {
			t.Fatalf("the encoding of an accepted request is refused: %v", err)
		}
		if !bytes.Equal(again.Encode(), req.Encode()) {
			t.Fatalf("the encoding is not stable")
		}
		if req.Redact(red) != nil {
			return
		}
		if _, err := prompt.Check(headers(), req.Encode(), p); err != nil {
			t.Fatalf("a redacted request is refused: %v", err)
		}
	})
}

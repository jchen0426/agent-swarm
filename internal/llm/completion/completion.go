// Package completion reads the model vendor's answer to a Messages API
// request: the tokens it used, which the budget counts; the completion as
// its record keeps it, redacted (spec Sections 9.3 and 9.4); the thinking
// blocks the next request may send back; and how the vendor served it.
package completion

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
)

// maxFailureBytes caps the record of a failure that is not JSON.
const maxFailureBytes = 4096

// Completion is an answer, read for the budget, the record and the next
// request.
type Completion struct {
	Usage cost.Usage
	// Record is the answer's content, stop reason and stop sequence, every
	// string, key and number redacted, as JSON. It is "" when an escape
	// hides a match in it: redaction cannot change such a match, and no
	// record holds one.
	Record string
	// Thinking holds the hashes of the answer's thinking blocks, which the
	// next request of the session may send back unchanged.
	Thinking [][32]byte
	// Details is how the vendor served the answer, as JSON: its id, the
	// model, the service tier, the inference region, the thinking tokens
	// among the output, and any use of the vendor's own tools.
	Details string
}

// Read reads an answer the vendor sent with status 200. An answer whose
// token counts cannot be read is an error: the budget would count nothing
// for it.
func Read(body []byte, red *redact.Redactor) (*Completion, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("the answer is not a JSON object")
	}
	usage, ok := doc["usage"].(map[string]any)
	if !ok {
		return nil, errors.New("the answer reports no usage")
	}
	u, err := read(usage)
	if err != nil {
		return nil, err
	}
	c := &Completion{Usage: u}
	record := map[string]any{"content": doc["content"], "stop_reason": doc["stop_reason"], "stop_sequence": doc["stop_sequence"]}
	if redacted := red.Value(record); !red.EscapedValue(redacted) {
		c.Record = string(encode(redacted))
	}
	if content, ok := doc["content"].([]any); ok {
		for _, b := range content {
			if block, ok := b.(map[string]any); ok {
				if h, ok := prompt.ThinkingHash(block); ok {
					c.Thinking = append(c.Thinking, h)
				}
			}
		}
	}
	var thinkingTokens any
	if d, ok := usage["output_tokens_details"].(map[string]any); ok {
		thinkingTokens = d["thinking_tokens"]
	}
	c.Details = string(encode(map[string]any{
		"id": doc["id"], "model": doc["model"], "service_tier": usage["service_tier"], "inference_geo": usage["inference_geo"],
		"thinking_tokens": thinkingTokens, "server_tool_use": usage["server_tool_use"],
	}))
	return c, nil
}

// read reads the token counts. Cache writes never count below the total
// the vendor reports: without a breakdown, and beyond it, they count as
// one-hour writes, the dearer.
func read(usage map[string]any) (cost.Usage, error) {
	var u cost.Usage
	var written int64
	counts := []struct {
		name     string
		to       *int64
		required bool
	}{
		{"input_tokens", &u.Input, true}, {"output_tokens", &u.Output, true},
		{"cache_read_input_tokens", &u.CacheRead, false}, {"cache_creation_input_tokens", &written, false},
	}
	for _, c := range counts {
		n, err := count(usage, c.name, c.required)
		if err != nil {
			return cost.Usage{}, err
		}
		*c.to = n
	}
	breakdown, ok := usage["cache_creation"].(map[string]any)
	if !ok {
		u.CacheWrite1h = written
		return u, nil
	}
	m5, err := count(breakdown, "ephemeral_5m_input_tokens", false)
	if err != nil {
		return cost.Usage{}, err
	}
	h1, err := count(breakdown, "ephemeral_1h_input_tokens", false)
	if err != nil {
		return cost.Usage{}, err
	}
	u.CacheWrite5m, u.CacheWrite1h = m5, h1+max(written-m5-h1, 0)
	return u, nil
}

// count reads one token count: a whole number from 0 to cost.MaxCount.
func count(obj map[string]any, name string, required bool) (int64, error) {
	v, ok := obj[name]
	if !ok || v == nil {
		if required {
			return 0, errors.New("the answer reports no " + name)
		}
		return 0, nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, errors.New("the answer's " + name + " is not a number")
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || i < 0 || i > cost.MaxCount {
		return 0, errors.New("the answer's " + name + " is not a count of tokens")
	}
	return i, nil
}

// Failure returns what the vendor answered a failed call with, for its
// record: JSON with every string, key and number redacted, other text
// redacted and cut short. It is "" for bytes that are not text, and for a
// body in which an escape hides a match, which redaction cannot change.
func Failure(body []byte, red *redact.Redactor) string {
	if s := prompt.Redacted(body, red); s != "" {
		return s
	}
	if !utf8.Valid(body) {
		return ""
	}
	s := red.Text(string(body))
	if red.Escaped(s) {
		return ""
	}
	if len(s) > maxFailureBytes {
		s = strings.ToValidUTF8(s[:maxFailureBytes], "") + "…"
	}
	return s
}

func encode(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

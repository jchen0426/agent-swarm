// Package prompt reads, checks, redacts and encodes a Messages API request,
// as the LLM gateway lets one through to the model vendor (spec Section 9.3;
// ADR-007). The allowlist is what the platform's runtime sends (ADR-006):
// text, the tools the client runs, their calls and results, the thinking
// blocks of the model's own answers, and a cache that lives five minutes.
// Everything else is refused: tools the vendor runs, content with a source
// of any kind, fields and beta headers outside the list. A request is read
// strictly, so that no two readers can read it in two ways, and the gateway
// forwards its own encoding of what it checked, never the client's bytes.
package prompt

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
)

// maxDepth is how deeply a request may nest objects and arrays.
const maxDepth = 64

// Policy is what the requests of one session may hold.
type Policy struct {
	// Model is the model the agent's configuration names.
	Model string
	// MaxTokens caps max_tokens.
	MaxTokens int64
	// Version is the anthropic-version the gateway sends.
	Version string
	// Answered reports whether the gateway gave the session a thinking
	// block of this hash, in an answer: only such a block may come back,
	// so that no text the gateway did not redact reaches the vendor in one.
	Answered func(hash [32]byte) bool
	// Called reports whether the gateway gave the session a tool call of
	// this id, in an answer. The vendor wrote such an id, so it is no
	// channel for the agent's text: it may come back, with the call's
	// result, whatever a redaction rule matches in it. Names are checked
	// all the same.
	Called func(id string) bool
	// Hides reports whether a value of the client's holds, on its own, what
	// a redaction rule hides: a refusal quotes no such value (Quote). With
	// no Hides, a refusal quotes no value at all.
	Hides func(s string) bool
}

// Refusal says why a request is refused.
type Refusal struct {
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

func refuse(format string, args ...any) error {
	return &Refusal{Reason: fmt.Sprintf(format, args...)}
}

// Request is a request that passed every check.
type Request struct {
	doc       map[string]any
	maxTokens int64
	called    func(id string) bool
	// redactedBy is the redactor of the last Redact that passed, which
	// Record needs.
	redactedBy *redact.Redactor
}

// Model returns the model the request names.
func (r *Request) Model() string { return r.doc["model"].(string) }

// MaxTokens returns the request's max_tokens.
func (r *Request) MaxTokens() int64 { return r.maxTokens }

// Check reads a request and its headers and checks them against the
// allowlist and the session's policy. Headers other than anthropic-version,
// anthropic-beta and content-encoding are not checked: the gateway forwards
// none of them.
func Check(header map[string][]string, body []byte, p *Policy) (*Request, error) {
	if err := checkHeaders(header, p); err != nil {
		return nil, err
	}
	v, err := read(body, p.Hides)
	if err != nil {
		return nil, err
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, refuse("the request is not a JSON object")
	}
	r := &Request{doc: doc, called: p.Called}
	for _, key := range []string{"model", "max_tokens", "messages"} {
		if _, ok := doc[key]; !ok {
			return nil, refuse("the request has no %s", key)
		}
	}
	for _, key := range sortedKeys(doc) {
		check, ok := fields[key]
		if !ok {
			return nil, refuse("the field %s is not allowed", Quote(key, p.Hides))
		}
		if err := check(r, p, doc[key]); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// fields are the top-level fields a request may hold, each with its check:
// the fields the runtime sends.
var fields = map[string]func(r *Request, p *Policy, v any) error{
	"model": func(_ *Request, p *Policy, v any) error {
		if s, ok := v.(string); !ok || s != p.Model {
			return refuse("the model is not the agent's: %s", quoteValue(v, p.Hides))
		}
		return nil
	},
	"max_tokens": func(r *Request, p *Policy, v any) error {
		n, ok := v.(json.Number)
		if !ok || !wholeNumber.MatchString(string(n)) {
			return refuse("max_tokens must be a whole number")
		}
		i, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil || i > p.MaxTokens {
			return refuse("max_tokens must be at most %d", p.MaxTokens)
		}
		r.maxTokens = i
		return nil
	},
	"messages": func(_ *Request, p *Policy, v any) error { return checkMessages(v, p) },
	"system":   func(_ *Request, p *Policy, v any) error { return checkContent(v, "system", textOnly, p) },
	"tools":    func(_ *Request, p *Policy, v any) error { return checkTools(v, p) },
}

var (
	// wholeNumber is a positive whole number written without a sign, a
	// point or an exponent.
	wholeNumber = regexp.MustCompile(`^[1-9]\d{0,18}$`)
	// name is what an id or a tool's name may be: never a channel for text.
	name = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// blocks lists the content blocks each place allows.
type blocks []string

var (
	textOnly  = blocks{"text"}
	userKinds = blocks{"text", "tool_result"}
	assistant = blocks{"text", "tool_use", "thinking", "redacted_thinking"}
)

func checkMessages(v any, p *Policy) error {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return refuse("messages must be a list of messages")
	}
	for i, m := range list {
		msg, ok := m.(map[string]any)
		if !ok {
			return refuse("message %d is not an object", i)
		}
		where := fmt.Sprintf("message %d", i)
		if err := only(msg, where, p, "role", "content"); err != nil {
			return err
		}
		kinds := userKinds
		switch msg["role"] {
		case "user":
		case "assistant":
			kinds = assistant
		default:
			return refuse("message %d has no role of user or assistant", i)
		}
		if err := checkContent(msg["content"], where, kinds, p); err != nil {
			return err
		}
	}
	return nil
}

// checkContent checks content that is a string or a list of blocks of the
// kinds allowed.
func checkContent(v any, where string, kinds blocks, p *Policy) error {
	if _, ok := v.(string); ok {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return refuse("the content of %s must be text or a list of blocks", where)
	}
	for i, b := range list {
		block, ok := b.(map[string]any)
		if !ok {
			return refuse("block %d of %s is not an object", i, where)
		}
		typ, _ := block["type"].(string)
		if !slices.Contains(kinds, typ) {
			return refuse("block %d of %s is of a kind not allowed: %s", i, where, quoteValue(block["type"], p.Hides))
		}
		if err := checkBlock(block, typ, fmt.Sprintf("block %d of %s", i, where), p); err != nil {
			return err
		}
	}
	return nil
}

func checkBlock(block map[string]any, typ, where string, p *Policy) error {
	var err error
	switch typ {
	case "text":
		err = checkText(block, where, p)
	case "tool_use":
		err = checkToolUse(block, where, p)
	case "tool_result":
		err = checkToolResult(block, where, p)
	case "thinking", "redacted_thinking":
		return checkThinking(block, where, p)
	}
	if err != nil {
		return err
	}
	return cacheControl(block, where, p)
}

func checkText(block map[string]any, where string, p *Policy) error {
	if err := only(block, where, p, "type", "text", "cache_control"); err != nil {
		return err
	}
	if !isString(block["text"]) {
		return refuse("%s has no text", where)
	}
	return nil
}

func checkToolUse(block map[string]any, where string, p *Policy) error {
	if err := only(block, where, p, "type", "id", "name", "input", "caller", "cache_control"); err != nil {
		return err
	}
	if !named(block["id"]) || !named(block["name"]) {
		return refuse("%s needs an id and a name of letters, digits, _ and -", where)
	}
	if _, ok := block["input"].(map[string]any); !ok {
		return refuse("%s needs an input object", where)
	}
	if c, ok := block["caller"]; ok {
		caller, isObject := c.(map[string]any)
		if !isObject || len(caller) != 1 || caller["type"] != "direct" {
			return refuse("the caller of %s may only be the model itself", where)
		}
	}
	return nil
}

func checkToolResult(block map[string]any, where string, p *Policy) error {
	if err := only(block, where, p, "type", "tool_use_id", "content", "is_error", "cache_control"); err != nil {
		return err
	}
	if !named(block["tool_use_id"]) {
		return refuse("%s needs a tool_use_id of letters, digits, _ and -", where)
	}
	if c, ok := block["content"]; ok {
		if err := checkContent(c, where, textOnly, p); err != nil {
			return err
		}
	}
	if e, ok := block["is_error"]; ok {
		if _, isBool := e.(bool); !isBool {
			return refuse("is_error of %s must be true or false", where)
		}
	}
	return nil
}

// checkThinking checks a thinking block of an answer that comes back, as
// the vendor needs it: unchanged. It must be one the gateway gave the
// session.
func checkThinking(block map[string]any, where string, p *Policy) error {
	hash, ok := ThinkingHash(block)
	if !ok || len(block) != len(thinkingFields[block["type"].(string)]) {
		return refuse("%s is not a thinking block as an answer holds one", where)
	}
	if p.Answered == nil || !p.Answered(hash) {
		return refuse("%s is not a thinking block the gateway gave this session", where)
	}
	return nil
}

// thinkingFields are the fields of a thinking block, and of a redacted
// one, that a request sends back.
var thinkingFields = map[string][]string{
	"thinking":          {"type", "thinking", "signature"},
	"redacted_thinking": {"type", "data"},
}

// ThinkingHash returns the hash of a thinking or redacted thinking block,
// over the fields a request sends back, and false for any other block. A
// field an answer holds besides them is not hashed, so that one the vendor
// adds does not keep the block from going back.
func ThinkingHash(block map[string]any) ([32]byte, bool) {
	typ, _ := block["type"].(string)
	keys, ok := thinkingFields[typ]
	if !ok {
		return [32]byte{}, false
	}
	fields := make(map[string]any, len(keys))
	for _, k := range keys {
		if !isString(block[k]) {
			return [32]byte{}, false
		}
		fields[k] = block[k]
	}
	return sha256.Sum256(encode(fields)), true
}

// ToolCallID returns the id of a tool call of the model's, in an answer, when
// a request can send it back: an id is letters, digits, _ and -, at most 128
// bytes, as Check requires of a request's ids. It returns false for any other
// block, and for an id no request can hold, which could never come back.
func ToolCallID(block map[string]any) (string, bool) {
	id, _ := block["id"].(string)
	if block["type"] != "tool_use" || !named(id) {
		return "", false
	}
	return id, true
}

// cacheControl checks a cache breakpoint: an ephemeral entry that lives
// five minutes, the runtime's.
func cacheControl(obj map[string]any, where string, p *Policy) error {
	v, ok := obj["cache_control"]
	if !ok {
		return nil
	}
	cc, ok := v.(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		return refuse("the cache_control of %s must be ephemeral", where)
	}
	if err := only(cc, "the cache_control of "+where, p, "type", "ttl"); err != nil {
		return err
	}
	if ttl, ok := cc["ttl"]; ok && ttl != "5m" {
		return refuse("the cache_control of %s must live five minutes", where)
	}
	return nil
}

func checkTools(v any, p *Policy) error {
	list, ok := v.([]any)
	if !ok {
		return refuse("tools must be a list")
	}
	for i, t := range list {
		tool, ok := t.(map[string]any)
		if !ok {
			return refuse("tool %d is not an object", i)
		}
		where := fmt.Sprintf("tool %d", i)
		if typ, ok := tool["type"]; ok {
			return refuse("tool %d has a type, as a tool the vendor runs does: %s", i, quoteValue(typ, p.Hides))
		}
		if err := only(tool, where, p, "name", "description", "input_schema", "cache_control"); err != nil {
			return err
		}
		if !named(tool["name"]) {
			return refuse("tool %d needs a name of letters, digits, _ and -", i)
		}
		if d, ok := tool["description"]; ok && !isString(d) {
			return refuse("the description of tool %d must be text", i)
		}
		if _, ok := tool["input_schema"].(map[string]any); !ok {
			return refuse("tool %d needs an input_schema object", i)
		}
		if err := cacheControl(tool, where, p); err != nil {
			return err
		}
	}
	return nil
}

// only refuses an object that holds a key other than those named.
func only(obj map[string]any, where string, p *Policy, keys ...string) error {
	for _, k := range sortedKeys(obj) {
		if !slices.Contains(keys, k) {
			return refuse("%s holds a field not allowed: %s", where, Quote(k, p.Hides))
		}
	}
	return nil
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

// named reports an id or a name: letters, digits, _ and -.
func named(v any) bool {
	s, ok := v.(string)
	return ok && name.MatchString(s)
}

// Quote writes text a client sent, for a refusal: in quotes when it is whole,
// at most 64 bytes, strconv writes it with no escape, and hides reports that
// no redaction rule hides it on its own; otherwise only how long it is. With
// no hides to ask, it quotes nothing. A refusal is stored redacted as written,
// as a whole sentence: a cut or escaped copy of text could hide from the
// rules a match the whole text holds, and a rule anchored to a whole value,
// such as ^AKIA[A-Z0-9]{16}$, matches the value on its own but not inside the
// sentence.
func Quote(s string, hides func(string) bool) string {
	if len(s) <= 64 && hides != nil {
		if q := strconv.Quote(s); q == `"`+s+`"` && !hides(s) {
			return q
		}
	}
	return fmt.Sprintf("a value of %d bytes", len(s))
}

// QuoteParts writes text a client sent that is made of parts, as a path, a
// query or a header's list is, for a refusal: as Quote writes it, and only
// when hides finds nothing in any part on its own, since a rule anchored to
// a whole value matches the part, not the text the parts make. Parts are
// what separators and white space divide.
func QuoteParts(s, separators string, hides func(string) bool) string {
	if hides != nil {
		parts := strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(separators, r) })
		if slices.ContainsFunc(parts, hides) {
			return fmt.Sprintf("a value of %d bytes", len(s))
		}
	}
	return Quote(s, hides)
}

// quoteValue writes a JSON value of the client's, for a refusal: a string as
// Quote writes it, and any other value by its kind only. A value written out
// whole, such as a list of strings, is a string composed from the client's
// values, and a rule anchored to a whole value would check it, not each
// value on its own.
func quoteValue(v any, hides func(string) bool) string {
	switch v := v.(type) {
	case string:
		return Quote(v, hides)
	case bool:
		return "true or false"
	case json.Number:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return "none"
}

func checkHeaders(header map[string][]string, p *Policy) error {
	for name, values := range header {
		switch {
		case strings.EqualFold(name, "anthropic-version"):
			for _, v := range values {
				if v != p.Version {
					return refuse("anthropic-version %s is not the gateway's, %s", Quote(v, p.Hides), p.Version)
				}
			}
		case strings.EqualFold(name, "anthropic-beta") && len(values) > 0:
			// Only the first value is quoted, part by part: values joined
			// would be a string composed from them (quoteValue).
			return refuse("no anthropic-beta is allowed: %s", QuoteParts(values[0], ",", p.Hides))
		case strings.EqualFold(name, "content-encoding"):
			for _, v := range values {
				if !strings.EqualFold(strings.TrimSpace(v), "identity") {
					return refuse("a request body is sent as it is, not as %s", Quote(v, p.Hides))
				}
			}
		}
	}
	return nil
}

// Redact redacts everything the model reads with red: text, the system
// prompt, tool results, every string, key and number of a tool call's input
// and of a tool's input schema, and tools' descriptions. A tool's result that
// is one JSON object or array is redacted as the MCP gateway redacts JSON
// output, value by value, and forwarded in the gateway's encoding. Each part
// is redacted on its own, so that what was forwarded once is forwarded again
// the same. Types, cache control and the model stay as written, and
// thinking blocks too: the vendor needs them back unchanged, and the
// gateway gave them. Ids and names stay as written as well, since a
// conversation pairs a tool call with its result by them, so a request
// whose id or name holds what red hides is refused, unless the id is one
// the gateway gave the session for a tool call (Policy.Called). A part whose
// escapes may hide a match is refused too, since redaction cannot change it
// safely: one whose escapes hide a match, or nest deeper than redaction
// decodes, which cannot be checked. The refusal names the part, never its
// text. A refused request is left as it was.
func (r *Request) Redact(red *redact.Redactor) error {
	if err := r.checkNames(red); err != nil {
		return err
	}
	// The redaction runs on a copy, which replaces the request only when
	// every part has passed.
	doc := cloneJSON(r.doc).(map[string]any)
	if s, ok := doc["system"]; ok {
		v, err := redactContent(s, red, "the system prompt")
		if err != nil {
			return err
		}
		doc["system"] = v
	}
	for i, m := range doc["messages"].([]any) {
		msg := m.(map[string]any)
		v, err := redactContent(msg["content"], red, fmt.Sprintf("message %d", i))
		if err != nil {
			return err
		}
		msg["content"] = v
	}
	if tools, ok := doc["tools"].([]any); ok {
		for i, t := range tools {
			tool := t.(map[string]any)
			if d, ok := tool["description"].(string); ok {
				v, err := redactText(d, red, fmt.Sprintf("the description of tool %d", i))
				if err != nil {
					return err
				}
				tool["description"] = v
			}
			schema := red.Value(tool["input_schema"])
			if red.EscapedValue(schema) {
				return refuse("the input schema of tool %d holds escapes that may hide what a redaction rule matches, and cannot be redacted", i)
			}
			tool["input_schema"] = schema
		}
	}
	r.doc, r.redactedBy = doc, red
	return nil
}

// checkNames refuses an id or a name that holds what red hides: tools'
// names, and the ids and names of tool calls and tool results. It lets
// through an id of a tool call or of a result that the gateway gave the
// session.
func (r *Request) checkNames(red *redact.Redactor) error {
	hides := func(v any) bool {
		s, ok := v.(string)
		return ok && red.Text(s) != s
	}
	gave := func(v any) bool {
		id, ok := v.(string)
		return ok && r.called != nil && r.called(id)
	}
	for i, m := range r.doc["messages"].([]any) {
		blocks, _ := m.(map[string]any)["content"].([]any)
		for j, b := range blocks {
			block := b.(map[string]any)
			for _, k := range []string{"id", "name", "tool_use_id"} {
				if hides(block[k]) && (k == "name" || !gave(block[k])) {
					return refuse("the %s of message %d's block %d holds what a redaction rule hides, and cannot be redacted", k, i, j)
				}
			}
		}
	}
	tools, _ := r.doc["tools"].([]any)
	for i, t := range tools {
		if hides(t.(map[string]any)["name"]) {
			return refuse("the name of tool %d holds what a redaction rule hides, and cannot be redacted", i)
		}
	}
	return nil
}

// redactContent redacts content that is a string or a list of blocks, and
// returns the redacted content. A list is changed in place, so Redact passes
// it a copy. where names the content in a refusal. A block of a kind it does
// not name is refused, so that a kind the allowlist admits without a case
// here never reaches the vendor unredacted.
func redactContent(v any, red *redact.Redactor, where string) (any, error) {
	if s, ok := v.(string); ok {
		return redactText(s, red, where)
	}
	for i, b := range v.([]any) {
		block := b.(map[string]any)
		here := fmt.Sprintf("block %d of %s", i, where)
		switch block["type"] {
		case "text":
			s, err := redactText(block["text"].(string), red, here)
			if err != nil {
				return nil, err
			}
			block["text"] = s
		case "tool_use":
			input := red.Value(block["input"])
			if red.EscapedValue(input) {
				return nil, refuse("the input of %s holds escapes that may hide what a redaction rule matches, and cannot be redacted", here)
			}
			block["input"] = input
		case "tool_result":
			if c, ok := block["content"]; ok {
				out, err := redactResult(c, red, "the content of "+here)
				if err != nil {
					return nil, err
				}
				block["content"] = out
			}
		case "thinking", "redacted_thinking":
			// The vendor needs a thinking block back unchanged, and Check
			// let it through only as the gateway gave it: it is not redacted.
		default:
			return nil, refuse("%s is of a kind the gateway cannot redact", here)
		}
	}
	return v, nil
}

// redactResult redacts the content of a tool's result, a string or a list of
// text blocks, each text as a tool's output (redactOutput), and returns it. A
// list is changed in place. A block other than text is refused, as
// redactContent refuses a kind it does not name.
func redactResult(v any, red *redact.Redactor, where string) (any, error) {
	if s, ok := v.(string); ok {
		return redactOutput(s, red, where)
	}
	for i, b := range v.([]any) {
		block := b.(map[string]any)
		here := fmt.Sprintf("block %d of %s", i, where)
		if block["type"] != "text" {
			return nil, refuse("%s is of a kind the gateway cannot redact", here)
		}
		s, err := redactOutput(block["text"].(string), red, here)
		if err != nil {
			return nil, err
		}
		block["text"] = s
	}
	return v, nil
}

// redactOutput redacts a text of a tool's result. A text that is exactly one
// JSON object or array is what the MCP gateway shows of a tool's JSON output,
// or a board snapshot, and is redacted as that gateway redacts JSON output:
// value by value, each string, key and number at its own level and checked
// there for escapes that may hide a match, then written in the gateway's own
// encoding. That is what the MCP gateway showed. Read as text, its escapes
// would be checked a level above its values: an escaped quote in a value
// would close a key that the quote before it opens, and complete a member
// that neither level holds, and a run of backslashes would gain a level and
// nest too deep to check. Any other text is redacted as text (redactText).
func redactOutput(s string, red *redact.Redactor, where string) (string, error) {
	doc, ok := document(s)
	if !ok {
		return redactText(s, red, where)
	}
	v := red.Value(doc)
	if red.EscapedValue(v) {
		return "", refuse("%s holds escapes that may hide what a redaction rule matches, and cannot be redacted", where)
	}
	return string(encode(v)), nil
}

// document decodes s when it is exactly one JSON object or array, as the MCP
// gateway reads a tool's output, with numbers as written.
func document(s string) (any, bool) {
	if t := strings.TrimSpace(s); t == "" || (t[0] != '{' && t[0] != '[') {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return v, true
}

// redactText redacts a text, and refuses it when its escapes may hide a
// match: when they hide one, or nest deeper than redaction decodes, since
// redaction cannot change such text safely. where names the text in a
// refusal.
func redactText(s string, red *redact.Redactor, where string) (string, error) {
	out := red.Text(s)
	if red.Escaped(out) {
		return "", refuse("%s holds escapes that may hide what a redaction rule matches, and cannot be redacted", where)
	}
	return out, nil
}

// cloneJSON copies the objects and arrays of a decoded JSON value, so that
// redacting the copy leaves the value as it was.
func cloneJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = cloneJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneJSON(e)
		}
		return out
	}
	return v
}

// Pin sets fields the gateway sends with every request: the service tier,
// and the inference region when one is given, so that a call is billed as
// the model's card prices it.
func (r *Request) Pin(serviceTier, inferenceGeo string) {
	r.doc["service_tier"] = serviceTier
	if inferenceGeo != "" {
		r.doc["inference_geo"] = inferenceGeo
	}
}

// Encode returns the request in the gateway's own encoding: compact JSON,
// keys sorted, numbers as written.
func (r *Request) Encode() []byte { return encode(r.doc) }

func encode(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// Record returns what the record of a forwarded request holds, once Redact
// has passed it and Pin has pinned it: the request in the gateway's encoding,
// with every string, key and number redacted as Redacted redacts a body, so
// that its thinking blocks, and ids the gateway gave the session, are
// redacted too, though they reach the vendor as the gateway gave them. The
// texts Redact read on their own (the system prompt, a message's text, every
// text block, a tool's result and a tool's description) are kept as they
// are forwarded: Redact redacted each, and checked it for escapes that may
// hide a match. Read again as values of the request's JSON, each would be
// checked with its key, as "text=..." or "content=...", which is the
// request's shape, not the client's text, and a text both gateways passed
// could not be recorded. Record is "" when an escape may hide a match
// anywhere else, as Redacted is, so that no record holds one; the gateway
// refuses such a request. It is "" too unless the last Redact to pass the
// request used red: the texts are kept as Redact left them, unredacted
// before Redact runs.
func (r *Request) Record(red *redact.Redactor) string {
	if red == nil || r.redactedBy != red {
		return ""
	}
	doc := cloneJSON(r.doc).(map[string]any)
	if s, ok := doc["system"]; ok {
		doc["system"] = forwardedTexts(s)
	}
	for _, m := range doc["messages"].([]any) {
		msg := m.(map[string]any)
		msg["content"] = forwardedTexts(msg["content"])
	}
	if tools, ok := doc["tools"].([]any); ok {
		for _, t := range tools {
			tool := t.(map[string]any)
			if d, ok := tool["description"].(string); ok {
				tool["description"] = forwarded(d)
			}
		}
	}
	redacted := red.Value(doc)
	if red.EscapedValue(redacted) {
		return ""
	}
	return string(encode(redacted))
}

// forwardedTexts marks the texts of a content as forwarded: the content when
// it is text, each text block's text, and each tool's result.
func forwardedTexts(content any) any {
	switch c := content.(type) {
	case string:
		return forwarded(c)
	case []any:
		for _, b := range c {
			block := b.(map[string]any)
			switch block["type"] {
			case "text":
				block["text"] = forwarded(block["text"].(string))
			case "tool_result":
				if content, ok := block["content"]; ok {
					block["content"] = forwardedTexts(content)
				}
			}
		}
	}
	return content
}

// forwarded is a text that a record keeps as it was forwarded. Redaction
// reads only what a decoded JSON value holds, so it leaves this one as it is,
// and encode writes it as the string it is.
type forwarded string

// MarshalJSON writes the text as encode writes a string.
func (f forwarded) MarshalJSON() ([]byte, error) { return encode(string(f)), nil }

// Redacted returns what a record holds of a request body: its JSON with
// every string, key and number redacted. The records it serves are those of
// refused requests, and of the completions and the failure bodies the vendor
// sends back; Record serves forwarded requests. It is "" when the body is not
// one JSON document in valid UTF-8, or when a match an escape hides is left
// in it, so that no record holds such a match. A key it holds twice keeps
// its last value.
func Redacted(body []byte, red *redact.Redactor) string {
	if !utf8.Valid(body) {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return ""
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ""
	}
	redacted := red.Value(v)
	if red.EscapedValue(redacted) {
		return ""
	}
	return string(encode(redacted))
}

// read reads one JSON document strictly: valid UTF-8, no key twice in one
// object, no deeper than maxDepth, nothing after it. Numbers stay as
// written. hides is the policy's, for a refusal that quotes a key.
func read(body []byte, hides func(string) bool) (any, error) {
	if !utf8.Valid(body) {
		return nil, refuse("the request is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := value(dec, 0, hides)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, refuse("the request holds data after its end")
	}
	return v, nil
}

func value(dec *json.Decoder, depth int, hides func(string) bool) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, refuse("the request is not JSON")
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	if depth == maxDepth {
		return nil, refuse("the request nests deeper than %d levels", maxDepth)
	}
	if d == '[' {
		list := []any{}
		for dec.More() {
			v, err := value(dec, depth+1, hides)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return list, end(dec)
	}
	obj := map[string]any{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, refuse("the request is not JSON")
		}
		key := tok.(string)
		if _, twice := obj[key]; twice {
			return nil, refuse("the request holds the key %s twice in one object", Quote(key, hides))
		}
		v, err := value(dec, depth+1, hides)
		if err != nil {
			return nil, err
		}
		obj[key] = v
	}
	return obj, end(dec)
}

// end reads the delimiter that closes an object or an array.
func end(dec *json.Decoder) error {
	if _, err := dec.Token(); err != nil {
		return refuse("the request is not JSON")
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Package redact applies redaction rules (spec Section 9.4) to what the
// gateway stores and shows: outputs, errors and arguments. Rules apply to
// decoded text, so an escape cannot hide a match: patterns replace what they
// match in every string, key and number of a decoded JSON value, and in each
// member written as key=value, so that a rule for "password: ..." also finds
// {"password": "..."}; paths replace whole values. In text, each member
// written there as JSON, Python or Ruby write one, as in a log line or a JSON
// line, gets the same member rule, and each quoted value written there is
// redacted as text of its own, decoded.
package redact

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

// maxDepth is how far down redaction decodes: levels of JSON escapes, and
// string values written inside string values.
const maxDepth = 4

// writtenKey finds a key written in text the way JSON, Python and Ruby
// write one: a quoted key, then a colon or "=>". Group 1 or 2 is the key, in
// double or single quotes. A key does not span a line.
var writtenKey = regexp.MustCompile(`(?:"((?:[^"\\\n]|\\.)*)"|'((?:[^'\\\n]|\\.)*)')[ \t]*(?::|=>)[ \t]*`)

// writtenScalar finds a number or a boolean at the start of text.
var writtenScalar = regexp.MustCompile(`^(?:-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|True|False)\b`)

// Rule is one redaction rule.
type Rule struct {
	Patterns    []*regexp.Regexp
	Paths       []selector.Selector
	Replacement string
}

// FromConfig returns the rule a configured redaction rule holds.
func FromConfig(r *config.RedactionRule) Rule {
	return Rule{Patterns: r.Patterns(), Paths: r.Paths(), Replacement: r.Replacement()}
}

// Redactor applies rules in order.
type Redactor struct {
	rules []Rule
}

// New returns a redactor of rules, applied in the order given.
func New(rules ...Rule) *Redactor { return &Redactor{rules: rules} }

// HasPaths reports whether any rule has paths, which only a JSON value can
// answer.
func (r *Redactor) HasPaths() bool {
	return slices.ContainsFunc(r.rules, func(rule Rule) bool { return len(rule.Paths) > 0 })
}

// Replacements returns each rule's replacement text.
func (r *Redactor) Replacements() []string {
	out := make([]string, 0, len(r.rules))
	for _, rule := range r.rules {
		out = append(out, rule.Replacement)
	}
	return out
}

// Text replaces every match of every pattern in s. Before that, each member
// written in s gets the member rule, and each quoted value written there is
// redacted as text of its own, decoded, and written again as JSON.
func (r *Redactor) Text(s string) string { return r.text(s, 0) }

func (r *Redactor) text(s string, depth int) string {
	if len(r.rules) == 0 {
		return s
	}
	s = r.members(s, depth)
	for _, rule := range r.rules {
		for _, re := range rule.Patterns {
			s = re.ReplaceAllLiteralString(s, rule.Replacement)
		}
	}
	return s
}

// members applies the member rule to each member written in s, and redacts
// each quoted value written there, down to maxDepth levels. Keys are found
// first, each on its own, so a value that runs on, as raw JSON inside a
// string or JSON cut inside a string, never hides the key after it: a value
// ends at the next key at the latest. A value redaction does not change
// keeps its spelling.
func (r *Redactor) members(s string, depth int) string {
	keys := writtenKey.FindAllStringSubmatchIndex(s, -1)
	if keys == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for i, m := range keys {
		limit := len(s)
		if i+1 < len(keys) {
			limit = keys[i+1][0] + 1
		}
		from, to, value, quoted := valueAt(s, m[1], limit)
		if to < 0 {
			continue
		}
		redacted, ok := r.member(unescape(group(s, m, 1)+group(s, m, 2)), value)
		if !ok {
			if !quoted || depth == maxDepth {
				continue
			}
			if redacted = r.text(value, depth+1); redacted == value {
				continue
			}
		}
		b.WriteString(s[last:from])
		b.WriteString(quote(redacted))
		last = to
	}
	b.WriteString(s[last:])
	return b.String()
}

// valueAt reads the value written at s[start:], before limit: a quoted
// string, decoded, or a number or a boolean. It returns where the value is
// written; to is -1 when there is none. A quoted value that does not close
// before limit was cut short, and runs to limit, short of the next key's
// opening quote.
func valueAt(s string, start, limit int) (from, to int, value string, quoted bool) {
	if start >= limit {
		return 0, -1, "", false
	}
	if q := s[start]; q == '"' || q == '\'' {
		for j := start + 1; j < limit; j++ {
			switch s[j] {
			case '\\':
				j++
			case q:
				return start, j + 1, unescape(s[start+1 : j]), true
			}
		}
		end := limit
		if limit < len(s) {
			end--
		}
		if end <= start {
			return 0, -1, "", false
		}
		return start, end, unescape(s[start+1 : end]), true
	}
	if v := writtenScalar.FindString(s[start:limit]); v != "" {
		return start, start + len(v), v, false
	}
	return 0, -1, "", false
}

// group returns the text of submatch g of m in s, or "" when it did not take
// part in the match.
func group(s string, m []int, g int) string {
	if m[2*g] < 0 {
		return ""
	}
	return s[m[2*g]:m[2*g+1]]
}

// Escaped reports whether text, with its JSON escapes decoded, holds what
// redaction would change: a match an escape hides from the text as written,
// as in "dsn":"postgres:\/\/u:pw@h". Such text cannot be redacted safely.
// Escapes are decoded level by level, down to maxDepth levels; text that
// still holds escapes below that cannot be checked, and is reported too.
func (r *Redactor) Escaped(text string) bool {
	if len(r.rules) == 0 {
		return false
	}
	view := text
	for range maxDepth {
		next := unescape(view)
		if next == view {
			return false
		}
		if r.Text(next) != next {
			return true
		}
		view = next
	}
	return unescape(view) != view
}

// EscapedValue reports whether any string or key of v, a value Value has
// redacted, is Escaped.
func (r *Redactor) EscapedValue(v any) bool {
	switch t := v.(type) {
	case string:
		return r.Escaped(t)
	case map[string]any:
		for k, e := range t {
			if r.Escaped(k) || r.EscapedValue(e) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(t, r.EscapedValue)
	}
	return false
}

// Value redacts v, a value decoded from JSON with numbers as json.Number, and
// returns the result. Paths first replace the values they select. Patterns
// then apply to every string, key and number, and to each member with a
// scalar value written as key=value: a match that runs from the key into the
// value replaces the value whole. A number a pattern changes becomes a
// string. Keys that redaction makes equal are told apart by a suffix, " (2)",
// " (3)", in sorted order. v itself may change.
func (r *Redactor) Value(v any) any {
	for _, rule := range r.rules {
		for _, p := range rule.Paths {
			v = p.Replace(v, func(any) any { return rule.Replacement })
		}
	}
	return r.walk(v)
}

func (r *Redactor) walk(v any) any {
	switch t := v.(type) {
	case string:
		return r.Text(t)
	case json.Number:
		if s := r.Text(t.String()); s != t.String() {
			return s
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			key := r.Text(k)
			for n := 2; ; n++ {
				if _, taken := out[key]; !taken {
					break
				}
				key = fmt.Sprintf("%s (%d)", r.Text(k), n)
			}
			if text, ok := scalar(t[k]); ok {
				if replacement, ok := r.member(k, text); ok {
					out[key] = replacement
					continue
				}
			}
			out[key] = r.walk(t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = r.walk(t[i])
		}
		return t
	default:
		return v
	}
}

// scalar returns a string, number or boolean as text.
func scalar(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}

// member reports whether a pattern matches a member written as key=value
// in a way that reaches the value: its match needs both the key and the
// value, matching neither alone, or runs from the key past the equals sign
// into the value. It returns that rule's replacement, which replaces the
// value whole. A match within the key or within the value is the walk's to
// replace, in part.
func (r *Redactor) member(key, value string) (string, bool) {
	kv := key + "=" + value
	for _, rule := range r.rules {
		for _, re := range rule.Patterns {
			if !re.MatchString(kv) {
				continue
			}
			if !re.MatchString(key) && !re.MatchString(value) {
				return rule.Replacement, true
			}
			for _, m := range re.FindAllStringIndex(kv, -1) {
				if m[0] <= len(key) && m[1] > len(key)+1 {
					return rule.Replacement, true
				}
			}
		}
	}
	return "", false
}

// quote writes s as a JSON string, without escaping HTML characters.
func quote(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// unescape decodes JSON string escapes in s: \" \\ \/ \b \f \n \r \t and \uXXXX, with surrogate pairs,
// and \' as Python writes it. Other backslashes stay.
func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch c := s[i+1]; c {
		case '"', '\\', '/', '\'':
			b.WriteByte(c)
			i++
		case 'b', 'f', 'n', 'r', 't':
			b.WriteByte(map[byte]byte{'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}[c])
			i++
		case 'u':
			r, n := unicodeEscape(s[i:])
			if n == 0 {
				b.WriteByte(s[i])
				continue
			}
			b.WriteRune(r)
			i += n - 1
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// unicodeEscape decodes \uXXXX at the start of s, and a following low surrogate,
// returning the rune and the bytes used, or 0 bytes when there is none.
func unicodeEscape(s string) (r rune, n int) {
	hex := func(t string) (rune, bool) {
		if len(t) < 6 || t[0] != '\\' || t[1] != 'u' {
			return 0, false
		}
		v, err := strconv.ParseUint(t[2:6], 16, 16)
		return rune(v), err == nil
	}
	r1, ok := hex(s)
	if !ok {
		return 0, 0
	}
	if utf16.IsSurrogate(r1) {
		if r2, ok := hex(s[6:]); ok {
			if pair := utf16.DecodeRune(r1, r2); pair != 0xFFFD {
				return pair, 12
			}
		}
	}
	return r1, 6
}

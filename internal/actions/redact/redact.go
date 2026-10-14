// Package redact applies redaction rules (spec Section 9.4) to what the
// gateway stores and shows: outputs, errors and arguments. Rules apply to
// decoded text, so an escape cannot hide a match: patterns replace what they
// match in every string, key and number of a decoded JSON value, and in each
// member written as key=value, so that a rule for "password: ..." also finds
// {"password": "..."}; paths replace whole values. In text, each JSON member
// written there, as in a log line or a JSON line, gets the same member rule,
// and each string value written there is redacted as text of its own,
// decoded.
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

// jsonMember finds a JSON member written in text: a quoted key, a colon, and
// a quoted string, a number or a boolean. Neither string spans a line.
var jsonMember = regexp.MustCompile(`"((?:[^"\\\n]|\\.)*)"[ \t]*:[ \t]*(?:"((?:[^"\\\n]|\\.)*)"|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false)\b)`)

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

// Text replaces every match of every pattern in s. Before that, each JSON
// member written in s gets the member rule, and each string value written
// there is redacted as text of its own, decoded, and written again.
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

// members applies the member rule to each JSON member written in s, and
// redacts each string value written there, down to maxDepth levels. A value
// redaction does not change keeps its spelling.
func (r *Redactor) members(s string, depth int) string {
	found := jsonMember.FindAllStringSubmatchIndex(s, -1)
	if found == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range found {
		key := unescape(s[m[2]:m[3]])
		quoted := m[4] >= 0
		from, to, value := m[6], m[7], ""
		if quoted {
			from, to, value = m[4]-1, m[5]+1, unescape(s[m[4]:m[5]])
		} else {
			value = s[from:to]
		}
		redacted, ok := r.member(key, value)
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
// with a match that runs from the key past the equals sign into the value,
// and returns that rule's replacement, which replaces the value whole. A
// match within the key or within the value is the walk's to replace, in
// part.
func (r *Redactor) member(key, value string) (string, bool) {
	kv := key + "=" + value
	for _, rule := range r.rules {
		for _, re := range rule.Patterns {
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

// unescape decodes JSON string escapes in s: \" \\ \/ \b \f \n \r \t and \uXXXX, with surrogate pairs.
// Other backslashes stay.
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
		case '"', '\\', '/':
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

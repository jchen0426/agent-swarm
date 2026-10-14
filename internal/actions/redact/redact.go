// Package redact applies redaction rules (spec Section 9.4) to what the
// gateway stores and shows: outputs, errors and arguments. Rules apply to
// decoded text, so an escape cannot hide a match: patterns replace what they
// match in every string, key and number of a decoded JSON value, and in each
// member written as key=value, so that a rule for "password: ..." also finds
// {"password": "..."}; paths replace whole values.
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

// Text replaces every match of every pattern in s.
func (r *Redactor) Text(s string) string {
	for _, rule := range r.rules {
		for _, re := range rule.Patterns {
			s = re.ReplaceAllLiteralString(s, rule.Replacement)
		}
	}
	return s
}

// Escaped reports whether text, with its JSON escapes decoded, still holds a
// match. Text that is not one JSON document can carry escaped JSON, such as
// "dsn":"postgres:\/\/u:pw@h", which a pattern written for the decoded
// form does not find in the raw text; such text cannot be redacted safely.
func (r *Redactor) Escaped(text string) bool {
	view := unescape(text)
	if view == text {
		return false
	}
	for _, rule := range r.rules {
		for _, re := range rule.Patterns {
			if re.MatchString(view) {
				return true
			}
		}
	}
	return false
}

// Value redacts v, a value decoded from JSON with numbers as json.Number, and
// returns the result. Paths first replace the values they select. Patterns
// then apply to every string, key and number, and to each member with a
// scalar value written as key=value: a match that needs both replaces the
// value whole. A
// number a pattern changes becomes a string. Keys that redaction makes equal
// are told apart by a suffix, " (2)", " (3)", in sorted order. v itself may
// change.
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
			if replacement, ok := r.member(k, t[k]); ok {
				out[key] = replacement
				continue
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

// member reports whether a pattern matches a member with a scalar value only
// when written as key=value, matching neither the key nor the value alone,
// and returns that rule's replacement. A match within the key or within the
// value is the walk's to replace, in part.
func (r *Redactor) member(key string, v any) (string, bool) {
	var text string
	switch t := v.(type) {
	case string:
		text = t
	case json.Number:
		text = t.String()
	case bool:
		text = strconv.FormatBool(t)
	default:
		return "", false
	}
	kv := key + "=" + text
	for _, rule := range r.rules {
		for _, re := range rule.Patterns {
			if re.MatchString(kv) && !re.MatchString(key) && !re.MatchString(text) {
				return rule.Replacement, true
			}
		}
	}
	return "", false
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

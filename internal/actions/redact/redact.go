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
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

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

// Rule is one redaction rule. Literals are matched as text, folded as Fold
// folds it: in any case, and with any run of white space where a literal
// has one, in time linear in the text, however long the literal.
type Rule struct {
	Patterns    []*regexp.Regexp
	Paths       []selector.Selector
	Literals    []string
	Replacement string
}

// FromConfig returns the rule a configured redaction rule holds.
func FromConfig(r *config.RedactionRule) Rule {
	return Rule{Patterns: r.Patterns(), Paths: r.Paths(), Replacement: r.Replacement()}
}

// Redactor applies rules in order.
type Redactor struct {
	rules []Rule
	// needles holds each rule's literals, folded once, when the redactor
	// is made.
	needles [][]string
}

// New returns a redactor of rules, applied in the order given.
func New(rules ...Rule) *Redactor {
	r := &Redactor{rules: rules, needles: make([][]string, len(rules))}
	for i, rule := range rules {
		for _, lit := range rule.Literals {
			if n := Fold(lit); n != "" && !slices.Contains(r.needles[i], n) {
				r.needles[i] = append(r.needles[i], n)
			}
		}
	}
	return r
}

// Before returns a redactor that applies rules, then r's.
func (r *Redactor) Before(rules ...Rule) *Redactor {
	return New(append(slices.Clone(rules), r.rules...)...)
}

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

// isReplacement reports whether text is some rule's replacement.
func (r *Redactor) isReplacement(text string) bool {
	for _, rule := range r.rules {
		if rule.Replacement == text {
			return true
		}
	}
	return false
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
	for i, rule := range r.rules {
		if len(r.needles[i]) > 0 {
			s = replaceFolded(s, r.needles[i], rule.Replacement)
		}
		for _, re := range rule.Patterns {
			s = re.ReplaceAllLiteralString(s, rule.Replacement)
		}
	}
	return s
}

// Fold returns s as literals are compared: each letter in one case, lower
// case after upper, so that letters whose cases map unevenly, such as the
// long s and the Kelvin sign, meet their plain forms; each run of white
// space, as unicode.IsSpace finds it, as one space; and no white space at
// either end.
func Fold(s string) string {
	folded, _, _ := fold(s)
	return strings.TrimSpace(folded)
}

// replaceFolded replaces each occurrence in s of any of needles, folded
// literals, with with, in one pass from the left: at each point the
// occurrence that starts first, the longest where two start together, and
// never two that overlap. s is folded once, and each match is mapped back to
// the bytes of s it spans, so the rest of s stays as it was. A fold at most
// triples a text, so needles longer than that are skipped without folding
// it.
func replaceFolded(s string, needles []string, with string) string {
	shortest := len(needles[0])
	for _, n := range needles[1:] {
		shortest = min(shortest, len(n))
	}
	if 3*len(s) < shortest {
		return s
	}
	hay, starts, ends := fold(s)
	m := newMatcher(hay, needles)
	var b strings.Builder
	last, at := 0, 0
	for k := m.earliest(0); k >= 0; k = m.earliest(at) {
		from, to := m.next[k], m.next[k]+len(needles[k])
		b.WriteString(s[last:starts[from]])
		b.WriteString(with)
		last, at = ends[to-1], to
	}
	if at == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// matcher finds needles in a folded text, each from where the last search
// for it ended, so that no needle is searched for twice over one stretch.
type matcher struct {
	hay     string
	needles []string
	// next holds where each needle next occurs, or -1 when it does not.
	next []int
}

func newMatcher(hay string, needles []string) *matcher {
	m := &matcher{hay: hay, needles: needles, next: make([]int, len(needles))}
	for k := range needles {
		m.next[k] = m.find(k, 0)
	}
	return m
}

func (m *matcher) find(k, from int) int {
	if i := strings.Index(m.hay[from:], m.needles[k]); i >= 0 {
		return from + i
	}
	return -1
}

// earliest returns the needle whose next occurrence at or after at starts
// first, the longest where two start together, or -1 when none occurs.
func (m *matcher) earliest(at int) int {
	best := -1
	for k := range m.needles {
		if m.next[k] >= 0 && m.next[k] < at {
			m.next[k] = m.find(k, at)
		}
		if m.next[k] < 0 {
			continue
		}
		if best < 0 || m.next[k] < m.next[best] || m.next[k] == m.next[best] && len(m.needles[k]) > len(m.needles[best]) {
			best = k
		}
	}
	return best
}

// fold returns s with each letter in one case, lower after upper, and each
// run of white space as one space, and, for each byte of the result, where
// the rune it came from starts and ends in s.
func fold(s string) (folded string, starts, ends []int) {
	var b strings.Builder
	space := false
	for i, r := range s {
		end := i + utf8.RuneLen(r)
		if r == utf8.RuneError {
			_, end = utf8.DecodeRuneInString(s[i:])
			end += i
		}
		if unicode.IsSpace(r) {
			if space {
				ends[len(ends)-1] = end
				continue
			}
			space = true
			b.WriteByte(' ')
			starts, ends = append(starts, i), append(ends, end)
			continue
		}
		space = false
		n, _ := b.WriteRune(unicode.ToLower(unicode.ToUpper(r)))
		for range n {
			starts, ends = append(starts, i), append(ends, end)
		}
	}
	return b.String(), starts, ends
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
// redacted, is Escaped, or whether a member written as key=value is Escaped
// as one. Value applies member rules to such a member with its key and value
// together, so an escape in the key can hide what a rule matches. A member
// whose value is a rule's replacement is not checked that way: the replacement
// holds nothing to hide, and the key is checked on its own, by Escaped.
func (r *Redactor) EscapedValue(v any) bool {
	switch t := v.(type) {
	case string:
		return r.Escaped(t)
	case map[string]any:
		for k, e := range t {
			if r.Escaped(k) || r.EscapedValue(e) {
				return true
			}
			if text, ok := scalar(e); ok && !r.isReplacement(text) {
				if kv := k + "=" + text; r.Escaped(kv) {
					return true
				}
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
// " (3)", in sorted order. v itself never changes: the result is a copy.
func (r *Redactor) Value(v any) any {
	v = clone(v)
	for _, rule := range r.rules {
		for _, p := range rule.Paths {
			v = p.Replace(v, func(any) any { return rule.Replacement })
		}
	}
	return r.walk(v)
}

// Change is a string of a value that redaction changed: as written, and as
// the redacted value shows it.
type Change struct {
	From, To string
}

// Changes returns the strings, keys and numbers of v, a value decoded from
// JSON with numbers as json.Number, that Value changes, each with what the
// redacted value shows instead: the text as redacted, or the replacement a
// path put where the value around it was. A string whose key alone changed
// is left out; its key is not.
func (r *Redactor) Changes(v any) []Change {
	var out []Change
	changes(v, r.Value(v), r, &out)
	return out
}

// changes walks a value and its redacted copy together. Where a path
// replaced a value, the copy holds the replacement, which stands for every
// string, key and number below. Each member's value is compared with what
// the copy holds under the key walk gave it, so that two keys redaction
// makes equal never pair one's value with the other's.
func changes(v, redacted any, r *Redactor, out *[]Change) {
	switch t := v.(type) {
	case string:
		to, ok := redacted.(string)
		if !ok {
			to = r.Text(t)
		}
		if to != t {
			*out = append(*out, Change{From: t, To: to})
		}
	case json.Number:
		if to, ok := redacted.(string); ok {
			*out = append(*out, Change{From: t.String(), To: to})
		}
	case map[string]any:
		m, isMap := redacted.(map[string]any)
		replaced, isReplaced := redacted.(string)
		placed := map[string]bool{}
		for _, k := range slices.Sorted(maps.Keys(t)) {
			key := r.placedKey(k, func(key string) bool { return placed[key] })
			placed[key] = true
			switch to := r.Text(k); {
			case isReplaced:
				*out = append(*out, Change{From: k, To: replaced})
			case to != k:
				*out = append(*out, Change{From: k, To: to})
			}
			next := redacted
			if isMap {
				next = m[key]
			}
			changes(t[k], next, r, out)
		}
	case []any:
		a, isArray := redacted.([]any)
		for i, e := range t {
			next := redacted
			if isArray {
				next = nil
				if i < len(a) {
					next = a[i]
				}
			}
			changes(e, next, r, out)
		}
	}
}

// placedKey returns the key that k, a key of an object, has in the
// object's redacted copy: k as redacted, or, when an earlier key in sorted
// order took that, with " (2)", " (3)" and so on.
func (r *Redactor) placedKey(k string, taken func(string) bool) string {
	key := r.Text(k)
	for n := 2; taken(key); n++ {
		key = fmt.Sprintf("%s (%d)", r.Text(k), n)
	}
	return key
}

// clone copies the objects and arrays of a decoded JSON value, so that
// redacting the copy leaves the value as it was.
func clone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = clone(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = clone(e)
		}
		return out
	}
	return v
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
			key := r.placedKey(k, func(key string) bool { _, taken := out[key]; return taken })
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

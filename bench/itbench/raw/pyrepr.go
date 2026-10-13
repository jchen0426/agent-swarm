// Package raw reads ITBench-Lite's raw snapshot files: ClickHouse tables that
// pandas exported as tab-separated values, whose map columns hold Python
// literals.
package raw

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ParsePython parses one Python literal as pandas writes it: dicts, lists,
// tuples, strings, numbers, True, False and None. A call such as
// np.datetime64('…') becomes its single argument.
func ParsePython(s string) (any, error) {
	p := &pyParser{s: s}
	p.space()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i != len(p.s) {
		return nil, p.errorf("unexpected %q", p.s[p.i:min(p.i+20, len(p.s))])
	}
	return v, nil
}

type pyParser struct {
	s string
	i int
}

func (p *pyParser) errorf(format string, args ...any) error {
	return fmt.Errorf("python literal at %d: %s", p.i, fmt.Sprintf(format, args...))
}

func (p *pyParser) space() {
	for p.i < len(p.s) && strings.ContainsRune(" \t\r\n", rune(p.s[p.i])) {
		p.i++
	}
}

func (p *pyParser) value() (any, error) {
	if p.i >= len(p.s) {
		return nil, p.errorf("unexpected end")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.dict()
	case c == '[':
		return p.seq(']')
	case c == '(':
		return p.seq(')')
	case c == '\'' || c == '"':
		return p.str()
	case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
		return p.number()
	case c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z'):
		return p.ident()
	default:
		return nil, p.errorf("unexpected %q", string(c))
	}
}

// expect consumes c after optional space.
func (p *pyParser) expect(c byte) error {
	p.space()
	if p.i >= len(p.s) || p.s[p.i] != c {
		return p.errorf("want %q", string(c))
	}
	p.i++
	return nil
}

// closes reports, after optional space, whether the next byte is end, and
// consumes it if so.
func (p *pyParser) closes(end byte) bool {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == end {
		p.i++
		return true
	}
	return false
}

func (p *pyParser) dict() (any, error) {
	p.i++ // {
	out := map[string]any{}
	for !p.closes('}') {
		p.space()
		k, err := p.value()
		if err != nil {
			return nil, err
		}
		if err := p.expect(':'); err != nil {
			return nil, err
		}
		p.space()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		key, ok := k.(string)
		if !ok {
			key = fmt.Sprint(k)
		}
		out[key] = v
		if p.closes('}') {
			break
		}
		if err := p.expect(','); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p *pyParser) seq(end byte) (any, error) {
	p.i++ // [ or (
	out := []any{}
	for !p.closes(end) {
		p.space()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if p.closes(end) {
			break
		}
		if err := p.expect(','); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p *pyParser) str() (any, error) {
	quote := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == quote:
			p.i++
			return b.String(), nil
		case c == '\\' && p.i+1 < len(p.s):
			if err := p.escape(&b); err != nil {
				return nil, err
			}
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	return nil, p.errorf("unterminated string")
}

// escape decodes one backslash escape. Unknown escapes keep the backslash,
// as Python does.
func (p *pyParser) escape(b *strings.Builder) error {
	c := p.s[p.i+1]
	p.i += 2
	switch c {
	case 'n':
		b.WriteByte('\n')
	case 't':
		b.WriteByte('\t')
	case 'r':
		b.WriteByte('\r')
	case '0':
		b.WriteByte(0)
	case '\\', '\'', '"':
		b.WriteByte(c)
	case 'x', 'u', 'U':
		width := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
		if p.i+width > len(p.s) {
			return p.errorf("short \\%c escape", c)
		}
		n, err := strconv.ParseInt(p.s[p.i:p.i+width], 16, 32)
		if err != nil || !utf8.ValidRune(rune(n)) {
			return p.errorf("bad \\%c escape", c)
		}
		b.WriteRune(rune(n))
		p.i += width
	default:
		b.WriteByte('\\')
		b.WriteByte(c)
	}
	return nil
}

func (p *pyParser) number() (any, error) {
	start := p.i
	for p.i < len(p.s) && strings.ContainsRune("0123456789+-.eE", rune(p.s[p.i])) {
		p.i++
	}
	text := p.s[start:p.i]
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, p.errorf("bad number %q", text)
	}
	return f, nil
}

// keywords are the bare names a literal can hold.
var keywords = map[string]any{"True": true, "False": false, "None": nil, "nan": math.NaN(), "inf": math.Inf(1)}

func isNameByte(c byte) bool {
	return c == '_' || c == '.' || (c >= '0' && c <= '9') || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func (p *pyParser) ident() (any, error) {
	start := p.i
	for p.i < len(p.s) && isNameByte(p.s[p.i]) {
		p.i++
	}
	name := p.s[start:p.i]
	if p.i < len(p.s) && p.s[p.i] == '(' {
		args, err := p.seq(')')
		if err != nil {
			return nil, err
		}
		if list, _ := args.([]any); len(list) == 1 {
			return list[0], nil
		}
		return args, nil
	}
	if v, ok := keywords[name]; ok {
		return v, nil
	}
	return nil, p.errorf("unknown name %q", name)
}

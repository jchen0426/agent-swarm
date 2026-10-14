// Package tmpl renders the {name} templates of configuration (ADR-015):
// literal text with named holes, each filled with one whole value. A value is
// never parsed again, so text in it that looks like a hole stays text.
package tmpl

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Errors returned by Parse and Render.
var (
	ErrSyntax  = errors.New("tmpl: invalid template")
	ErrMissing = errors.New("tmpl: no value")
)

// name is a hole's name: dot-separated segments of letters, digits, '_' and '-'.
var name = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

type part struct {
	text string // literal text, or a hole's name
	hole bool
}

// Template is a parsed template. "{{" and "}}" stand for literal braces. The
// zero value renders as the empty string.
type Template struct {
	text  string
	parts []part
}

// Parse parses a template.
func Parse(text string) (Template, error) {
	var parts []part
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, part{text: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case (c == '{' || c == '}') && i+1 < len(text) && text[i+1] == c:
			lit.WriteByte(c)
			i++
		case c == '{':
			end := strings.IndexByte(text[i+1:], '}')
			if end < 0 {
				return Template{}, fmt.Errorf("%w: %q has an unclosed {", ErrSyntax, text)
			}
			n := text[i+1 : i+1+end]
			if !name.MatchString(n) {
				return Template{}, fmt.Errorf("%w: %q has an invalid name %q", ErrSyntax, text, n)
			}
			flush()
			parts = append(parts, part{text: n, hole: true})
			i += end + 1
		case c == '}':
			return Template{}, fmt.Errorf("%w: %q has an unmatched }", ErrSyntax, text)
		default:
			lit.WriteByte(c)
		}
	}
	flush()
	return Template{text: text, parts: parts}, nil
}

// String returns the template as written.
func (t Template) String() string { return t.text }

// Names returns the names of the template's holes, each once, in order of
// first use.
func (t Template) Names() []string {
	var names []string
	seen := map[string]bool{}
	for _, p := range t.parts {
		if p.hole && !seen[p.text] {
			seen[p.text] = true
			names = append(names, p.text)
		}
	}
	return names
}

// Render fills each hole with the value that lookup returns for its name. A
// hole without a value is an error.
func (t Template) Render(lookup func(name string) (string, bool)) (string, error) {
	var b strings.Builder
	for _, p := range t.parts {
		if !p.hole {
			b.WriteString(p.text)
			continue
		}
		v, ok := lookup(p.text)
		if !ok {
			return "", fmt.Errorf("%w for %q", ErrMissing, p.text)
		}
		b.WriteString(v)
	}
	return b.String(), nil
}

// Root returns the first segment of a hole's name, such as "args" for
// "args.service".
func Root(name string) string {
	root, _, _ := strings.Cut(name, ".")
	return root
}

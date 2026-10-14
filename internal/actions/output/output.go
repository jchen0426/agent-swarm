// Package output turns a tool's result into its one canonical form (spec
// Sections 4.2 and 6.2): what a partialWhen condition sees, what redaction
// reads, what the gateway stores and what the agent is shown, so that all of
// them are about the same text. Content the gateway cannot redact is left
// out, with a note; a result that has no canonical form is a read error.
package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
)

// ErrForm marks a result that has no canonical form. Its message says why.
var ErrForm = errors.New("output: no canonical form")

// Kind is a content block's type.
type Kind string

// Kinds of content blocks.
const (
	Text     Kind = "text"
	Image    Kind = "image"
	Audio    Kind = "audio"
	Link     Kind = "resource_link"
	Resource Kind = "resource"
)

// Block is one content block of a result, as the MCP client decodes it.
type Block struct {
	Kind Kind
	// Text is a text block's text, or an embedded resource's text.
	Text string
	// Binary is true for an embedded resource that holds bytes, not text.
	Binary bool
}

// Result is a tool's answer, as the MCP client decodes it.
type Result struct {
	Blocks []Block
	// Structured is the structured content, or nil when there is none.
	Structured any
	IsError    bool
}

// exact is the largest integer a float64 holds exactly, 2^53.
const exact = 1 << 53

// Canonical is a result's canonical form.
type Canonical struct {
	// Text is the canonical form, before redaction.
	Text string
	// JSON reports whether Text is one JSON object or array.
	JSON bool
	// Notes say what was left out. They are shown but never stored, so they
	// cannot be quoted.
	Notes []string
}

// Read returns the canonical form of res. structured says whether the tool's
// pinned definition declares an output schema. If it does, the form is the
// structured content, which must be present; numbers beyond 2^53, which the
// MCP client could not read exactly, are refused. Otherwise the form is the
// text of the text blocks and embedded text resources, in order, one per
// line; a block that looks like a JSON object or array must be one. NUL
// characters and invalid UTF-8 become U+FFFD.
func Read(res *Result, structured bool) (*Canonical, error) {
	if structured {
		return readStructured(res.Structured)
	}
	c := &Canonical{}
	var parts []string
	for _, b := range res.Blocks {
		switch {
		case b.Kind == Text, b.Kind == Resource && !b.Binary:
			part := scrub(b.Text)
			if looksLikeJSON(part) {
				v, ok := Decode(part)
				if !ok {
					return nil, fmt.Errorf("%w: a text block looks like JSON but is not one JSON document", ErrForm)
				}
				part = encode(v)
			}
			parts = append(parts, part)
		default:
			c.Notes = append(c.Notes, fmt.Sprintf("%s content was left out: the gateway cannot redact it", b.Kind))
		}
	}
	c.Text = strings.Join(parts, "\n")
	c.JSON = len(parts) == 1 && looksLikeJSON(c.Text)
	return c, nil
}

func readStructured(v any) (*Canonical, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: the tool declares structured output but returned none", ErrForm)
	}
	if tooLarge(v) {
		return nil, fmt.Errorf("%w: the output holds a number too large to be read exactly", ErrForm)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrForm, err)
	}
	doc, ok := Decode(scrub(string(raw)))
	if !ok {
		return nil, fmt.Errorf("%w: the structured output is not a JSON object or array", ErrForm)
	}
	text := encode(doc)
	return &Canonical{Text: text, JSON: true}, nil
}

// tooLarge reports whether v holds a number that a float64 may have rounded.
func tooLarge(v any) bool {
	switch t := v.(type) {
	case float64:
		return math.Abs(t) > exact
	case map[string]any:
		for _, e := range t {
			if tooLarge(e) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if tooLarge(e) {
				return true
			}
		}
	}
	return false
}

// scrub replaces invalid UTF-8 and NUL characters with U+FFFD.
func scrub(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	return strings.ReplaceAll(s, "\x00", "\uFFFD")
}

func looksLikeJSON(s string) bool {
	t := strings.TrimSpace(s)
	return t != "" && (t[0] == '{' || t[0] == '[')
}

// Plain returns the canonical form decoded with numbers as numbers, as the
// condition language reads them, or false when it is not JSON.
func (c *Canonical) Plain() (any, bool) {
	if !c.JSON {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(c.Text), &v); err != nil {
		return nil, false
	}
	return v, true
}

// Show redacts the canonical form and returns the text the gateway stores and
// shows. JSON is redacted value by value and written again; plain text is
// redacted as text, and fails when a match hides behind JSON escapes. The
// patterns then run once more over the whole. Redaction paths need JSON.
func (c *Canonical) Show(red *redact.Redactor) (string, error) {
	var text string
	switch {
	case c.JSON:
		doc, ok := Decode(c.Text)
		if !ok {
			return "", fmt.Errorf("%w: the output is not JSON", ErrForm)
		}
		text = encode(red.Value(doc))
	case red.HasPaths():
		return "", fmt.Errorf("%w: the tool's redaction paths need JSON output", ErrForm)
	default:
		text = red.Text(c.Text)
		if red.Escaped(text) {
			return "", fmt.Errorf("%w: the output holds an escaped value that a redaction rule matches", ErrForm)
		}
	}
	return red.Text(text), nil
}

// Decode decodes s when it is exactly one JSON object or array, keeping
// numbers as json.Number so that no digit is lost.
func Decode(s string) (any, bool) {
	if !looksLikeJSON(s) {
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

// encode writes v as compact JSON, without escaping HTML characters.
func encode(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

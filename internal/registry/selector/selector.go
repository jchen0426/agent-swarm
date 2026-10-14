// Package selector picks values out of JSON documents with RFC 9535 JSONPath
// queries (ADR-015). Selectors call no functions: the parser knows none, so a
// query that calls one does not parse.
package selector

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/theory/jsonpath"
	"github.com/theory/jsonpath/registry"
)

// ErrSyntax marks text that is not a query this package accepts.
var ErrSyntax = errors.New("selector: invalid JSONPath query")

// parser knows no function extensions, so filters cannot call length(),
// count(), match(), search() or value().
var parser = jsonpath.NewParser(jsonpath.WithRegistry(&registry.Registry{}))

// Selector is a parsed query. The zero value selects nothing.
type Selector struct {
	text string
	path *jsonpath.Path
}

// Parse parses a JSONPath query. It refuses empty text and function calls.
func Parse(text string) (Selector, error) {
	if text == "" {
		return Selector{}, fmt.Errorf("%w: empty", ErrSyntax)
	}
	p, err := parser.Parse(text)
	if err != nil {
		return Selector{}, fmt.Errorf("%w: %w", ErrSyntax, err)
	}
	return Selector{text: text, path: p}, nil
}

// String returns the query as written.
func (s Selector) String() string { return s.text }

// Select returns the values the query selects from doc, a document decoded
// from JSON into maps, slices and scalars, in document order.
func (s Selector) Select(doc any) []any {
	if s.path == nil {
		return nil
	}
	return s.path.Select(doc)
}

// SelectJSON decodes raw as one JSON document and selects from it.
func (s Selector) SelectJSON(raw []byte) ([]any, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("selector: decode JSON: %w", err)
	}
	return s.Select(doc), nil
}

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
	"github.com/theory/jsonpath/spec"
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

// Replace replaces each value the query selects in doc with what with returns
// for it, and returns the document: doc, changed in place, or the new value
// when the query selects the root. doc is a document decoded from JSON. A
// value inside one that an earlier replacement removed is skipped.
func (s Selector) Replace(doc any, with func(any) any) any {
	if s.path == nil {
		return doc
	}
	for _, n := range s.path.SelectLocated(doc) {
		doc = set(doc, n.Path, with(n.Node))
	}
	return doc
}

// set puts v at path in doc, if the path still leads somewhere.
func set(doc any, path spec.NormalizedPath, v any) any {
	if len(path) == 0 {
		return v
	}
	parent := doc
	for _, step := range path[:len(path)-1] {
		var ok bool
		if parent, ok = child(parent, step); !ok {
			return doc
		}
	}
	switch step := path[len(path)-1].(type) {
	case spec.Name:
		if m, ok := parent.(map[string]any); ok {
			if _, ok := m[string(step)]; ok {
				m[string(step)] = v
			}
		}
	case spec.Index:
		if a, ok := parent.([]any); ok && int(step) >= 0 && int(step) < len(a) {
			a[step] = v
		}
	}
	return doc
}

func child(parent any, step spec.NormalSelector) (any, bool) {
	switch step := step.(type) {
	case spec.Name:
		m, ok := parent.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[string(step)]
		return v, ok
	case spec.Index:
		a, ok := parent.([]any)
		if !ok || int(step) < 0 || int(step) >= len(a) {
			return nil, false
		}
		return a[step], true
	}
	return nil, false
}

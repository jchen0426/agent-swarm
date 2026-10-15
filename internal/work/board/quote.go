package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
)

// NormalizeSpace collapses every run of white space to one space and trims both ends.
func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// QuoteAppears reports whether quote appears verbatim, after white-space
// normalization, in a read call's stored output (spec Section 4.2). For output
// that is one JSON document, or one cut short by a size limit, the quote may
// instead appear inside a single decoded string, a key or a value, so that
// escaping does not decide whether evidence counts. Decoded strings are matched
// one at a time: text that joins two of them, or a key with its value, never
// appeared in the output, so it must match the raw output instead. An empty
// quote never appears.
func QuoteAppears(output []byte, quote string) bool {
	q := NormalizeSpace(quote)
	if q == "" {
		return false
	}
	if strings.Contains(NormalizeSpace(string(output)), q) {
		return true
	}
	decoded, ok := decodedStrings(output)
	return ok && slices.ContainsFunc(decoded, func(s string) bool {
		return strings.Contains(NormalizeSpace(s), q)
	})
}

// inArguments reports whether a quote lies within one string of a call's
// arguments, a value or a key, after white-space normalization. A read that
// repeats what it was asked, such as a search that answers "no results for
// ...", would otherwise let an agent quote its own words as evidence.
func inArguments(args []byte, quote string) bool {
	values, ok := argumentStrings(args)
	return ok && slices.ContainsFunc(values, func(v string) bool {
		return strings.Contains(NormalizeSpace(v), quote)
	})
}

// argumentStrings returns every string of one JSON document, decoded: its
// values and its keys, below the top level, where the arguments' own names
// are the tool's.
func argumentStrings(doc []byte) ([]string, bool) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			for k, e := range t {
				out = append(out, k)
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	if top, ok := v.(map[string]any); ok {
		for _, e := range top {
			walk(e)
		}
		return out, true
	}
	walk(v)
	return out, true
}

// decodedStrings returns every string of one JSON document, keys and values
// alike, with escapes decoded and duplicates kept. It reads tokens rather than
// decoding into maps, so that a duplicate key cannot hide a value. A document
// cut short keeps the strings that were whole before the cut. It reports false
// when the output is not one JSON document or the start of one.
func decodedStrings(output []byte) ([]string, bool) {
	dec := json.NewDecoder(bytes.NewReader(output))
	var decoded []string
	depth, done := 0, false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return decoded, len(decoded) > 0 || done
		}
		if err != nil || done {
			return nil, false
		}
		switch t := tok.(type) {
		case json.Delim:
			if t == '{' || t == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			decoded = append(decoded, t)
		}
		done = depth == 0
	}
}

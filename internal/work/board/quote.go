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
// that is one JSON document, the quote may instead appear inside a single
// decoded string, a key or a value, so that escaping does not decide whether
// evidence counts. Decoded strings are matched one at a time: text that joins
// two of them, or a key with its value, never appeared in the output, so it
// must match the raw output instead. An empty quote never appears.
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

// decodedStrings returns every string of one JSON document, keys and values
// alike, with escapes decoded and duplicates kept. It reads tokens rather than
// decoding into maps, so that a duplicate key cannot hide a value. It reports
// false when the output is not exactly one JSON document.
func decodedStrings(output []byte) ([]string, bool) {
	dec := json.NewDecoder(bytes.NewReader(output))
	var decoded []string
	depth, done := 0, false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return decoded, done
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

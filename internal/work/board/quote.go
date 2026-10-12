package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// NormalizeSpace collapses every run of white space to one space and trims both ends.
func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// QuoteAppears reports whether quote appears verbatim, after white-space
// normalization, in a read call's stored output. For JSON output it also
// matches the decoded text, so that escaping does not decide whether evidence
// counts (spec Section 4.2). An empty quote never appears.
func QuoteAppears(output []byte, quote string) bool {
	q := NormalizeSpace(quote)
	if q == "" {
		return false
	}
	if strings.Contains(NormalizeSpace(string(output)), q) {
		return true
	}
	text, ok := decodedJSONText(output)
	return ok && strings.Contains(NormalizeSpace(text), q)
}

// decodedJSONText renders one JSON document as plain text: every object key
// and scalar value, with strings unescaped, one value per line. It reports
// false when the output is not a single JSON document.
func decodedJSONText(output []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(output))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", false
	}
	var b strings.Builder
	writeJSONText(&b, v)
	return b.String(), true
}

func writeJSONText(b *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(t)) {
			b.WriteString(k)
			b.WriteString(": ")
			writeJSONText(b, t[k])
		}
	case []any:
		for _, e := range t {
			writeJSONText(b, e)
		}
	case string:
		b.WriteString(t)
		b.WriteByte('\n')
	case json.Number:
		b.WriteString(t.String())
		b.WriteByte('\n')
	case bool:
		b.WriteString(strconv.FormatBool(t))
		b.WriteByte('\n')
	default:
		b.WriteString("null\n")
	}
}

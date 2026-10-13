package board_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

func TestNormalizeSpace(t *testing.T) {
	assert.Equal(t, "a b c", board.NormalizeSpace("  a\t b\n\nc  "))
	assert.Empty(t, board.NormalizeSpace(" \n\t "))
}

func TestQuoteAppears(t *testing.T) {
	text := []byte("10:03:01 ERROR  checkout\n  NullPointerException in CheckoutHandler")
	jsonOutput := []byte(`{"events":[{"msg":"pool \"orders\" exhausted","count":42,"ok":false}]}`)

	tests := []struct {
		name   string
		output []byte
		quote  string
		want   bool
	}{
		{"verbatim", text, "NullPointerException in CheckoutHandler", true},
		{"white space differs", text, "ERROR checkout NullPointerException", true},
		{"not in output", text, "OutOfMemoryError", false},
		{"empty quote", text, "", false},
		{"white space only", text, " \n ", false},
		{"raw JSON fragment", jsonOutput, `"count":42`, true},
		{"decoded JSON string", jsonOutput, `pool "orders" exhausted`, true},
		{"raw JSON boolean", jsonOutput, `"ok":false`, true},
		{"a key is not joined to its value", jsonOutput, "count: 42", false},
		{"invented text", jsonOutput, "pool orders healthy", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, board.QuoteAppears(tt.output, tt.quote))
		})
	}
}

func TestQuoteAppearsIgnoresTrailingDataAfterJSON(t *testing.T) {
	// Two JSON documents are not one structured output, so only the raw text counts.
	output := []byte(`{"msg":"a \"b\""} {"msg":"c"}`)

	assert.False(t, board.QuoteAppears(output, `a "b"`))
	assert.True(t, board.QuoteAppears(output, `{"msg":"c"}`))
}

func TestQuoteAppearsAcrossUnicodeSpaceAndText(t *testing.T) {
	output := []byte("Zahlung\u00a0für Bestellung 42\tfehlgeschlagen")

	assert.True(t, board.QuoteAppears(output, "Zahlung für Bestellung 42 fehlgeschlagen"))
}

func TestQuoteAppearsInEscapedJSON(t *testing.T) {
	output := []byte(`{"msg":"\u003cnil\u003e pointer\nin handler"}`)

	assert.True(t, board.QuoteAppears(output, "<nil> pointer in handler"))
}

func TestQuoteCannotStitchSeparateJSONValues(t *testing.T) {
	// Each decoded value counts on its own. Joining neighboring values gives a
	// sentence the output never contained, which must not pass as provenance.
	output := []byte(`{"checks":["error rate","is normal"],"b":"the deploy","a":"was rolled back"}`)

	assert.False(t, board.QuoteAppears(output, "error rate is normal"))
	assert.False(t, board.QuoteAppears(output, "was rolled back b: the deploy"))
	assert.False(t, board.QuoteAppears(output, "a: was rolled back"), "a key and its value are two strings")
	assert.True(t, board.QuoteAppears(output, "error rate"))
	assert.True(t, board.QuoteAppears(output, "was rolled back"))
}

// A read's output can be cut to a size limit, so that it is no longer a whole
// JSON document. Each string decoded before the cut was shown in full, so a
// quote may still match one of them.
func TestAQuoteMatchesAStringOfAJSONDocumentCutShort(t *testing.T) {
	cutInAString := []byte(`{"logs":[{"body":"cart said \"connection refused\""},{"body":"tr`)
	cutBetweenTokens := []byte(`{"logs":[{"body":"cart said \"connection refused\""}`)

	for _, output := range [][]byte{cutInAString, cutBetweenTokens} {
		assert.True(t, board.QuoteAppears(output, `cart said "connection refused"`), string(output))
		assert.False(t, board.QuoteAppears(output, `"connection refused"tr`), "text across two strings never appeared")
	}
	assert.False(t, board.QuoteAppears([]byte(`{"logs":[{"body":"said \"x\""},}`), `said "x"`), "a broken document is matched raw only")
}

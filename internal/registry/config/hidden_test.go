package config_test

import (
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// Hidden finds every character that can carry text a reviewer does not see.
func TestHiddenFindsEveryInvisibleCarrier(t *testing.T) {
	for _, r := range []rune{
		0x200b, 0x200d, 0x202e, 0x2066, 0xfeff, 0x00ad, // zero-width, bidi and format characters
		0xe0049, 0xe0001, // tag characters
		0xfe0f, 0xe0100, 0x180b, // variation selectors
		0x034f, 0x115f, 0x1160, 0x3164, 0xffa0, // ignorable by default
		0x0007, 0x0000, 0x2028, // controls and separators
	} {
		assert.True(t, config.Hidden(r), "U+%04X", r)
	}
	for _, r := range []rune{'a', ' ', '\t', '\n', '\r', 0x00a0, 0x3000, 0x00e9, 0x4e2d, 0x1f600} {
		assert.False(t, config.Hidden(r), "U+%04X", r)
	}
}

// Unseen is the write path's rule for values a reader must see whole: a
// control character, below U+0020 or U+007F, or one Hidden reports. Unlike
// Hidden alone, it finds tab, line feed and carriage return; like Hidden, it
// lets every space separator through.
func TestUnseenIsTheWritePathsRule(t *testing.T) {
	var table []rune
	for r := rune(0); r < ' '; r++ {
		table = append(table, r)
	}
	for r := rune(0x80); r <= 0x9f; r++ {
		table = append(table, r)
	}
	table = append(table, 0x7f, 0x200b, 0x202e, 0xfe0f, ' ', 0x00a0, 0x3000, 'a', 'Z', 0x00e9, 0x4e2d)

	for _, r := range table {
		assert.Equal(t, r < ' ' || r == 0x7f || config.Hidden(r), config.Unseen(r), "U+%04X", r)
	}
	for _, r := range []rune{'\t', '\n', '\r'} {
		assert.True(t, config.Unseen(r), "U+%04X", r)
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.Is(unicode.Zs, r) {
			assert.False(t, config.Unseen(r), "the space separator U+%04X", r)
		}
	}
}

// A definition's hidden characters are found in its decoded schemas too,
// where JSON escapes them: a reviewer reads the escape, a model the
// character.
func TestHiddenFieldsAreFoundInDecodedSchemas(t *testing.T) {
	def := config.ToolDefinition{
		Name: "get", Description: "Reads a flag.",
		InputSchema: []byte(`{"type":"object","properties":{"flag\u200b":{"type":"string"}}}`),
		Annotations: []byte(`{"title":"\udb40\udc49gnore"}`),
	}

	assert.Equal(t, []string{"inputSchema", "annotations"}, def.HiddenFields())
	plain := config.ToolDefinition{Name: "get", Description: "Reads a flag.", InputSchema: []byte(`{"type":"object"}`)}
	assert.Empty(t, plain.HiddenFields())
}

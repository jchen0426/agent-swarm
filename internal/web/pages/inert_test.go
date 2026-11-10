package pages_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/web/pages"
)

// The two display functions for stored text each mark the runes their set
// reports, as ⟨U+XXXX⟩, four hex digits at least, upper case, and give
// U+FFFD for an invalid byte (design D11; R154): inertText, for multi-line
// text, marks a bidi control and a zero-width space and keeps line feeds and
// tabs; inertLine, for a single-line value, marks line feeds and tabs too.
// Plain text, its spaces included, is unchanged by either. (The plan's.)
func TestInertTextAndInertLineMarkWhatTheirSetsHide(t *testing.T) {
	for _, tc := range []struct {
		name       string
		in         string
		text, line string
	}{
		{"plain text", "checkout v42 deployed at 10:02", "checkout v42 deployed at 10:02", "checkout v42 deployed at 10:02"},
		{"a space separator", "a\u00a0b", "a\u00a0b", "a\u00a0b"},
		{"a bidi override", "abc\u202edef", "abc⟨U+202E⟩def", "abc⟨U+202E⟩def"},
		{"a zero-width space", "a\u200bb", "a⟨U+200B⟩b", "a⟨U+200B⟩b"},
		{"a byte order mark, upper case", "\ufeffa", "⟨U+FEFF⟩a", "⟨U+FEFF⟩a"},
		{"a tag, past four digits", "a\U000e0041", "a⟨U+E0041⟩", "a⟨U+E0041⟩"},
		{"a line feed", "one\ntwo", "one\ntwo", "one⟨U+000A⟩two"},
		{"a tab", "one\ttwo", "one\ttwo", "one⟨U+0009⟩two"},
		{"a control character", "a\x07b", "a⟨U+0007⟩b", "a⟨U+0007⟩b"},
		{"an invalid byte", "a\xffb", "a\ufffdb", "a\ufffdb"},
		{"an empty text", "", "", ""},
	} {
		assert.Equal(t, tc.text, pages.InertText(tc.in), "inertText, %s", tc.name)
		assert.Equal(t, tc.line, pages.InertLine(tc.in), "inertLine, %s", tc.name)
	}
}

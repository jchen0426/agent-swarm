package pages

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rendered is l as the template "line" renders it.
func rendered(t *testing.T, l line) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, templates["board"].ExecuteTemplate(&b, "line", l))
	return b.String()
}

// Each part of a line renders by its kind (R171; the words of the board):
// the platform's words as text; a name inside its <bdi>, through inertLine;
// a single-line value as quoted data, through inertLine; a quote or a title
// as quoted data, through inertText, its line feeds kept; model-written text
// marked so, through inertText; a person's words marked so, through
// inertText; and a link of words or of a name to its path. html/template
// escapes every part, a marker's "+" as &#43; among them, which a reader
// reads as "+"; and every value of every kind sits inside its <bdi>.
func TestALineRendersEachPartByItsKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		l    line
		want string
	}{
		{"the platform's words", line(nil).say("Closed at 14:20 <now> & then"), "Closed at 14:20 &lt;now&gt; &amp; then"},
		{"a name", line(nil).name("sso:ri\u202eta"), "<bdi>sso:ri⟨U&#43;202E⟩ta</bdi>"},
		{"a single-line value", line(nil).value("eu\n1 <b>"), `<q class="data"><bdi>eu⟨U&#43;000A⟩1 &lt;b&gt;</bdi></q>`},
		{"a quote", line(nil).quote("disk\nfull\u200b"), "<q class=\"data\"><bdi>disk\nfull⟨U&#43;200B⟩</bdi></q>"},
		{"model-written text", line(nil).model("<script>x</script>"), `model-written: <span class="model"><bdi>&lt;script&gt;x&lt;/script&gt;</bdi></span>`},
		{"a person's words", line(nil).person("it was\tme\u202e"), "a person's words: <bdi>it was\tme⟨U&#43;202E⟩</bdi>"},
		{"a link of words", line(nil).link("raw output", "/items/a%2F1/calls/c-1"), `<a href="/items/a%2F1/calls/c-1">raw output</a>`},
		{"a link of a name", line(nil).nameLink("s\u202e1", "/items/s%E2%80%AE1"), `<a href="/items/s%E2%80%AE1"><bdi>s⟨U&#43;202E⟩1</bdi></a>`},
		{"a line of parts", line(nil).say("Item ").name("a1").say(". Title: ").quote("Disk full"),
			`Item <bdi>a1</bdi>. Title: <q class="data"><bdi>Disk full</bdi></q>`},
	} {
		assert.Equal(t, tc.want, rendered(t, tc.l), tc.name)
	}
	for as := asName; as <= asPerson; as++ {
		assert.Contains(t, rendered(t, line{{Value: "v\u202e<i>", As: as}}), "<bdi>v⟨U&#43;202E⟩&lt;i&gt;</bdi>", "kind %d: a value inside its <bdi>", as)
	}
}

// Two lines built on one prefix share no part: each part added copies the
// line, so that a later part never overwrites another line's.
func TestLinesBuiltOnOnePrefixShareNothing(t *testing.T) {
	prefix := line(nil).say("a").say("b").say("c")
	one, two := prefix.name("x"), prefix.name("y")
	linked := one.nameLink("z", "/z")
	assert.Equal(t, "x", one[3].Value)
	assert.Equal(t, "y", two[3].Value)
	assert.Len(t, prefix, 3)
	assert.Equal(t, "z", linked[4].Value)
	assert.Len(t, one, 4)
}

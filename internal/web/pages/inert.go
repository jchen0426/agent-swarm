package pages

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// inertText is s for multi-line stored text: each rune config.Hidden
// reports, which keeps spaces, tabs and line feeds, replaced by ⟨U+XXXX⟩
// (four hex digits at least, upper case); an invalid byte by U+FFFD. A
// plain string, which html/template still escapes (design D11).
func inertText(s string) string { return marked(s, config.Hidden) }

// inertLine is s for a single-line value: each rune config.Unseen reports,
// tab, line feed and carriage return among them, so marked.
func inertLine(s string) string { return marked(s, config.Unseen) }

// marked is s with each rune hidden reports replaced by a marker a reader
// sees, ⟨U+XXXX⟩, and each invalid byte by U+FFFD; s itself when it holds
// neither. The templates print it inside <bdi>, so that a text's direction
// cannot reorder the platform's words around it (design D11; R154).
func marked(s string, hidden func(rune) bool) string {
	if utf8.ValidString(s) && strings.IndexFunc(s, hidden) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		// An invalid byte ranges as utf8.RuneError, U+FFFD, which neither set
		// reports.
		if hidden(r) {
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// boardWait bounds a board read through the watch (R141); tests set it.
var boardWait = 2 * time.Second

// The raw output's bounds: its first 256 KiB are shown (R159); and a
// document nested deeper than outlineLevels levels is shown as stored, not
// outlined (R153's "any other output"; review 1's M2), so that nothing of its
// structure is lost and 256 KiB of brackets never makes a page of gigabytes
// of indentation.
const (
	rawShown      = 256 << 10
	outlineLevels = 32
)

// outputPage is a read call's raw output (design D10): its header, the
// sentence that names the call's server, tool and agent, a line of parts
// (R171); the lines of its arguments and of its output, as R153 shows them;
// its output as stored, when a quote lies only there; and whether the output
// was cut. Refusal is a refusal's words, or a failure's, and NotKept those of
// a cited call whose stored output is no longer kept, each shown in place of
// the rest.
type outputPage struct {
	Refusal, NotKept string
	Header           line
	Args, Output     []rawLine
	Stored           []rawLine
	Cut              bool
}

// output serves the raw output of a read call that the item's board cites,
// GET /items/{id}/calls/{callID} (design D10; R152, R153), to the item's
// members alone. It reads the board through the watch, and then the call,
// both on the web's own pool, within boardWait (R141, R143); its frame takes
// the kill switch from the board's read, as the board page's does (see
// boardFrame); a failed board read is answered as the board page answers it
// (see boardFailed). Every refusal is the same 404 page, which says nothing
// of its cause: an item with no record; a board that visible, asked once
// before anything of the board is shown, says may not be shown; an actor
// whose grounds May does not give as Member, a fresh admin's override among
// them (R152); a call that no evidence on the board cites; and a cited call
// of another run than the item's. A cited call that the store no longer
// holds is past its retention, 410; another failure to read it, 503.
func (s *site) output(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sg, actor, _ := s.a.Acting(r)
	id, callID := item.ID(r.PathValue("id")), r.PathValue("callID")
	page := &outputPage{}
	reading, cancel := context.WithTimeout(ctx, boardWait)
	defer cancel()
	read, err := s.p.Boards.Board(reading, id)
	if err != nil {
		l := s.boardFrame(ctx, &sg, "Raw output", nil)
		l.Body = page
		status, refusal := s.boardFailed(ctx, id, err)
		if status == http.StatusNotFound {
			s.noOutput(w, r, &l)
			return
		}
		page.Refusal = refusal
		s.render(w, r, status, "output", &l)
		return
	}
	l := s.boardFrame(ctx, &sg, "Raw output", &read.Facts)
	l.Body = page
	bv := read.Board
	quotes, cited := bv.Cited[callID]
	if !visible(&bv.View) || !member(actor, &bv.Opened) || !cited {
		s.noOutput(w, r, &l)
		return
	}
	call, err := s.p.ReadCalls.ReadCall(reading, callID)
	switch {
	case errors.Is(err, app.ErrCallNotFound):
		page.NotKept = notKeptWords
		s.render(w, r, http.StatusGone, "output", &l)
		return
	case err != nil:
		s.log.ErrorContext(ctx, "the web interface could not read a cited call", "item", id, "class", app.FailureClass(err))
		page.Refusal = unavailableCopy
		s.render(w, r, http.StatusServiceUnavailable, "output", &l)
		return
	case call.Run != bv.Version.Run:
		s.noOutput(w, r, &l)
		return
	}
	page.Header = line(nil).say("Raw output of ").name(call.Server).say(".").name(call.Tool).say(" by ").name(call.Agent).say(".")
	page.Args, _, _ = rawLines(call.Args, nil)
	page.Output, page.Stored, page.Cut = rawLines(call.Output, quotes)
	s.render(w, r, http.StatusOK, "output", &l)
}

// callPath is the path of the raw output of call on item id's board, each
// id a path segment escaped by url.PathEscape.
func callPath(id item.ID, call string) string {
	return "/items/" + url.PathEscape(string(id)) + "/calls/" + url.PathEscape(call)
}

// noOutput answers the raw output's refusal, whatever its cause: the one 404
// page, framed by l, a page of its own, so that nothing of the board or the
// call reaches it.
func (s *site) noOutput(w http.ResponseWriter, r *http.Request, l *layout) {
	l.Body = &outputPage{Refusal: noOutputWords}
	s.render(w, r, http.StatusNotFound, "output", l)
}

// member reports whether actor reads the raw outputs of the item o opened:
// May gives its grounds as Member, never an admin's override (R152).
func member(actor app.Actor, o *item.Opened) bool {
	grounds, err := app.May(actor, app.ActReadOutput, o)
	return err == nil && grounds == app.Member
}

// rawLine is a line of a raw output or of its arguments as the page shows
// it: its text, and whether a quote that cites the call marks it (R153).
type rawLine struct {
	Text   string
	Quoted bool
}

// rawLines are output's lines as R153 shows them, its stored form's when a
// quote lies only there, and whether they were cut at 256 KiB; a line holds
// its text and whether a quote marks it. An output that is one JSON
// document, or the start of one that the page cut, is shown as an outline of
// its decoded tokens (see outlined), and any other as stored. Each quote is
// marked in the form that holds it (see mark): the outline, or else the
// output as stored, which is given only then; a quote neither holds, as one
// past the cut, is marked nowhere.
func rawLines(output []byte, quotes []string) (outline, stored []rawLine, cut bool) {
	if len(output) > rawShown {
		output, cut = output[:runeStart(output, rawShown)], true
	}
	outline, ok := outlined(output, cut)
	if !ok {
		outline = asStored(output)
		mark(outline, quotes)
		return outline, nil, cut
	}
	if missed := mark(outline, quotes); len(missed) > 0 {
		stored = asStored(output)
		if len(mark(stored, missed)) == len(missed) {
			stored = nil
		}
	}
	return outline, stored, cut
}

// runeStart is n, or, when byte n of b continues a rune, that rune's start,
// so that a cut at n splits no rune; a run of bytes that continue none is cut
// where three of them end.
func runeStart(b []byte, n int) int {
	for i := 0; i < utf8.UTFMax-1 && n > 0 && !utf8.RuneStart(b[n]); i++ {
		n--
	}
	return n
}

// asStored is output's lines as stored, split at its line breaks; an empty
// output has none.
func asStored(output []byte) []rawLine {
	if len(output) == 0 {
		return nil
	}
	pieces := splitLines(string(output))
	lines := make([]rawLine, len(pieces))
	for i, p := range pieces {
		lines[i].Text = p
	}
	return lines
}

// splitLines is s split at each line break, a CR LF, a LF or a CR, each of
// which a browser shows as one; a break that ends s ends its last line and
// opens none, and an empty s is one empty line.
func splitLines(s string) []string {
	var out []string
	for {
		i := strings.IndexAny(s, "\r\n")
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i])
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			i++
		}
		if s = s[i+1:]; s == "" {
			return out
		}
	}
}

// outlined is doc's outline (R153), and whether doc is one JSON document,
// whole, or, when the page cut it, the start of one (review 1's I1): one
// member or element a line, indented two spaces a level; the top level opens
// no line; a member is its decoded key, ": " and its value; a string is
// decoded, without its quotes, its further lines continuing two spaces past
// its member's indent; a number, a boolean or null is its JSON text; an
// object or an array is its key's line alone, or an element's line of its
// own, empty, its members a level in. The tokens are read one at a time, as
// the board reads them (board.QuoteAppears), so that a duplicate key hides
// no value, and a document the page cut keeps what was whole before the
// cut. A document the page did not cut that ends with a token or a container
// open, as a tool that cut its own output stores it, would lose its tail
// unseen, and is no outline; nor is one nested deeper than outlineLevels.
func outlined(doc []byte, cut bool) ([]rawLine, bool) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var o outliner
	read := false
	for {
		tok, err := dec.Token()
		done := read && len(o.open) == 0
		switch {
		case done:
			// The document is whole: nothing but its end may follow it, no
			// second document and no other text.
			return o.lines, errors.Is(err, io.EOF)
		case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
			// No token at all, or the start of a document, ended inside a
			// token or with a container open.
			return o.lines, read && cut
		case err != nil:
			return nil, false
		}
		if !o.take(tok) {
			return nil, false
		}
		read = true
	}
}

// outliner is an outline as its tokens build it: its lines, and the objects
// and arrays it is in, the innermost last.
type outliner struct {
	lines []rawLine
	open  []container
}

// container is an object or an array an outline is in: for an object,
// whether its next token is a key, and the key its next value is of.
type container struct {
	object, wantKey bool
	key             string
}

// take adds tok, the document's next token, to the outline, and reports
// whether the outline takes it: an object's key is a string, and no object
// or array opens past outlineLevels.
func (o *outliner) take(tok json.Token) bool {
	d, delim := tok.(json.Delim)
	if delim && (d == '}' || d == ']') {
		o.open = o.open[:len(o.open)-1]
		return true
	}
	var in *container
	if n := len(o.open); n > 0 {
		in = &o.open[n-1]
	}
	if in != nil && in.wantKey {
		key, ok := tok.(string)
		in.key, in.wantKey = key, false
		return ok
	}
	level, lead := max(len(o.open)-1, 0), ""
	if in != nil && in.object {
		lead, in.wantKey = in.key+": ", true
	}
	switch {
	case !delim:
		o.add(level, lead+scalar(tok))
		return true
	case len(o.open) == outlineLevels:
		return false
	case in != nil:
		o.add(level, strings.TrimSuffix(lead, " "))
	}
	o.open = append(o.open, container{object: d == '{', wantKey: d == '{'})
	return true
}

// scalar is a value's token as an outline shows it, a value that is no
// object or array: a string decoded, and a number, a boolean or null as its
// JSON text.
func scalar(tok json.Token) string {
	switch t := tok.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	}
	return "null"
}

// add adds text's lines at level: its first indented two spaces a level, and
// its further lines two spaces more.
func (o *outliner) add(level int, text string) {
	pad := strings.Repeat("  ", level)
	for i, l := range splitLines(text) {
		if i == 1 {
			pad += "  "
		}
		o.lines = append(o.lines, rawLine{Text: pad + l})
	}
}

// mark marks the lines that hold each of quotes, as R153 says, and returns
// the quotes no line holds: a quote, its white space normalized as the board
// matches it (board.QuoteAppears), marks each line whose normalized text
// holds it, or else the lines of each smallest window of lines whose
// normalized texts, joined, hold it.
func mark(lines []rawLine, quotes []string) (missed []string) {
	if len(quotes) == 0 {
		return nil
	}
	j := joinLines(lines)
	for _, q := range quotes {
		if !j.mark(lines, board.NormalizeSpace(q)) {
			missed = append(missed, q)
		}
	}
	return missed
}

// joined is lines' texts, each normalized, joined by one space: the text,
// and where each line's normalized text starts and ends in it, an empty
// line's both at the end of the text before it.
type joined struct {
	text       string
	start, end []int
}

// joinLines is lines joined.
func joinLines(lines []rawLine) *joined {
	j := &joined{start: make([]int, len(lines)), end: make([]int, len(lines))}
	var b strings.Builder
	for i := range lines {
		n := board.NormalizeSpace(lines[i].Text)
		if n != "" && b.Len() > 0 {
			b.WriteByte(' ')
		}
		j.start[i] = b.Len()
		b.WriteString(n)
		j.end[i] = b.Len()
	}
	j.text = b.String()
	return j
}

// window is the lines an occurrence of a quote spans, from the first whose
// text it overlaps to the last.
type window struct{ first, last int }

// mark marks the lines of j that hold q, normalized, and reports whether any
// does. An occurrence of q in j's text spans a window of lines, one line
// when that line's text holds it, since q neither starts nor ends with the
// space that joins two lines; the windows of the fewest lines are marked, so
// that a quote a line holds marks the lines that hold it alone. Every
// occurrence is found, those that overlap included, and the windows are
// marked through a difference array, each in time linear in the text and
// the lines however often q occurs (see occurrences).
func (j *joined) mark(lines []rawLine, q string) bool {
	if q == "" {
		return false
	}
	var windows []window
	least := len(lines)
	occurrences(j.text, q, func(at int) {
		end := at + len(q)
		w := window{
			first: sort.Search(len(lines), func(i int) bool { return j.end[i] > at }),
			last:  sort.Search(len(lines), func(i int) bool { return j.start[i] >= end }) - 1,
		}
		windows = append(windows, w)
		least = min(least, w.last-w.first)
	})
	if len(windows) == 0 {
		return false
	}
	runs := make([]int, len(lines)+1)
	for _, w := range windows {
		if w.last-w.first == least {
			runs[w.first]++
			runs[w.last+1]--
		}
	}
	marking := 0
	for i := range lines {
		if marking += runs[i]; marking > 0 {
			lines[i].Quoted = true
		}
	}
	return true
}

// occurrences calls found with the start of each occurrence of q in text, in
// order, those that overlap included: a Knuth-Morris-Pratt search, in time
// linear in text and q, so that a quote of a run of one character in an
// output of that character costs no more than any other.
func occurrences(text, q string, found func(at int)) {
	fail := make([]int, len(q))
	for i, k := 1, 0; i < len(q); i++ {
		for k > 0 && q[i] != q[k] {
			k = fail[k-1]
		}
		if q[i] == q[k] {
			k++
		}
		fail[i] = k
	}
	for i, k := 0, 0; i < len(text); i++ {
		for k > 0 && text[i] != q[k] {
			k = fail[k-1]
		}
		if text[i] == q[k] {
			k++
		}
		if k == len(q) {
			found(i - len(q) + 1)
			k = fail[k-1]
		}
	}
}

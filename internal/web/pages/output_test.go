package pages_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/pages"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// outputRun is the run of the raw outputs' items.
const outputRun board.RunID = "run-1"

// noOutput is the raw output's refusal, whatever its cause.
const noOutput = "There is no output here."

// citing is the read of item id's board, opened for the principal sso:carol
// with the responder sso:erin, in run outputRun, whose evidence cites each
// call of cited with its quotes.
func citing(id item.ID, cited map[string][]string) *app.BoardRead {
	return &app.BoardRead{Board: &app.BoardView{
		Version: app.BoardVersion{Run: outputRun},
		View:    app.View{ID: id, State: "investigating", Type: "incident"},
		Opened:  item.Opened{Principal: "sso:carol", Responders: []string{"sso:erin"}},
		Cited:   cited,
	}}
}

// readCall is read call id of run, agent-a's of logs.search, with its stored
// arguments and output.
func readCall(id string, run board.RunID, args, output string) *board.ReadCall {
	return &board.ReadCall{ID: id, Run: run, Agent: "agent-a", Server: "logs", Tool: "search", Args: []byte(args), Output: []byte(output)}
}

// outputPath is the path of call's raw output on item id's board.
func outputPath(id item.ID, call string) string { return "/items/" + string(id) + "/calls/" + call }

// shown is a line of a raw block as a page shows it: its text, and whether
// it is marked quoted.
type shown struct {
	Text   string
	Quoted bool
}

// block is the lines of the raw block under the heading named, and whether
// the page shows that heading.
func block(t *testing.T, body []byte, heading string) ([]shown, bool) {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "h2" || nodeText(n) != heading {
			continue
		}
		for list := n.NextSibling; list != nil; list = list.NextSibling {
			if list.Type != html.ElementNode || list.Data != "ol" {
				continue
			}
			out := []shown{}
			for li := range list.ChildNodes() {
				if li.Type == html.ElementNode && li.Data == "li" {
					out = append(out, shownOf(t, li))
				}
			}
			return out, true
		}
		require.Failf(t, "no lines", "under %q", heading)
	}
	return nil, false
}

// shownOf is li as a line of a raw block: one <bdi>, which holds its text
// and nothing else, and the word quoted beside it when it is marked so.
func shownOf(t *testing.T, li *html.Node) shown {
	t.Helper()
	var s shown
	bdis, marks := 0, 0
	for c := range li.ChildNodes() {
		switch {
		case c.Type == html.ElementNode && c.Data == "bdi":
			bdis++
			for d := range c.Descendants() {
				require.NotEqual(t, html.ElementNode, d.Type, "a line's <bdi> holds text alone")
			}
			s.Text = nodeText(c)
		case c.Type == html.ElementNode && c.Data == "span" && attr(c, "class") == "mark":
			marks++
			assert.Equal(t, "quoted", nodeText(c))
		}
	}
	require.Equal(t, 1, bdis, "a line is one <bdi>")
	s.Quoted = attr(li, "class") == "quoted"
	assert.Equal(t, s.Quoted, marks == 1, "a line marked quoted says so in words")
	return s
}

// mainOf is body's <main>, rendered.
func mainOf(t *testing.T, body []byte) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "main" {
			var b bytes.Buffer
			require.NoError(t, html.Render(&b, n))
			return b.String()
		}
	}
	require.Fail(t, "no <main>")
	return ""
}

// firstParagraph is the text of the first <p> of body's <main>.
func firstParagraph(t *testing.T, body []byte) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "main" {
			for p := range n.Descendants() {
				if p.Type == html.ElementNode && p.Data == "p" {
					return nodeText(p)
				}
			}
		}
	}
	return ""
}

// The raw output of a call the item's board cites is shown to the item's
// members alone, its principal and its responders, whatever their roles
// (design D10, D15; R152): a viewer, and a fresh admin who is neither, are
// refused, as a call no entry cites, a cited call of another run, a board the
// seam hides, an item a later build holds and an item that does not exist
// are; every refusal is the same 404 page, and none reads the call but the
// other run's, which its own read tells. The page keeps 9a-1's policy.
func TestARawOutputIsShownToTheItemsMembersAlone(t *testing.T) {
	w := newWorld(t)
	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {"bad release"}, "call-2": {"another run"}}))
	w.boards.set("s1", citing("s1", map[string][]string{"call-1": {"bad release"}}))
	w.boards.set("h1", &app.BoardRead{Board: &app.BoardView{View: app.View{ID: "h1", HeldByLaterBuild: true}}})
	w.calls.set(readCall("call-1", outputRun, `{"q":"error"}`, `{"message":"a bad release"}`))
	w.calls.set(readCall("call-2", "run-9", `{}`, `"another run"`))
	w.calls.set(readCall("call-3", outputRun, `{}`, `"cited by no entry"`))
	restore := pages.SetVisible(func(v *app.View) bool { return v.ID != "s1" })
	defer restore()
	principal, responder := w.browser(t, "carol", auth.Viewer), w.browser(t, "erin", auth.Responder)
	viewer, admin := w.browser(t, "vic", auth.Viewer), w.browser(t, "adam", auth.Admin)
	var refused []string
	for _, tc := range []struct {
		name   string
		b      *webtest.Browser
		path   string
		status int
	}{
		{"its principal, a viewer by role", principal, outputPath("o1", "call-1"), http.StatusOK},
		{"its responder", responder, outputPath("o1", "call-1"), http.StatusOK},
		{"a viewer", viewer, outputPath("o1", "call-1"), http.StatusNotFound},
		{"a fresh admin who is not a member", admin, outputPath("o1", "call-1"), http.StatusNotFound},
		{"a call cited on no entry", principal, outputPath("o1", "call-3"), http.StatusNotFound},
		{"a cited call of another run", principal, outputPath("o1", "call-2"), http.StatusNotFound},
		{"a board the seam hides", principal, outputPath("s1", "call-1"), http.StatusNotFound},
		{"an item a later build holds", principal, outputPath("h1", "call-1"), http.StatusNotFound},
		{"an item that does not exist", principal, outputPath("nope", "call-1"), http.StatusNotFound},
	} {
		p := tc.b.Get(w.public + tc.path)

		require.Equal(t, tc.status, p.Resp.StatusCode, tc.name)
		assert.Equal(t, "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'",
			p.Resp.Header.Get("Content-Security-Policy"), "%s: 9a-1's policy", tc.name)
		assert.Equal(t, "no-store", p.Resp.Header.Get("Cache-Control"), tc.name)
		words := text(t, p.Body)
		if tc.status == http.StatusOK {
			assert.Equal(t, "Raw output of logs.search by agent-a.", firstParagraph(t, p.Body), tc.name)
			assert.NotContains(t, words, noOutput, tc.name)
			continue
		}
		assert.Contains(t, words, noOutput, tc.name)
		assert.NotContains(t, words, "Raw output of", tc.name)
		refused = append(refused, mainOf(t, p.Body))
	}
	require.Len(t, refused, 7)
	for i := range refused {
		assert.Equal(t, refused[0], refused[i], "every refusal is the same page")
	}
	assert.Equal(t, []string{"call-1", "call-1", "call-2"}, w.calls.read(), "no refusal reads the call but the other run's")
}

// A JSON output is shown as an outline of its decoded tokens, its lines
// numbered, and the line that holds a quote citing the call marked quoted
// (R153; plan review M10(b)): a string's further lines continue two spaces
// past its member's indent; the arguments beside it are shown the same way.
// An outline opens no line for the top level, a member's object or array as
// its key's line alone and an element's as a line of its own, with their
// members a level in, and a number, a boolean or null as its JSON text. (The
// outline's shape beyond M10(b) is the plan's.)
func TestARawOutputIsDecodedWithLineNumbersAndItsQuoteMarked(t *testing.T) {
	w := newWorld(t)
	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {"bad release"}, "call-2": {"twenty"}, "call-3": {"two"}}))
	w.calls.set(readCall("call-1", outputRun, `{"q":"error"}`, `{"message":"one\nchecked: bad release here\nthree"}`))
	w.calls.set(readCall("call-2", outputRun, `{"q":"x","limit":20}`,
		`{"a":{"b":[1,true,null,"twenty\nthirty"]},"c":"z","n":-1.50e3}`))
	w.calls.set(readCall("call-3", outputRun, `[]`, `[{"a":"one"},{"a":"two"}]`))
	b := w.browser(t, "carol", auth.Viewer)
	for _, tc := range []struct {
		call         string
		args, output []shown
	}{
		{"call-1", []shown{{"q: error", false}}, []shown{{"message: one", false}, {"  checked: bad release here", true}, {"  three", false}}},
		{"call-2", []shown{{"q: x", false}, {"limit: 20", false}}, []shown{
			{"a:", false}, {"  b:", false}, {"    1", false}, {"    true", false}, {"    null", false},
			{"    twenty", true}, {"      thirty", false}, {"c: z", false}, {"n: -1.50e3", false},
		}},
		{"call-3", []shown{}, []shown{{"", false}, {"  a: one", false}, {"", false}, {"  a: two", true}}},
	} {
		p := b.Get(w.public + outputPath("o1", tc.call))

		require.Equal(t, http.StatusOK, p.Resp.StatusCode, tc.call)
		output, ok := block(t, p.Body, "Output")
		require.True(t, ok, tc.call)
		assert.Equal(t, tc.output, output, tc.call)
		args, ok := block(t, p.Body, "Arguments")
		require.True(t, ok, tc.call)
		assert.Equal(t, tc.args, args, tc.call)
		_, stored := block(t, p.Body, "As stored, where the quote lies")
		assert.False(t, stored, "%s: the outline holds the quote, so no stored form is shown", tc.call)
		assert.NotContains(t, text(t, p.Body), "The output is cut here", tc.call)
	}
}

// A quote that no line holds is marked on the lines of the smallest window
// of lines that holds it, its every such window; a quote a line holds marks
// that line alone, though a window holds it too (R153).
func TestAQuoteSpanningLinesMarksItsSmallestWindow(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "erin", auth.Responder)
	for name, tc := range map[string]struct {
		output, quote string
		want          []shown
	}{
		"a window of two lines, within a larger one": {
			`{"m":"x\nbad release\nhere\ny"}`, "bad release here",
			[]shown{{"m: x", false}, {"  bad release", true}, {"  here", true}, {"  y", false}},
		},
		"a line, though a window holds it too": {
			`{"m":"bad release\nhere\nbad release here"}`, "bad release here",
			[]shown{{"m: bad release", false}, {"  here", false}, {"  bad release here", true}},
		},
		"the smaller of two windows that overlap": {
			`{"m":"x\nx\nx x"}`, "x x x",
			[]shown{{"m: x", false}, {"  x", true}, {"  x x", true}},
		},
		"every smallest window": {
			`{"m":"bad\nrelease\nok\nbad\nrelease"}`, "bad release",
			[]shown{{"m: bad", true}, {"  release", true}, {"  ok", false}, {"  bad", true}, {"  release", true}},
		},
	} {
		w.boards.set("o1", citing("o1", map[string][]string{"call-1": {tc.quote}}))
		w.calls.set(readCall("call-1", outputRun, `{}`, tc.output))

		p := b.Get(w.public + outputPath("o1", "call-1"))

		require.Equal(t, http.StatusOK, p.Resp.StatusCode, name)
		output, ok := block(t, p.Body, "Output")
		require.True(t, ok, name)
		assert.Equal(t, tc.want, output, name)
		_, stored := block(t, p.Body, "As stored, where the quote lies")
		assert.False(t, stored, name)
	}
}

// A quote that counted against the raw text alone, which the outline holds
// nowhere, is marked in the output as stored, which the page then also
// shows, under "As stored, where the quote lies", numbered and marked by the
// same rule; a quote the outline holds shows no stored form (R153; plan
// review I4).
func TestARawFormQuoteIsMarkedInTheOutputAsStored(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "carol", auth.Responder)
	for name, tc := range map[string]struct {
		output, quote string
		outline       []shown
		stored        []shown
	}{
		"a member's raw text": {
			`{"code": 504, "msg": "gateway"}`, `"code": 504`,
			[]shown{{"code: 504", false}, {"msg: gateway", false}},
			[]shown{{`{"code": 504, "msg": "gateway"}`, true}},
		},
		"an escape as stored": {
			"{\"m\":\"a\\nb\",\n\"n\":\"c\"}", `a\nb`,
			[]shown{{"m: a", false}, {"  b", false}, {"n: c", false}},
			[]shown{{`{"m":"a\nb",`, true}, {`"n":"c"}`, false}},
		},
		"a quote the outline holds": {
			`{"code": 504, "msg": "gateway"}`, "gateway",
			[]shown{{"code: 504", false}, {"msg: gateway", true}},
			nil,
		},
	} {
		w.boards.set("o1", citing("o1", map[string][]string{"call-1": {tc.quote}}))
		w.calls.set(readCall("call-1", outputRun, `{}`, tc.output))

		p := b.Get(w.public + outputPath("o1", "call-1"))

		require.Equal(t, http.StatusOK, p.Resp.StatusCode, name)
		outline, ok := block(t, p.Body, "Output")
		require.True(t, ok, name)
		assert.Equal(t, tc.outline, outline, name)
		stored, shownStored := block(t, p.Body, "As stored, where the quote lies")
		assert.Equal(t, tc.stored != nil, shownStored, name)
		assert.Equal(t, tc.stored, stored, name)
	}
}

// A raw output past 256 KiB shows its first 256 KiB, and says where it is
// cut; one of 256 KiB is shown whole (design D10; R159).
func TestARawOutputPastItsSizeIsCut(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "carol", auth.Responder)
	line := strings.Repeat("x", 1000) + "\n"
	for name, tc := range map[string]struct {
		size  int
		lines int
		last  int
		cut   bool
	}{
		// 256 KiB is 261 lines of 1001 bytes and 883 bytes of the next.
		"300 KiB": {300 << 10, 262, 883, true},
		"256 KiB": {256 << 10, 262, 883, false},
	} {
		output := strings.Repeat(line, 400)[:tc.size]
		w.boards.set("o1", citing("o1", map[string][]string{"call-1": {"x"}}))
		w.calls.set(readCall("call-1", outputRun, `{}`, output))

		p := b.Get(w.public + outputPath("o1", "call-1"))

		require.Equal(t, http.StatusOK, p.Resp.StatusCode, name)
		shownLines, ok := block(t, p.Body, "Output")
		require.True(t, ok, name)
		require.Len(t, shownLines, tc.lines, name)
		total := 0
		for i, l := range shownLines {
			total += len(l.Text)
			if i < len(shownLines)-1 {
				assert.Len(t, l.Text, 1000, "%s: line %d", name, i+1)
				total++
			}
		}
		assert.Len(t, shownLines[len(shownLines)-1].Text, tc.last, name)
		assert.Equal(t, 256<<10, total, "%s: its first 256 KiB", name)
		assert.Equal(t, tc.cut, strings.Contains(text(t, p.Body), "The output is cut here, at 256 KiB."), name)
	}
}

// A JSON output past 256 KiB is the start of one document cut by a size
// limit, and is shown as an outline of what was whole before the cut, which
// says where it is cut; a quote that lies past the cut is marked nowhere,
// and shows no stored form (R153). (The plan's.)
func TestAJSONOutputPastItsSizeIsAnOutlineCut(t *testing.T) {
	w := newWorld(t)
	rows := make([]string, 0, 30_000)
	for i := range 30_000 {
		rows = append(rows, fmt.Sprintf(`"row %05d"`, i))
	}
	output := `{"rows":[` + strings.Join(rows, ",") + `],"last":"past the cut"}`
	require.Greater(t, len(output), 300<<10)
	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {"past the cut"}}))
	w.calls.set(readCall("call-1", outputRun, `{}`, output))

	p := w.browser(t, "carol", auth.Responder).Get(w.public + outputPath("o1", "call-1"))

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	lines, ok := block(t, p.Body, "Output")
	require.True(t, ok)
	// 256 KiB holds {"rows":[ and as many whole rows, each of 12 bytes with
	// its comma, as fit after it: 21,844, and 7 bytes of the next, cut inside
	// its string.
	whole := (256<<10 - len(`{"rows":[`)) / len(`"row 00000",`)
	require.Equal(t, 21_844, whole)
	require.Len(t, lines, 1+whole)
	assert.Equal(t, shown{"rows:", false}, lines[0])
	assert.Equal(t, shown{"  row 00000", false}, lines[1])
	assert.Equal(t, shown{fmt.Sprintf("  row %05d", whole-1), false}, lines[len(lines)-1])
	assert.Contains(t, text(t, p.Body), "The output is cut here, at 256 KiB.")
	_, stored := block(t, p.Body, "As stored, where the quote lies")
	assert.False(t, stored, "a quote past the cut lies in neither form")
}

// A document nested deeper than 32 levels is not outlined: it is shown as
// stored, so that nothing of its structure is lost, and 256 KiB of brackets
// never makes a page of gigabytes of indentation; one of 32 levels is
// outlined, its deepest member at 31 levels' indent (R153's "any other
// output"; review 1's M2).
func TestADocumentNestedDeeperThan32LevelsIsShownAsStored(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "carol", auth.Responder)
	nested := func(levels int) string {
		return strings.Repeat(`{"k":`, levels) + `"deep\nline"` + strings.Repeat(`}`, levels)
	}
	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {"deep"}}))

	w.calls.set(readCall("call-1", outputRun, `{}`, nested(32)))
	p := b.Get(w.public + outputPath("o1", "call-1"))

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	lines, ok := block(t, p.Body, "Output")
	require.True(t, ok)
	require.Len(t, lines, 33, "32 levels are outlined")
	for i := range 31 {
		assert.Equal(t, shown{strings.Repeat("  ", i) + "k:", false}, lines[i], "level %d", i)
	}
	assert.Equal(t, shown{strings.Repeat("  ", 31) + "k: deep", true}, lines[31])
	assert.Equal(t, shown{strings.Repeat("  ", 32) + "line", false}, lines[32], "a string's further line, two spaces more")

	w.calls.set(readCall("call-1", outputRun, `{}`, nested(33)))
	p = b.Get(w.public + outputPath("o1", "call-1"))

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	lines, ok = block(t, p.Body, "Output")
	require.True(t, ok)
	assert.Equal(t, []shown{{nested(33), true}}, lines, "33 levels are shown as stored, whole")
	_, stored := block(t, p.Body, "As stored, where the quote lies")
	assert.False(t, stored, "the output is its stored form already")
}

// An incomplete JSON document is outlined only where the page cut it (R153:
// "one JSON document, or the start of one cut by a size limit"; review 1's
// I1): one the page did not cut, as a tool that cut its own output stores
// it, is shown as stored, whole, its open string, literal or member dropped
// from nothing, and a quote marked there; one the page cut is still
// outlined, as far as it was whole, with the cut sentence, whether it ends
// inside a token or between two.
func TestAnIncompleteJSONDocumentIsOutlinedOnlyWhereThePageCutIt(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "carol", auth.Responder)
	for name, tc := range map[string]struct {
		output, quote string
	}{
		"a string open, after a member": {`{"status":"ok","log":"line one\nline two\nline three`, "status"},
		"a string open, alone":          {`{"log":"line one\nline two\nline three`, "line two"},
		"a literal open":                {`{"a":tru`, `"a"`},
		"a member open in an array":     {`[{"a":1},{"a":2},{"a":"trunc`, "trunc"},
		"an object open after a number": {`{"a": 12`, "12"},
		// Nor is a whole document followed by more, open or whole (the
		// plan's).
		"a document, then an open string": {`1 "abc`, "abc"},
		"a document, then a second":       {`{"a":1} {"b":2}`, `"b"`},
	} {
		w.boards.set("o1", citing("o1", map[string][]string{"call-1": {tc.quote}}))
		w.calls.set(readCall("call-1", outputRun, `{}`, tc.output))

		p := b.Get(w.public + outputPath("o1", "call-1"))

		require.Equal(t, http.StatusOK, p.Resp.StatusCode, name)
		lines, ok := block(t, p.Body, "Output")
		require.True(t, ok, name)
		assert.Equal(t, []shown{{tc.output, true}}, lines, "%s: shown as stored, whole, the quote marked there", name)
		_, stored := block(t, p.Body, "As stored, where the quote lies")
		assert.False(t, stored, name)
		assert.NotContains(t, text(t, p.Body), "The output is cut here", name)
	}

	// Cut between two tokens: past the cut the document goes on.
	output := `{"rows":["a","b"` + strings.Repeat(" ", 300<<10) + `]}`
	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {"b"}}))
	w.calls.set(readCall("call-1", outputRun, `{}`, output))

	p := b.Get(w.public + outputPath("o1", "call-1"))

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	lines, ok := block(t, p.Body, "Output")
	require.True(t, ok)
	assert.Equal(t, []shown{{"rows:", false}, {"  a", false}, {"  b", true}}, lines, "a cut document is outlined")
	assert.Contains(t, text(t, p.Body), "The output is cut here, at 256 KiB.")
}

// Each raw block scrolls inside itself, and so takes the keyboard's focus,
// which a scroll container in Safari does not by itself, named by its
// section's heading (review 1's M4).
func TestARawBlockScrollsFromTheKeyboard(t *testing.T) {
	w := newWorld(t)
	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {`"code": 504`}}))
	w.calls.set(readCall("call-1", outputRun, `{"q":"error"}`, `{"code": 504, "msg": "gateway"}`))

	p := w.browser(t, "carol", auth.Responder).Get(w.public + outputPath("o1", "call-1"))

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	doc, err := html.Parse(bytes.NewReader(p.Body))
	require.NoError(t, err)
	headings := map[string]string{}
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "h2" {
			headings[attr(n, "id")] = nodeText(n)
		}
	}
	var named []string
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "ol" || attr(n, "class") != "raw" {
			continue
		}
		assert.Equal(t, "0", attr(n, "tabindex"), "a raw block takes the keyboard's focus")
		id := attr(n, "aria-labelledby")
		require.NotEmpty(t, id, "a raw block is named")
		named = append(named, headings[id])
	}
	assert.Equal(t, []string{"Arguments", "Output", "As stored, where the quote lies"}, named, "each by its section's heading")
}

// A raw block takes the keyboard's focus only when it holds lines, which it
// scrolls: an empty one, as the arguments {} give, is no tab stop (Task 4's
// re-review).
func TestAnEmptyRawBlockIsNoTabStop(t *testing.T) {
	w := newWorld(t)
	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {"gateway"}}))
	w.calls.set(readCall("call-1", outputRun, `{}`, `{"msg": "gateway"}`))

	p := w.browser(t, "carol", auth.Responder).Get(w.public + outputPath("o1", "call-1"))

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	args, ok := block(t, p.Body, "Arguments")
	require.True(t, ok)
	assert.Empty(t, args)
	doc, err := html.Parse(bytes.NewReader(p.Body))
	require.NoError(t, err)
	stops := map[string]bool{}
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "ol" && attr(n, "class") == "raw" {
			stops[attr(n, "aria-labelledby")] = hasAttr(n, "tabindex")
		}
	}
	assert.Equal(t, map[string]bool{"arguments": false, "output": true}, stops, "the empty arguments are no tab stop; the output is")
}

// A cited call whose stored output is no longer kept says so, and shows
// nothing of the call (design D10).
func TestACitedCallNoLongerKeptSaysSo(t *testing.T) {
	w := newWorld(t)
	w.boards.set("o1", citing("o1", map[string][]string{"gone-1": {"bad release"}}))

	p := w.browser(t, "erin", auth.Responder).Get(w.public + outputPath("o1", "gone-1"))

	assert.Equal(t, http.StatusGone, p.Resp.StatusCode)
	words := text(t, p.Body)
	assert.Contains(t, words, "This call's stored output is no longer kept: outputs are kept 30 days.")
	assert.NotContains(t, words, noOutput)
	assert.NotContains(t, words, "Raw output of")
	assert.Equal(t, []string{"gone-1"}, w.calls.read())
}

// Every line of a raw output, of its arguments and of its stored form is
// inert and isolated (design D11; R154): a script in an output is text,
// escaped, and no element; a bidi control is marked, inside a <bdi>; and the
// call's names are each inside a <bdi> too.
func TestEveryRawOutputLineIsInertAndIsolated(t *testing.T) {
	w := newWorld(t)
	output := "{\"m\":\"<script>alert(1)</script>\",\"n\":\"abc\u202edef\"}"
	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {`"m":"<script>`}}))
	call := readCall("call-1", outputRun, `{"q":"<script>alert(2)</script>"}`, output)
	call.Agent = "agent\u202ea"
	w.calls.set(call)

	p := w.browser(t, "carol", auth.Responder).Get(w.public + outputPath("o1", "call-1"))

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	body := string(p.Body)
	assert.NotContains(t, strings.ToLower(body), "<script")
	assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
	assert.Contains(t, body, "&lt;script&gt;alert(2)&lt;/script&gt;")
	doc, err := html.Parse(bytes.NewReader(p.Body))
	require.NoError(t, err)
	marks := 0
	for n := range doc.Descendants() {
		require.False(t, n.Type == html.ElementNode && n.Data == "script", "no script element")
		if n.Type == html.TextNode && strings.Contains(n.Data, "⟨U+202E⟩") {
			marks++
			require.NotNil(t, n.Parent)
			assert.Equal(t, "bdi", n.Parent.Data, "a marked rune sits inside a <bdi>: %q", n.Data)
		}
		if n.Type == html.TextNode {
			assert.NotContains(t, n.Data, "\u202e", "no bidi control reaches the page unmarked")
		}
	}
	assert.Equal(t, 3, marks, "in the outline, the stored form and the agent's name")
	for _, heading := range []string{"Arguments", "Output", "As stored, where the quote lies"} {
		lines, ok := block(t, p.Body, heading)
		require.True(t, ok, heading)
		require.NotEmpty(t, lines, heading)
	}
}

// A board read's failure, the board page's or a raw output's, is logged by
// its class and its item, never by its error's text, which on a corrupt row
// can quote a stored value (Task 4's ruling): records or a board that are
// invalid are corrupt, a 500; a read whose wait ran out is a deadline, a
// 503 within the wait; any other failure is a store's, a 503; and a cited
// call whose read failed is a store's, a 503. No page says the error either.
func TestABoardReadsFailureIsLoggedByItsClassAndItemNeverItsText(t *testing.T) {
	const marker = "a stored text the log never holds"
	corrupt := fmt.Errorf("a board rule quoting %q: %w", marker, board.ErrInvalid)
	store := fmt.Errorf("read the records of %q: %w", marker, errors.New("connection reset"))
	restore := pages.SetBoardWait(50 * time.Millisecond)
	defer restore()
	w := newWorld(t)
	w.boards.set("o4", citing("o4", map[string][]string{"call-1": {"bad release"}}))
	b := w.browser(t, "carol", auth.Responder)
	for _, tc := range []struct {
		name   string
		fail   func()
		path   string
		status int
		msg    string
		class  string
	}{
		{"the board page, records invalid", func() { w.boards.fail(fmt.Errorf("fold %q: %w", marker, item.ErrInvalid), false) }, "/items/b1",
			http.StatusInternalServerError, "an item's records are invalid", "corrupt"},
		{"the board page, a store's failure", func() { w.boards.fail(store, false) }, "/items/b2",
			http.StatusServiceUnavailable, "the web interface could not read an item", "store"},
		{"the board page, past its wait", func() { w.boards.fail(nil, true) }, "/items/b3",
			http.StatusServiceUnavailable, "the web interface could not read an item", "deadline"},
		{"a raw output, a board invalid", func() { w.boards.fail(corrupt, false) }, outputPath("o1", "call-1"),
			http.StatusInternalServerError, "an item's records are invalid", "corrupt"},
		{"a raw output, a store's failure", func() { w.boards.fail(store, false) }, outputPath("o2", "call-1"),
			http.StatusServiceUnavailable, "the web interface could not read an item", "store"},
		{"a raw output, past its wait", func() { w.boards.fail(nil, true) }, outputPath("o3", "call-1"),
			http.StatusServiceUnavailable, "the web interface could not read an item", "deadline"},
		{"a raw output, its call's read failed", func() { w.boards.fail(nil, false); w.calls.fail(store) }, outputPath("o4", "call-1"),
			http.StatusServiceUnavailable, "the web interface could not read a cited call", "store"},
	} {
		tc.fail()
		began := time.Now()

		p := b.Get(w.public + tc.path)

		assert.Less(t, time.Since(began), time.Second, "%s: within the board's wait", tc.name)
		assert.Equal(t, tc.status, p.Resp.StatusCode, tc.name)
		assert.NotContains(t, string(p.Body), marker, tc.name)
		id := strings.Split(tc.path, "/")[2]
		line := pageLog(t, w.logs.String(), tc.msg, id)
		assert.Equal(t, tc.class, line["class"], "%s: the line names the failure's class", tc.name)
		assert.NotContains(t, line, "err", tc.name)
	}
	assert.NotContains(t, w.logs.String(), marker, "never the error's text")
}

// pageLog is the first of the pages' JSON log lines whose message starts
// with msg and that names item id.
func pageLog(t *testing.T, logs, msg, id string) map[string]any {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(logs), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if m, _ := entry["msg"].(string); strings.HasPrefix(m, msg) && entry["item"] == id {
			return entry
		}
	}
	require.Failf(t, "no line logged", "%q of item %s in %s", msg, id, logs)
	return nil
}

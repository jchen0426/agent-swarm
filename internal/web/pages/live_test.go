package pages_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/web"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/pages"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The policies (design D3, D1; the Global Constraints): 9a-1's, which every
// page but the board's keeps, byte for byte; and the board pages', 9a-1's
// with their own script, their polls to their own origin, and Trusted Types
// required with no policy allowed (review M1).
const (
	nineA1Policy = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
	boardsPolicy = nineA1Policy + "; script-src 'self'; connect-src 'self'; require-trusted-types-for 'script'; trusted-types 'none'"
)

// livePath is live.js's path: /static/live.<the first 12 hex of the SHA-256
// of the script the package embeds>.js.
func livePath() string {
	sum := sha256.Sum256(pages.LiveScript)
	return "/static/live." + hex.EncodeToString(sum[:])[:12] + ".js"
}

// polled is a GET of target as live.js sends it, with the session cookie c,
// nil for none, and an If-None-Match line for each of etags; it follows no
// redirect.
func (w *world) polled(t *testing.T, target string, c *http.Cookie, etags ...string) (resp *http.Response, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, w.public+target, http.NoBody)
	require.NoError(t, err)
	for _, e := range etags {
		req.Header.Add("If-None-Match", e)
	}
	if c != nil {
		req.AddCookie(c)
	}
	return w.do(t, req)
}

// viewer is a live session of sso:vic, a viewer, signed in now for an hour.
func (w *world) viewer(t *testing.T) *http.Cookie {
	t.Helper()
	return w.session(t, "vic", 0, time.Hour, auth.Viewer)
}

// scriptsOf are body's script elements.
func scriptsOf(t *testing.T, body []byte) []*html.Node {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	var out []*html.Node
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "script" {
			out = append(out, n)
		}
	}
	return out
}

// liveTag is the board page's one script element, as it is written.
func liveTag() string { return `<script src="` + livePath() + `" defer></script>` }

// fedRead is the read of item id's board whose feed holds f's lines, its
// times fixed, so that two reads of it render alike: last updated at the
// feed's first line's minute.
func fedRead(f *feedFixture) *app.BoardRead {
	r := f.read()
	r.Board.Header.Updated = feedAt
	return r
}

// A board's regions answer a poll with 304 and no body while its ETag holds,
// and 200 with the regions document once they change (design D1; R156): the
// page's own data-etag polled answers 304; a new entry in its feed answers
// 200 with a new ETag, in the header and in the document's #board, and the
// regions, of which the feed's data-v alone differs from the page's; polled
// with the new ETag, 304 again.
func TestTheRegionsAnswer304ToTheirETagAnd200WhenTheyChange(t *testing.T) {
	w := newClockWorld(t)
	c := w.viewer(t)
	f := &feedFixture{id: "p1"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	f.entry(agentA, readEvidence("the disk is full", "call-1", "disk full on db-1", "logs/search", "", ""))
	w.boards.set("p1", fedRead(f))
	resp, page := w.get(t, "/items/p1", c)
	require.Equal(t, http.StatusOK, resp.StatusCode, page)
	shown := versionsOf(t, []byte(page))

	resp, body := w.polled(t, "/items/p1/board", c, shown["etag"])
	require.Equal(t, http.StatusNotModified, resp.StatusCode, body)
	assert.Empty(t, body, "a 304 has no body")
	assert.Equal(t, shown["etag"], resp.Header.Get("ETag"))

	f.entry(agentB, readEvidence("the disk filled at 14:00", "call-2", "disk full since 14:00", "logs/search", "", ""))
	w.boards.set("p1", fedRead(f))
	resp, body = w.polled(t, "/items/p1/board", c, shown["etag"])
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	got := versionsOf(t, []byte(body))
	assert.NotEqual(t, shown["etag"], got["etag"], "a new ETag")
	assert.Equal(t, got["etag"], resp.Header.Get("ETag"), "the header's ETag is the document's")
	for _, id := range regionNames {
		if id == "feed" {
			assert.NotEqual(t, shown[id], got[id], "the feed changed")
			continue
		}
		assert.Equal(t, shown[id], got[id], "%s did not change", id)
	}
	assert.Len(t, feedAnchors(t, []byte(body)), 3, "the regions document holds the feed's every line")

	resp, body = w.polled(t, "/items/p1/board", c, got["etag"])
	assert.Equal(t, http.StatusNotModified, resp.StatusCode, body)
	assert.Empty(t, body)
}

// If-None-Match is compared weakly (R173; RFC 9110 §13.1.2; plan review M4):
// the ETag itself, weakened by W/ as a proxy that compresses may weaken it,
// in a list, or on a header line of its own among others, answers 304; *,
// another tag, the tag unquoted or weakened by a lowercase w/, and a list
// that does not parse, as one with an element that is no entity tag, each
// answer 200, as no If-None-Match does.
func TestIfNoneMatchIsComparedWeakly(t *testing.T) {
	w := newWorld(t)
	c := w.viewer(t)
	w.boards.set("w1", boardRead("w1", "investigating", running))
	resp, page := w.get(t, "/items/w1", c)
	require.Equal(t, http.StatusOK, resp.StatusCode, page)
	etag := attr(node(t, []byte(page), "board"), "data-etag")
	require.Regexp(t, `^"[0-9a-f]{32}"$`, etag)
	opaque := strings.Trim(etag, `"`)

	for _, tc := range []struct {
		name   string
		values []string
		status int
	}{
		{"the ETag", []string{etag}, http.StatusNotModified},
		{"the ETag weakened", []string{"W/" + etag}, http.StatusNotModified},
		{"a list holding it", []string{`"0123", ` + etag + `, "4567"`}, http.StatusNotModified},
		{"a list holding it weakened, without spaces", []string{`"0123",W/` + etag}, http.StatusNotModified},
		{"a list with empty elements", []string{` , ` + etag + ` ,`}, http.StatusNotModified},
		{"a line of its own among others", []string{`"0123"`, etag}, http.StatusNotModified},
		{"*", []string{"*"}, http.StatusOK},
		{"another tag", []string{`"0123456789abcdef0123456789abcdef"`}, http.StatusOK},
		{"the tag unquoted", []string{opaque}, http.StatusOK},
		{"weakened by a lowercase w/", []string{"w/" + etag}, http.StatusOK},
		{"weakened with a space", []string{"W/ " + etag}, http.StatusOK},
		{"a list with an element that is no entity tag", []string{etag + `, garbage`}, http.StatusOK},
		{"a tag left open", []string{`"` + opaque}, http.StatusOK},
		{"a tag with a space in it", []string{`"a b", ` + etag}, http.StatusOK},
		{"none", nil, http.StatusOK},
	} {
		resp, body := w.polled(t, "/items/w1/board", c, tc.values...)

		if !assert.Equal(t, tc.status, resp.StatusCode, tc.name) {
			continue
		}
		if tc.status == http.StatusNotModified {
			assert.Empty(t, body, tc.name)
			continue
		}
		if root := node(t, []byte(body), "board"); assert.NotNil(t, root, tc.name) {
			assert.Equal(t, etag, attr(root, "data-etag"), "%s: the regions document", tc.name)
		}
	}
}

// The ETag changes with whatever a board's regions show (re-review N1; plan
// review M3): from one board, each change alone changes it, the strip, the
// queue's position, the minute an "ago" counts, the kill switch, and the
// standby's notice; nothing changed keeps it, and so does the clock moved
// 30 s within the minute, no time's words changing.
func TestTheETagChangesWithTheStripTheQueueTheMinuteAndTheKillSwitch(t *testing.T) {
	w := newClockWorld(t)
	c := w.viewer(t)
	for _, tc := range []struct {
		name    string
		change  func(r *app.BoardRead)
		changes bool
	}{
		{name: "nothing changed"},
		{name: "the clock moved 30 s, no time's words changing", change: func(*app.BoardRead) { w.clock.advance(30 * time.Second) }},
		{name: "the strip: a sibling's state", changes: true, change: func(r *app.BoardRead) { r.Strip.Items[0].State = item.StatusAwaitingClose }},
		{name: "the strip: its listing failed", changes: true, change: func(r *app.BoardRead) { r.Strip = app.Strip{Unread: true} }},
		{name: "the queue: its position", changes: true, change: func(r *app.BoardRead) { r.Facts.QueuePosition = 1 }},
		{name: "the minute an ago counts", changes: true, change: func(*app.BoardRead) { w.clock.advance(time.Minute) }},
		{name: "the kill switch: turned on", changes: true, change: func(r *app.BoardRead) {
			r.Facts.KillSwitch = app.KillSwitch{On: true, Seq: 2, OnSeq: 2, By: "sso:bob", Since: r.Board.Header.Updated}
		}},
		{name: "the standby's notice", changes: true, change: func(*app.BoardRead) { w.leader.set(false, false) }},
	} {
		// The board, as of now: a queued item at position 2, last updated
		// 10 min 5 s ago, its strip of one sibling, investigating.
		w.leader.set(true, false)
		r := boardRead("e1", "queued", func(r *app.BoardRead) {
			r.Facts.QueuePosition = 2
			r.Board.Header.Updated = w.clock.Now().Add(-10*time.Minute - 5*time.Second)
			r.Strip = app.Strip{Items: []app.StripItem{{ID: "s1", Type: "incident", State: item.StatusInvestigating}}}
		})
		w.boards.set("e1", r)
		resp, page := w.get(t, "/items/e1", c)
		require.Equal(t, http.StatusOK, resp.StatusCode, tc.name)
		etag := attr(node(t, []byte(page), "board"), "data-etag")
		if tc.change != nil {
			tc.change(r)
			w.boards.set("e1", r)
		}

		resp, body := w.polled(t, "/items/e1/board", c, etag)

		if !tc.changes {
			assert.Equal(t, http.StatusNotModified, resp.StatusCode, "%s: the same ETag", tc.name)
			continue
		}
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: the ETag changed", tc.name)
		assert.NotEqual(t, etag, attr(node(t, []byte(body), "board"), "data-etag"), tc.name)
	}
}

// Every region exists in every regions document (R170; plan review I2), as
// on the page: each board state's fixture, a leader's empty standby notice
// and a lone item's empty strip among them, the hidden copy, and a later
// build's item each give all seven region ids, once each, inside the
// document's #board; and each region's data-v, and the ETag, are the page's,
// since the document runs the page's own templates over the same models.
func TestEveryRegionExistsInEveryRegionsDocument(t *testing.T) {
	w := newClockWorld(t)
	c := w.viewer(t)
	regions := func(name string, id item.ID) {
		t.Helper()
		resp, page := w.get(t, "/items/"+string(id), c)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", name, page)
		resp, body := w.polled(t, "/items/"+string(id)+"/board", c)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", name, body)
		root := node(t, []byte(body), "board")
		require.NotNil(t, root, name)
		count := map[string]int{}
		for n := range root.Descendants() {
			if n.Type == html.ElementNode && hasAttr(n, "id") {
				count[attr(n, "id")]++
			}
		}
		for _, region := range regionNames {
			assert.Equal(t, 1, count[region], "%s: region %s inside #board", name, region)
		}
		assert.Equal(t, versionsOf(t, []byte(page)), versionsOf(t, []byte(body)), "%s: the page's versions", name)
	}
	for _, row := range stateRows() {
		w.boards.set(row.read.Board.View.ID, row.read)
		w.leader.set(!row.standby, false)
		regions(row.name, row.read.Board.View.ID)
	}
	w.leader.set(true, false)
	w.boards.set("h1", richRead("h1", ""))
	restore := pages.SetVisible(func(*app.View) bool { return false })
	regions("the hidden copy", "h1")
	restore()
	w.boards.set("l1", &app.BoardRead{Board: &app.BoardView{View: app.View{ID: "l1", HeldByLaterBuild: true}}})
	regions("a later build's item", "l1")
}

// orderOf is the order an order string gives, its record seq and its board
// seq.
func orderOf(t *testing.T, order string) (record, entry int64) {
	t.Helper()
	r, b, ok := strings.Cut(order, ".")
	require.True(t, ok, "an order %q", order)
	record, err := strconv.ParseInt(r, 10, 64)
	require.NoError(t, err)
	entry, err = strconv.ParseInt(b, 10, 64)
	require.NoError(t, err)
	return record, entry
}

// X-Board-Order is sent for a visible item alone (design D1; re-review N3,
// N5): for one version it is the page's data-order, the latest record's seq
// and the latest entry's, on a 200 and on a 304, and the regions document's
// #board carries it too; after entries and records it is larger in both
// parts; a hidden item's polls, and a later build's item's, carry none, and
// no data-order.
func TestXBoardOrderIsSentForAVisibleItemAlone(t *testing.T) {
	w := newClockWorld(t)
	c := w.viewer(t)
	f := &feedFixture{id: "o1"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	f.entry(agentA, readEvidence("the disk is full", "call-1", "disk full on db-1", "logs/search", "", ""))
	w.boards.set("o1", fedRead(f))
	resp, page := w.get(t, "/items/o1", c)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	root := node(t, []byte(page), "board")
	order, etag := attr(root, "data-order"), attr(root, "data-etag")
	require.Equal(t, "3.7", order, "the fixture's latest record, seq 3, and entry, seq 7")

	resp, body := w.polled(t, "/items/o1/board", c)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, order, resp.Header.Get("X-Board-Order"))
	assert.Equal(t, order, attr(node(t, []byte(body), "board"), "data-order"))
	resp, _ = w.polled(t, "/items/o1/board", c, etag)
	require.Equal(t, http.StatusNotModified, resp.StatusCode)
	assert.Equal(t, order, resp.Header.Get("X-Board-Order"), "a 304 carries it too")

	f.record(&item.Body{Started: &item.Started{Commit: "0123456789abcdef"}})
	f.entry(agentB, readEvidence("the disk filled at 14:00", "call-2", "disk full since 14:00", "logs/search", "", ""))
	grown := fedRead(f)
	grown.Board.Version.Record, grown.Board.Version.Board = app.Mark{ID: "rec-5", Seq: 5}, app.Mark{ID: "ev-9", Seq: 9}
	w.boards.set("o1", grown)
	resp, body = w.polled(t, "/items/o1/board", c, etag)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	later := resp.Header.Get("X-Board-Order")
	assert.Equal(t, later, attr(node(t, []byte(body), "board"), "data-order"))
	r0, b0 := orderOf(t, order)
	r1, b1 := orderOf(t, later)
	assert.Greater(t, r1, r0, "its records' part")
	assert.Greater(t, b1, b0, "its entries' part")

	restore := pages.SetVisible(func(*app.View) bool { return false })
	defer restore()
	w.boards.set("l1", &app.BoardRead{Board: &app.BoardView{View: app.View{ID: "l1", HeldByLaterBuild: true}}})
	for _, id := range []string{"o1", "l1"} {
		resp, body = w.polled(t, "/items/"+id+"/board", c)
		require.Equal(t, http.StatusOK, resp.StatusCode, id)
		assert.Empty(t, resp.Header.Values("X-Board-Order"), id)
		assert.False(t, hasAttr(node(t, []byte(body), "board"), "data-order"), id)
		resp, _ = w.polled(t, "/items/"+id+"/board", c, resp.Header.Get("ETag"))
		require.Equal(t, http.StatusNotModified, resp.StatusCode, id)
		assert.Empty(t, resp.Header.Values("X-Board-Order"), "%s: a 304 carries none either", id)
	}
}

// A poll reads the feed's paging as its page does, through one helper (R167;
// Task 6's review): a page of the whole feed, ?feed=all, polled with its
// query, as live.js sends location.search, answers 304, its feed whole; the
// same ETag polled without the query answers 200, its feed the newest 50
// lines, with "Show earlier". (The plan's.)
func TestAPollKeepsThePagesWholeFeed(t *testing.T) {
	w := newClockWorld(t)
	c := w.viewer(t)
	f := &feedFixture{id: "f2"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	for i := range 59 {
		f.entry(agentA, readEvidence("claim "+strconv.Itoa(i), "call-1", "disk full on db-1", "logs/search", "", ""))
	}
	w.boards.set("f2", fedRead(f))
	resp, page := w.get(t, "/items/f2?feed=all", c)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, feedAnchors(t, []byte(page)), 60, "the page shows the whole feed")
	etag := attr(node(t, []byte(page), "board"), "data-etag")

	resp, body := w.polled(t, "/items/f2/board?feed=all", c, etag)
	assert.Equal(t, http.StatusNotModified, resp.StatusCode, "its poll keeps the whole feed")
	assert.Empty(t, body)
	resp, body = w.polled(t, "/items/f2/board", c, etag)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the newest lines are other regions")
	assert.Len(t, feedAnchors(t, []byte(body)), 50)
	assert.Contains(t, regionWords(t, []byte(body), "feed"), "Show earlier")
}

// A poll past the web pool's wait answers 503 within it, in the board page's
// words (R141), with no ETag, no order and no redirect, so that live.js
// backs off from it.
func TestAPollPastTheWebsPoolWaitAnswers503(t *testing.T) {
	restore := pages.SetBoardWait(50 * time.Millisecond)
	defer restore()
	w := newWorld(t)
	w.boards.fail(nil, true)
	began := time.Now()

	resp, body := w.polled(t, "/items/a1/board", w.viewer(t))

	assert.Less(t, time.Since(began), time.Second, "within the board's wait")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, text(t, []byte(body)), "The board could not be read just now. Reload the page to try again.")
	assert.Empty(t, resp.Header.Get("ETag"))
	assert.Empty(t, resp.Header.Values("X-Board-Order"))
	assert.Empty(t, resp.Header.Get("Location"))
}

// The board pages, GET /items/{id} and its polls, GET /items/{id}/board, send
// the board pages' policy exactly, whatever they answer; every other page
// the interface serves keeps 9a-1's byte for byte (design D15): the raw
// output's page among them, though it shows a board too, and the script and
// the stylesheet.
func TestBoardPagesSendTheBoardPolicyAndEveryOtherPageItsOwn(t *testing.T) {
	assert.Equal(t, boardsPolicy, web.BoardPolicy)
	for name, p := range servedResponses(t) {
		want := nineA1Policy
		if slices.Contains(boardPageNames, name) {
			want = boardsPolicy
		}
		assert.Equal(t, want, p.policy, name)
	}

	w := newWorld(t)
	c := w.viewer(t)
	w.boards.set("b1", richRead("b1", ""))
	resp, page := w.get(t, "/items/b1", c)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	etag := attr(node(t, []byte(page), "board"), "data-etag")
	expired := w.session(t, "vic", 9*time.Hour, -time.Minute, auth.Viewer)
	for _, tc := range []struct {
		name   string
		path   string
		c      *http.Cookie
		etags  []string
		status int
		policy string
	}{
		{"a board's poll", "/items/b1/board", c, nil, http.StatusOK, boardsPolicy},
		{"a board's poll, unchanged", "/items/b1/board", c, []string{etag}, http.StatusNotModified, boardsPolicy},
		{"a board's poll, signed out", "/items/b1/board", expired, nil, http.StatusUnauthorized, boardsPolicy},
		{"a board's poll, no such item", "/items/b9/board", c, nil, http.StatusNotFound, boardsPolicy},
		{"no such board", "/items/b9", c, nil, http.StatusNotFound, boardsPolicy},
		{"a board's page, signed out", "/items/b1", expired, nil, http.StatusSeeOther, boardsPolicy},
		{"the script", livePath(), nil, nil, http.StatusOK, nineA1Policy},
		{"the stylesheet", "/static/app.css", nil, nil, http.StatusOK, nineA1Policy},
	} {
		resp, _ := w.polled(t, tc.path, tc.c, tc.etags...)

		assert.Equal(t, tc.status, resp.StatusCode, tc.name)
		assert.Equal(t, tc.policy, resp.Header.Get("Content-Security-Policy"), tc.name)
	}
}

// live.js is served by its hash alone, with no session (design D1; plan
// review M10(i); review I1.4): the board page names /static/live.<the first
// 12 hex of its SHA-256>.js, with defer and no content; that path answers
// the embedded script, as JavaScript, kept a year as immutable, with a
// session, with none, and with an expired one, never a redirect and never a
// cookie; any other name under /static/ answers 404, and the stylesheet is
// still served.
func TestTheLiveScriptIsServedByItsHashWithoutASession(t *testing.T) {
	require.NotEmpty(t, pages.LiveScript, "the package embeds live.js")
	w := newWorld(t)
	w.boards.set("b1", richRead("b1", ""))
	resp, page := w.get(t, "/items/b1", w.viewer(t))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	scripts := scriptsOf(t, []byte(page))
	require.Len(t, scripts, 1)
	assert.Equal(t, []html.Attribute{{Key: "src", Val: livePath()}, {Key: "defer"}}, scripts[0].Attr)
	assert.Nil(t, scripts[0].FirstChild, "no content")
	assert.Regexp(t, `^/static/live\.[0-9a-f]{12}\.js$`, livePath())

	expired := w.session(t, "vic", 9*time.Hour, -time.Minute, auth.Viewer)
	for name, c := range map[string]*http.Cookie{"a session": w.viewer(t), "none": nil, "an expired one": expired} {
		resp, body := w.get(t, livePath(), c)

		require.Equal(t, http.StatusOK, resp.StatusCode, name)
		assert.Equal(t, string(pages.LiveScript), body, name)
		assert.Equal(t, "text/javascript; charset=utf-8", resp.Header.Get("Content-Type"), name)
		assert.Equal(t, "public, max-age=31536000, immutable", resp.Header.Get("Cache-Control"), name)
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"), name)
		assert.Empty(t, resp.Header.Values("Set-Cookie"), name)
		assert.Empty(t, resp.Header.Get("Location"), name)
	}
	for _, other := range []string{"/static/live.js", "/static/live.000000000000.js", "/static/other.css", "/static/" + strings.TrimPrefix(livePath(), "/static/live.")} {
		resp, _ := w.get(t, other, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, other)
	}
	resp, _ = w.get(t, "/static/app.css", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// sinks are the names live.js never holds (the Global Constraints; R166):
// every HTML sink, the evaluation of a string, a javascript: URL, fetch,
// which its polls do not use, and localStorage, which outlives the tab.
var sinks = []string{
	"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "DOMParser", "createContextualFragment", "srcdoc",
	"setHTMLUnsafe", "parseHTMLUnsafe", "eval", "Function", "javascript:", "fetch(", "localStorage",
}

// stringTimer is a timer given a string, which would be evaluated.
const stringTimer = "set(Timeout|Interval)\\s*\\(\\s*[\"'`]"

// literals are the string literals of the script src, its comments left
// out: each "…", '…' or `…`, as written, its escapes kept. (The script holds
// no regular expression literal, which this reading would take for code.)
func literals(src string) []string {
	var out []string
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case strings.HasPrefix(src[i:], "//"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return append(out, src[i:])
			}
			i += end + 3
		case c == '"' || c == '\'' || c == '`':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			out = append(out, src[i+1:min(j, len(src))])
			i = j
		}
	}
	return out
}

// live.js holds no HTML sink and no word (the Global Constraints; R155,
// R166), as a reading of the script the package embeds finds, which runs in
// no browser before the gate: none of the sinks' names, no timer given a
// string, at most 150 lines, and no string literal that holds a space, as a
// word or a sentence would; and it names each region of the board by its id.
// The reading finds a spaced literal, and leaves out a comment's.
func TestTheLiveScriptHoldsNoHTMLSinkAndNoWords(t *testing.T) {
	src := string(pages.LiveScript)
	require.NotEmpty(t, src, "the package embeds live.js")
	for _, name := range sinks {
		assert.NotContains(t, src, name)
	}
	assert.NotRegexp(t, stringTimer, src, "no timer given a string")
	lines := strings.Count(strings.TrimRight(src, "\n"), "\n") + 1
	assert.LessOrEqual(t, lines, 150, "at most 150 lines")
	found := literals(src)
	assert.NotEmpty(t, found)
	for _, l := range found {
		assert.NotContains(t, l, " ", "a string literal that holds a space, as a word would: %q", l)
	}
	for _, region := range regionNames {
		assert.Contains(t, found, region, "the script names region %s", region)
	}

	assert.Equal(t, []string{"a b", "c", "d"}, literals("x = \"a b\"; // \"not\" one\ny = 'c' /* \"nor\" this */ + `d`"))
	assert.Regexp(t, stringTimer, `setTimeout( "go()", 5)`)
}

// The live bar's words are the server's (R155; the words of the board), so
// that live.js holds none: the bar, #board's first child, says live in its
// data-live and shows the note of a page without its script; its Pause
// button, unpressed, and its Sign in again link, back to the board as shown,
// ?feed=all kept, wait hidden; the polite summary's region is empty; the
// header's data-stale says reconnecting and when the board was last
// updated, a hidden item's reconnecting alone; and each region's first child
// is its update control, hidden, in the page and in its regions document
// alike. Every id live.js names is on the page.
func TestTheLiveBarCarriesTheServersWords(t *testing.T) {
	w := newWorld(t)
	c := w.viewer(t)
	r := richRead("b1", "")
	f := &feedFixture{id: "b1"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	r.Board.Feed = f.lines
	w.boards.set("b1", r)
	resp, page := w.get(t, "/items/b1?feed=all", c)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := []byte(page)
	root := node(t, body, "board")
	require.NotNil(t, root)
	bar := root.FirstChild
	for bar != nil && bar.Type != html.ElementNode {
		bar = bar.NextSibling
	}
	require.NotNil(t, bar, "the live bar")
	assert.Equal(t, "live", attr(bar, "class"))
	assert.Equal(t, "live", attr(bar, "data-live"))
	assert.Equal(t, "This page does not refresh itself: reload it to see more.", regionWords(t, body, "live-state"))
	pause := node(t, body, "live-pause")
	require.NotNil(t, pause)
	assert.Equal(t, "Pause updates", nodeText(pause))
	assert.Equal(t, "false", attr(pause, "aria-pressed"))
	assert.True(t, hasAttr(pause, "hidden"))
	signIn := node(t, body, "live-signin")
	require.NotNil(t, signIn)
	assert.Equal(t, "Sign in again", nodeText(signIn))
	assert.Equal(t, auth.SignInPath+"?return="+url.QueryEscape("/items/b1?feed=all"), attr(signIn, "href"))
	assert.True(t, hasAttr(signIn, "hidden"))
	announce := node(t, body, "announce")
	require.NotNil(t, announce)
	assert.Equal(t, "polite", attr(announce, "aria-live"))
	assert.Empty(t, nodeText(announce))
	for _, id := range []string{"board", "live-state", "live-pause", "live-signin", "announce", "feed-list"} {
		assert.Contains(t, literals(string(pages.LiveScript)), id, "live.js names %s", id)
		assert.NotNil(t, node(t, body, id), "the page holds %s", id)
	}

	stale := "reconnecting, last updated at " + shownAt(r.Board.Header.Updated)
	resp, doc := w.polled(t, "/items/b1/board?feed=all", c)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	for name, b := range map[string][]byte{"the page": body, "its regions document": []byte(doc)} {
		assert.Equal(t, stale, attr(node(t, b, "board-header"), "data-stale"), name)
		for _, id := range regionNames {
			region := node(t, b, id)
			require.NotNil(t, region, "%s: %s", name, id)
			first := region.FirstChild
			require.NotNil(t, first, "%s: %s", name, id)
			assert.Equal(t, "button", first.Data, "%s: %s's update control", name, id)
			assert.Equal(t, "update", attr(first, "class"), "%s: %s", name, id)
			assert.True(t, hasAttr(first, "hidden"), "%s: %s", name, id)
			assert.Equal(t, "New entries: update", nodeText(first), "%s: %s", name, id)
		}
	}

	restore := pages.SetVisible(func(*app.View) bool { return false })
	defer restore()
	for _, path := range []string{"/items/b1", "/items/b1/board"} {
		resp, b := w.get(t, path, c)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		assert.Equal(t, "reconnecting", attr(node(t, []byte(b), "board-header"), "data-stale"), "%s: a hidden item's", path)
	}
}

// A board's poll without a live session answers 401, with no body, no
// Location and no Set-Cookie, and never starts a sign-in (Review Focus 4;
// R161; review I1.4): with an expired session, a cookie sealed under a
// rotated key, a cookie that does not open, or none; and while the session
// key cannot be read, Available's 503, with none of them either; the fake
// IdP is never asked, so no flow cookie churns; and the script itself loads
// with no session.
func TestABoardPollWithoutALiveSessionAnswers401AndNeverStartsASignIn(t *testing.T) {
	w := newIDPWorld(t)
	w.boards.set("a1", boardRead("a1", "investigating", running))
	rotated, err := auth.NewKeys(newSessionKey(t), w.keys.Issuer(), clientID)
	require.NoError(t, err)
	now := time.Now().Truncate(time.Second)
	sealed, err := (&auth.Session{Issuer: w.keys.Issuer(), ClientID: clientID, Subject: "vic", Roles: []auth.Role{auth.Viewer},
		AuthTime: now, IssuedAt: now, Expires: now.Add(time.Hour)}).Seal(rotated)
	require.NoError(t, err)
	expired := w.session(t, "vic", 9*time.Hour, -time.Minute, auth.Viewer)

	for name, c := range map[string]*http.Cookie{"none": nil, "an expired one": expired} {
		resp, body := w.get(t, livePath(), c)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "the script loads with %s", name)
		assert.Equal(t, string(pages.LiveScript), body, name)
		assert.Empty(t, resp.Header.Values("Set-Cookie"), name)
	}
	for _, tc := range []struct {
		name, path string
		c          *http.Cookie
	}{
		{"an expired session", "/items/a1/board", expired},
		{"an expired session, the whole feed", "/items/a1/board?feed=all", expired},
		{"a cookie under a rotated key", "/items/a1/board", auth.SessionCookie(sealed, now.Add(time.Hour))},
		{"a cookie that does not open", "/items/a1/board", &http.Cookie{Name: auth.CookieName, Value: "garbage"}},
		{"no cookie", "/items/a1/board", nil},
	} {
		resp, body := w.polled(t, tc.path, tc.c)

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, tc.name)
		assert.Empty(t, body, "%s: no body", tc.name)
		assert.Empty(t, resp.Header.Get("Location"), tc.name)
		assert.Empty(t, resp.Header.Values("Set-Cookie"), tc.name)
	}
	resp, _ := w.polled(t, "/items/a1/board", w.viewer(t))
	require.Equal(t, http.StatusOK, resp.StatusCode, "a live session's poll is answered")

	w.secrets.set("")
	w.clock.advance(61 * time.Second)
	resp, body := w.polled(t, "/items/a1/board", w.viewer(t))
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the key cannot be read")
	assert.Contains(t, body, "sign-in is unavailable")
	assert.Empty(t, resp.Header.Get("Location"))
	assert.Empty(t, resp.Header.Values("Set-Cookie"))

	assert.Zero(t, w.idp.Discoveries(), "the IdP was never asked")
	assert.Empty(t, w.idp.Authorizations())
}

// A hidden item's board tells nothing of its run (Review Focus 5; design
// D13; re-review N5): with the seam saying no, its page and its polls hold
// no X-Board-Order, no data-order, no "last updated", no round, no time and
// no seq of its run; its ETag stays the same while the hidden run gains
// rounds, entries and records, and changes when the kill switch turns; and
// every call id its board cites answers 404.
func TestAHiddenItemsBoardTellsNothingOfItsRun(t *testing.T) {
	w := newClockWorld(t)
	c := w.viewer(t)
	restore := pages.SetVisible(func(*app.View) bool { return false })
	defer restore()
	f := &feedFixture{id: "h1"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	f.entry(agentA, readEvidence("the disk is full", "call-1", "disk full on db-1", "logs/search", "", ""))
	runAt := time.Date(2026, 11, 11, 13, 7, 0, 0, time.UTC)
	hidden := func(change func(r *app.BoardRead)) *app.BoardRead {
		r := richRead("h1", "")
		r.Board.Feed, r.Board.Header.Updated = f.lines, runAt
		r.Board.Cited = map[string][]string{"call-1": {"disk full on db-1"}}
		if change != nil {
			change(r)
		}
		return r
	}
	w.boards.set("h1", hidden(nil))
	w.calls.set(readCall("call-1", outputRun, `{}`, `"disk full on db-1"`))
	nothing := func(name string, resp *http.Response, body string) {
		t.Helper()
		assert.Empty(t, resp.Header.Values("X-Board-Order"), name)
		root := node(t, []byte(body), "board")
		require.NotNil(t, root, name)
		assert.False(t, hasAttr(root, "data-order"), name)
		assert.False(t, hasAttr(node(t, []byte(body), "summary"), "data-round"), name)
		words := text(t, []byte(body))
		for _, absent := range []string{"Last updated", "last updated", "Round", shownAt(runAt), "3.7", "Checkout fails", "the disk is full"} {
			assert.NotContains(t, words, absent, name)
		}
		for _, absent := range []string{"rec-", "ev-", "data-round", "3.7"} {
			assert.NotContains(t, body, absent, name)
		}
	}
	resp, page := w.get(t, "/items/h1", c)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	nothing("the page", resp, page)
	etag := attr(node(t, []byte(page), "board"), "data-etag")
	resp, body := w.polled(t, "/items/h1/board", c)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	nothing("a poll", resp, body)
	assert.Equal(t, etag, resp.Header.Get("ETag"))

	f.record(&item.Body{RoundOpened: &item.RoundOpened{Round: 4, OpenedAt: runAt, Deadline: runAt.Add(time.Minute)}})
	f.entry(agentB, readEvidence("the disk filled at 13:00", "call-2", "disk full since 13:00", "logs/search", "", ""))
	w.boards.set("h1", hidden(func(r *app.BoardRead) {
		r.Board.Version.Record, r.Board.Version.Board = app.Mark{ID: "rec-9", Seq: 9}, app.Mark{ID: "ev-12", Seq: 12}
		r.Board.Header.Round, r.Board.Header.Ended, r.Board.Header.Updated = 5, 4, runAt.Add(3*time.Minute)
		r.Board.View.Rounds = append(r.Board.View.Rounds, app.RoundView{Round: 4})
		r.Board.Header.Notices = 7
	}))
	resp, body = w.polled(t, "/items/h1/board", c, etag)
	assert.Equal(t, http.StatusNotModified, resp.StatusCode, "the run gained rounds, entries and records: the same ETag")
	assert.Empty(t, resp.Header.Values("X-Board-Order"))
	assert.Empty(t, body)

	w.boards.set("h1", hidden(func(r *app.BoardRead) {
		r.Facts.KillSwitch = app.KillSwitch{On: true, Seq: 2, OnSeq: 2, By: "sso:bob", Since: w.clock.Now()}
	}))
	resp, body = w.polled(t, "/items/h1/board", c, etag)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the kill switch turned: a new ETag")
	nothing("a poll, the switch on", resp, body)
	assert.NotEqual(t, etag, resp.Header.Get("ETag"))

	for _, call := range []string{"call-1", "call-2"} {
		resp, _ := w.get(t, outputPath("h1", call), c)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, call)
	}
	assert.Empty(t, w.calls.read(), "no call is read")
}

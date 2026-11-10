package pages_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/pages"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// hyp is a hypothesis of the board's assessment.
func hyp(id, service, statement string, score int, change func(*board.HypothesisView)) board.HypothesisView {
	h := board.HypothesisView{
		ID: board.EntryID(id), Key: key.Key{"service": service}, Statement: statement, Score: score,
		Supporters: []board.Author{{Kind: board.AuthorAgent, Name: "a"}, {Kind: board.AuthorAgent, Name: "b"}},
		Refuters:   []board.Author{{Kind: board.AuthorHuman, Name: "h"}},
		Sources:    []board.Source{"logs"},
	}
	if change != nil {
		change(&h)
	}
	return h
}

// The board's hidden copy (spec 10.2).
const hiddenCopy = "Agents are working on this alert for measurement. Their output stays hidden until it is rated. Handle it with your usual process."

// shownAt is t as the pages show it in UTC, to the minute.
func shownAt(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 MST") }

// The board page shows the state in spec 10.2's words, the banners, the
// stop entry, the conclusion and the hypotheses with their status words,
// and when it was last updated (design D9), from the board watch's read
// (R162): row 1's position and row 8's close in the words of the board.
func TestTheBoardPageRendersStatesBannersStopEntryAndStatusWords(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	get := func(id string) (int, string) {
		p := b.Get(w.public + "/items/" + id)
		return p.Resp.StatusCode, text(t, p.Body)
	}

	w.boards.set("q1", boardRead("q1", "queued", func(r *app.BoardRead) { r.Facts.QueuePosition = 2 }))
	code, words := get("q1")
	require.Equal(t, http.StatusOK, code, words)
	assert.Contains(t, words, "Queued")
	assert.Contains(t, words, "Queue position 2")

	w.boards.set("i0", boardRead("i0", "investigating", nil))
	_, words = get("i0")
	assert.Contains(t, words, "Agents are reading.")

	conclusion := hyp("e1", "checkout", "A bad release of checkout", 2, func(h *board.HypothesisView) { h.Converged = true })
	w.boards.set("c1", assessed("c1", "awaiting_close", board.Assessment{Conclusion: "e1", Hypotheses: []board.HypothesisView{conclusion}}, func(r *app.BoardRead) {
		r.Board.View.Stop = "converged"
		r.Board.View.StopEntry = &board.Stop{Reason: "converged", Rounds: 3, Tokens: 4200, WallClock: 90 * time.Second}
		r.Facts.ClosesAt = time.Now().Add(time.Hour)
	}))
	_, words = get("c1")
	assert.Contains(t, words, "Run stopped: converged")
	assert.Contains(t, words, "3 rounds")
	assert.Contains(t, words, "4200 tokens")
	assert.Contains(t, words, "Conclusion")
	assert.Contains(t, words, `service="checkout"`)
	assert.Contains(t, words, "model-written")
	assert.Contains(t, words, "A bad release of checkout")
	assert.Contains(t, words, "converged", "its status word")
	assert.Contains(t, words, "Closes by itself at")

	statuses := board.Assessment{Hypotheses: []board.HypothesisView{
		hyp("e1", "a", "first", 3, nil),
		hyp("e2", "b", "second", 2, nil),
		hyp("e3", "c", "third", 1, func(h *board.HypothesisView) { h.StandingRefutations = []board.EntryID{"r"} }),
		hyp("e4", "d", "fourth", 0, func(h *board.HypothesisView) { h.Rejected = true }),
	}}
	w.boards.set("s1", assessed("s1", "investigating", statuses, nil))
	p := b.Get(w.public + "/items/s1")
	rows := text(t, p.Body)
	for _, want := range []string{
		`leading · service="a" · score 3 · 2 supporting · 1 refuting`,
		`trailing · service="b" · score 2 · 2 supporting · 1 refuting`,
		`refuted · service="c" · score 1 · 2 supporting · 1 refuting`,
		`rejected · service="d" · score 0 · 2 supporting · 1 refuting`,
		"model-written: first",
	} {
		assert.Contains(t, rows, want)
	}
	assert.NotContains(t, rows, "Agents are reading.", "the board has hypotheses")

	// Stopped without converging: the leading hypothesis, not converged.
	w.boards.set("n1", assessed("n1", "awaiting_close", board.Assessment{Hypotheses: []board.HypothesisView{hyp("e1", "a", "first", 1, nil)}},
		func(r *app.BoardRead) { r.Board.View.Stop = "budget_exhausted" }))
	_, words = get("n1")
	assert.Contains(t, words, "not converged")

	// Not staffed, with the kill switch's banner; and the banner while it is on.
	w.boards.set("u1", boardRead("u1", "awaiting_close", func(r *app.BoardRead) { r.Board.View.NotStaffed = item.CauseKillSwitch }))
	_, words = get("u1")
	assert.Contains(t, words, "Not staffed: handle with your usual process")
	assert.Contains(t, words, "Stopped by the kill switch")
	w.boards.set("u2", boardRead("u2", "awaiting_close", func(r *app.BoardRead) { r.Board.View.NotStaffed = item.CauseDailyCap }))
	_, words = get("u2")
	assert.Contains(t, words, "Not staffed: handle with your usual process")
	assert.NotContains(t, words, "Stopped by the kill switch")
	w.boards.set("u2", boardRead("u2", "awaiting_close", func(r *app.BoardRead) {
		r.Board.View.NotStaffed = item.CauseDailyCap
		r.Facts.KillSwitch = app.KillSwitch{On: true, Seq: 3, OnSeq: 3}
	}))
	_, words = get("u2")
	assert.Contains(t, words, "Stopped by the kill switch", "the banner shows while the kill switch is on")

	// An auto-close time that could not be read.
	w.boards.set("x1", boardRead("x1", "awaiting_close", func(r *app.BoardRead) {
		r.Board.View.Stop = "converged"
		r.Facts.ClosesAtUnread = true
	}))
	_, words = get("x1")
	assert.Contains(t, words, "auto-close time not known")

	// Closed.
	at := time.Now().Add(-3 * time.Hour)
	w.boards.set("d1", boardRead("d1", "closed", func(r *app.BoardRead) {
		r.Board.View.Closed = &app.ClosedView{At: at, By: "sso:alice", ReopenableUntil: time.Now().Add(time.Hour)}
	}))
	_, words = get("d1")
	assert.Contains(t, words, "Closed at")
	assert.Contains(t, words, "UTC")
	assert.Contains(t, words, "3 h ago")
	assert.Contains(t, words, "sso:alice")

	// Last updated, in the zone and in words.
	assert.Regexp(t, `Last updated at \d{4}-\d\d-\d\d \d\d:\d\d UTC \(just now\)`, words)

	// A later build's item, an item that does not exist, and a read that fails.
	w.boards.set("l1", &app.BoardRead{Board: &app.BoardView{View: app.View{ID: "l1", HeldByLaterBuild: true}}})
	code, words = get("l1")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, words, "held by a later build")
	code, _ = get("nope")
	assert.Equal(t, http.StatusNotFound, code)
	w.boards.fail(assert.AnError, false)
	code, words = get("q1")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Contains(t, words, "could not be read")
	assert.NotContains(t, words, assert.AnError.Error())
}

// Hypothesis statements are model-written: they are marked so, and escaped;
// the page carries no script but live.js's, and no refresh of its own: the
// script polls (Task 8). (The feed's model-written text is
// TestEveryFeedLineHasItsWords'.)
func TestModelTextIsMarkedModelWrittenAndEscaped(t *testing.T) {
	w := newWorld(t)
	evil := `<script>alert("x")</script> & <b>bold</b>`
	w.boards.set("m1", assessed("m1", "investigating", board.Assessment{
		Conclusion: "e1", Hypotheses: []board.HypothesisView{hyp("e1", "a", evil, 2, nil)},
	}, nil))
	b := w.browser(t, "vic", auth.Viewer)
	p := b.Get(w.public + "/items/m1")
	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	body := string(p.Body)
	assert.NotContains(t, body, "<script>alert")
	assert.NotContains(t, body, "<b>bold</b>")
	assert.Contains(t, body, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; &lt;b&gt;bold&lt;/b&gt;")
	assert.Contains(t, text(t, p.Body), "model-written")
	assert.Equal(t, 2, strings.Count(body, "model-written"), "the conclusion's statement and the hypothesis's are each marked")
	assert.Equal(t, 1, strings.Count(body, liveTag()), "live.js's script element")
	assert.NotContains(t, strings.ToLower(strings.Replace(body, liveTag(), "", 1)), "<script", "and no other")
	assert.NotContains(t, strings.ToLower(body), `http-equiv="refresh"`)
}

// Every route that shows a board asks the visibility seam first, once, and
// a board the seam hides shows nothing of itself: the board page, and its
// regions' poll, show the fixed copy of a hidden run, no board, and a raw
// output, here its member's, answers 404, as any refusal of it, and reads no
// call (Plan 8a replaces the seam; design D9, D10, D13).
func TestEveryBoardRouteCallsTheVisibleSeam(t *testing.T) {
	w := newWorld(t)
	read := assessed("v1", "investigating", board.Assessment{
		Hypotheses: []board.HypothesisView{hyp("e1", "a", "secret statement", 1, nil)},
	}, func(r *app.BoardRead) {
		r.Board.Version.Run = outputRun
		r.Board.Opened.Principal = "sso:vic"
		r.Board.Cited = map[string][]string{"call-1": {"secret output"}}
	})
	w.boards.set("v1", read)
	w.calls.set(readCall("call-1", outputRun, `{}`, `"a secret output"`))
	routes := pages.BoardRoutes()
	require.Contains(t, routes, "/items/{id}")
	require.Contains(t, routes, "/items/{id}/board")
	require.Contains(t, routes, "/items/{id}/calls/{callID}")
	var seen []item.ID
	restore := pages.SetVisible(func(v *app.View) bool { seen = append(seen, v.ID); return false })
	defer restore()
	b := w.browser(t, "vic", auth.Viewer)
	for _, route := range routes {
		seen = nil
		p := b.Get(w.public + strings.NewReplacer("{id}", "v1", "{callID}", "call-1").Replace(route))
		assert.Equal(t, []item.ID{"v1"}, seen, "%s asks the seam once", route)
		words := text(t, p.Body)
		switch route {
		case "/items/{id}", "/items/{id}/board":
			require.Equal(t, http.StatusOK, p.Resp.StatusCode, route)
			assert.Contains(t, words, "Agents are working on this alert for measurement.", route)
			assert.NotContains(t, words, "secret statement", route)
		case "/items/{id}/calls/{callID}":
			require.Equal(t, http.StatusNotFound, p.Resp.StatusCode, route)
			assert.Contains(t, words, noOutput)
			assert.NotContains(t, words, "secret output", route)
			assert.Empty(t, w.calls.read(), "%s reads no call", route)
		default:
			assert.Failf(t, "a board route this test does not know", "%s", route)
		}
	}
}

// stateRow is a board state of spec 10.2's table (the words of the board,
// rows 1 to 9 and 14, and the investigating row with hypotheses): its item's
// read, whether it is read on a standby, and the words its header shows, and
// does not.
type stateRow struct {
	name    string
	read    *app.BoardRead
	standby bool
	header  []string
	not     []string
}

// stateRows are the board states' fixtures, each its own item.
func stateRows() []stateRow {
	now := time.Now()
	deadline := time.Date(2026, 11, 11, 14, 20, 0, 0, time.UTC)
	const shown = "2026-11-11 14:20 UTC"
	converged := board.Assessment{Conclusion: "e1", Hypotheses: []board.HypothesisView{
		hyp("e1", "checkout", "A bad release of checkout", 2, func(h *board.HypothesisView) { h.Converged = true }),
	}}
	leading := board.Assessment{Hypotheses: []board.HypothesisView{hyp("e1", "checkout", "A bad release of checkout", 1, nil)}}
	stopped := func(reason string, rounds int, tokens int64, wall time.Duration) func(r *app.BoardRead) {
		return func(r *app.BoardRead) {
			r.Board.View.Stop = reason
			r.Board.View.StopEntry = &board.Stop{Reason: reason, Rounds: rounds, Tokens: tokens, WallClock: wall}
		}
	}
	awaiting := func(change func(r *app.BoardRead)) *app.BoardRead {
		return assessed("r8", "awaiting_close", board.Assessment{}, func(r *app.BoardRead) {
			stopped("quiet", 4, 3000, 2*time.Minute)(r)
			change(r)
		})
	}
	closedAt, reopenable, autoAt, autoUntil := now.Add(-3*time.Hour), now.Add(21*time.Hour), now.Add(-50*time.Hour), now.Add(-2*time.Hour)
	closesAt := now.Add(2*time.Hour + time.Minute)
	return []stateRow{
		{name: "1 queued", read: boardRead("r1", "queued", func(r *app.BoardRead) { r.Facts.QueuePosition = 2 }),
			header: []string{"Queued", "Queue position 2", "Promoting it starts its run now, if the limit allows; the promote form shows what it does."},
			not:    []string{"not known"}},
		{name: "1 queued, on a standby", standby: true, read: boardRead("r1", "queued", func(r *app.BoardRead) { r.Facts.Held = false }),
			header: []string{"Queued", "Queue position: not known on this instance", "Promoting it starts its run now"}},
		{name: "2 investigating, no hypothesis", read: boardRead("r2", "investigating", func(r *app.BoardRead) {
			running(r)
			r.Board.Header.StopBy = deadline
		}), header: []string{"Investigating", "Agents are reading. A message comes when the run stops, by " + shown +
			" at the latest. An outage, a takeover or a resume moves that time later."}},
		{name: "2 investigating, before its run's creation", read: boardRead("r2", "investigating", nil),
			header: []string{"Investigating", "Agents are reading. A message comes when the run stops."}, not: []string{"at the latest"}},
		{name: "investigating, with hypotheses", read: assessed("r2", "investigating", leading, func(r *app.BoardRead) { r.Board.Header.StopBy = deadline }),
			header: []string{"Investigating", "The run stops by " + shown + " at the latest. An outage, a takeover or a resume moves that time later."},
			not:    []string{"Agents are reading"}},
		{name: "3 converged, confirming", read: assessed("r3", "investigating", converged, func(r *app.BoardRead) {
			r.Board.Hypotheses[0].Status = board.StatusConfirming
			r.Board.Header.Conclusion = &r.Board.Hypotheses[0]
			r.Board.Header.ConfirmingUntil = deadline
		}), header: []string{"Conclusion", `service="checkout" · confirming until ` + shown}},
		{name: "4 converged", read: assessed("r4", "awaiting_close", converged, stopped("converged", 3, 4200, 90*time.Second)),
			header: []string{"Awaiting close", "Run stopped: converged.", `service="checkout" · converged`, "3 rounds, 4200 tokens, 1m30s of wall clock"}},
		{name: "5 stopped without converging", read: assessed("r5", "awaiting_close", board.Assessment{Conclusion: "e1", Hypotheses: leading.Hypotheses},
			stopped("budget_exhausted", 5, 9000, 25*time.Minute)),
			header: []string{"Run stopped: the budget was used up.", `service="checkout" · not converged`}},
		{name: "5 stopped without converging, no conclusion", read: assessed("r5", "awaiting_close", leading, stopped("budget_exhausted", 5, 9000, 25*time.Minute)),
			header: []string{"Run stopped: the budget was used up.", `service="checkout" · not converged`}, not: []string{"Conclusion"}},
		{name: "6 trigger resolved", read: assessed("r6", "awaiting_close", board.Assessment{}, stopped("trigger_resolved", 2, 900, 45*time.Second)),
			header: []string{"Alert resolved.", "Run stopped: the alert resolved.", "2 rounds, 900 tokens, 45s of wall clock"}},
		{name: "7 not staffed", read: boardRead("r7", "awaiting_close", func(r *app.BoardRead) {
			r.Board.View.Stop, r.Board.View.NotStaffed = "not_staffed", item.CauseRouting
		}), header: []string{"Awaiting close", "Not staffed: handle with your usual process.", "Cause: its routing failed its check"},
			not: []string{"Stopped by the kill switch", "Run stopped"}},
		{name: "8 awaiting close, closing by itself", read: awaiting(func(r *app.BoardRead) { r.Facts.ClosesAt = closesAt }),
			header: []string{"Awaiting close", "Run stopped: a round passed with nothing new.", "4 rounds, 3000 tokens, 2m0s of wall clock",
				"Closes by itself at " + shownAt(closesAt) + " (2 h from now)"}},
		{name: "8 awaiting close, its alert firing", read: awaiting(func(r *app.BoardRead) { r.Board.Header.StaysOpen = true }),
			header: []string{"Stays open while its alert fires"}, not: []string{"Closes by itself"}},
		{name: "8 awaiting close, the kill switch on", read: awaiting(func(r *app.BoardRead) { r.Facts.AutoClosePaused = true }),
			header: []string{"auto-close paused while the kill switch is on"}},
		{name: "8 awaiting close, the kill switch unread", read: awaiting(func(r *app.BoardRead) {
			r.Facts.KillSwitchRead, r.Facts.AutoCloseUnread = false, true
		}), header: []string{"auto-close not known: the kill switch could not be read"}},
		{name: "8 awaiting close, its delay unread", read: awaiting(func(r *app.BoardRead) { r.Facts.ClosesAtUnread = true }),
			header: []string{"auto-close time not known"}},
		{name: "9 closed by a person", read: boardRead("r9", "closed", func(r *app.BoardRead) {
			r.Board.View.Closed = &app.ClosedView{At: closedAt, By: "sso:rita", ReopenableUntil: reopenable}
		}), header: []string{"Closed", "Closed at " + shownAt(closedAt) + " (3 h ago) by sso:rita; may be reopened until " + shownAt(reopenable) + "."}},
		{name: "9 closed by itself, past its reopen window", read: boardRead("r9", "closed", func(r *app.BoardRead) {
			r.Board.View.Closed = &app.ClosedView{At: autoAt, Auto: true, ReopenableUntil: autoUntil}
		}), header: []string{"Closed at " + shownAt(autoAt) + " (2 days ago) by itself; may no longer be reopened."}},
		{name: "14 the kill switch on", read: boardRead("r14", "investigating", func(r *app.BoardRead) {
			running(r)
			r.Facts.KillSwitch = app.KillSwitch{On: true, Seq: 4, OnSeq: 4, By: "sso:bob", Since: now}
		}), header: []string{"Stopped by the kill switch"}},
		{name: "14 left not staffed by it, the switch off", read: boardRead("r14", "awaiting_close", func(r *app.BoardRead) {
			r.Board.View.Stop, r.Board.View.NotStaffed = "not_staffed", item.CauseKillSwitch
			r.Facts.KillSwitch = app.KillSwitch{Seq: 5, OnSeq: 4, Since: now}
		}), header: []string{"Stopped by the kill switch", "Not staffed: handle with your usual process.", "Cause: the kill switch was on"}},
	}
}

// Every row of the board states renders its words (the words of the board;
// spec 10.2, rows 1 to 9 and 14, and the investigating row with hypotheses).
func TestEveryBoardStateRowRendersItsText(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	for _, row := range stateRows() {
		t.Run(row.name, func(t *testing.T) {
			id := string(row.read.Board.View.ID)
			w.boards.set(row.read.Board.View.ID, row.read)
			w.leader.set(!row.standby, false)

			p := b.Get(w.public + "/items/" + id)

			require.Equal(t, http.StatusOK, p.Resp.StatusCode, text(t, p.Body))
			header := regionWords(t, p.Body, "board-header")
			for _, want := range row.header {
				assert.Contains(t, header, want)
			}
			for _, absent := range row.not {
				assert.NotContains(t, header, absent)
			}
		})
	}
}

// Every region of the board exists on its page in every state, and in the
// hidden copy, each once, as an element with its id, empty when it has
// nothing to show (R170; plan review I2): a lone item's strip and a leader's
// standby notice included; and so on a later build's item's page.
func TestEveryRegionExistsInEveryBoardState(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	regions := func(name string, id item.ID) {
		t.Helper()
		p := b.Get(w.public + "/items/" + string(id))
		require.Equal(t, http.StatusOK, p.Resp.StatusCode, name)
		doc, err := html.Parse(bytes.NewReader(p.Body))
		require.NoError(t, err)
		count := map[string]int{}
		for n := range doc.Descendants() {
			if n.Type == html.ElementNode && hasAttr(n, "id") {
				count[attr(n, "id")]++
			}
		}
		for _, region := range regionNames {
			assert.Equal(t, 1, count[region], "%s: region %s", name, region)
		}
	}
	for _, row := range stateRows() {
		w.boards.set(row.read.Board.View.ID, row.read)
		w.leader.set(!row.standby, false)
		regions(row.name, row.read.Board.View.ID)
	}
	w.leader.set(true, false)
	assert.Empty(t, regionWords(t, b.Get(w.public+"/items/r1").Body, "standby"), "a leader's standby region is empty")
	assert.Empty(t, regionWords(t, b.Get(w.public+"/items/r1").Body, "strip"), "a lone item's strip is empty")

	w.boards.set("h1", richRead("h1", ""))
	restore := pages.SetVisible(func(*app.View) bool { return false })
	regions("the hidden copy", "h1")
	restore()
	w.boards.set("l1", &app.BoardRead{Board: &app.BoardView{View: app.View{ID: "l1", HeldByLaterBuild: true}}})
	regions("a later build's item", "l1")
}

// markersIn asserts that no text body shows holds a raw U+202E, and that
// each ⟨U+202E⟩ it shows sits in a text node of its own <bdi>; it returns how
// many it shows.
func markersIn(t *testing.T, body []byte) int {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	n := 0
	for d := range doc.Descendants() {
		if d.Type != html.TextNode {
			continue
		}
		assert.NotContains(t, d.Data, "\u202e", "a raw bidi control in %q", d.Data)
		if c := strings.Count(d.Data, "⟨U+202E⟩"); c > 0 {
			n += c
			require.NotNil(t, d.Parent)
			assert.Equal(t, "bdi", d.Parent.Data, "a marked rune sits inside a <bdi>: %q", d.Data)
		}
	}
	return n
}

// 9a-1's closed lines, and every sentence the board's header, summary, strip
// and hypotheses build in Go, are lines of parts (R171): each stored value
// holding U+202E renders ⟨U+202E⟩ inside its <bdi>, and no text or attribute
// of the page holds a raw U+202E; a hypothesis's key, shown in its canonical
// form, quotes its values (key.Key.String), the rune escaped as \u202e; and
// the raw output's header sentence is a line of parts too.
func TestNoGoBuiltLineHoldsAStoredValue(t *testing.T) {
	const rlo = "\u202e"
	w := newWorld(t)
	b := w.browser(t, "carol", auth.Responder)

	w.boards.set("d1", boardRead("d1", "closed", func(r *app.BoardRead) {
		r.Board.View.Closed = &app.ClosedView{At: time.Now().Add(-time.Hour), By: "sso:ri" + rlo + "ta", ReopenableUntil: time.Now().Add(time.Hour)}
	}))
	p := b.Get(w.public + "/items/d1")
	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	assert.Equal(t, 1, markersIn(t, p.Body), "the close's By")
	assert.Contains(t, regionWords(t, p.Body, "board-header"), "by sso:ri⟨U+202E⟩ta; may be reopened until")

	w.boards.set("x1", richRead("x1", rlo))
	p = b.Get(w.public + "/items/x1")
	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	assert.NotContains(t, string(p.Body), rlo, "no attribute holds it either")
	// The header's type, title, conclusion's statement, first instance's id,
	// two sources and flap's item; the strip's first item; the cards'
	// statements, the ranked one's twice with the conclusion's; the person's
	// name and words; the agent's two quotes and sources; the target's
	// argument and value; the summary's value; the fact's name and value; the
	// proposer and its source: 23.
	assert.Equal(t, 23, markersIn(t, p.Body), "every stored value of the board's lines but the keys")
	words := text(t, p.Body)
	for _, k := range []string{`service="checkout\u202e"`, `service="payments\u202e"`} {
		assert.Contains(t, words, k, "a key quotes its values")
	}

	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {"x"}}))
	call := readCall("call-1", outputRun, `{}`, `"x"`)
	call.Server, call.Tool, call.Agent = "lo"+rlo+"gs", "sea"+rlo+"rch", "agent"+rlo+"a"
	w.calls.set(call)
	p = b.Get(w.public + outputPath("o1", "call-1"))
	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	assert.Equal(t, "Raw output of lo⟨U+202E⟩gs.sea⟨U+202E⟩rch by agent⟨U+202E⟩a.", firstParagraph(t, p.Body))
	assert.Equal(t, 3, markersIn(t, p.Body), "the call's server, tool and agent")
}

// The header shows the rules' facts (spec 10.2; design D3; R168): the item
// and its title, quoted data; the budget used; the suggestions counted; the
// alert's instances, each id quoted data; when it was last updated, in the
// zone and in words; the item a flap's opening followed, linked; and the
// notices of this round. The header region's stale words name the same
// time (R155). A board with none of them says so of the suggestions alone.
func TestTheHeaderShowsTheRulesFacts(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	now := time.Now()
	fired, resolved, updated := now.Add(-40*time.Minute), now.Add(-5*time.Minute), now.Add(-10*time.Minute-5*time.Second)
	w.boards.set("i1", boardRead("i1", "investigating", func(r *app.BoardRead) {
		running(r)
		r.Board.View.Budget.Used = item.Budget{Rounds: 2, WallClock: 4 * time.Minute, Tokens: 1200}
		h := &r.Board.Header
		h.Suggestions, h.FlapOf, h.Notices, h.Updated = 3, "p1", 2, updated
		h.Alert = []item.TriggerInstance{
			{Source: "alerting", ID: "inst-1", Status: item.Firing, At: fired},
			{Source: "alerting", ID: "inst-2", Status: item.Resolved, At: resolved},
		}
		h.Firing = true
	}))

	p := b.Get(w.public + "/items/i1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	header := node(t, p.Body, "board-header")
	require.NotNil(t, header)
	words := shownWords(header)
	for _, want := range []string{
		"Item i1 · type incident. Title: Checkout fails",
		"Used 2 of 5 rounds, 4m0s of 30m0s wall clock, 1200 of 50000 tokens",
		"3 suggestions",
		"Alert: 2 instances, 1 firing",
		"inst-1 from alerting: firing since " + shownAt(fired),
		"inst-2 from alerting: resolved at " + shownAt(resolved),
		"This item opened when the alert of item p1 fired again after that item closed.",
		"2 notices this round",
		"Last updated at " + shownAt(updated) + " (10 min ago)",
	} {
		assert.Contains(t, words, want)
	}
	assert.Equal(t, []string{"Checkout fails", "inst-1", "inst-2"}, quotedData(t, header), "the title and the instances' ids are quoted data")
	assert.Contains(t, links(t, p.Body), "/items/p1", "the flap's origin links to its board")
	assert.Equal(t, "reconnecting, last updated at "+shownAt(updated), attr(header, "data-stale"))

	w.boards.set("i2", boardRead("i2", "investigating", running))
	words = regionWords(t, b.Get(w.public+"/items/i2").Body, "board-header")
	assert.Contains(t, words, "No suggestion yet")
	for _, absent := range []string{"Alert:", "fired again", "notices this round"} {
		assert.NotContains(t, words, absent)
	}
}

// The times a person waits on, "by T at the latest" and "confirming until
// T", are rounded up to the minute, so that the minute shown is never before
// T (R146): one second, or one nanosecond, past 14:20 shows 14:21; 14:20
// itself shows 14:20.
func TestTheDeadlinesAreShownRoundedUpToTheMinute(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	minute := time.Date(2026, 11, 11, 14, 20, 0, 0, time.UTC)
	converged := board.Assessment{Conclusion: "e1", Hypotheses: []board.HypothesisView{
		hyp("e1", "checkout", "A bad release of checkout", 2, func(h *board.HypothesisView) { h.Converged = true }),
	}}
	for _, tc := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"a second past the minute", minute.Add(time.Second), "2026-11-11 14:21 UTC"},
		{"a nanosecond past it", minute.Add(time.Nanosecond), "2026-11-11 14:21 UTC"},
		{"on the minute", minute, "2026-11-11 14:20 UTC"},
	} {
		w.boards.set("s1", boardRead("s1", "investigating", func(r *app.BoardRead) {
			running(r)
			r.Board.Header.StopBy = tc.at
		}))
		assert.Contains(t, regionWords(t, b.Get(w.public+"/items/s1").Body, "board-header"), "by "+tc.want+" at the latest", tc.name)

		w.boards.set("c1", assessed("c1", "investigating", converged, func(r *app.BoardRead) {
			r.Board.Hypotheses[0].Status = board.StatusConfirming
			r.Board.Header.Conclusion = &r.Board.Hypotheses[0]
			r.Board.Header.ConfirmingUntil = tc.at
		}))
		assert.Contains(t, regionWords(t, b.Get(w.public+"/items/c1").Body, "board-header"), "confirming until "+tc.want, tc.name)
	}
}

// The strip shows the incident's other items (design D7; R148, R172): each
// by its id, linked to its board, and its state in words; a stopped run's
// conclusion, its key and whether it converged; a disagreement; a hidden
// item by its state alone, Queued, Open or Closed; an unreadable item as
// such; and how many more there are. A listing that failed says so, and a
// lone item's strip is empty.
func TestTheStripRendersItsItems(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	w.boards.set("a1", boardRead("a1", "investigating", func(r *app.BoardRead) {
		r.Strip = app.Strip{Items: []app.StripItem{
			{ID: "s1", Type: "incident", State: item.StatusInvestigating},
			{ID: "s2", Type: "incident", State: item.StatusAwaitingClose, Conclusion: key.Key{"service": "checkout"}, Converged: true, Disagrees: true},
			{ID: "s3", Type: "incident", State: item.StatusAwaitingClose, Conclusion: key.Key{"service": "payments"}},
			{ID: "s4", State: item.StatusQueued, Hidden: true},
			{ID: "s5", State: item.StatusInvestigating, Hidden: true},
			{ID: "s6", State: item.StatusClosed, Hidden: true},
			{ID: "s7", Unreadable: true},
		}, More: 4}
	}))

	p := b.Get(w.public + "/items/a1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	strip := node(t, p.Body, "strip")
	require.NotNil(t, strip)
	var items, hrefs []string
	for n := range strip.Descendants() {
		switch {
		case n.Type == html.ElementNode && n.Data == "li":
			items = append(items, shownWords(n))
		case n.Type == html.ElementNode && n.Data == "a":
			hrefs = append(hrefs, attr(n, "href"))
		}
	}
	assert.Equal(t, []string{
		"s1 · Investigating",
		`s2 · Awaiting close · service="checkout" · converged · disagrees`,
		`s3 · Awaiting close · service="payments" · not converged`,
		"s4 · Queued",
		"s5 · Open",
		"s6 · Closed",
		"s7 · this item could not be read",
	}, items)
	assert.Equal(t, []string{"/items/s1", "/items/s2", "/items/s3", "/items/s4", "/items/s5", "/items/s6", "/items/s7"}, hrefs)
	words := shownWords(strip)
	assert.True(t, strings.HasPrefix(words, "The incident's other items "), words)
	assert.True(t, strings.HasSuffix(words, " and 4 more"), words)

	w.boards.set("a2", boardRead("a2", "investigating", func(r *app.BoardRead) { r.Strip = app.Strip{Unread: true} }))
	assert.Equal(t, "The incident's other items could not be read just now.", regionWords(t, b.Get(w.public+"/items/a2").Body, "strip"))
	w.boards.set("a3", boardRead("a3", "investigating", nil))
	assert.Empty(t, regionWords(t, b.Get(w.public+"/items/a3").Body, "strip"))
}

// Each hypothesis shows its quotes and its suggestions (design D4.4, D6;
// the words of the board): on each side the quotes the projection picked,
// each of them, and how many more; an agent's as its quote, quoted data, with
// its source and, for the item's members alone, its raw output's link; a
// person's as their words, with their name. Its suggestions are grouped by
// target, each target's values quoted data, and each shows its summary, its
// value parts quoted data; its facts, values quoted data, how many more, or
// why one is unavailable; its hypothesis's status; its proposer and the
// proposer's own evidence by source; the superseded mark; and either service
// mark, a suggestion inside the item's service saying nothing of it.
func TestEachHypothesisShowsItsQuotesAndSuggestions(t *testing.T) {
	w := newWorld(t)
	w.boards.set("h1", richRead("h1", ""))

	member := w.browser(t, "carol", auth.Viewer).Get(w.public + "/items/h1")

	require.Equal(t, http.StatusOK, member.Resp.StatusCode)
	hyps := node(t, member.Body, "hypotheses")
	require.NotNil(t, hyps)
	words := shownWords(hyps)
	for _, want := range []string{
		`leading · service="checkout" · score 2 · 2 supporting · 1 refuting · 1 source`,
		"model-written: A bad release of checkout",
		"Supporting: sso:bob wrote, a person's words: I saw the release go out disk full on db-1, from logs/search (raw output) and 3 more",
		"Refuting: no deploy since Monday, from deploys/list (raw output)",
		"Target: service checkout, region eu-1",
		"Roll back checkout to v41",
		`Facts: owner: unavailable: no time was left to read it; version: "v42", "v41" and 2 more`,
		`service="checkout" is leading`,
		"Proposed by agent-a, which supports it with its own evidence from logs/search (2), metrics/query (1)",
		"superseded: its hypothesis was rejected or lost convergence",
		"outside this item's service",
		"Restart checkout",
		"Proposed by agent-b",
		"service not compared",
	} {
		assert.Contains(t, words, want)
	}
	assert.NotContains(t, words, "agent-b, which supports", "a proposer with no evidence of its own")
	assert.Equal(t, []string{"disk full on db-1", "no deploy since Monday", "checkout", "eu-1", "checkout", `"v42"`, `"v41"`, "checkout"}, quotedData(t, hyps))
	var sides []int
	for n := range hyps.Descendants() {
		if n.Type == html.ElementNode && n.Data == "ul" && attr(n, "class") == "quotes" {
			lis := 0
			for li := range n.ChildNodes() {
				if li.Type == html.ElementNode && li.Data == "li" {
					lis++
				}
			}
			sides = append(sides, lis)
		}
	}
	assert.Equal(t, []int{2, 1}, sides, "the leading hypothesis's two supporting quotes and its one refuting, each shown")
	assert.Subset(t, links(t, member.Body), []string{"/items/h1/calls/call-1", "/items/h1/calls/call-3"}, "a member's links to the raw outputs")

	viewer := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/h1")
	require.Equal(t, http.StatusOK, viewer.Resp.StatusCode)
	assert.NotContains(t, regionWords(t, viewer.Body, "hypotheses"), "raw output", "no link to a raw output for a viewer who is no member")
	for _, l := range links(t, viewer.Body) {
		assert.NotContains(t, l, "/calls/", "no link to a raw output for a viewer who is no member")
	}
}

// The ruled-out hypotheses, rejected or against which a refutation stands,
// follow the ranked ones, under "Ruled out", each in the order of its rank.
func TestRuledOutHypothesesFollowTheRanked(t *testing.T) {
	w := newWorld(t)
	w.boards.set("r1", assessed("r1", "investigating", board.Assessment{Hypotheses: []board.HypothesisView{
		hyp("e1", "a", "first", 3, nil),
		hyp("e2", "b", "second", 2, func(h *board.HypothesisView) { h.Rejected = true }),
		hyp("e3", "c", "third", 1, nil),
		hyp("e4", "d", "fourth", 0, func(h *board.HypothesisView) { h.StandingRefutations = []board.EntryID{"r"} }),
	}}, nil))

	words := regionWords(t, w.browser(t, "vic", auth.Viewer).Get(w.public+"/items/r1").Body, "hypotheses")

	at := func(s string) int {
		i := strings.Index(words, s)
		require.GreaterOrEqual(t, i, 0, "%q in %q", s, words)
		return i
	}
	order := []int{at(`leading · service="a"`), at(`trailing · service="c"`), at("Ruled out"), at(`rejected · service="b"`), at(`refuted · service="d"`)}
	for i := 1; i < len(order); i++ {
		assert.Less(t, order[i-1], order[i], words)
	}
	assert.True(t, strings.HasPrefix(words, "Hypotheses "), words)
}

// The round summary names the latest ended round and the current
// assessment's rank-0 hypothesis with its status word now, its data-round
// that round and the run's stops so far (R177; Task 5's review, decision
// 10): the assessment's first, though it be ruled out; a wrap-up round open
// reads the hypothesis it confirms, confirming, in the summary and on its
// card, as the projection gives its status (Task 2's carry); a round ended
// with no hypothesis says so; a run that stopped says why. Before a round
// ended it is empty, with no round.
func TestTheRoundSummaryNamesTheLatestEndedRound(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	ended := func(round, last int) func(r *app.BoardRead) {
		return func(r *app.BoardRead) { r.Board.Header.Round, r.Board.Header.Ended = round, last }
	}
	two := board.Assessment{Hypotheses: []board.HypothesisView{hyp("e1", "a", "first", 2, nil), hyp("e2", "b", "second", 1, nil)}}
	confirming := assessed("s2", "investigating", board.Assessment{Conclusion: "e1", Hypotheses: []board.HypothesisView{
		hyp("e1", "checkout", "A bad release of checkout", 2, func(h *board.HypothesisView) { h.Converged = true }),
	}}, func(r *app.BoardRead) {
		ended(3, 2)(r)
		r.Board.Hypotheses[0].Status = board.StatusConfirming
		r.Board.Header.Conclusion = &r.Board.Hypotheses[0]
	})
	for _, tc := range []struct {
		name  string
		read  *app.BoardRead
		round string
		words string
	}{
		{"a round ended, its rank-0 hypothesis leading", assessed("s1", "investigating", two, ended(3, 2)), "2.0", `Round 2 ended: leading, service="a"`},
		{"its rank-0 hypothesis ruled out", assessed("s6", "investigating", board.Assessment{Hypotheses: []board.HypothesisView{
			hyp("e1", "a", "first", 3, func(h *board.HypothesisView) { h.StandingRefutations = []board.EntryID{"r"} }), hyp("e2", "b", "second", 2, nil),
		}}, ended(3, 2)), "2.0", `Round 2 ended: refuted, service="a"`},
		{"a wrap-up round open", confirming, "2.0", `Round 2 ended: confirming, service="checkout"`},
		{"a round ended with no hypothesis", boardRead("s3", "investigating", func(r *app.BoardRead) {
			running(r)
			ended(2, 1)(r)
		}), "1.0", "Round 1 ended: no hypothesis yet"},
		{"the run stopped", assessed("s4", "awaiting_close", two, func(r *app.BoardRead) {
			ended(4, 4)(r)
			r.Board.View.Stop, r.Board.View.Stops = "converged", 1
		}), "4.1", "The run stopped: converged"},
	} {
		w.boards.set(tc.read.Board.View.ID, tc.read)
		p := b.Get(w.public + "/items/" + string(tc.read.Board.View.ID))
		require.Equal(t, http.StatusOK, p.Resp.StatusCode, tc.name)
		summary := node(t, p.Body, "summary")
		require.NotNil(t, summary, tc.name)
		assert.Equal(t, tc.words, shownWords(summary), tc.name)
		assert.Equal(t, tc.round, attr(summary, "data-round"), tc.name)
	}
	assert.Contains(t, regionWords(t, b.Get(w.public+"/items/s2").Body, "hypotheses"), `confirming · service="checkout"`,
		"the card reads its wrap-up status, as the projection gives it")

	w.boards.set("s5", assessed("s5", "investigating", two, ended(1, 0)))
	summary := node(t, b.Get(w.public+"/items/s5").Body, "summary")
	require.NotNil(t, summary)
	assert.Empty(t, shownWords(summary), "no round ended yet")
	assert.False(t, hasAttr(summary, "data-round"))
}

// A code that a words table holds no words for shows as itself, a name, in
// its <bdi>, its hidden runes marked, never among the platform's words (Task
// 5's review, ruling U): a stop, in the header's stop line and the summary;
// a fact's unavailable code; a state, in the header and the strip; and a
// cause of an item not staffed. A known code shows its words, as text.
func TestAnUnknownCodeShowsAsItselfInert(t *testing.T) {
	const rlo = "\u202e"
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	stopped := richRead("u1", "")
	stopped.Board.View.Stop = "melt" + rlo + "down"
	facts := stopped.Board.Hypotheses[0].Suggestions[0].Suggestions[0].Event.Body.Proposal.Facts
	facts["owner"] = board.Fact{Unavailable: "gone" + rlo}
	w.boards.set("u1", stopped)
	w.boards.set("u2", boardRead("u2", "melt"+rlo+"ing", func(r *app.BoardRead) {
		r.Strip = app.Strip{Items: []app.StripItem{{ID: "s1", Type: "incident", State: item.Status("drift" + rlo + "ing")}}}
	}))
	w.boards.set("u3", boardRead("u3", "awaiting_close", func(r *app.BoardRead) { r.Board.View.NotStaffed = item.Cause("flood" + rlo) }))

	p := b.Get(w.public + "/items/u1")
	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	assert.Equal(t, 3, markersIn(t, p.Body), "the stop, in the header and the summary, and the fact's code")
	assert.Contains(t, regionWords(t, p.Body, "board-header"), "Run stopped: melt⟨U+202E⟩down.")
	assert.Equal(t, "The run stopped: melt⟨U+202E⟩down", regionWords(t, p.Body, "summary"))
	assert.Contains(t, regionWords(t, p.Body, "hypotheses"), "owner: unavailable: gone⟨U+202E⟩;")

	p = b.Get(w.public + "/items/u2")
	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	assert.Equal(t, 2, markersIn(t, p.Body), "the state, in the header and the strip")
	assert.Contains(t, regionWords(t, p.Body, "board-header"), "melt⟨U+202E⟩ing")
	assert.Equal(t, "The incident's other items s1 · drift⟨U+202E⟩ing", regionWords(t, p.Body, "strip"))

	p = b.Get(w.public + "/items/u3")
	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	assert.Equal(t, 1, markersIn(t, p.Body), "the cause")
	words := regionWords(t, p.Body, "board-header")
	assert.Contains(t, words, "Cause: flood⟨U+202E⟩")
	assert.Contains(t, words, "Awaiting close", "a known state's words")

	w.boards.set("u4", boardRead("u4", "investigating", func(r *app.BoardRead) {
		running(r)
		r.Board.View.Stop = "meltdown"
	}))
	p = b.Get(w.public + "/items/u4")
	assert.Contains(t, string(p.Body), "Run stopped: <bdi>meltdown</bdi>.", "a code with no hidden rune, a name too")
	assert.Contains(t, string(p.Body), "The run stopped: <bdi>meltdown</bdi>")
}

// A standby's board pages keep the lease holder's read for a second from
// the read's end, however long it took (Task 5's review M1): a read that
// takes 0.9 s is kept 0.5 s after it ends, which a stamp of its start would
// have dropped, and is read again past its second.
func TestAStandbysHolderReadIsKeptASecondFromItsEnd(t *testing.T) {
	w := newClockWorld(t)
	w.leader.set(false, false)
	w.holder.name, w.holder.expires = "instance-b", w.clock.Now().Add(time.Hour)
	w.holder.during = func() { w.clock.advance(900 * time.Millisecond) }
	w.boards.set("a1", boardRead("a1", "investigating", nil))
	b := w.browser(t, "vic", auth.Viewer)

	assert.Contains(t, regionWords(t, b.Get(w.public+"/items/a1").Body, "standby"), "instance-b")
	w.clock.advance(500 * time.Millisecond)
	assert.Contains(t, regionWords(t, b.Get(w.public+"/items/a1").Body, "standby"), "instance-b")
	assert.Equal(t, 1, w.holder.read(), "kept from its end")
	w.clock.advance(600 * time.Millisecond)
	b.Get(w.public + "/items/a1")
	assert.Equal(t, 2, w.holder.read(), "read again a second past its end")
}

// Board pages on a standby that arrive while the lease holder's read runs
// wait for it, each within its own context (Task 5's review M1): five pages
// at once make one read, and each names the leader. The read is held until
// the four other pages are seen waiting, and runs under a bound no stall
// reaches, so that nothing depends on real time (re-review N2).
func TestConcurrentBoardPagesOnAStandbyMakeOneHolderRead(t *testing.T) {
	waiting := make(chan struct{}, 8)
	t.Cleanup(pages.SetHolderWaiting(func() { waiting <- struct{}{} }))
	t.Cleanup(pages.SetHolderTimeout(time.Hour))
	w := newWorld(t)
	w.leader.set(false, false)
	w.holder.name = "instance-b"
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release) // before the server's close, which waits for the pages the gate holds
	w.holder.gate = gate
	w.boards.set("a1", boardRead("a1", "investigating", nil))
	cookie := w.session(t, "vic", 0, time.Hour, auth.Viewer)
	client := w.client()
	get := func() (string, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, w.public+"/items/a1", http.NoBody)
		if err != nil {
			return "", err
		}
		req.AddCookie(cookie)
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		return string(body), err
	}
	bodies, errs := make([]string, 5), make([]error, 5)
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Go(func() { bodies[i], errs[i] = get() })
	}
	for range 4 {
		select {
		case <-waiting:
		case <-time.After(30 * time.Second):
			release()
			wg.Wait()
			require.Fail(t, "the other pages never waited for the read that runs")
		}
	}
	release()
	wg.Wait()

	assert.Equal(t, 1, w.holder.read(), "one read for every page that arrived while it ran")
	for i := range bodies {
		require.NoError(t, errs[i])
		assert.Contains(t, regionWords(t, []byte(bodies[i]), "standby"), "instance-b", "page %d", i)
	}
}

// stopEntry is the feed's line of a run's stop entry, of seq.
func stopEntry(seq int64) app.FeedLine {
	return app.FeedLine{Event: &board.Event{Seq: seq, ID: board.EntryID(fmt.Sprintf("ev-%d", seq)), Run: "run-1", Author: board.Coordinator,
		Body: board.Body{Stop: &board.Stop{Reason: "quiet"}}}}
}

// resumeEntry is the feed's line of a run's resume entry, of seq.
func resumeEntry(seq int64) app.FeedLine {
	return app.FeedLine{Event: &board.Event{Seq: seq, ID: board.EntryID(fmt.Sprintf("ev-%d", seq)), Run: "run-1", Author: board.Coordinator,
		Body: board.Body{Resume: &board.Resume{Cause: "extension", Rounds: 1}}}}
}

// The summary's data-round is "<n>.<k>", n the latest ended round and k the
// run's stops so far, from its records' fold (Task 5's review, decision 10;
// re-review N1), so that a script that announces each change of it
// announces each round's end once and each stop once, and a resume nothing:
// round 3 ends, 3.0; the run stops, it changes, in the version that sets the
// stop's sentence, though the board's stop entry has not landed; the
// entry's arrival changes nothing; a resume changes nothing, the round's
// sentence back; round 4 ends, it changes; a second stop with no round
// between, it changes. A halt with a round open, its round's end and its
// stop in one version, changes it once, its entry nothing more; and a run
// stopped before any round ended reads 0.1.
func TestTheSummarysRoundChangesOnceForEachRoundsEndAndEachStop(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	two := board.Assessment{Hypotheses: []board.HypothesisView{hyp("e1", "a", "first", 2, nil), hyp("e2", "b", "second", 1, nil)}}
	// at is item id's summary's data-round and words, its run in state, its
	// latest ended round ended, its stop and its stops so far as its fold
	// gives them, and its board's entries in the feed.
	at := func(id item.ID, state string, ended int, stop string, stops int, feed ...app.FeedLine) (round, words string) {
		t.Helper()
		w.boards.set(id, assessed(id, state, two, func(r *app.BoardRead) {
			r.Board.Header.Round, r.Board.Header.Ended = ended+1, ended
			r.Board.View.Stop, r.Board.View.Stops, r.Board.Feed = stop, stops, feed
		}))
		n := node(t, b.Get(w.public+"/items/"+string(id)).Body, "summary")
		require.NotNil(t, n)
		return attr(n, "data-round"), shownWords(n)
	}

	ended3, words3 := at("s1", "investigating", 3, "", 0)
	assert.Equal(t, "3.0", ended3)
	assert.Equal(t, `Round 3 ended: leading, service="a"`, words3)
	stopped, words := at("s1", "investigating", 3, "quiet", 1)
	assert.NotEqual(t, ended3, stopped, "the stop changes it, its entry not landed yet")
	assert.Equal(t, "The run stopped: a round passed with nothing new", words)
	landed, _ := at("s1", "awaiting_close", 3, "quiet", 1, stopEntry(9))
	assert.Equal(t, stopped, landed, "the stop entry's arrival changes nothing")
	resumed, words := at("s1", "investigating", 3, "", 1, stopEntry(9), resumeEntry(10))
	assert.Equal(t, stopped, resumed, "a resume changes nothing")
	assert.Equal(t, words3, words, "the round's sentence back")
	ended4, _ := at("s1", "investigating", 4, "", 1, stopEntry(9), resumeEntry(10))
	assert.NotEqual(t, resumed, ended4, "round 4's end changes it")
	again, _ := at("s1", "awaiting_close", 4, "budget_exhausted", 2, stopEntry(9), resumeEntry(10), stopEntry(15))
	assert.NotEqual(t, ended4, again, "a second stop, with no round between, changes it")

	open, _ := at("s2", "investigating", 3, "", 0)
	halted, words := at("s2", "investigating", 4, "killed", 1)
	assert.Equal(t, []string{"3.0", "4.1"}, []string{open, halted}, "a halt with round 4 open: its end and the stop in one version")
	assert.Equal(t, "The run stopped: the kill switch stopped it", words)
	entered, _ := at("s2", "awaiting_close", 4, "killed", 1, stopEntry(20))
	assert.Equal(t, halted, entered, "its stop entry changes nothing more")

	early, words := at("s3", "awaiting_close", 0, "not_staffed", 1, stopEntry(3))
	assert.Equal(t, "0.1", early, "a run stopped before any round ended")
	assert.Equal(t, "The run stopped: no agent was left to invite", words)
}

// Each region's data-v is a hash of its content as rendered (R156; plan
// review M3): every region carries one; the same content gives the same
// versions, and the same ETag; a change of the strip's model changes its
// version alone; the clock moved 30 s, no time's words changing, changes
// none; moved past the minute an "ago" counts, the header's, which holds it,
// changes, and those that hold no time do not.
func TestRegionVersionsAreContentHashes(t *testing.T) {
	w := newClockWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	updated := w.clock.Now().Add(-10*time.Minute - 5*time.Second)
	read := func(strip app.Strip) *app.BoardRead {
		r := richRead("v1", "")
		r.Board.Header.Updated, r.Board.Header.StopBy, r.Strip = updated, time.Time{}, strip
		for i := range r.Board.Header.Alert {
			r.Board.Header.Alert[i].At = updated
		}
		return r
	}
	first := read(app.Strip{Items: []app.StripItem{{ID: "s1", Type: "incident", State: item.StatusInvestigating}}})
	w.boards.set("v1", first)
	get := func() map[string]string {
		t.Helper()
		p := b.Get(w.public + "/items/v1")
		require.Equal(t, http.StatusOK, p.Resp.StatusCode)
		return versionsOf(t, p.Body)
	}

	versions := get()
	for _, id := range regionNames {
		assert.Regexp(t, `^[0-9a-f]{16}$`, versions[id], id)
	}
	assert.Regexp(t, `^"[0-9a-f]{32}"$`, versions["etag"])
	assert.Equal(t, versions, get(), "the same content, the same versions")

	w.boards.set("v1", read(app.Strip{Items: []app.StripItem{{ID: "s1", Type: "incident", State: item.StatusAwaitingClose}}}))
	changed := get()
	for _, id := range regionNames {
		if id == "strip" {
			assert.NotEqual(t, versions[id], changed[id], id)
			continue
		}
		assert.Equal(t, versions[id], changed[id], "%s: the strip's change changes no other region", id)
	}
	assert.NotEqual(t, versions["etag"], changed["etag"])

	w.clock.advance(30 * time.Second)
	assert.Equal(t, changed, get(), "the clock moved 30 s, and no time's words changed")

	w.clock.advance(time.Minute)
	moved := get()
	assert.NotEqual(t, changed["board-header"], moved["board-header"], "the header's ago changed")
	for _, id := range []string{"switch", "standby", "summary", "strip", "hypotheses", "feed"} {
		assert.Equal(t, changed[id], moved[id], "%s holds no time", id)
	}
}

// A board read that outlasts its wait, as one whose pool has no connection
// free, answers 503 within it, in words (R141).
func TestABoardReadPastItsWaitAnswers503(t *testing.T) {
	restore := pages.SetBoardWait(50 * time.Millisecond)
	defer restore()
	w := newWorld(t)
	w.boards.fail(nil, true)
	began := time.Now()

	p := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/a1")

	assert.Less(t, time.Since(began), time.Second, "within the board's wait")
	assert.Equal(t, http.StatusServiceUnavailable, p.Resp.StatusCode)
	assert.Contains(t, text(t, p.Body), "The board could not be read just now. Reload the page to try again.")
}

// errorLines counts logs' Error lines of the board read's failure of item
// id.
func errorLines(t *testing.T, logs, id string) int {
	t.Helper()
	n := 0
	for line := range strings.SplitSeq(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if entry["level"] == "ERROR" && entry["msg"] == "the web interface could not read an item" && entry["item"] == id {
			n++
		}
	}
	return n
}

// A board read's failure is logged at Error at most once a minute an item
// (R176), by its class and item: ten failing reads of one item in a minute
// log one line; another item's, its own; and the next minute's first read of
// the first item, one more.
func TestABoardReadsFailureIsLoggedOnceAMinute(t *testing.T) {
	w := newClockWorld(t)
	w.boards.fail(errors.New("connection reset"), false)
	b := w.browser(t, "vic", auth.Viewer)
	for range 10 {
		require.Equal(t, http.StatusServiceUnavailable, b.Get(w.public+"/items/f1").Resp.StatusCode)
		w.clock.advance(5 * time.Second)
	}
	assert.Equal(t, 1, errorLines(t, w.logs.String(), "f1"), "ten reads in a minute, one line")
	b.Get(w.public + "/items/f2")
	assert.Equal(t, 1, errorLines(t, w.logs.String(), "f2"), "another item's own")

	w.clock.advance(15 * time.Second)
	b.Get(w.public + "/items/f1")
	assert.Equal(t, 2, errorLines(t, w.logs.String(), "f1"), "the next minute's first")
	assert.NotContains(t, w.logs.String(), "connection reset", "never the error's text")
}

// A hidden item's page shows its hidden copy and its state word alone
// (design D13): nothing of its run, its strip, its hypotheses, its summary
// or its feed, no last update, no order, and a stale indicator with no
// time; every region still exists, empty.
func TestAHiddenItemsPageShowsItsCopyAndStateAlone(t *testing.T) {
	w := newWorld(t)
	read := richRead("h1", "")
	f := &feedFixture{id: "h1"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	f.entry(agentA, readEvidence("the disk is full", "call-1", "disk full on db-1", "logs/search", "", ""))
	read.Board.Feed = f.lines
	w.boards.set("h1", read)
	restore := pages.SetVisible(func(*app.View) bool { return false })
	defer restore()

	p := w.browser(t, "carol", auth.Responder).Get(w.public + "/items/h1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	assert.Equal(t, "Open "+hiddenCopy, regionWords(t, p.Body, "board-header"))
	for _, id := range []string{"summary", "strip", "hypotheses", "feed"} {
		assert.Empty(t, regionWords(t, p.Body, id), id)
	}
	root := node(t, p.Body, "board")
	require.NotNil(t, root)
	assert.NotEmpty(t, attr(root, "data-etag"))
	assert.False(t, hasAttr(root, "data-order"), "no order for a hidden item")
	assert.Equal(t, "reconnecting", attr(node(t, p.Body, "board-header"), "data-stale"))
	assert.False(t, hasAttr(node(t, p.Body, "summary"), "data-round"))
	words := text(t, p.Body)
	for _, absent := range []string{"Last updated", "Checkout fails", "A bad release", "Alert", "suggestion", "s2", "p1", "Round", "Opened by", "the disk is full"} {
		assert.NotContains(t, words, absent)
	}
	assert.NotRegexp(t, `\d{4}-\d\d-\d\d \d\d:\d\d`, words, "no time of its run")
	for _, l := range links(t, p.Body) {
		assert.NotContains(t, l, "/calls/")
	}
}

// A board page, and a raw output's, frame themselves with the kill switch
// from their read's facts, which the board watch's tick read, never with a
// read of their own on the main pool; and so does a read that failed with
// its tick in hand (Task 5's review, decision 6), an item with no record, a
// 404, or one whose records are invalid, a 500, on or off as the tick read
// it. A read with no tick in hand, or whose tick could not read the switch,
// shows it not read. The items list still reads the table. (Task 5's own;
// Task 4's concern 6.)
func TestABoardPagesFrameTakesTheKillSwitchFromItsRead(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "carol", auth.Responder)
	w.views.setSwitch(&app.KillSwitch{Seq: 9, OnSeq: 8, Since: time.Now()})
	w.boards.set("k1", boardRead("k1", "investigating", func(r *app.BoardRead) {
		r.Facts.KillSwitch = app.KillSwitch{On: true, Seq: 4, OnSeq: 4, By: "sso:bob", Since: time.Now().Add(-2 * time.Minute)}
	}))
	raw := citing("o1", map[string][]string{"call-1": {"x"}})
	raw.Facts = app.BoardFacts{KillSwitchRead: true, KillSwitch: app.KillSwitch{On: true, Seq: 6, OnSeq: 6, By: "sso:dan", Since: time.Now()}}
	w.boards.set("o1", raw)
	w.calls.set(readCall("call-1", outputRun, `{}`, `"x"`))

	onBoard := text(t, b.Get(w.public+"/items/k1").Body)
	onOutput := text(t, b.Get(w.public+outputPath("o1", "call-1")).Body)

	assert.Contains(t, onBoard, "Kill switch: on since")
	assert.Contains(t, onBoard, "(2 min ago) by sso:bob ; row 4")
	assert.Contains(t, onOutput, "by sso:dan ; row 6")

	on := app.BoardFacts{KillSwitchRead: true, KillSwitch: app.KillSwitch{On: true, Seq: 7, OnSeq: 7, By: "sso:eve", Since: time.Now()}}
	off := app.BoardFacts{KillSwitchRead: true, KillSwitch: app.KillSwitch{Seq: 8, OnSeq: 7, Since: time.Now()}}
	unread := app.BoardFacts{Now: time.Now()}
	corrupt := fmt.Errorf("fold item k8: %w", item.ErrInvalid)
	for _, tc := range []struct {
		name   string
		tick   app.BoardFacts
		fail   error
		path   string
		status int
		want   string
	}{
		{"an item with no record, the switch on", on, nil, "/items/k9", http.StatusNotFound, "Kill switch: on since"},
		{"an item with no record, the switch off", off, nil, "/items/k9", http.StatusNotFound, "Kill switch: off, since"},
		{"a raw output of an item with no record", on, nil, outputPath("k9", "call-1"), http.StatusNotFound, "by sso:eve ; row 7"},
		{"records that are invalid", on, corrupt, "/items/k8", http.StatusInternalServerError, "by sso:eve ; row 7"},
		{"a raw output, records that are invalid", off, corrupt, outputPath("k8", "call-1"), http.StatusInternalServerError, "; row 8"},
		{"no tick in hand", app.BoardFacts{}, errors.New("the probe failed"), "/items/k7", http.StatusServiceUnavailable,
			"Kill switch: its state could not be read"},
		{"a tick that could not read the switch", unread, nil, "/items/k9", http.StatusNotFound, "Kill switch: its state could not be read"},
	} {
		w.boards.inTick(&tc.tick)
		w.boards.fail(tc.fail, false)

		p := b.Get(w.public + tc.path)

		assert.Equal(t, tc.status, p.Resp.StatusCode, tc.name)
		words := text(t, p.Body)
		assert.Contains(t, words, tc.want, tc.name)
		assert.NotContains(t, words, "row 9", tc.name)
	}
	assert.Zero(t, w.views.reads(), "no board page reads the kill switch's table itself")
	assert.Contains(t, text(t, b.Get(w.public+"/items").Body), "row 9")
	assert.Equal(t, 1, w.views.reads(), "the items list reads it")
}

// On a standby, the board pages keep their read of the lease's holder for a
// second, a cache of their own (R139), so that polls on a standby do not
// each read it; the items list reads it each time, as 9a-1's pages do.
// (Task 5's own.)
func TestAStandbysBoardPagesKeepTheLeasesHolderASecond(t *testing.T) {
	w := newClockWorld(t)
	w.leader.set(false, false)
	w.holder.name = "instance-b"
	w.holder.expires = w.clock.Now().Add(time.Minute)
	w.boards.set("a1", boardRead("a1", "investigating", nil))
	b := w.browser(t, "vic", auth.Viewer)

	for range 3 {
		assert.Contains(t, regionWords(t, b.Get(w.public+"/items/a1").Body, "standby"), "instance-b")
	}
	assert.Equal(t, 1, w.holder.read(), "read once in its second")
	w.clock.advance(1100 * time.Millisecond)
	b.Get(w.public + "/items/a1")
	assert.Equal(t, 2, w.holder.read(), "and again past it")
	b.Get(w.public + "/items")
	b.Get(w.public + "/items")
	assert.Equal(t, 4, w.holder.read(), "9a-1's pages read it each time")
}

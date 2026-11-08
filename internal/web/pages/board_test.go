package pages_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// reported is a view with one round whose report is assessment.
func reported(id, state string, a board.Assessment) app.View {
	return app.View{ID: item.ID(id), State: state, Type: "incident", Rounds: []app.RoundView{{Round: 1, Report: board.Report{Assessment: a}}}}
}

// The board page shows the state in spec 10.2's words, the banners, the
// stop entry, the conclusion and the hypotheses with their status words,
// and when it was last updated (design D9).
func TestTheBoardPageRendersStatesBannersStopEntryAndStatusWords(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	get := func(id string) (int, string) {
		p := b.Get(w.public + "/items/" + id)
		return p.Resp.StatusCode, text(t, p.Body)
	}

	w.views.setItem(&app.View{ID: "q1", State: "queued", Type: "incident", QueuePosition: 2})
	code, words := get("q1")
	require.Equal(t, http.StatusOK, code, words)
	assert.Contains(t, words, "Queued")
	assert.Contains(t, words, "position 2 in the queue")

	w.views.setItem(&app.View{ID: "i0", State: "investigating", Type: "incident"})
	_, words = get("i0")
	assert.Contains(t, words, "Agents are reading.")

	conclusion := hyp("e1", "checkout", "A bad release of checkout", 2, func(h *board.HypothesisView) { h.Converged = true })
	w.views.setItem(&app.View{
		ID: "c1", State: "awaiting_close", Type: "incident", Stop: "converged",
		Rounds:    []app.RoundView{{Round: 1, Report: board.Report{Assessment: board.Assessment{Conclusion: "e1", Hypotheses: []board.HypothesisView{conclusion}}}}},
		StopEntry: &board.Stop{Reason: "converged", Rounds: 3, Tokens: 4200, WallClock: 90 * time.Second},
		ClosesAt:  time.Now().Add(time.Hour),
	})
	_, words = get("c1")
	assert.Contains(t, words, "Run stopped: converged")
	assert.Contains(t, words, "3 rounds")
	assert.Contains(t, words, "4200 tokens")
	assert.Contains(t, words, "Conclusion")
	assert.Contains(t, words, `service="checkout"`)
	assert.Contains(t, words, "model-written")
	assert.Contains(t, words, "A bad release of checkout")
	assert.Contains(t, words, "converged", "its status word")
	assert.Contains(t, words, "closes by itself at")

	statuses := board.Assessment{Hypotheses: []board.HypothesisView{
		hyp("e1", "a", "first", 3, nil),
		hyp("e2", "b", "second", 2, nil),
		hyp("e3", "c", "third", 1, func(h *board.HypothesisView) { h.StandingRefutations = []board.EntryID{"r"} }),
		hyp("e4", "d", "fourth", 0, func(h *board.HypothesisView) { h.Rejected = true }),
	}}
	w.views.setItem(ptr(reported("s1", "investigating", statuses)))
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
	w.views.setItem(&app.View{
		ID: "n1", State: "awaiting_close", Type: "incident", Stop: "budget_exhausted",
		Rounds: []app.RoundView{{Round: 1, Report: board.Report{Assessment: board.Assessment{Hypotheses: []board.HypothesisView{hyp("e1", "a", "first", 1, nil)}}}}},
	})
	_, words = get("n1")
	assert.Contains(t, words, "not converged")

	// Not staffed, with the kill switch's banner; and the banner while it is on.
	w.views.setItem(&app.View{ID: "u1", State: "awaiting_close", Type: "incident", NotStaffed: item.CauseKillSwitch})
	_, words = get("u1")
	assert.Contains(t, words, "Not staffed: handle with your usual process")
	assert.Contains(t, words, "Stopped by the kill switch")
	w.views.setItem(&app.View{ID: "u2", State: "awaiting_close", Type: "incident", NotStaffed: item.CauseDailyCap})
	_, words = get("u2")
	assert.Contains(t, words, "Not staffed: handle with your usual process")
	assert.NotContains(t, words, "Stopped by the kill switch")
	w.views.setSwitch(&app.KillSwitch{On: true, Seq: 3, OnSeq: 3})
	_, words = get("u2")
	assert.Contains(t, words, "Stopped by the kill switch", "the banner shows while the kill switch is on")
	w.views.setSwitch(&app.KillSwitch{})

	// An auto-close time that could not be read, and the queue on a standby.
	w.views.setItem(&app.View{ID: "x1", State: "awaiting_close", Type: "incident", Stop: "converged", ClosesAtUnread: true})
	_, words = get("x1")
	assert.Contains(t, words, "auto-close time not known")

	// Closed.
	at := time.Now().Add(-3 * time.Hour)
	w.views.setItem(&app.View{ID: "d1", State: "closed", Type: "incident", Closed: &app.ClosedView{At: at, By: "sso:alice", ReopenableUntil: time.Now().Add(time.Hour)}})
	_, words = get("d1")
	assert.Contains(t, words, "Closed at")
	assert.Contains(t, words, "UTC")
	assert.Contains(t, words, "3 h ago")
	assert.Contains(t, words, "sso:alice")

	// Last updated, in the zone and in words.
	assert.Regexp(t, `Last updated at \d{4}-\d\d-\d\d \d\d:\d\d UTC \(just now\)`, words)

	// A later build's item, an item that does not exist, and a read that fails.
	w.views.setItem(&app.View{ID: "l1", HeldByLaterBuild: true})
	code, words = get("l1")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, words, "held by a later build")
	code, _ = get("nope")
	assert.Equal(t, http.StatusNotFound, code)
	w.views.itemErr = assert.AnError
	code, words = get("q1")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Contains(t, words, "could not be read")
	assert.NotContains(t, words, assert.AnError.Error())
}

func ptr[T any](v T) *T { return &v }

// Hypothesis statements are model-written: they are marked so, and escaped;
// the page carries no quote, no raw output, no feed and no live refresh.
func TestModelTextIsMarkedModelWrittenAndEscaped(t *testing.T) {
	w := newWorld(t)
	evil := `<script>alert("x")</script> & <b>bold</b>`
	w.views.setItem(ptr(reported("m1", "investigating", board.Assessment{
		Conclusion: "e1", Hypotheses: []board.HypothesisView{hyp("e1", "a", evil, 2, nil)},
	})))
	b := w.browser(t, "vic", auth.Viewer)
	p := b.Get(w.public + "/items/m1")
	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	body := string(p.Body)
	assert.NotContains(t, body, "<script>alert")
	assert.NotContains(t, body, "<b>bold</b>")
	assert.Contains(t, body, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; &lt;b&gt;bold&lt;/b&gt;")
	assert.Contains(t, text(t, p.Body), "model-written")
	assert.Equal(t, 2, strings.Count(body, "model-written"), "the conclusion's statement and the hypothesis's are each marked")
	assert.NotContains(t, strings.ToLower(body), "<script")
	assert.NotContains(t, strings.ToLower(body), `http-equiv="refresh"`)
	assert.NotContains(t, strings.ToLower(text(t, p.Body)), "feed")
}

// Every route that shows a board asks the visibility seam first, and a
// board the seam hides shows the fixed copy of a hidden run, no board
// (Plan 8a replaces the seam; design D9).
func TestEveryBoardRouteCallsTheVisibleSeam(t *testing.T) {
	w := newWorld(t)
	w.views.setItem(ptr(reported("v1", "investigating", board.Assessment{
		Hypotheses: []board.HypothesisView{hyp("e1", "a", "secret statement", 1, nil)},
	})))
	routes := pages.BoardRoutes()
	require.Contains(t, routes, "/items/{id}")
	var seen []item.ID
	restore := pages.SetVisible(func(v *app.View) bool { seen = append(seen, v.ID); return false })
	defer restore()
	b := w.browser(t, "vic", auth.Viewer)
	for _, route := range routes {
		seen = nil
		p := b.Get(w.public + strings.NewReplacer("{id}", "v1").Replace(route))
		require.Equal(t, http.StatusOK, p.Resp.StatusCode, route)
		assert.Equal(t, []item.ID{"v1"}, seen, "%s asks the seam once", route)
		words := text(t, p.Body)
		assert.Contains(t, words, "Agents are working on this alert for measurement.")
		assert.NotContains(t, words, "secret statement", route)
	}
}

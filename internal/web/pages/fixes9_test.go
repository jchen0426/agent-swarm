package pages_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// section is the text of body between from and to (to may be empty for the
// end).
func section(words, from, to string) string {
	_, rest, _ := strings.Cut(words, from)
	if to == "" {
		return rest
	}
	got, _, _ := strings.Cut(rest, to)
	return got
}

// A run that stopped without converging marks the conclusion board.Assess
// picked (the top hypothesis no one rejected) "not converged", by the stop
// and not by whether a conclusion exists; with no conclusion, the leading
// hypothesis; and a run that converged is not marked (review I1).
func TestAStoppedRunThatDidNotConvergeMarksItsConclusionNotConverged(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	get := func(id string) string { return text(t, b.Get(w.public+"/items/"+id).Body) }

	// The shape Assess gives: a conclusion that did not converge.
	w.views.setItem(&app.View{
		ID: "b1", State: "awaiting_close", Type: "incident", Stop: "budget_exhausted",
		Rounds: []app.RoundView{{Round: 1, Report: board.Report{Assessment: board.Assessment{
			Conclusion: "e1", Hypotheses: []board.HypothesisView{hyp("e1", "a", "first", 1, nil), hyp("e2", "b", "second", 0, nil)},
		}}}},
	})
	words := get("b1")
	assert.Contains(t, section(words, "Conclusion", "Hypotheses"), "not converged")
	assert.Equal(t, 1, strings.Count(section(words, "Hypotheses", ""), "not converged"), "marked on the leading hypothesis alone")

	// No conclusion: the first hypothesis that is leading, not a rejected one.
	w.views.setItem(&app.View{
		ID: "b2", State: "awaiting_close", Type: "incident", Stop: "quiet",
		Rounds: []app.RoundView{{Round: 1, Report: board.Report{Assessment: board.Assessment{
			Hypotheses: []board.HypothesisView{hyp("e1", "a", "first", 1, func(h *board.HypothesisView) { h.Rejected = true }), hyp("e2", "b", "second", 0, nil)},
		}}}},
	})
	assert.NotContains(t, get("b2"), "not converged", "no hypothesis is leading")

	// Converged: not marked.
	w.views.setItem(&app.View{
		ID: "b3", State: "awaiting_close", Type: "incident", Stop: "converged",
		Rounds: []app.RoundView{{Round: 1, Report: board.Report{Assessment: board.Assessment{
			Conclusion: "e1", Hypotheses: []board.HypothesisView{hyp("e1", "a", "first", 2, func(h *board.HypothesisView) { h.Converged = true })},
		}}}},
	})
	assert.NotContains(t, get("b3"), "not converged")
}

// An item whose records cannot be read says so in fixed words with no
// advice to retry, is not a 503, and is logged once at Warn, not at Error
// for every view; a read that fails otherwise stays a 503 (review I2).
func TestAnItemWithCorruptRecordsIsUnreadableNotUnavailable(t *testing.T) {
	w := newWorld(t)
	w.views.itemErr = fmt.Errorf("fold item c1: %w", item.ErrInvalid)
	b := w.browser(t, "vic", auth.Viewer)
	for range 3 {
		p := b.Get(w.public + "/items/c1")
		assert.Equal(t, http.StatusInternalServerError, p.Resp.StatusCode)
		words := text(t, p.Body)
		assert.Contains(t, words, "This item's records cannot be read.")
		assert.NotContains(t, words, "try again")
		assert.NotContains(t, words, "Reload")
	}
	w.views.itemErr = fmt.Errorf("read the board: %w", board.ErrInvalid)
	assert.Equal(t, http.StatusInternalServerError, b.Get(w.public+"/items/c2").Resp.StatusCode)
	logs := w.logs.String()
	assert.Equal(t, 2, strings.Count(logs, `"level":"WARN"`), "once for each item: %s", logs)
	assert.NotContains(t, logs, `"level":"ERROR"`)
}

// The items page links to the opening form for a responder and an admin, not
// for a viewer; and every row links to its board, whatever the row is
// (spec 10.2; review I3, I4).
func TestTheItemsPageLinksToTheOpeningFormAndEveryBoard(t *testing.T) {
	w := newWorld(t)
	w.views.rows = []app.Row{
		{ID: "a1", State: "investigating", Title: "one"},
		{ID: "a2", Unreadable: true},
		{ID: "a3", HeldByLaterBuild: true},
	}
	for who, roles := range map[string][]auth.Role{"resp": {auth.Responder}, "adm": {auth.Admin}} {
		got := links(t, w.browser(t, who, roles...).Get(w.public+"/items").Body)
		assert.Contains(t, got, "/items/new", who)
	}
	viewer := links(t, w.browser(t, "vic", auth.Viewer).Get(w.public+"/items").Body)
	assert.NotContains(t, viewer, "/items/new")
	for _, id := range []string{"a1", "a2", "a3"} {
		assert.Contains(t, viewer, "/items/"+id, "a row links to its board")
	}
	w.leader.set(false, false)
	standby := links(t, w.browser(t, "resp", auth.Responder).Get(w.public+"/items").Body)
	assert.NotContains(t, standby, "/items/new", "acts are disabled on a standby")
	assert.Contains(t, standby, "/items/a1")
}

// A POST that opens an item lists no items (review M1); the services are
// read when a form is rendered.
func TestAnOpeningPostListsNoItems(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/new")
	before := w.views.listCount()
	require.Equal(t, 1, before)
	answer := b.Submit(form, "/items/new", url.Values{"type": {"incident"}, "title": {"t"}})
	require.Equal(t, http.StatusOK, answer.Resp.StatusCode)
	assert.Equal(t, before, w.views.listCount())
}

// A retry after an unknown outcome keeps its key when no revision is
// current for a moment: the opening is one item (review M2).
func TestARetryKeepsItsKeyWhileNoRevisionIsCurrent(t *testing.T) {
	w := newWorld(t)
	w.coord.queueOpen(errors.New("the database is gone"))
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/new")
	first := b.Submit(form, "/items/new", url.Values{"type": {"incident"}, "title": {"t"}})
	require.Equal(t, http.StatusServiceUnavailable, first.Resp.StatusCode)
	w.revs.set(nil)
	second := b.Submit(first, "/items/new", nil)
	require.Equal(t, http.StatusOK, second.Resp.StatusCode, text(t, second.Body))
	opens := w.coord.openings()
	require.Len(t, opens, 2)
	assert.Equal(t, opens[0].ID, opens[1].ID)
	assert.Equal(t, 1, w.coord.openedItems())

	// A form needs the revision's types: without one, none is offered.
	again := b.Get(w.public + "/items/new")
	assert.Equal(t, http.StatusServiceUnavailable, again.Resp.StatusCode)
}

// Stop reasons and causes are shown in words, and an item that never had a
// run does not say its run stopped (review M3).
func TestStopsAndCausesAreInWords(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	w.views.setItem(&app.View{
		ID: "w1", State: "awaiting_close", Type: "incident", Stop: "budget_exhausted",
		Rounds: []app.RoundView{{Round: 1}},
	})
	words := text(t, b.Get(w.public+"/items/w1").Body)
	assert.Contains(t, words, "Run stopped: the budget was used up")
	assert.NotContains(t, words, "budget_exhausted")

	w.views.setItem(&app.View{ID: "w2", State: "awaiting_close", Type: "incident", Stop: "cut", NotStaffed: item.CauseDailyCap})
	words = text(t, b.Get(w.public+"/items/w2").Body)
	assert.NotContains(t, words, "Run stopped")
	assert.Contains(t, words, "Cause: the day's spend reached the daily cap")
	assert.NotContains(t, words, "daily_cap")
}

// The board offers Promote where app.May allows it for the viewer's actor:
// not to a viewer, not on a standby (review M4).
func TestTheBoardOffersPromoteOnlyWhereTheActorMay(t *testing.T) {
	w := newWorld(t)
	w.views.setItem(&app.View{ID: "q1", State: "queued", Type: "incident", QueuePosition: 1})
	promote := func(roles ...auth.Role) bool {
		p := w.browser(t, "u", roles...).Get(w.public + "/items/q1")
		require.Equal(t, http.StatusOK, p.Resp.StatusCode)
		for _, l := range links(t, p.Body) {
			if l == "/items/q1/promote" {
				return true
			}
		}
		return false
	}
	assert.True(t, promote(auth.Responder))
	assert.True(t, promote(auth.Admin))
	assert.False(t, promote(auth.Viewer))
	w.leader.set(false, false)
	assert.False(t, promote(auth.Responder), "none on a standby")
}

// An admin whose freshness lapsed is a responder: the opening form offers no
// principal field but a link to authenticate again, a GET; naming another
// principal is refused by the coordinator with the same link, never a
// redirect (review M5; R76, I8).
func TestAStaleAdminIsOfferedFreshAuthenticationOnTheOpeningForm(t *testing.T) {
	w := newWorld(t)
	c := w.session(t, "erin", 10*time.Minute, time.Hour, auth.Admin)
	resp, body := w.get(t, "/items/new", c)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, field(t, []byte(body), "/items/new", "principal"))
	assert.Contains(t, links(t, []byte(body)), auth.FreshPath+"?return=%2Fitems%2Fnew")

	k := "AAAAAAAAAAAAAAAAAAAAAA"
	resp, body = w.post(t, "/items/new", c, url.Values{
		"type": {"incident"}, "title": {"t"}, "principal": {"sso:bob"}, "k": {k},
		"mac": {w.keys.FormMAC("sso:erin", "open", "open", k)}, auth.TokenField: {w.token(t, c)},
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))
	assert.Contains(t, links(t, []byte(body)), auth.FreshPath+"?return=%2Fitems%2Fnew")
	opens := w.coord.openings()
	require.Len(t, opens, 1)
	assert.Equal(t, "sso:bob", opens[0].Principal)
	assert.False(t, opens[0].Actor.Admin)
	assert.Equal(t, 0, w.coord.openedItems())
}

// The board shows its time in the deployment's zone (review M6).
func TestTheBoardShowsTimeInTheDeploymentsZone(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	require.NoError(t, err)
	w := newZoneWorld(t, sydney)
	w.views.setItem(&app.View{ID: "q1", State: "queued", Type: "incident"})
	words := text(t, w.browser(t, "vic", auth.Viewer).Get(w.public+"/items/q1").Body)
	assert.Regexp(t, `Last updated at \d{4}-\d\d-\d\d \d\d:\d\d (AEDT|AEST) \(just now\)`, words)
	assert.NotContains(t, words, " UTC ")
}

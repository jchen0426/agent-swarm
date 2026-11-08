package pages_test

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// The fixed header shows the kill switch from its table alone, as
// Views.KillSwitch reads it: on since when, by whom, escaped, and the row it
// read, or off, or that it could not be read; it links to /kill-switch, and
// it never asks the coordinator for a preview (M6; design D7).
func TestTheHeaderShowsTheSwitchFromTheTableAndComputesNoPreview(t *testing.T) {
	w := newWorld(t)
	w.views.ks = app.KillSwitch{
		On: true, Seq: 7, OnSeq: 7, By: "sso:<b>bob</b>", Since: time.Now().Add(-4 * time.Minute),
		Reason: board.Reason{Code: "runaway_cost", Text: "<i>spend</i>"},
	}
	b := w.browser(t, "alice", auth.Responder)

	page := b.Get(w.public + "/items")
	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	body := string(page.Body)
	words := text(t, page.Body)
	assert.Contains(t, words, "Kill switch: on")
	assert.Contains(t, words, "by sso:<b>bob</b>", "the subject is shown as text")
	assert.Contains(t, body, "sso:&lt;b&gt;bob&lt;/b&gt;", "and escaped by html/template")
	assert.NotContains(t, body, "<b>bob</b>")
	assert.NotContains(t, body, "<i>spend</i>")
	assert.Contains(t, words, "row 7")
	assert.Contains(t, words, "4 min ago")
	assert.Contains(t, links(t, page.Body), "/kill-switch")
	assert.Equal(t, 1, w.views.reads(), "the header reads the table once")
	assert.Zero(t, w.coord.switched(), "the header computes no preview")
	assert.Zero(t, w.coord.previews())

	w.views.ks = app.KillSwitch{Seq: 8, By: "sso:bob", Since: time.Now()}
	words = text(t, b.Get(w.public+"/items").Body)
	assert.Contains(t, words, "Kill switch: off")
	assert.Contains(t, words, "row 8")

	w.views.ksErr = errors.New("the table cannot be read")
	words = text(t, b.Get(w.public+"/items").Body)
	assert.Contains(t, words, "Kill switch: its state could not be read")
	assert.Zero(t, w.coord.switched())
}

// /items groups its rows by incident, and each row offers the acts that
// app.May allows the actor on it, by its principal and responders, and no
// other: a member's item offers them, another's none (R76, R77; design D8).
func TestItemsPageGroupsByIncidentAndShowsOnlyTheButtonsMayAllows(t *testing.T) {
	w := newWorld(t)
	w.views.rows = []app.Row{
		{ID: "a1", Title: "Disk full", Incident: "inc-a", Principal: "sso:alice", State: "investigating", PositionKnown: true},
		{ID: "a2", Title: "Queue stuck", Incident: "inc-b", Principal: "sso:carol", Responders: []string{"sso:alice"}, State: "queued", QueuePosition: 2, PositionKnown: true},
		{ID: "a3", Title: "Latency", Incident: "inc-a", Principal: "sso:carol", State: "awaiting_close", PositionKnown: true},
		{ID: "a4", Title: "Old", Incident: "inc-c", Principal: "sso:alice", State: "closed", PositionKnown: true,
			ClosedAt: time.Now().Add(-time.Hour), ReopenableUntil: time.Now().Add(time.Hour)},
	}
	page := w.browser(t, "alice", auth.Responder).Get(w.public + "/items")
	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	words := text(t, page.Body)

	incA, incB := strings.Index(words, "Incident inc-a"), strings.Index(words, "Incident inc-b")
	require.NotEqual(t, -1, incA)
	require.NotEqual(t, -1, incB)
	for _, title := range []string{"Disk full", "Latency"} {
		at := strings.Index(words, title)
		assert.True(t, at > incA && (at < incB || incB < incA), "%s is under inc-a", title)
	}
	assert.Contains(t, words, "position 2 in the queue")
	for _, label := range []string{"Investigating", "Queued", "Awaiting close", "Closed"} {
		assert.Contains(t, words, label)
	}

	got := links(t, page.Body)
	for _, want := range []string{"/items/a1/close", "/items/a2/promote", "/items/a2/close", "/items/a4/reopen"} {
		assert.Contains(t, got, want)
	}
	for _, l := range got {
		assert.False(t, strings.HasPrefix(l, "/items/a3/"), "no act on another's item: %s", l)
	}
	assert.NotContains(t, got, "/items/a1/reopen", "no reopen of an item that is not closed")
	assert.NotContains(t, got, "/items/a4/close", "no close of a closed item")

	// A viewer is no member and no admin: no act at all.
	page = w.browser(t, "dave", auth.Viewer).Get(w.public + "/items")
	assert.False(t, slices.ContainsFunc(links(t, page.Body), func(l string) bool { return strings.HasPrefix(l, "/items/") }))

	// A fresh admin overrides on every item.
	page = w.browser(t, "erin", auth.Admin).Get(w.public + "/items")
	assert.Contains(t, links(t, page.Body), "/items/a3/close")

	// A row held by a later build says so, as fixed text, and offers nothing.
	w.views.rows = []app.Row{{ID: "a9", HeldByLaterBuild: true}, {ID: "a8", Unreadable: true}}
	page = w.browser(t, "erin", auth.Admin).Get(w.public + "/items")
	words = text(t, page.Body)
	assert.Contains(t, words, "held by a later build")
	assert.Contains(t, words, "records could not be read")
	assert.False(t, slices.ContainsFunc(links(t, page.Body), func(l string) bool { return strings.HasPrefix(l, "/items/a9") }))

	// A list that cannot be read says so.
	w.views.listErr = errors.New("down")
	page = w.browser(t, "erin", auth.Admin).Get(w.public + "/items")
	assert.Equal(t, http.StatusServiceUnavailable, page.Resp.StatusCode)
	assert.Contains(t, text(t, page.Body), "The items could not be read")
}

// An item opened by the admin API without a principal says that an admin
// acts on it: no responder is its member (m25).
func TestAnItemWithNoPrincipalSaysAnAdminActs(t *testing.T) {
	w := newWorld(t)
	w.views.rows = []app.Row{{ID: "a1", Title: "From the API", Incident: "inc-a", State: "investigating", PositionKnown: true}}
	page := w.browser(t, "alice", auth.Responder).Get(w.public + "/items")
	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	assert.Contains(t, text(t, page.Body), "no principal: an admin acts")
	assert.NotContains(t, links(t, page.Body), "/items/a1/close")
}

// A standby offers no act, says that it does not lead, and names the leader
// from the lease's holder, a name and not an address (R-b; R90; design D7);
// its act pages refuse without asking the coordinator, and its queue
// positions are not known on this instance.
func TestAStandbyDisablesTheActsAndNamesTheLeader(t *testing.T) {
	w := newWorld(t)
	w.leader.set(false, false)
	w.holder.name = "instance-b"
	w.coord.open("a1", "sso:alice")
	w.views.rows = []app.Row{{ID: "a1", Title: "Disk full", Incident: "inc-a", Principal: "sso:alice", State: "queued"}}
	b := w.browser(t, "alice", auth.Responder)

	page := b.Get(w.public + "/items")
	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	words := text(t, page.Body)
	assert.Contains(t, words, "This instance does not lead")
	assert.Contains(t, words, "instance-b")
	assert.Contains(t, words, "a name, not an address")
	assert.Contains(t, words, "not known on this instance")
	assert.False(t, slices.ContainsFunc(links(t, page.Body), func(l string) bool { return strings.HasPrefix(l, "/items/a1/") }))

	form := b.Get(w.public + "/items/a1/close")
	assert.Equal(t, http.StatusConflict, form.Resp.StatusCode)
	assert.Contains(t, text(t, form.Body), "instance-b")
	assert.Contains(t, text(t, form.Body), "Nothing was done here", "a standby says it tried nothing")
	assert.NotContains(t, text(t, form.Body), "not known", "nothing was sent, so no outcome is in doubt")
	assert.Empty(t, field(t, form.Body, "/items/a1/close", "k"), "no act form on a standby")

	c := w.session(t, "alice", 0, time.Hour, auth.Responder)
	resp, body := w.post(t, "/items/a1/close", c, w.form("sso:alice", app.ActClose, "a1", "AAAAAAAAAAAAAAAAAAAAAA", w.token(t, c)))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, body, "instance-b")
	assert.Contains(t, body, "Nothing was done here")
	assert.Zero(t, w.coord.previews())
	assert.Empty(t, w.coord.commits())

	// A lease nobody holds now names no one.
	w.holder.expires = time.Now().Add(-time.Minute)
	assert.Contains(t, text(t, b.Get(w.public+"/items").Body), "No instance leads right now")
}

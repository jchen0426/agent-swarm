package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/admin"
	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// closedRulings is how long after its close a closed item takes late rulings
// in the views the tests make: the deployment's default (Decision 15).
const closedRulings = 72 * time.Hour

// viewsOf makes the views of the harness's items as serve makes them: over
// the snapshot of the harness's coordinator, whichever instance it is, the
// kill switch's table, through the harness's fake, its reads alone, and the
// registry's revisions, with the harness's reopen window.
func viewsOf(h *harness) *app.Views {
	return app.NewViews(h.records, h.boards, &app.ViewPorts{
		Lifecycle: func() app.Lifecycle { return h.c.Lifecycle() }, Controls: killSwitchReads{h.controls}, Revisions: h.revs,
		ReopenWindow: h.opts.ReopenWindow, ClosedRulings: closedRulings,
	})
}

// killSwitchReads is the kill switch's table as the views take it, its reads
// alone (Contract C16): serve gives the views the table unguarded, so that
// they must have no way to write it.
type killSwitchReads struct{ table *controls }

func (r killSwitchReads) KillSwitch(ctx context.Context) (app.KillSwitch, error) {
	return r.table.KillSwitch(ctx)
}

// adminToken is the admin token of the admin API the views are served
// through (see shown).
const adminToken = "admin-token-4f1c9a2e7b"

// adminInstance is the instance as the admin API reads it, for the tests that
// read the views through it: a leader of epoch 1 that does not drain, a
// schema it runs on, and the admin token.
type adminInstance struct{}

func (adminInstance) Status() (epoch int64, held, draining bool)     { return 1, true, false }
func (adminInstance) State(context.Context) (db.State, error)        { return db.Compatible, nil }
func (adminInstance) Secret(context.Context, string) (string, error) { return adminToken, nil }

// processRunner is the process runner, as the status shows it.
type processRunner struct{}

func (processRunner) Status() admin.RunnerStatus { return admin.RunnerStatus{Kind: "process"} }

// shown is an item's view as the admin API shows it (Contract C4), served
// over views and the harness's coordinator: its JSON members by their names.
func shown(t *testing.T, h *harness, views *app.Views, id item.ID) map[string]json.RawMessage {
	t.Helper()
	api := admin.New(admin.Ports{
		Coordinator: h.c, Leader: adminInstance{}, Schema: adminInstance{}, Items: views, Secrets: adminInstance{}, Runner: processRunner{},
	}, admin.Options{Tenant: "example", TokenRef: "secret://admin/token"})
	r := httptest.NewRequest(http.MethodGet, "/v1/items/"+string(id), http.NoBody)
	r.RemoteAddr = "127.0.0.1:50123"
	r.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &members), w.Body.String())
	return members
}

// jsonOf is v as JSON.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

// An item's view folds its records and reads its run's board: each round as
// it opened, the evidence its end recorded (RoundEnded), and the board's
// report of it; and the item's incident, type and opening time, its state and
// its run's stop, and, as it awaits its close, when it closes by itself. In
// round 1 two agents make a hypothesis converge while two tool calls fail, a
// model call fails and another is still in flight when the wait for it ends;
// round 2, its wrap-up round, which the agents end with nothing to post,
// stops the run converged. An id with no records is no item.
func TestAnItemsViewFoldsItsRecordsAndReadsItsBoard(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, hypothesis
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.tools.set(func(c *toolCalls) { c.failed[roundOf{string(run), 1}] = 2 })
	h.spend.set(func(s *spend) {
		s.inFlight[roundOf{string(run), 1}] = 1
		s.failed[string(run)] = []app.ModelCall{{ID: "call-1", Agent: release.Name, Status: "error"}}
	})
	h.play()
	h.clock.Advance(h.opts.Grace + h.opts.SettleWait)
	h.scripts[release.Name], h.scripts[runtime.Name] = endTurn, endTurn
	h.round()
	views := viewsOf(h)

	view, err := views.Item(t.Context(), id)

	require.NoError(t, err)
	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	want := app.View{
		ID: id, State: "awaiting_close", Stop: "converged", Incident: s.Incident, Type: "incident", OpenedAt: s.OpenedAt,
		ClosesAt: s.AwaitingSince.Add(closesAfter),
	}
	for i := range s.Run.Rounds {
		r := &s.Run.Rounds[i]
		report, err := h.boards.Report(t.Context(), run, r.Opened.Round)
		require.NoError(t, err)
		want.Rounds = append(want.Rounds, app.RoundView{
			Round: r.Opened.Round, WrapUp: r.Opened.WrapUp != "", Ended: string(r.Ended.EndedBy), Quiet: r.Ended.Quiet,
			TurnEnds: r.Ended.TurnEnds, FailedCalls: r.Ended.FailedCalls, FailedToolCalls: r.Ended.FailedToolCalls,
			Settled: r.Ended.Settled, Report: report,
		})
	}
	assert.Equal(t, want, view)
	require.Len(t, view.Rounds, 2)
	first, wrapUp := view.Rounds[0], view.Rounds[1]
	assert.Equal(t, []any{1, false, "turns", false, []string{release.Name, runtime.Name}, 1, 2, false},
		[]any{first.Round, first.WrapUp, first.Ended, first.Quiet, first.TurnEnds, first.FailedCalls, first.FailedToolCalls, first.Settled})
	assert.Positive(t, first.Report.Facts.Entries, "the round's report counts its entries")
	assert.Equal(t, []any{2, true, "turns", []string{release.Name, runtime.Name}, 0, 0, true},
		[]any{wrapUp.Round, wrapUp.WrapUp, wrapUp.Ended, wrapUp.TurnEnds, wrapUp.FailedCalls, wrapUp.FailedToolCalls, wrapUp.Settled})
	assert.Equal(t, []string{release.Name, runtime.Name}, wrapUp.Report.Facts.Ended)

	_, err = views.Item(t.Context(), "no-such-item")
	require.ErrorIs(t, err, app.ErrNoItem)
}

// An item that holds a record of a later version, or whose run's board holds
// an event of one, is held by a later build (Decision 19): its view says so,
// and nothing else, since this build cannot read what that build wrote.
func TestAViewOfAnItemOfALaterVersionSaysHeld(t *testing.T) {
	for name, later := range map[string]func(t *testing.T, h *harness, id item.ID){
		"a record of version 2": func(t *testing.T, h *harness, id item.ID) {
			appendRaw(t, h, id, item.Body{V: item.Version + 1})
		},
		"a board event of version 2": func(t *testing.T, h *harness, id item.ID) {
			appendLaterEvent(t, h, h.state(id).Run.ID)
		},
	} {
		h := newHarness(t)
		id := h.open("checkout fails")
		later(t, h, id)

		view, err := viewsOf(h).Item(t.Context(), id)

		require.NoError(t, err, name)
		assert.Equal(t, app.View{ID: id, HeldByLaterBuild: true}, view, name)
	}
}

// The view of an item whose round is open shows the round, ended by nothing
// yet, with the board's report of it so far; the run has not stopped.
func TestAViewShowsAnOpenRoundAsNotEnded(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	s := h.state(id)
	report, err := h.boards.Report(t.Context(), s.Run.ID, 1)
	require.NoError(t, err)

	view, err := viewsOf(h).Item(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, app.View{
		ID: id, State: "investigating", Incident: s.Incident, Type: "incident", OpenedAt: s.OpenedAt, Rounds: []app.RoundView{{Round: 1, Report: report}},
	}, view)
}

// An item opened once the day's spend reached the daily cap awaits its close
// without a run: its view says why, the cause too, and shows no round; it
// closes by itself its delay after its opening.
func TestAViewOfAnItemNeverStaffedSaysWhy(t *testing.T) {
	h := newHarness(t)
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
	id := h.open("checkout fails")
	s := h.state(id)

	view, err := viewsOf(h).Item(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, app.View{
		ID: id, State: "awaiting_close", Stop: "not_staffed", Incident: s.Incident, Type: "incident", OpenedAt: s.OpenedAt,
		NotStaffed: item.CauseDailyCap, ClosesAt: s.AwaitingSince.Add(closesAfter),
	}, view)
}

// An item whose records do not fold is corrupt, not unknown, nor held by a
// later build: its view fails.
func TestAViewOfACorruptItemFails(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	appendRaw(t, h, id, item.Body{V: item.Version})

	_, err := viewsOf(h).Item(t.Context(), id)

	require.ErrorIs(t, err, item.ErrInvalid)
	assert.NotErrorIs(t, err, app.ErrNoItem)
}

// A standby's view of an item awaiting its close shows its auto-close paused
// while the kill switch's table holds the switch on (Decision 14; R27): the
// views read the switch from its table, not from the coordinator, whose
// snapshot is a standby's, empty. Once the table holds it off, the item
// closes by itself its delay after the off, which came after the item's
// entry into awaiting_close (Decision 8). An item whose delay its records
// hold as zero, and whose revision cannot be read, shows no time, though its
// auto-close is not paused. A table that cannot be read leaves both unknown
// (R28).
func TestAStandbysItemViewShowsTheAutoClosePaused(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	zero := h.open("payments fail")
	_, err := h.c.Close(t.Context(), zero, closing("close-1"))
	require.NoError(t, err)
	loseCommit(h, commit)
	_, err = h.c.Reopen(t.Context(), zero, reopening("reopen-1"))
	require.NoError(t, err)
	require.Equal(t, item.StatusAwaitingClose, h.state(zero).Status())
	require.Zero(t, h.state(zero).Closing.AutoClose, "its reopen recorded no delay, as it could read none")
	h.crash()
	require.Equal(t, app.Lifecycle{}, h.c.Lifecycle(), "a standby's snapshot is empty")
	views := viewsOf(h)
	writeRows(t, h, true)

	paused, err := views.Item(t.Context(), id)

	require.NoError(t, err)
	assert.True(t, paused.AutoClosePaused, "the table holds the switch on")
	assert.Zero(t, paused.ClosesAt, "a paused auto-close gives no time")
	assert.False(t, paused.AutoCloseUnread)

	h.clock.Advance(time.Minute)
	writeRows(t, h, false)
	off := tableView(t, h).OffAt
	require.True(t, off.After(h.state(id).AwaitingSince), "the off came after the item's entry into awaiting_close")
	resumed, err := views.Item(t.Context(), id)
	require.NoError(t, err)
	assert.False(t, resumed.AutoClosePaused)
	assert.Equal(t, off.Add(closesAfter), resumed.ClosesAt, "its delay after the off")

	unknown, err := views.Item(t.Context(), zero)
	require.NoError(t, err)
	assert.False(t, unknown.AutoClosePaused)
	assert.Zero(t, unknown.ClosesAt, "a delay recorded as zero, and a revision that cannot be read, give no time")
	assert.False(t, unknown.AutoCloseUnread)

	h.controls.failReads(true)
	unread, err := views.Item(t.Context(), id)
	require.NoError(t, err, "the view does not fail for the table")
	assert.True(t, unread.AutoCloseUnread)
	assert.False(t, unread.AutoClosePaused)
	assert.Zero(t, unread.ClosesAt)
}

// An item's view gives its place in the lifecycle (Contract C4): every item's
// incident, type and opening time; a waiting item's place in the queue, from
// 1, in the queue's order (R17); an item awaiting its close not staffed, its
// cause, or kill_switch for an item whose run the kill switch stopped (R18);
// and a closed item's close, reopenable until its close plus the reopen
// window and taking late rulings until its close plus their window. Under a
// limit of 1, one item investigates and two wait, and one opens at the daily
// cap; then the kill switch stops the run of the first, and leaves the two
// waiting not staffed; then a person closes the one opened at the cap. The
// admin API shows each under C4's names, each only where it applies.
func TestAnItemsViewShowsItsLifecycle(t *testing.T) {
	h := newHarness(t, limit(1))
	running := h.open("checkout fails")
	first, second := openLater(h, "payments fail"), openLater(h, "search fails")
	capped := notStaffedAtTheCap(h)
	views := viewsOf(h)
	view := func(id item.ID) app.View {
		t.Helper()
		v, err := views.Item(t.Context(), id)
		require.NoError(t, err)
		return v
	}
	lifecycle := []string{"queue", "notStaffed", "closesAt", "autoClosePaused", "closed"}
	// only checks that an item's JSON gives, of the lifecycle's members, those
	// named, and returns them.
	only := func(id item.ID, names ...string) map[string]json.RawMessage {
		t.Helper()
		members := shown(t, h, views, id)
		s := h.state(id)
		assert.JSONEq(t, jsonOf(t, s.Incident), string(members["incident"]), id)
		assert.JSONEq(t, `"incident"`, string(members["type"]), id)
		assert.JSONEq(t, jsonOf(t, s.OpenedAt.UTC()), string(members["openedAt"]), id)
		for _, name := range lifecycle {
			_, given := members[name]
			assert.Equal(t, slices.Contains(names, name), given, "%s: %s", id, name)
		}
		return members
	}

	for _, id := range []item.ID{running, first, second, capped} {
		s := h.state(id)
		v := view(id)
		assert.Equal(t, []any{s.Incident, "incident", s.OpenedAt}, []any{v.Incident, v.Type, v.OpenedAt}, id)
	}
	assert.Zero(t, view(running).QueuePosition, "an item investigating waits in no queue")
	assert.Equal(t, 1, view(first).QueuePosition)
	assert.Equal(t, 2, view(second).QueuePosition)
	only(running)
	assert.JSONEq(t, `{"position": 1}`, string(only(first, "queue")["queue"]))
	assert.JSONEq(t, `{"position": 2}`, string(only(second, "queue")["queue"]))
	capView := view(capped)
	assert.Equal(t, item.CauseDailyCap, capView.NotStaffed)
	assert.Equal(t, h.state(capped).AwaitingSince.Add(closesAfter), capView.ClosesAt)
	assert.False(t, capView.AutoClosePaused)
	capJSON := only(capped, "notStaffed", "closesAt", "autoClosePaused")
	assert.JSONEq(t, `"daily_cap"`, string(capJSON["notStaffed"]))
	assert.JSONEq(t, jsonOf(t, capView.ClosesAt.UTC()), string(capJSON["closesAt"]))
	assert.JSONEq(t, `false`, string(capJSON["autoClosePaused"]))

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	requireKilled(t, h, running)

	killed := view(running)
	assert.Equal(t, []any{"awaiting_close", "killed", item.CauseKillSwitch, true}, []any{killed.State, killed.Stop, killed.NotStaffed, killed.AutoClosePaused},
		"a run the kill switch stopped leaves its item not staffed for the kill switch, its auto-close paused")
	assert.Zero(t, killed.ClosesAt)
	killedJSON := only(running, "notStaffed", "autoClosePaused")
	assert.JSONEq(t, `"kill_switch"`, string(killedJSON["notStaffed"]))
	assert.JSONEq(t, `true`, string(killedJSON["autoClosePaused"]))
	for _, id := range []item.ID{first, second} {
		v := view(id)
		assert.Equal(t, []any{"awaiting_close", "not_staffed", item.CauseKillSwitch, 0}, []any{v.State, v.Stop, v.NotStaffed, v.QueuePosition}, id)
		only(id, "notStaffed", "autoClosePaused")
	}

	_, err = h.c.Close(t.Context(), capped, closing("close-capped"))
	require.NoError(t, err)

	closedAt := h.state(capped).ClosedAt
	closed := view(capped)
	assert.Equal(t, app.View{
		ID: capped, State: "closed", Incident: h.state(capped).Incident, Type: "incident", OpenedAt: h.state(capped).OpenedAt,
		Closed: &app.ClosedView{
			At: closedAt, By: "admin", ReopenableUntil: closedAt.Add(h.opts.ReopenWindow), LateRulingsUntil: closedAt.Add(closedRulings),
		},
	}, closed, "a closed item shows its close, and nothing of the awaiting_close it left")
	closedJSON := only(capped, "closed")
	assert.JSONEq(t, jsonOf(t, map[string]any{
		"at": closedAt.UTC(), "by": "admin", "auto": false,
		"reopenableUntil": closedAt.Add(h.opts.ReopenWindow).UTC(), "lateRulingsUntil": closedAt.Add(closedRulings).UTC(),
	}), string(closedJSON["closed"]))
	assert.JSONEq(t, `"closed"`, string(closedJSON["state"]))
}

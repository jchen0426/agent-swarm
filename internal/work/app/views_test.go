package app_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// An item's view folds its records and reads its run's board: each round as
// it opened, the evidence its end recorded (RoundEnded), and the board's
// report of it; and the item's state and its run's stop. In round 1 two
// agents make a hypothesis converge while two tool calls fail, a model call
// fails and another is still in flight when the wait for it ends; round 2,
// its wrap-up round, which the agents end with nothing to post, stops the run
// converged. An id with no records is no item.
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
	views := app.NewViews(h.records, h.boards)

	view, err := views.Item(t.Context(), id)

	require.NoError(t, err)
	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	want := app.View{ID: id, State: "awaiting_close", Stop: "converged"}
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

		view, err := app.NewViews(h.records, h.boards).Item(t.Context(), id)

		require.NoError(t, err, name)
		assert.Equal(t, app.View{ID: id, HeldByLaterBuild: true}, view, name)
	}
}

// The view of an item whose round is open shows the round, ended by nothing
// yet, with the board's report of it so far; the run has not stopped.
func TestAViewShowsAnOpenRoundAsNotEnded(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	report, err := h.boards.Report(t.Context(), run, 1)
	require.NoError(t, err)

	view, err := app.NewViews(h.records, h.boards).Item(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, app.View{ID: id, State: "investigating", Rounds: []app.RoundView{{Round: 1, Report: report}}}, view)
}

// An item opened once the day's spend reached the daily cap awaits its close
// without a run: its view says why, and shows no round.
func TestAViewOfAnItemNeverStaffedSaysWhy(t *testing.T) {
	h := newHarness(t)
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
	id := h.open("checkout fails")

	view, err := app.NewViews(h.records, h.boards).Item(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, app.View{ID: id, State: "awaiting_close", Stop: "not_staffed"}, view)
}

// An item whose records do not fold is corrupt, not unknown, nor held by a
// later build: its view fails.
func TestAViewOfACorruptItemFails(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	appendRaw(t, h, id, item.Body{V: item.Version})

	_, err := app.NewViews(h.records, h.boards).Item(t.Context(), id)

	require.ErrorIs(t, err, item.ErrInvalid)
	assert.NotErrorIs(t, err, app.ErrNoItem)
}

package app_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// reopening is a reopen through the admin API, by its admin, under key, with
// no reason.
func reopening(key string) *app.Act {
	return &app.Act{Actor: app.Actor{Subject: "admin", Admin: true}, Key: key}
}

// closedAwaiting opens an incident and plays its round, which stops its run
// quiet, so that the item awaits its close; then a person closes it, under
// the key close-1.
func closedAwaiting(h *harness, title string) item.ID {
	h.t.Helper()
	id := awaitingItsClose(h, title)
	_, err := h.c.Close(h.t.Context(), id, closing("close-1"))
	require.NoError(h.t, err)
	require.Equal(h.t, item.StatusClosed, h.state(id).Status())
	return id
}

// reopenAnswerLost is the records store whose appends of a reopen commit,
// then fail with errAnswerLost, as when the database's answer is lost on its
// way back. Every other append answers.
type reopenAnswerLost struct{ *failing }

func (l reopenAnswerLost) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	committed, err := l.failing.Append(ctx, id, decide)
	if err != nil {
		return nil, err
	}
	for i := range committed {
		if committed[i].Body.Reopened != nil {
			return nil, errAnswerLost
		}
	}
	return committed, nil
}

// reopensLoseTheirAnswers has the harness's instance take over again, under
// the same epoch, with a reopenAnswerLost of the harness's records store as
// its records store.
func reopensLoseTheirAnswers(t *testing.T, h *harness) {
	t.Helper()
	ports := h.ports()
	ports.Records = reopenAnswerLost{h.records}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
}

// A person reopens a closed item within the reopen window (spec Section 4.1;
// Decision 7): here an item that awaited its close quiet, closed by a person,
// is reopened an hour later. One record, item_reopened, by the admin, with the
// act's request, which keeps the key's hash, and the awaiting_close the item
// returns to: its run's reason, quiet, and its delay, 30 minutes. The item
// awaits its close, and the reopen answers changed, with the time it closes by
// itself, the reopen plus 30 minutes. Its request is a reopen's, whose digest
// is not the close's.
func TestAReopenWithinTheWindowReturnsTheItemToAwaitingClose(t *testing.T) {
	h := newHarness(t)
	id := closedAwaiting(h, "checkout fails")
	closed := lastRecord(h, id).Body.Closed
	h.clock.Advance(time.Hour)
	kinds := h.kinds(id)

	out, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))

	require.NoError(t, err)
	assert.Equal(t, append(slices.Clone(kinds), item.KindReopened), h.kinds(id), "one record")
	r := lastRecord(h, id)
	reopened := r.Body.Reopened
	require.NotNil(t, reopened)
	assert.Equal(t, "admin", reopened.By)
	assert.Nil(t, reopened.Reason)
	assert.Equal(t, keyHash("reopen-1"), reopened.Request.Key)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, reopened.Request.Digest)
	assert.NotEqual(t, closed.Request.Digest, reopened.Request.Digest, "a reopen's request, not a close's")
	assert.Equal(t, item.AwaitingClose{Reason: item.StopQuiet, AutoClose: closesAfter}, reopened.AwaitingClose)
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{ClosesAt: r.At.Add(closesAfter)}}, out)
}

// A reopen past the window is refused (Decision 7): 24 hours and 1 second
// after an item's close, a reopen answers Refused, reopen_window_passed, and
// records nothing.
func TestAReopenPastTheWindowIsRefused(t *testing.T) {
	h := newHarness(t)
	id := closedAwaiting(h, "checkout fails")
	advanceTo(h, h.state(id).ClosedAt.Add(h.opts.ReopenWindow+time.Second))
	before := h.carried()

	_, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))

	var refused *app.Refused
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, app.CodeReopenWindowPassed, refused.Code)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
}

// The reopen window is judged in the database's time, the coordinator's clock,
// not in the process's own (Decision 7): here the harness's clock, the
// database's, moves 24 hours and 1 second past an item's close while the
// process's own time moves only seconds, and the item's reopen is refused.
// Another item, closed 2 seconds after it, is then 24 hours less 1 second past
// its close, and its reopen is taken.
func TestTheReopenWindowIsJudgedInTheDatabasesTime(t *testing.T) {
	began := time.Now()
	h := newHarness(t)
	first, second := awaitingItsClose(h, "checkout fails"), awaitingItsClose(h, "payments fail")
	_, err := h.c.Close(t.Context(), first, closing("close-1"))
	require.NoError(t, err)
	h.clock.Advance(2 * time.Second)
	_, err = h.c.Close(t.Context(), second, closing("close-1"))
	require.NoError(t, err)
	closed := h.state(first).ClosedAt
	require.Equal(t, closed.Add(2*time.Second), h.state(second).ClosedAt)
	advanceTo(h, closed.Add(h.opts.ReopenWindow+time.Second))
	kinds := h.kinds(first)

	_, refused := h.c.Reopen(t.Context(), first, reopening("reopen-1"))
	out, err := h.c.Reopen(t.Context(), second, reopening("reopen-1"))

	var r *app.Refused
	require.ErrorAs(t, refused, &r, "24 hours and 1 second past its close")
	assert.Equal(t, app.CodeReopenWindowPassed, r.Code)
	assert.Equal(t, kinds, h.kinds(first), "nothing is recorded")
	require.NoError(t, err, "24 hours less 1 second past its close")
	assert.True(t, out.Changed)
	assert.Equal(t, item.StatusAwaitingClose, h.state(second).Status())
	assert.Less(t, time.Since(began), time.Minute, "the process's own time moved only seconds")
}

// A reopen of an item that is not closed changes nothing (Decision 13): a
// reopen of an item awaiting its close answers changed false, and records
// nothing.
func TestReopeningAnItemThatIsNotClosedChangesNothing(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	kinds, before := h.kinds(id), h.carried()

	out, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{}, out)
	assert.Equal(t, kinds, h.kinds(id))
	assert.Equal(t, before, h.carried(), "no record and no board entry")
}

// A reopened item's auto-close counts from its reopen (Decisions 7 and 8): an
// item closed at T and reopened at T plus 1 hour closes by itself at T plus 1
// hour 30 minutes, not before, automatic, due then.
func TestAReopenedItemClosesByItselfFromItsReopen(t *testing.T) {
	h := newHarness(t)
	id := closedAwaiting(h, "checkout fails")
	closed := h.state(id).ClosedAt
	advanceTo(h, closed.Add(time.Hour))
	_, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)
	require.Equal(t, closed.Add(time.Hour), h.state(id).AwaitingSince)
	reopened := h.kinds(id)

	advanceTo(h, closed.Add(time.Hour+closesAfter-time.Second))
	before := h.kinds(id)
	h.clock.Advance(time.Second)

	assert.Equal(t, reopened, before, "not before T plus 1 hour 30 minutes")
	due := closed.Add(time.Hour + closesAfter)
	c, at := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, c)
	assert.Equal(t, due, at)
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
}

// A reopen never fails for want of a revision (Decision 7): an item that never
// started, as its start found no revision, closed, then reopened while none is
// current, is reopened, its item_reopened recording a delay of zero, and its
// reopen answers no time it closes by itself. Its timer cannot read the delay
// yet: its arming is tried again after the retry wait. Once a revision is
// current, the timer reads the type's 30 minutes, and the item closes 30
// minutes after its reopen.
func TestAReopenWithNoRevisionRecordsTheDelayLater(t *testing.T) {
	h := newHarness(t)
	rev := h.revs.rev
	// The opening reads the revision; its start reads none.
	h.revs.next(nil)
	id := h.open("checkout fails")
	require.Equal(t, item.StatusQueued, h.state(id).Status())
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)

	out, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))

	require.NoError(t, err, "the reopen does not fail")
	assert.Equal(t, app.Outcome{Changed: true}, out, "with no time it closes by itself")
	reopened := lastRecord(h, id).Body.Reopened
	require.NotNil(t, reopened)
	assert.Equal(t, item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseClosed}, reopened.AwaitingClose, "its delay recorded as zero")
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
	assert.Equal(t, []time.Time{h.clock.Now().Add(h.opts.Retry)}, h.clock.armed(), "no timer of its auto-close yet: its retry alone")
	h.revs.set(rev)
	h.clock.Advance(h.opts.Retry)
	due := h.state(id).AwaitingSince.Add(closesAfter)
	assert.Equal(t, []time.Time{due}, h.clock.armed(), "the timer reads the type's 30 minutes")
	advanceTo(h, due)
	closed, _ := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed, "it closes 30 minutes after its reopen")
}

// A reopen whose append committed, though the database's answer was lost, is
// found by its key (Decisions 7 and 13): Reopen loads the item, finds its
// item_reopened, and answers changed, as a repeat; the item, advanced, arms
// its auto-close, and closes by itself at its deadline.
func TestAReopenWhoseCommitAnswerWasLostArmsItsAutoClose(t *testing.T) {
	h := newHarness(t)
	reopensLoseTheirAnswers(t, h)
	id := closedAwaiting(h, "checkout fails")
	h.clock.Advance(time.Hour)

	out, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true}, out, "as a repeat")
	s := h.state(id)
	require.Equal(t, item.StatusAwaitingClose, s.Status(), "its item_reopened landed")
	due := s.AwaitingSince.Add(closesAfter)
	assert.Equal(t, []time.Time{due}, h.clock.armed(), "the item, advanced, armed its auto-close")
	advanceTo(h, due)
	closed, _ := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed, "it closes by itself at its deadline")
}

// A timer that a close and a reopen made stale closes nothing (Decision 8):
// an item awaits its close from T, its timer due at T plus 30 minutes; a
// person closes it at T plus 10 minutes, and reopens it at T plus 20 minutes.
// At T plus 30 minutes nothing is recorded; at T plus 50 minutes, 30 minutes
// after its reopen, it closes by itself, once.
func TestAnAutoCloseArmedBeforeAReopenClosesNothing(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	since := h.state(id).AwaitingSince
	require.Equal(t, []time.Time{since.Add(closesAfter)}, h.clock.armed(), "its timer, due at T plus 30 minutes")
	advanceTo(h, since.Add(10*time.Minute))
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	advanceTo(h, since.Add(20*time.Minute))
	_, err = h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)
	reopened := h.kinds(id)

	advanceTo(h, since.Add(closesAfter))
	atThirty := h.kinds(id)
	advanceTo(h, since.Add(50*time.Minute))
	h.clock.Advance(time.Hour)

	assert.Equal(t, reopened, atThirty, "nothing is recorded at T plus 30 minutes")
	assert.Equal(t, append(slices.Clone(reopened), item.KindClosed), h.kinds(id), "one item_closed")
	due := since.Add(50 * time.Minute)
	closed, at := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed, "automatic, due at T plus 50 minutes")
	assert.Equal(t, due, at)
}

// Arming an item's auto-close again cancels the timer armed before, so that an
// item has one at most (Decision 8): a reopen repeated under its key advances
// the item, which arms its auto-close again. One timer of the item is pending,
// and at its deadline one item_closed is recorded.
func TestArmingAnAutoCloseAgainCancelsTheEarlier(t *testing.T) {
	h := newHarness(t)
	id := closedAwaiting(h, "checkout fails")
	h.clock.Advance(time.Hour)
	_, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)
	reopened, made := h.kinds(id), h.clock.made()

	again, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true}, again, "a repeat, as the reopen did")
	assert.Equal(t, reopened, h.kinds(id), "no record more")
	assert.Equal(t, made+1, h.clock.made(), "its auto-close armed again")
	due := h.state(id).AwaitingSince.Add(closesAfter)
	assert.Equal(t, []time.Time{due}, h.clock.armed(), "one timer of the item is pending")
	advanceTo(h, due)
	h.clock.Advance(time.Hour)
	assert.Equal(t, append(slices.Clone(reopened), item.KindClosed), h.kinds(id), "one item_closed")
	closed, at := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed)
	assert.Equal(t, due, at, "at its deadline")
}

// A close found by its key acts on the item's state now (Decision 13). Here
// the close stopped the item's run mid-round. Found while the item is closed,
// it drops the run, which ends its sessions and closes its upstream sessions.
// Found after a reopen, it drops nothing: it ends no session, stops no turn
// and closes no upstream session, and it leaves the item awaiting its close,
// its auto-close armed.
func TestAFoundCloseKeyActsOnTheItemsStateNow(t *testing.T) {
	for _, c := range []struct {
		name     string
		reopened bool
		status   item.Status
	}{
		{"found while the item is closed", false, item.StatusClosed},
		{"found after a reopen", true, item.StatusAwaitingClose},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			order := tracing(t, h)
			id := h.open("checkout fails")
			run := h.state(id).Run.ID
			_, err := h.c.Close(t.Context(), id, closing("close-1"))
			require.NoError(t, err)
			if c.reopened {
				_, err = h.c.Reopen(t.Context(), id, reopening("reopen-1"))
				require.NoError(t, err)
			}
			seen := len(order.seen())

			out, err := h.c.Close(t.Context(), id, closing("close-1"))

			require.NoError(t, err)
			assert.Equal(t, app.Outcome{Changed: true}, out, "a repeat")
			acted := order.seen()[seen:]
			s := h.state(id)
			assert.Equal(t, c.status, s.Status())
			if c.reopened {
				assert.Empty(t, acted, "no EndRun, Stop or CloseRun")
				assert.Equal(t, []time.Time{s.AwaitingSince.Add(closesAfter)}, h.clock.armed(), "its auto-close armed")
			} else {
				assert.Contains(t, acted, "end "+string(run), "the run's sessions end")
				assert.Contains(t, acted, "close "+string(run), "its upstream sessions close")
			}
		})
	}
}

// A close repeated under its key after a reopen answers as the close did, and
// closes nothing (Decision 13; R14): a close under one key, a reopen under
// another, then the close repeated under the first answers changed, records
// nothing, and leaves the item awaiting its close.
func TestARepeatedCloseAfterAReopenAnswersAsTheFirstAndClosesNothing(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	first, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	_, err = h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)
	reopened, before := h.kinds(id), h.carried()

	again, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.True(t, first.Changed)
	assert.Equal(t, app.Outcome{Changed: true}, again, "changed, as the close did")
	assert.Equal(t, reopened, h.kinds(id), "nothing is recorded")
	assert.Equal(t, before, h.carried())
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
}

// A reopened item with a run awaits its close for its run's latest stop, as
// ItemReason gives it, closed included (Decision 7): an item whose run a
// person's close stopped mid-round, which never awaited its close, is reopened
// awaiting its close closed, with its type's delay.
func TestAnItemWhoseRunWasClosedIsReopenedAwaitingItsCloseClosed(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	require.Nil(t, h.state(id).Closing, "it never awaited its close")

	_, err = h.c.Reopen(t.Context(), id, reopening("reopen-1"))

	require.NoError(t, err)
	reopened := lastRecord(h, id).Body.Reopened
	require.NotNil(t, reopened)
	assert.Equal(t, item.AwaitingClose{Reason: item.StopClosed, AutoClose: closesAfter}, reopened.AwaitingClose)
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
}

// A reopen's preview takes no key, runs the reopen's checks on the same state,
// under the same lock, and answers its consequences with changed false,
// recording nothing and arming nothing (Decision 13): here when the item would
// close by itself were it reopened now, 30 minutes from now. Past the window,
// the preview is refused, as the reopen is.
func TestAReopensPreviewRecordsNothing(t *testing.T) {
	h := newHarness(t)
	id := closedAwaiting(h, "checkout fails")
	h.clock.Advance(time.Hour)
	before, armed := h.carried(), h.clock.armed()

	out, err := h.c.Reopen(t.Context(), id, previewing())

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Consequences: app.Consequences{ClosesAt: h.clock.Now().Add(closesAfter)}}, out)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, armed, h.clock.armed(), "nothing is armed")
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
	advanceTo(h, h.state(id).ClosedAt.Add(h.opts.ReopenWindow+time.Second))
	_, err = h.c.Reopen(t.Context(), id, previewing())
	var refused *app.Refused
	require.ErrorAs(t, err, &refused, "past the window")
	assert.Equal(t, app.CodeReopenWindowPassed, refused.Code)
}

// A reopen returns an item with no run to not_staffed, for the cause of its
// latest item_awaiting_close before its close, or closed when it had none
// (R12, as the controller ruled for Task 8). An item not staffed at the daily
// cap, then closed, is reopened for the cause daily_cap; so is one not staffed
// at the daily cap, then promoted, then closed while queued, as a promotion
// leaves the cause of the record it lifted; and one closed while queued, which
// never awaited its close, is reopened for the cause closed. Each records the
// type's delay, 30 minutes.
func TestAReopenWithNoRunGivesTheCauseOfItsLatestAwaitingCloseBeforeItsClose(t *testing.T) {
	notStaffed := func(h *harness) item.ID {
		h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
		id := h.open("checkout fails")
		require.Equal(h.t, item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseDailyCap, AutoClose: closesAfter}, *h.state(id).Closing)
		return id
	}
	closeIt := func(h *harness, id item.ID) {
		_, err := h.c.Close(h.t.Context(), id, closing("close-1"))
		require.NoError(h.t, err)
	}
	for _, c := range []struct {
		name   string
		closed func(h *harness) item.ID
		cause  item.Cause
	}{
		{"not staffed at the daily cap, then closed", func(h *harness) item.ID {
			id := notStaffed(h)
			closeIt(h, id)
			return id
		}, item.CauseDailyCap},
		{"not staffed at the daily cap, then promoted, then closed while queued", func(h *harness) item.ID {
			id := notStaffed(h)
			appendRaw(h.t, h, id, item.Body{V: item.Version, Promoted: &item.Promoted{
				By: "admin", Request: item.Request{Key: keyHash("promote-1"), Digest: "sha256:promote"},
			}})
			require.Equal(h.t, item.StatusQueued, h.state(id).Status())
			closeIt(h, id)
			return id
		}, item.CauseDailyCap},
		{"closed while queued, never awaiting its close", func(h *harness) item.ID {
			rev := h.revs.rev
			// The opening reads the revision; its start reads none until the
			// item is closed.
			h.revs.next(nil)
			id := h.open("checkout fails")
			require.Equal(h.t, item.StatusQueued, h.state(id).Status())
			closeIt(h, id)
			h.revs.set(rev)
			return id
		}, item.CauseClosed},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			id := c.closed(h)
			require.Nil(t, h.state(id).Run, "an item with no run")

			_, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))

			require.NoError(t, err)
			reopened := lastRecord(h, id).Body.Reopened
			require.NotNil(t, reopened)
			assert.Equal(t, item.AwaitingClose{Reason: item.StopNotStaffed, Cause: c.cause, AutoClose: closesAfter}, reopened.AwaitingClose)
			assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
		})
	}
}

// A coordinator needs a reopen window above zero, against which a reopen
// judges the time since the item's close (Decision 7).
func TestACoordinatorNeedsAReopenWindowAboveZero(t *testing.T) {
	h := newHarness(t)
	for _, window := range []time.Duration{0, -time.Second} {
		opts := h.opts
		opts.ReopenWindow = window

		_, err := app.NewCoordinator(h.ports(), &opts)

		require.Error(t, err, "a window of %v", window)
	}
}

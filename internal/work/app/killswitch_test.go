package app_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// administrator is the admin API's actor: the subject admin, an admin since
// its token is an admin's.
var administrator = app.Actor{Subject: "admin", Admin: true}

// incident is the reason the tests turn the kill switch on for.
var incident = board.Reason{Code: "incident", Text: "checkout is down"}

// killing turns the kill switch on through the admin API, by its admin, for
// incident, as a request whose sender saw after as the latest row.
func killing(after int64) *app.Act {
	reason := incident
	return &app.Act{Actor: administrator, After: after, Reason: &reason}
}

// turningOff turns the kill switch off through the admin API, by its admin,
// with no reason, as a request whose sender saw after as the latest row.
func turningOff(after int64) *app.Act {
	return &app.Act{Actor: administrator, After: after}
}

// killedClosing is how an item whose run the kill switch stopped awaits its
// close, its finish ended: not staffed, with its type's delay, left so by
// the on-row 1.
var killedClosing = &item.AwaitingClose{Reason: item.StopNotStaffed, AutoClose: closesAfter, UnstaffedBy: 1}

// errRefused is what a store answers that refuses a write a test names.
var errRefused = errors.New("the store refuses the write")

// haltOf reports whether drafts hold a run's stop for the kill switch.
func haltOf(drafts []item.Draft) bool {
	for i := range drafts {
		if st := drafts[i].Body.RunStopped; st != nil && st.Reason == item.StopKilled {
			return true
		}
	}
	return false
}

// awaitingOf reports whether drafts hold an item_awaiting_close.
func awaitingOf(drafts []item.Draft) bool {
	for i := range drafts {
		if drafts[i].Body.AwaitingClose != nil {
			return true
		}
	}
	return false
}

// stopEntryOf reports whether drafts hold a board's stop entry.
func stopEntryOf(drafts []board.Draft) bool {
	for i := range drafts {
		if drafts[i].Body.Stop != nil {
			return true
		}
	}
	return false
}

// killNoticeOf reports whether drafts hold a kill_switch notice.
func killNoticeOf(drafts []board.Draft) bool {
	for i := range drafts {
		if n := drafts[i].Body.Notice; n != nil && n.Code == board.NoticeKillSwitch {
			return true
		}
	}
	return false
}

// noticesOf returns the notices of a run's board of code, in order.
func noticesOf(h *harness, run board.RunID, code board.NoticeCode) []board.Event {
	h.t.Helper()
	var out []board.Event
	events := h.events(run)
	for i := range events {
		if n := events[i].Body.Notice; n != nil && n.Code == code {
			out = append(out, events[i])
		}
	}
	return out
}

// countOf counts the records of an item of kind.
func countOf(h *harness, id item.ID, kind item.Kind) int {
	h.t.Helper()
	n := 0
	for _, k := range h.kinds(id) {
		if k == kind {
			n++
		}
	}
	return n
}

// requireKilled fails unless the item's run stopped for the kill switch, by
// the on-row 1, its round 1 ended stopped, and the item awaits its close not
// staffed, its board holding one kill_switch notice and the stop entry
// killed.
func requireKilled(t *testing.T, h *harness, id item.ID) {
	t.Helper()
	s := h.state(id)
	require.NotNil(t, s.Run, "%s has a run", id)
	run := s.Run.ID
	require.NotNil(t, s.Run.Stopped, "%s's run stopped", id)
	assert.Equal(t, item.StopKilled, s.Run.Stopped.Reason, id)
	assert.Equal(t, int64(1), s.Run.Stopped.Killed, "%s: killed by the on-row 1", id)
	ended := s.Run.Rounds[0].Ended
	require.NotNil(t, ended, "%s's round 1 ended", id)
	assert.Equal(t, item.EndedByStopped, ended.EndedBy, id)
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopKilled}, ended.Decision, id)
	kinds := h.kinds(id)
	assert.Equal(t, []item.Kind{item.KindRoundEnded, item.KindRunStopped, item.KindAwaitingClose}, kinds[len(kinds)-3:], id)
	assert.Equal(t, killedClosing, s.Closing, id)
	assert.Len(t, noticesOf(h, run, board.NoticeKillSwitch), 1, "%s: one kill_switch notice", id)
	stops := stopEntries(h, run)
	require.Len(t, stops, 1, id)
	assert.Equal(t, "killed", stops[0].Reason, id)
}

// pick returns the fields of a log line that keys name.
func pick(line map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		out[k] = line[k]
	}
	return out
}

// endingAll is an instance's live sessions, which count the times every
// session was ended at once.
type endingAll struct {
	app.Sessions
	n *atomic.Int32
}

func (s endingAll) EndAll() {
	s.n.Add(1)
	s.Sessions.EndAll()
}

// countEndAlls has the harness's instance take over again, under the same
// epoch, with its sessions counting each EndAll, and returns the count.
func countEndAlls(t *testing.T, h *harness) *atomic.Int32 {
	t.Helper()
	n := &atomic.Int32{}
	ports := h.ports()
	ports.Sessions = endingAll{Sessions: ports.Sessions, n: n}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	return n
}

// writeRows writes the kill switch's rows, on or off, each naming the latest,
// as another instance wrote them: this instance's view lacks them.
func writeRows(t *testing.T, h *harness, rows ...bool) {
	t.Helper()
	for _, on := range rows {
		seq := h.controls.rows(t)
		_, changed, err := h.controls.Controls.SetKillSwitch(t.Context(), on, "admin", incident, seq)
		require.NoError(t, err)
		require.True(t, changed)
	}
}

// byItem orders run stops by their items, as the answers give them.
func byItem(stops ...app.RunStop) []app.RunStop {
	slices.SortFunc(stops, func(a, b app.RunStop) int { return strings.Compare(string(a.Item), string(b.Item)) })
	return stops
}

// sortedIDs returns ids sorted, as the answers give them.
func sortedIDs(ids ...item.ID) []item.ID {
	slices.Sort(ids)
	return ids
}

// byUnstaffed orders the items that stayed unstaffed by their items, as the
// off's answer gives them.
func byUnstaffed(items ...app.UnstaffedItem) []app.UnstaffedItem {
	slices.SortFunc(items, func(a, b app.UnstaffedItem) int { return strings.Compare(string(a.Item), string(b.Item)) })
	return items
}

// tableView is the kill switch's state as its table holds it, in the fields
// the coordinator's view keeps: whether it is on, its latest row, its latest
// on-row and its latest off-row's time.
func tableView(t *testing.T, h *harness) app.KillSwitch {
	t.Helper()
	s, err := h.controls.KillSwitch(t.Context())
	require.NoError(t, err)
	return app.KillSwitch{On: s.On, Seq: s.Seq, OnSeq: s.OnSeq, OffAt: s.OffAt}
}

// breakBoard commits to a run's board, without the board's rules, a second
// event under the id of its first, as a bug might: the board's events no
// longer replay, and every post to it fails.
func breakBoard(t *testing.T, h *harness, run board.RunID) {
	t.Helper()
	first := h.events(run)[0]
	_, err := h.store.Store.Append(t.Context(), run, func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{{ID: first.ID, Run: run, Round: first.Round, Author: first.Author, Body: first.Body}}, nil
	})
	require.NoError(t, err)
}

// The kill switch stops every run at once, with a notice on each board (spec
// Section 8.3; Decision 5): two items in round 1, their turns running, and
// the switch turned on, after row 0, for a reason. Every turn stops and every
// session ends at once; each run's round ends stopped and the run stops
// killed, by the on-row 1, then its item awaits its close not staffed, left
// so by row 1; each board holds one kill_switch notice and its stop entry
// killed, and each run's upstream sessions close. The answer is on, at row 1,
// changed, and names both runs, stopped at round 1; the coordinator logs
// "kill switch set" at Warn (Contract C3).
func TestTheKillSwitchStopsEveryRunAtOnceWithANoticeOnEachBoard(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	endAlls := countEndAlls(t, h)
	a, b := h.open("checkout fails"), h.open("payments fail")
	runA, runB := h.state(a).Run.ID, h.state(b).Run.ID
	require.Len(t, h.turns.started, 4, "their turns run")
	stopAlls, ends := h.turns.all, endAlls.Load()

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{On: true, Seq: 1, Changed: true, Stopped: byItem(
		app.RunStop{Item: a, Run: runA, Round: 1, Reason: item.StopKilled}, app.RunStop{Item: b, Run: runB, Round: 1, Reason: item.StopKilled},
	)}, out)
	assert.Equal(t, stopAlls+1, h.turns.all, "every turn stops at once")
	assert.Equal(t, ends+1, endAlls.Load(), "every session ends at once")
	for _, id := range []item.ID{a, b} {
		requireKilled(t, h, id)
		assert.Contains(t, h.upstreams.closedRuns(), h.state(id).Run.ID, "%s's upstream sessions close", id)
	}
	lines := logged(t, logs.String(), "kill switch set")
	require.Len(t, lines, 1)
	assert.Equal(t, map[string]any{"level": "WARN", "on": true, "seq": 1.0, "cause": "act", "stopped": 2.0},
		pick(lines[0], "level", "on", "seq", "cause", "stopped"))
}

// The kill switch leaves queued items not staffed at once (Decision 5, the
// controller's call): with a limit of 1, one item investigating and one
// queued, the on stops the run and records the queued item's
// item_awaiting_close not_staffed, for the cause kill_switch, left so by the
// on-row 1. The queued item never starts, and the answer names it among the
// items not staffed.
func TestTheKillSwitchLeavesQueuedItemsNotStaffed(t *testing.T) {
	h := newHarness(t, limit(1))
	a := h.open("checkout fails")
	q := openLater(h, "payments fail")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q), "it waits queued")

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.NoError(t, err)
	assert.Equal(t, []item.ID{q}, out.Unstaffed)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(q), "no item_started")
	assert.Equal(t, &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseKillSwitch, UnstaffedBy: 1, AutoClose: closesAfter}, h.state(q).Closing)
	requireKilled(t, h, a)
	assert.Empty(t, out.Owed)
}

// While the kill switch is on, an item opens not staffed (spec Section 5.1,
// step 6; Decision 5): its opening records item_opened, which knew the row 1,
// and item_awaiting_close not_staffed, for the cause kill_switch, left so by
// the on-row 1, as at the daily cap. No turn starts.
func TestAnItemOpenedWhileTheKillSwitchIsOnIsNotStaffed(t *testing.T) {
	h := newHarness(t)
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)

	id := h.open("checkout fails")

	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(id))
	s := h.state(id)
	assert.Equal(t, int64(1), s.Opened.KillSwitchSeen)
	assert.Equal(t, &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseKillSwitch, UnstaffedBy: 1, AutoClose: closesAfter}, s.Closing)
	assert.Empty(t, h.turns.started)
}

// An item that started, and whose run's creation keeps failing, as the
// revision it locked cannot be read, is left not staffed by the kill switch
// with no run created first (Decision 12, rule 4): item_awaiting_close
// not_staffed, for the cause kill_switch, left so by the on-row 1, with no
// delay, which its writer cannot read.
func TestAKillOfAStartedItemWithoutARunLeavesItNotStaffed(t *testing.T) {
	h := newHarness(t)
	id, _ := withoutItsRun(h)

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.NoError(t, err)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindAwaitingClose}, h.kinds(id), "no run_created")
	assert.Equal(t, &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseKillSwitch, UnstaffedBy: 1}, h.state(id).Closing)
	assert.Equal(t, []item.ID{id}, out.Unstaffed)
}

// A kill while a round settles stops the run once (Decision 3): the round
// ended by its turns, and its end waits for its write calls' grace; the on
// records one round_ended, stopped, with the classes the instance knew, and
// one run_stopped, killed. Past the grace and the settling wait, no second
// round_ended is recorded: the drop cancelled the round's settling.
func TestAKillDuringARoundsSettlingStopsTheRunOnce(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.play()
	require.Nil(t, h.state(id).Run.Rounds[0].Ended, "its end waits for the grace")

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	h.clock.Advance(h.opts.Grace + h.opts.SettleWait)

	assert.Equal(t, 1, countOf(h, id, item.KindRoundEnded))
	assert.Equal(t, 1, countOf(h, id, item.KindRunStopped))
	s := h.state(id)
	assert.Equal(t, item.EndedByStopped, s.Run.Rounds[0].Ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, s.Run.Rounds[0].Ended.Turns)
	assert.Equal(t, item.StopKilled, s.Run.Stopped.Reason)
}

// A kill in a round's grace after its deadline halts the round this instance
// held, with the classes it knew (Decisions 3 and 5): alpha ended its turn,
// the deadline interrupted beta's, and the round's end waits for its grace.
// The on drops the run, keeping its snapshot, and the halt ends the round
// stopped with the snapshot's classes, not as a round this instance did not
// hold, missed, whose classes the board alone would give, unseen.
func TestAKillInARoundsGraceAfterItsDeadlineHaltsItWithTheClassesItKnew(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending("")
	id := h.open("checkout fails")
	h.play()
	advanceTo(h, h.state(id).Run.Latest().Opened.Deadline)
	require.Nil(t, h.state(id).Run.Rounds[0].Ended, "its end waits for the grace")

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)

	s := h.state(id)
	ended := s.Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByStopped, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnInterrupted}, ended.Turns)
	assert.False(t, ended.Unseen, "this instance saw the round")
	assert.Equal(t, item.StopKilled, s.Run.Stopped.Reason)
	assert.Equal(t, 1, countOf(h, id, item.KindRoundEnded))
}

// A kill while launches are pending posts no turn_unstarted notice (Decision
// 3): each turn's Start left its launch pending; the on stops every turn,
// which cancels the launches, and the runner reports each launch cancelled.
// No board holds a turn_unstarted notice, and no launch is tried again.
func TestAKillWhileLaunchesArePendingPostsNoTurnUnstartedNotice(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	a, b := h.open("checkout fails"), h.open("payments fail")
	launching := slices.Clone(h.turns.started)
	require.Len(t, launching, 4)

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	for i := range launching {
		h.launch(&launching[i], app.ReasonCancelled, errors.New("the stop cancelled the launch"))
	}
	h.clock.Advance(h.opts.Retry)

	for _, id := range []item.ID{a, b} {
		assert.Empty(t, unstartedNotices(h, h.state(id).Run.ID), id)
	}
	assert.Len(t, h.turns.started, 4, "no launch is tried again")
}

// A kill whose halt fails starts no turn (Decision 3; the plan's Review Focus
// 1): a turn's launch is pending, and the records store refuses the halt's
// append. The on drops every run before its halt, so the runner's report that
// the stop cancelled the launch finds no run: it arms no timer, and, the clock
// past the retry wait, the turn is not started again; the item's retry tries
// the halt again. Without the drop the cancelled launch would arm its retry,
// whatever begin's guard does.
func TestAKillWhoseHaltFailsStartsNoTurn(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	id := h.open("checkout fails")
	turn := h.last(release.Name)
	h.records.refuses(func(_ item.ID, drafts []item.Draft) error {
		if haltOf(drafts) {
			return errRefused
		}
		return nil
	})

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	require.Nil(t, h.state(id).Run.Stopped, "its halt failed")
	assert.Equal(t, []item.ID{id}, out.Owed)
	armed := h.clock.armed()

	h.launch(turn, app.ReasonCancelled, errors.New("the stop cancelled the launch"))

	assert.Equal(t, armed, h.clock.armed(), "the report arms no timer")
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, 1, h.startsOf(turn.ID), "the turn is not started again")
	assert.Len(t, h.turns.started, 2)
	assert.Nil(t, h.state(id).Run.Stopped, "the halt is tried again, and refused again")
}

// The on drops every run before it advances any item (Decision 5, step 3),
// so that no turn starts again of a run whose step cannot halt it yet: here
// every load of the item fails, so its advance never reaches its halt. The
// runner's report that the stop cancelled a launch finds no run, and arms no
// timer; once the item loads again, its retry halts the run, and the turn is
// never started again.
func TestTheOnDropsEveryRunBeforeItAdvancesAnyItem(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	id := h.open("checkout fails")
	turn := h.last(release.Name)
	h.records.loads(func([]item.Record) error { return errBlinked })

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	assert.Equal(t, []item.ID{id}, out.Owed, "its halt is owed")
	armed := h.clock.armed()

	h.launch(turn, app.ReasonCancelled, errors.New("the stop cancelled the launch"))

	assert.Equal(t, armed, h.clock.armed(), "the report arms no timer")
	h.records.loads(nil)
	h.clock.Advance(h.opts.Retry)
	requireKilled(t, h, id)
	assert.Equal(t, 1, h.startsOf(turn.ID), "the turn is not started again")
}

// A kill whose request ends once its row committed halts every run all the
// same (Decision 5; the plan's Review Focus 1): the row, and every halt and
// record it causes, are written under the coordinator's own context, which
// only its holding ends. Here the request's context ends as soon as the row
// is in: both runs stop killed, and their items await their close, each
// write under the holding's epoch.
func TestAKillWhoseRequestEndsAfterItsRowStillHaltsEveryRun(t *testing.T) {
	h := newHarness(t)
	a, b := h.open("checkout fails"), h.open("payments fail")
	request, cancel := context.WithCancel(t.Context())
	defer cancel()
	h.controls.onCommit(cancel)
	written := len(h.records.writes.noted())

	out, err := h.c.SetKillSwitch(request, true, killing(0))

	require.NoError(t, err)
	require.Error(t, request.Err(), "the request ended once its row committed")
	assert.True(t, out.Changed)
	assert.Equal(t, []int64{h.epoch}, h.controls.writes.noted(), "the row is written under the holding's epoch")
	assert.Len(t, out.Stopped, 2)
	for _, id := range []item.ID{a, b} {
		requireKilled(t, h, id)
	}
	writes := h.records.writes.noted()[written:]
	require.NotEmpty(t, writes)
	for _, epoch := range writes {
		assert.Equal(t, h.epoch, epoch, "written under the coordinator's context")
	}
}

// A halt drops the run's state, so that a launch it cancelled arms nothing
// (Decision 3): the harness's coordinator holds a run in round 1 whose launch
// is pending, and the view is set off with its latest on-row 1, above the
// row 0 the run's creation knew, as no public path leaves it, since every
// raise of the latest on-row drops every run. The item's advance halts the
// run, and its finish fails at the board, whose store refuses the stop entry,
// so that the item does not await its close. The runner's report that the
// launch was cancelled finds no run: no retry is armed, and, the clock past
// the retry wait, the turn is not started again. Without the drop the
// cancelled launch would arm its retry, whatever begin's guard does.
func TestAHaltDropsTheRunsStateSoACancelledLaunchArmsNothing(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	turn := h.last(release.Name)
	h.store.refuses(func(r board.RunID, drafts []board.Draft) error {
		if r == run && stopEntryOf(drafts) {
			return errRefused
		}
		return nil
	})
	h.c.SetKillSwitchViewForTest(app.KillSwitch{Seq: 2, OnSeq: 1, OffAt: h.clock.Now()})

	h.c.AdvanceForTest(t.Context(), id)

	s := h.state(id)
	require.NotNil(t, s.Run.Stopped, "its step halted the run")
	assert.Equal(t, item.StopKilled, s.Run.Stopped.Reason)
	assert.Equal(t, item.KindRunStopped, lastKind(h, id), "its finish failed at the board")
	armed := h.clock.armed()
	h.launch(turn, app.ReasonCancelled, errors.New("the stop cancelled the launch"))
	assert.Equal(t, armed, h.clock.armed(), "no retry is armed")
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, 1, h.startsOf(turn.ID), "the turn is not started again")
}

// A kill_switch notice posted after the board's stop entry counts as done
// (Decision 3, carry 5): a board that holds its stop entry takes no notice,
// and a finish that posted one would be tried again for ever.
//   - A killed run's finish fails once after its stop entry, its
//     item_awaiting_close append failing: on its retry, the notice is the
//     board's already, and the item awaits its close, with no failure of the
//     notice logged.
//   - A killed run's board holds its stop entry and no notice, as a finish
//     that posted none would leave it: here the finish's notice failed once,
//     and the stop entry was posted meanwhile. On the finish's retry the
//     notice's post meets board.ErrStopped, which counts as done: the item
//     awaits its close, the board holds no notice, and no failure of the
//     notice is logged.
func TestAKillSwitchNoticeAfterTheStopEntryCountsAsDone(t *testing.T) {
	t.Run("its finish failed after its stop entry", func(t *testing.T) {
		steps := &stepErrors{}
		h := newHarness(t, func(o *app.Options) { o.Log = slog.New(steps) })
		id := h.open("checkout fails")
		run := h.state(id).Run.ID
		refused := false
		h.records.refuses(func(_ item.ID, drafts []item.Draft) error {
			if !refused && awaitingOf(drafts) {
				refused = true
				return errRefused
			}
			return nil
		})
		_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
		require.NoError(t, err)
		require.Equal(t, item.KindRunStopped, lastKind(h, id), "its item_awaiting_close append failed")
		require.Len(t, stopEntries(h, run), 1, "after its stop entry")
		failed := len(steps.kept())

		h.clock.Advance(h.opts.Retry)

		requireKilled(t, h, id)
		assert.Len(t, steps.kept(), failed, "no failure of the notice is logged")
	})
	t.Run("its stop entry before its notice", func(t *testing.T) {
		steps := &stepErrors{}
		h := newHarness(t, func(o *app.Options) { o.Log = slog.New(steps) })
		id := h.open("checkout fails")
		run := h.state(id).Run.ID
		refused := false
		h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
			if !refused && killNoticeOf(drafts) {
				refused = true
				return errRefused
			}
			return nil
		})
		_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
		require.NoError(t, err)
		require.Equal(t, item.KindRunStopped, lastKind(h, id), "its finish failed at its notice")
		st := h.state(id).Run.Stopped
		_, err = h.boards.Stop(holding.WithEpoch(t.Context(), h.epoch), run, &board.StopInput{
			Reason: string(st.Reason), Conclusion: st.Conclusion, Rounds: st.Rounds, WallClock: st.WallClock, Tokens: st.Spent,
		})
		require.NoError(t, err)
		failed := len(steps.kept())

		h.clock.Advance(h.opts.Retry)

		assert.Equal(t, killedClosing, h.state(id).Closing, "its notice's ErrStopped counts as done")
		assert.Empty(t, noticesOf(h, run, board.NoticeKillSwitch), "a stopped board takes no notice")
		assert.Len(t, stopEntries(h, run), 1)
		assert.Len(t, steps.kept(), failed, "no failure of the notice is logged")
	})
}

// The answers name only what the kill switch's rule still acts on (Decision
// 5): of a running item whose halt the records store refuses, a killed item
// whose finish cannot post its stop entry, a queued item whose not-staffed
// record the records store refuses, an item awaiting its close not staffed at
// the daily cap, and a closed one, the on's Owed names the first three alone.
func TestTheOwedListNamesOnlyItemsTheRuleActsOn(t *testing.T) {
	h := newHarness(t, limit(2))
	notStaffedAtTheCap(h)
	closed := h.open("orders fail")
	_, err := h.c.Close(t.Context(), closed, closing("close-1"))
	require.NoError(t, err)
	running, killed := h.open("checkout fails"), h.open("payments fail")
	queuedItem := openLater(h, "search fails")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(queuedItem), "it waits queued")
	killedRun := h.state(killed).Run.ID
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if id == running && haltOf(drafts) || id == queuedItem && awaitingOf(drafts) {
			return errRefused
		}
		return nil
	})
	h.store.refuses(func(run board.RunID, drafts []board.Draft) error {
		if run == killedRun && stopEntryOf(drafts) {
			return errRefused
		}
		return nil
	})

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.NoError(t, err)
	assert.Equal(t, sortedIDs(running, killed, queuedItem), out.Owed)
	assert.Equal(t, item.StopKilled, stopReason(h, killed), "its halt committed")
	assert.Equal(t, item.KindRunStopped, lastKind(h, killed), "its finish did not end")
}

// The on's answer names the runs it stopped, and those that still owe the
// kill (Decision 5): of two runs, the records store refuses one's halt; the
// answer's Stopped names the other, and its Owed the first's item.
func TestTheOnsAnswerNamesTheRunsThatStillOweTheKill(t *testing.T) {
	h := newHarness(t)
	a, b := h.open("checkout fails"), h.open("payments fail")
	runB := h.state(b).Run.ID
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if id == a && haltOf(drafts) {
			return errRefused
		}
		return nil
	})

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.NoError(t, err)
	assert.Equal(t, []app.RunStop{{Item: b, Run: runB, Round: 1, Reason: item.StopKilled}}, out.Stopped)
	assert.Equal(t, []item.ID{a}, out.Owed)
}

// The kill switch's preview reads the switch from its table, not from the
// view (Decision 13): another instance wrote rows 1, on, and 2, off, which
// this instance's view lacks. The preview answers the table's state, at row
// 2, and the on's consequences: the run that would stop, at round 1, and the
// queued item that would not be staffed. It records nothing, posts nothing,
// stops no turn, writes no row and leaves the view as it was. An on naming
// row 2 as after then lands, as row 3, and stops what the preview said.
func TestAKillSwitchPreviewReadsTheTable(t *testing.T) {
	h := newHarness(t, limit(1))
	a := h.open("checkout fails")
	q := openLater(h, "payments fail")
	runA := h.state(a).Run.ID
	writeRows(t, h, true, false)
	before, stopAlls := h.carried(), h.turns.all
	stops := []app.RunStop{{Item: a, Run: runA, Round: 1, Reason: item.StopKilled}}

	out, err := h.c.SetKillSwitch(t.Context(), true, previewing())

	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{Seq: 2, Preview: true, Consequences: app.Consequences{Stops: stops, Unstaffed: []item.ID{q}}}, out)
	assert.Equal(t, before, h.carried(), "it records and posts nothing")
	assert.Equal(t, stopAlls, h.turns.all, "it stops no turn")
	assert.Equal(t, int64(2), h.controls.rows(t), "it writes no row")
	assert.Equal(t, app.KillSwitch{}, h.c.Lifecycle().KillSwitch, "it leaves the view as it was")

	out, err = h.c.SetKillSwitch(t.Context(), true, killing(2))

	require.NoError(t, err)
	assert.True(t, out.On)
	assert.Equal(t, int64(3), out.Seq)
	assert.True(t, out.Changed)
	assert.Equal(t, stops, out.Stopped)
	assert.Equal(t, []item.ID{q}, out.Unstaffed)
}

// The kill switch works with no current revision (Decision 5): its reason's
// text is checked against the configuration's redaction rules, so with none to
// read, an on whose reason has text is refused, ErrInvalid, and writes no row;
// an on whose reason is a code alone halts every run.
func TestTheKillSwitchWorksWithoutACurrentRevision(t *testing.T) {
	h := newHarness(t)
	a, b := h.open("checkout fails"), h.open("payments fail")
	h.revs.set(nil)

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.ErrorIs(t, err, app.ErrInvalid)
	assert.NotContains(t, err.Error(), incident.Text, "the error never quotes the reason")
	assert.Zero(t, h.controls.rows(t), "no row is written")

	out, err := h.c.SetKillSwitch(t.Context(), true, &app.Act{Actor: administrator, Reason: &board.Reason{Code: "incident"}})

	require.NoError(t, err)
	assert.Len(t, out.Stopped, 2)
	for _, id := range []item.ID{a, b} {
		requireKilled(t, h, id)
	}
}

// A reason is required where the spec requires one (spec Sections 4.2 and
// 8.3; Decision 9): an on without one is refused, reason_required, and writes
// no row; a close, a reopen and a promotion without one act.
func TestAReasonIsRequiredWhereTheSpecRequiresOne(t *testing.T) {
	h := newHarness(t, limit(1))

	_, err := h.c.SetKillSwitch(t.Context(), true, &app.Act{Actor: administrator})

	assert.Equal(t, app.CodeReasonRequired, refusal(err))
	assert.Zero(t, h.controls.rows(t), "no row is written")
	id := h.open("checkout fails")
	closed, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	assert.True(t, closed.Changed, "a close")
	reopened, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)
	assert.True(t, reopened.Changed, "a reopen")
	capped := notStaffedAtTheCap(h)
	promoted, err := h.c.Promote(t.Context(), capped, promoting("promote-1"))
	require.NoError(t, err)
	assert.True(t, promoted.Changed, "a promotion")
}

// While the kill switch is on, a promotion is refused (Decision 4, R23's
// order): an item opened while it is on awaits its close not staffed, with no
// run; its promotion answers kill_switch_on and records nothing. Once the
// switch is off, at row 2, the promotion records item_promoted, which knew
// row 2, and the item starts: its start and its run's creation knew row 2.
// (The plan's.)
func TestAPromotionIsRefusedWhileTheKillSwitchIsOn(t *testing.T) {
	h := newHarness(t)
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	id := h.open("checkout fails")
	require.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
	before := h.carried()

	_, err = h.c.Promote(t.Context(), id, promoting("promote-1"))

	assert.Equal(t, app.CodeKillSwitchOn, refusal(err))
	assert.Equal(t, before, h.carried(), "it records nothing")
	off, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))
	require.NoError(t, err)
	require.Equal(t, int64(2), off.Seq)

	_, err = h.c.Promote(t.Context(), id, promoting("promote-1"))

	require.NoError(t, err)
	s := h.state(id)
	require.NotNil(t, s.Promotion)
	assert.Equal(t, int64(2), s.Promotion.KillSwitchSeen)
	require.NotNil(t, s.Started, "it starts")
	assert.Equal(t, int64(2), s.Started.KillSwitchSeen)
	require.NotNil(t, s.Run)
	assert.Equal(t, int64(2), s.Run.Created.KillSwitchSeen)
	assert.Len(t, h.turns.started, 2, "its round's turns start")
}

// A killed item closed by a person before its finish posted its notice gets
// its notice from the closed step (Decision 6): the finish's notice is
// refused, so the run's board holds neither the notice nor its stop entry;
// the close's closed step posts the kill_switch notice, then the stop entry,
// killed.
func TestClosingAKilledItemPostsItsKillSwitchNotice(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
		if killNoticeOf(drafts) {
			return errRefused
		}
		return nil
	})
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	require.Equal(t, item.KindRunStopped, lastKind(h, id), "its finish fails at its notice")
	h.store.refuses(nil)

	_, err = h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
	events := h.events(run)
	require.GreaterOrEqual(t, len(events), 2)
	notice, stop := events[len(events)-2].Body.Notice, events[len(events)-1].Body.Stop
	require.NotNil(t, notice)
	assert.Equal(t, board.NoticeKillSwitch, notice.Code)
	require.NotNil(t, stop)
	assert.Equal(t, "killed", stop.Reason)
}

// A coordinator needs the platform-wide controls, as it needs every port: the
// kill switch's table (Contract C16).
func TestACoordinatorNeedsTheControls(t *testing.T) {
	h := newHarness(t)
	ports := h.ports()
	ports.Controls = nil

	_, err := app.NewCoordinator(ports, &h.opts)

	require.Error(t, err)
}

// Only an admin turns the kill switch off (spec Section 8.3; Decision 5),
// though anyone who may act turns it on: a responder who is no admin turns it
// on, as row 1. The responder's off, and its preview, are each refused,
// admin_only, before anything is written or previewed: no row is written, the
// preview gives no consequences, and the switch stays on. An admin's off,
// naming row 1, writes row 2 and turns it off.
func TestOnlyAnAdminTurnsTheKillSwitchOff(t *testing.T) {
	h := newHarness(t)
	responder := app.Actor{Subject: "alice"}
	reason := incident
	_, err := h.c.SetKillSwitch(t.Context(), true, &app.Act{Actor: responder, Reason: &reason})
	require.NoError(t, err, "an on needs no admin")
	require.Equal(t, int64(1), h.controls.rows(t))

	for _, c := range []struct {
		name string
		act  *app.Act
	}{
		{"an off", &app.Act{Actor: responder, After: 1}},
		{"its preview", &app.Act{Actor: responder, After: 1, Preview: true}},
	} {
		out, err := h.c.SetKillSwitch(t.Context(), false, c.act)

		assert.Equal(t, app.CodeAdminOnly, refusal(err), c.name)
		assert.Equal(t, app.KillSwitchOutcome{}, out, "%s: no answer, and no consequences", c.name)
		assert.Equal(t, int64(1), h.controls.rows(t), "%s writes no row", c.name)
	}
	assert.True(t, h.c.Lifecycle().KillSwitch.On, "the switch stays on")

	out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))

	require.NoError(t, err)
	assert.False(t, out.On)
	assert.Equal(t, int64(2), out.Seq)
	assert.True(t, out.Changed)
	assert.Equal(t, int64(2), h.controls.rows(t), "an admin's off writes row 2")
}

// Setting the kill switch to the state it holds changes nothing (Decision 5,
// its idempotency): an off while off, and an on while on, answer changed
// false and append no row. Such an answer still brings the coordinator's view
// up to the table: here another instance writes row 2, off, then row 3, on,
// which this instance's view lacks; an off naming row 1, then an on naming row
// 2, each answer changed false, the view following the table each time, and
// each logged with the cause answer (Contract C3).
func TestSettingTheKillSwitchToItsStateChangesNothing(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))

	out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(0))
	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{}, out, "an off while off")
	assert.Zero(t, h.controls.rows(t), "it appends no row")
	_, err = h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	out, err = h.c.SetKillSwitch(t.Context(), true, killing(1))
	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{On: true, Seq: 1}, out, "an on while on")
	assert.Equal(t, int64(1), h.controls.rows(t), "it appends no row")

	writeRows(t, h, false)
	out, err = h.c.SetKillSwitch(t.Context(), false, turningOff(1))
	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{Seq: 2}, out, "an off while off, the view on")
	assert.Equal(t, int64(2), h.controls.rows(t), "it appends no row")
	assert.Equal(t, app.KillSwitch{Seq: 2, OnSeq: 1, OffAt: tableView(t, h).OffAt}, h.c.Lifecycle().KillSwitch, "the view follows the table")

	writeRows(t, h, true)
	out, err = h.c.SetKillSwitch(t.Context(), true, killing(2))
	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{On: true, Seq: 3}, out, "an on while on, the view off")
	assert.Equal(t, int64(3), h.controls.rows(t), "it appends no row")
	assert.Equal(t, tableView(t, h), h.c.Lifecycle().KillSwitch, "the view follows the table")
	assert.True(t, h.c.Lifecycle().KillSwitch.On)

	var causes []any
	for _, line := range logged(t, logs.String(), "kill switch set") {
		causes = append(causes, line["cause"])
	}
	assert.Equal(t, []any{"act", "answer", "answer"}, causes, "the on's own row, then the two answers that moved the view")
}

// A kill switch held past the auto-close delay lists, once off, every item it
// left unstaffed, none of them closed by itself (Decisions 5 and 8; R4; the
// plan's Review Focus 4): with a limit of 1, the on stops one item's run and
// leaves a queued item not staffed, and an item opened while it is on is not
// staffed either, each left so by the on-row 1. The clock passes the
// 30-minute delay of each, and none closes: auto-close pauses while the switch
// is on. A person closes the queued item. The off answers StayedUnstaffed: the
// three, each with its state now, two awaiting their close and the queued one
// closed, by a person; and "kill switch set" logs them (Contract C3).
func TestTheKillSwitchHeldPastTheAutoCloseDelayListsEveryItemItLeftUnstaffed(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, limit(1), logTo(logs))
	killed := h.open("checkout fails")
	queuedItem := openLater(h, "payments fail")
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	opened := h.open("orders fail")
	unstaffed := []item.ID{killed, queuedItem, opened}
	for _, id := range unstaffed {
		require.Equal(t, int64(1), h.state(id).Closing.UnstaffedBy, "%s: left unstaffed by the on-row 1", id)
	}

	h.clock.Advance(closesAfter + time.Hour)

	for _, id := range unstaffed {
		assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status(), "%s does not close by itself while the switch is on", id)
	}
	_, err = h.c.Close(t.Context(), queuedItem, closing("close-1"))
	require.NoError(t, err)

	out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))

	require.NoError(t, err)
	assert.Equal(t, byUnstaffed(
		app.UnstaffedItem{Item: killed, State: item.StatusAwaitingClose},
		app.UnstaffedItem{Item: queuedItem, State: item.StatusClosed},
		app.UnstaffedItem{Item: opened, State: item.StatusAwaitingClose},
	), out.StayedUnstaffed)
	assert.Empty(t, out.Owed)
	closed, _ := lastClose(h, queuedItem)
	assert.Equal(t, "admin", closed.By, "closed by a person, not by itself")
	lines := logged(t, logs.String(), "kill switch set")
	require.Len(t, lines, 2, "the on, then the off")
	var ids []any
	for _, id := range sortedIDs(unstaffed...) {
		ids = append(ids, string(id))
	}
	assert.Equal(t, map[string]any{"level": "WARN", "on": false, "seq": 2.0, "cause": "act", "stayedUnstaffed": ids, "stayedUnstaffedUnread": false, "owed": nil},
		pick(lines[1], "level", "on", "seq", "cause", "stayedUnstaffed", "stayedUnstaffedUnread", "owed"))
}

// unstaffedUnread is what the coordinator logs when it cannot read the items
// the kill switch left unstaffed.
const unstaffedUnread = "the items the kill switch left unstaffed could not be read"

// An off that cannot read the items the latest on left unstaffed says so
// (Decision 5; the plan's Review Focus 4): its answer, and its log line, say
// that the list could not be read in full, so that it never reads as "none",
// and the off, whose row committed, still answers success and does all else.
//   - The records store fails the read of the list: the off writes row 2,
//     turns the view off, and arms again the auto-close of the item the on
//     left unstaffed, counting from the off; it answers success, with no item
//     listed and the list unread; the failure is logged at Error, and "kill
//     switch set" logs the list unread.
//   - The records store fails the load of one of two items the on left
//     unstaffed: that item is listed with its id and no state, the other with
//     its state, and the list is unread.
//
// (The plan's.)
func TestAnOffThatCannotReadTheItemsItLeftUnstaffedSaysSo(t *testing.T) {
	t.Run("the list cannot be read", func(t *testing.T) {
		logs := &syncBuffer{}
		h := newHarness(t, logTo(logs))
		id := h.open("checkout fails")
		_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
		require.NoError(t, err)
		require.Equal(t, killedClosing, h.state(id).Closing, "the on left it unstaffed")
		h.clock.Advance(time.Minute)
		h.records.unstaffedFails(func(int64) error { return errBlinked })

		out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))

		require.NoError(t, err, "the off committed: it answers success")
		assert.Equal(t, app.KillSwitchOutcome{Seq: 2, Changed: true, StayedUnstaffedUnread: true}, out)
		assert.Equal(t, int64(2), h.controls.rows(t), "its row is written")
		assert.False(t, h.c.Lifecycle().KillSwitch.On, "the view turns off")
		assert.Equal(t, []time.Time{tableView(t, h).OffAt.Add(closesAfter)}, h.clock.armed(), "the item's auto-close, armed again, counting from the off")
		unread := logged(t, logs.String(), unstaffedUnread)
		require.Len(t, unread, 1)
		assert.Equal(t, "ERROR", unread[0]["level"])
		set := logged(t, logs.String(), "kill switch set")
		require.Len(t, set, 2, "the on, then the off")
		assert.Equal(t, map[string]any{"stayedUnstaffed": []any{}, "stayedUnstaffedUnread": true}, pick(set[1], "stayedUnstaffed", "stayedUnstaffedUnread"))
	})
	t.Run("an item cannot be read", func(t *testing.T) {
		h := newHarness(t)
		a, b := h.open("checkout fails"), h.open("payments fail")
		_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
		require.NoError(t, err)
		h.clock.Advance(time.Minute)
		h.records.loads(func(records []item.Record) error {
			if len(records) > 0 && records[0].Item == b {
				return errBlinked
			}
			return nil
		})

		out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))

		h.records.loads(nil)
		require.NoError(t, err)
		assert.True(t, out.StayedUnstaffedUnread, "the list is not read in full")
		assert.Equal(t, byUnstaffed(
			app.UnstaffedItem{Item: a, State: item.StatusAwaitingClose},
			app.UnstaffedItem{Item: b},
		), out.StayedUnstaffed, "the item that cannot be read is listed with its id and no state")
	})
}

// No item closes by itself while the kill switch is on (Decision 8, the
// pause): an item awaiting its close has its auto-close armed; the on cancels
// it, and arms none, for that item nor for the item whose run it stopped,
// which awaits its close not staffed from then on. Past both delays, neither
// records item_closed.
func TestNoItemClosesByItselfWhileTheKillSwitchIsOn(t *testing.T) {
	h := newHarness(t)
	awaiting := awaitingItsClose(h, "checkout fails")
	killed := h.open("payments fail")
	require.Contains(t, h.clock.armed(), h.state(awaiting).AwaitingSince.Add(closesAfter), "its auto-close is armed")

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)

	assert.Empty(t, h.clock.armed(), "the on cancels its timer, and arms none")
	require.Equal(t, killedClosing, h.state(killed).Closing, "the on stopped the other item's run")
	advanceTo(h, h.state(killed).AwaitingSince.Add(closesAfter+time.Hour))
	for _, id := range []item.ID{awaiting, killed} {
		assert.Zero(t, countOf(h, id, item.KindClosed), "%s: no item_closed", id)
		assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status(), id)
	}
}

// An auto-close timer that fires while the kill switch is on records nothing
// (Decision 8): it records item_closed only while the switch is off. No public
// path leaves a timer armed while the switch is on, since the on cancels every
// timer and none is armed until the off, so the test sets the view on itself,
// the item's timer armed. Past its deadline the timer fires: nothing is
// recorded, and no timer is armed again, as the off arms it. (The plan's.)
func TestAnAutoCloseTimerThatFiresWhileTheKillSwitchIsOnRecordsNothing(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	due := h.state(id).AwaitingSince.Add(closesAfter)
	require.Equal(t, []time.Time{due}, h.clock.armed())
	kinds := h.kinds(id)
	h.c.SetKillSwitchViewForTest(app.KillSwitch{On: true, Seq: 1, OnSeq: 1})

	advanceTo(h, due.Add(time.Hour))

	assert.Equal(t, kinds, h.kinds(id), "no item_closed")
	assert.Empty(t, h.clock.armed(), "no timer is armed again")
}

// Auto-close counts from the kill switch's latest turning off (Decision 8;
// R19): an item awaiting its close since T; the switch turned on at T plus 10
// minutes, and off at T plus 40. The off arms the item's timer at T plus 70
// minutes: the off, the later of the off and the item's entry into
// awaiting_close, plus its delay. The item closes then, due then, not before.
func TestAutoCloseCountsFromTheKillSwitchsTurningOff(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	since := h.state(id).AwaitingSince
	advanceTo(h, since.Add(10*time.Minute))
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	advanceTo(h, since.Add(40*time.Minute))

	_, err = h.c.SetKillSwitch(t.Context(), false, turningOff(1))

	require.NoError(t, err)
	due := since.Add(70 * time.Minute)
	assert.Equal(t, []time.Time{due}, h.clock.armed(), "the off arms its timer, counting from the off")
	advanceTo(h, due.Add(-time.Second))
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status(), "not before")
	h.clock.Advance(time.Second)
	closed, recorded := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed, "due at T plus 70 minutes")
	assert.Equal(t, due, recorded)
}

// The kill switch turns off at once though an item's board does not replay
// (Decision 5: turning it off never waits; the plan's Review Focus 1): the on
// halts the item's run, killed, but the run's finish cannot post its
// kill_switch notice on a board whose events do not replay, so the item does
// not await its close. The off answers at once, its row written, and its Owed
// names the item, whose finish is still to end.
func TestTheKillSwitchTurnsOffThoughAnItemsBoardDoesNotReplay(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	breakBoard(t, h, h.state(id).Run.ID)
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	require.Equal(t, item.StopKilled, stopReason(h, id), "the on halted its run")
	require.Equal(t, item.KindRunStopped, lastKind(h, id), "its finish cannot end")

	out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))

	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{Seq: 2, Changed: true, Owed: []item.ID{id}}, out)
	assert.Equal(t, int64(2), h.controls.rows(t), "its row is written")
	assert.False(t, h.c.Lifecycle().KillSwitch.On)
}

// A run that owes the kill is halted after the kill switch is off (Decision
// 5: row numbers, not the switch's state, decide what is owed; the plan's
// Review Focus 3): the on's halt fails at its append, and the run goes on
// owing row 1; the switch is turned off, and its answer names the item owed.
// Once the store takes the halt, the item's retry halts the run killed, by the
// on-row 1, its round 1 ended stopped: no round opens, and no turn starts.
func TestARunThatOwesAKillIsHaltedAfterTheKillSwitchIsOff(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.records.refuses(func(_ item.ID, drafts []item.Draft) error {
		if haltOf(drafts) {
			return errRefused
		}
		return nil
	})
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	require.Nil(t, h.state(id).Run.Stopped, "its halt failed")

	off, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))
	require.NoError(t, err)
	assert.Equal(t, []item.ID{id}, off.Owed, "the off names it owed")
	h.records.refuses(nil)
	h.clock.Advance(h.opts.Retry)

	requireKilled(t, h, id)
	assert.Equal(t, 1, countOf(h, id, item.KindRoundOpened), "no round opens")
	assert.Len(t, h.turns.started, 2, "no turn starts")
}

// An item with no run that owes the kill is not staffed after the kill switch
// is off (Decision 5; Decision 12, rule 4): with a limit of 1, the on stops
// one item's run, and the not-staffed record of a queued item fails at its
// append, so the queued item goes on owing row 1; the switch is turned off,
// and its answer names the item owed. Once the store takes the record, the
// item's retry records item_awaiting_close not_staffed, for the cause
// kill_switch, left so by the on-row 1: the item never starts.
func TestAnItemWithNoRunThatOwesAKillIsNotStaffedAfterTheOff(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	queuedItem := openLater(h, "payments fail")
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if id == queuedItem && awaitingOf(drafts) {
			return errRefused
		}
		return nil
	})
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(queuedItem), "its not-staffed record failed")

	off, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))
	require.NoError(t, err)
	assert.Equal(t, []item.ID{queuedItem}, off.Owed, "the off names it owed")
	h.records.refuses(nil)
	h.clock.Advance(h.opts.Retry)

	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(queuedItem), "no item_started")
	assert.Equal(t, &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseKillSwitch, UnstaffedBy: 1, AutoClose: closesAfter},
		h.state(queuedItem).Closing)
}

// An off whose answer reveals an on the view never applied runs the on's
// steps, then the off's (Decision 5, the two rules): another instance writes
// row 1, on, and row 2, off, which this instance's view lacks, while an
// item's run, created before row 1, is in round 1. A minute later this
// instance's off, naming row 0, answers changed false, the switch off at row
// 2; the view's latest on-row rises to 1, so the run is halted killed, by the
// on-row 1, and its item awaits its close not staffed. Then the off's steps
// run, though the view was never on: the answer lists the item among those
// that stayed unstaffed, and its auto-close is armed, counting from its entry
// into awaiting_close, which came after the off. (The plan's.)
func TestAnOffThatRevealsAnOnTheViewLackedRunsTheOnsStepsThenTheOffs(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	writeRows(t, h, true, false)
	h.clock.Advance(time.Minute)

	out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(0))

	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{
		Seq: 2, Stopped: []app.RunStop{{Item: id, Run: run, Round: 1, Reason: item.StopKilled}},
		StayedUnstaffed: []app.UnstaffedItem{{Item: id, State: item.StatusAwaitingClose}},
	}, out)
	requireKilled(t, h, id)
	assert.Equal(t, []time.Time{h.state(id).AwaitingSince.Add(closesAfter)}, h.clock.armed(), "its auto-close is armed")
}

// The off lists an item the latest on left unstaffed, though a person closed
// and reopened it since (Decision 5; R4; the plan's): the item, opened while
// the switch is on, records item_awaiting_close not_staffed, left so by the
// on-row 1; a person closes it, then reopens it, which returns it to an
// awaiting_close of the reopen's own, naming no on-row. The reopen answers
// its auto-close paused, with no time, and arms no timer (Decision 8). A
// minute later, the off finds the item by its item_awaiting_close, as
// Records.UnstaffedBy reads the records, not the fold, and lists it, awaiting
// its close; and arms its auto-close, counting from the off.
func TestTheOffListsAnItemReopenedSinceTheOnLeftItUnstaffed(t *testing.T) {
	h := newHarness(t)
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	id := h.open("checkout fails")
	require.Equal(t, int64(1), h.state(id).Closing.UnstaffedBy)
	_, err = h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)

	reopened, err := h.c.Reopen(t.Context(), id, reopening("reopen-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{AutoClosePaused: true, Limit: 3}}, reopened, "its auto-close paused, with no time")
	assert.Empty(t, h.clock.armed(), "no timer is armed while the switch is on")
	s := h.state(id)
	require.Equal(t, item.StatusAwaitingClose, s.Status())
	require.Zero(t, s.Closing.UnstaffedBy, "the reopen's awaiting_close names no on-row")
	h.clock.Advance(time.Minute)

	out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))

	require.NoError(t, err)
	assert.Equal(t, []app.UnstaffedItem{{Item: id, State: item.StatusAwaitingClose}}, out.StayedUnstaffed, "found by its item_awaiting_close")
	assert.Equal(t, []time.Time{tableView(t, h).OffAt.Add(closesAfter)}, h.clock.armed(), "its auto-close, counting from the off")
}

package app_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// requireUnavailable fails unless err is an error the admin API answers 503,
// unavailable (Contract C4): one that none of its other answers maps, as an
// act's lost answer that it could not check yet.
func requireUnavailable(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	for _, mapped := range []error{app.ErrNotHeld, app.ErrLeaseLost, app.ErrStale, app.ErrInvalid, app.ErrNoItem, app.ErrKeyReused, app.ErrSkipped} {
		require.NotErrorIs(t, err, mapped)
	}
	require.Empty(t, refusal(err), "no refusal")
}

// unknownOn turns the kill switch on, after row 0, with the outcome of its
// write unknown (Decision 5, step 1; R37): its row lands, as row 1, and its
// answer is lost; or, with lands false, the write fails before its row. The
// read after it fails, as every read does until the test lets them read: the
// act answers an error the admin API answers 503.
func unknownOn(t *testing.T, h *harness, lands bool) {
	t.Helper()
	h.controls.loseAnswers(lands)
	h.controls.failWrites(!lands)
	h.controls.failReads(true)
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	h.controls.loseAnswers(false)
	h.controls.failWrites(false)
	requireUnavailable(t, err)
}

// issuesNoted and startsNoted are an instance's live sessions and turns,
// which note each session issued or moved, and each turn started, in one
// order with what the test notes beside them (see noteWork).
type issuesNoted struct {
	app.Sessions
	order *trace
}

func (s issuesNoted) Issue(session *app.Session) app.Tokens {
	s.order.note("issue " + string(session.Run) + "/" + session.Agent)
	return s.Sessions.Issue(session)
}

func (s issuesNoted) Move(run board.RunID, agent string, round int, deadline time.Time, budget int64) error {
	s.order.note("move " + string(run) + "/" + agent)
	return s.Sessions.Move(run, agent, round, deadline, budget)
}

type startsNoted struct {
	app.Turns
	order *trace
}

func (s startsNoted) Start(ctx context.Context, t *app.Turn) (bool, error) {
	s.order.note("start " + t.ID)
	return s.Turns.Start(ctx, t)
}

// noteWork has the harness's instance take over again, under the same epoch,
// with its sessions and its turns noting each Issue, Move and Start, and
// returns the order they note, which also notes each kill switch row as it
// commits ("commit"), and each item's halt as its append is made ("halt" and
// the item).
func noteWork(t *testing.T, h *harness) *trace {
	t.Helper()
	order := &trace{}
	ports := h.ports()
	ports.Sessions = issuesNoted{Sessions: ports.Sessions, order: order}
	ports.Turns = startsNoted{Turns: ports.Turns, order: order}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	h.controls.onCommit(func() { order.note("commit") })
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if haltOf(drafts) {
			order.note("halt " + string(id))
		}
		return nil
	})
	return order
}

// endingHolding has the harness's instance take over again, under the same
// epoch, with a holding whose context end ends, as a drain's first moment
// ends it (Decision 12), and returns end.
func endingHolding(t *testing.T, h *harness) (end func()) {
	t.Helper()
	held, end := context.WithCancel(t.Context())
	t.Cleanup(end)
	c, err := app.NewCoordinator(h.ports(), &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(held, app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	return end
}

// workAfterCommit returns what order noted after the kill switch's first
// commit that is work begun: a turn started, or a session issued or moved.
func workAfterCommit(t *testing.T, order *trace) []string {
	t.Helper()
	seen := order.seen()
	at := slices.Index(seen, "commit")
	require.GreaterOrEqual(t, at, 0, "a row committed")
	var work []string
	for _, what := range seen[at+1:] {
		if strings.HasPrefix(what, "start ") || strings.HasPrefix(what, "issue ") || strings.HasPrefix(what, "move ") {
			work = append(work, what)
		}
	}
	return work
}

// A kill switch row whose commit answer is lost still stops every run
// (Decision 5, step 1; the plan's Review Focus 2): two items in round 1, and
// the store commits the on as row 1, then answers an error, as when the
// database's answer is lost on its way back. The coordinator reads the switch
// again, under its lock, and finds row 1: the view turns on, and both runs
// are halted killed, by the on-row 1. The act answers on, at row 1, changed,
// since a row after the one its sender saw set the state it asked, and names
// both runs. No turn starts after. Fails first: the error is returned, and
// the runs go on.
func TestAKillSwitchRowWhoseCommitAnswerIsLostStillStopsEveryRun(t *testing.T) {
	h := newHarness(t)
	a, b := h.open("checkout fails"), h.open("payments fail")
	runA, runB := h.state(a).Run.ID, h.state(b).Run.ID
	h.controls.loseAnswers(true)

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	h.controls.loseAnswers(false)
	for _, id := range []item.ID{a, b} {
		assert.Equal(t, item.StopKilled, stopReason(h, id), "%s's run is halted", id)
	}
	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{On: true, Seq: 1, Changed: true, Stopped: byItem(
		app.RunStop{Item: a, Run: runA, Round: 1, Reason: item.StopKilled}, app.RunStop{Item: b, Run: runB, Round: 1, Reason: item.StopKilled},
	)}, out)
	for _, id := range []item.ID{a, b} {
		requireKilled(t, h, id)
	}
	assert.Equal(t, app.Lifecycle{KillSwitch: tableView(t, h), Limit: 3}, h.c.Lifecycle(), "the view turns on, settled")
	started := len(h.turns.started)
	h.clock.Advance(3 * h.opts.Retry)
	assert.Len(t, h.turns.started, started, "no turn starts after")
}

// A kill switch on whose answer was lost, sent again as it was, stops every
// run (Decision 5, step 1; R37): two items in round 1; the on commits as row
// 1, its answer is lost, and the read after it fails, so the act answers an
// error the admin API answers 503, and the view stays off, unsettled. The on
// sent again, naming row 0 as before, finds the state it asks holding: it
// answers changed false, the view follows the store's answer, and both runs
// are halted killed, by the on-row 1. That answer settles the view.
func TestARetriedKillSwitchOnAfterALostAnswerStopsEveryRun(t *testing.T) {
	h := newHarness(t)
	a, b := h.open("checkout fails"), h.open("payments fail")
	runA, runB := h.state(a).Run.ID, h.state(b).Run.ID
	unknownOn(t, h, true)
	h.controls.failReads(false)
	require.Equal(t, int64(1), h.controls.rows(t), "its row committed")
	assert.Equal(t, 1, h.controls.readsMade(), "the coordinator read the switch again")
	assert.True(t, h.c.Lifecycle().KillSwitchUnsettled, "its outcome is unknown")

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{On: true, Seq: 1, Stopped: byItem(
		app.RunStop{Item: a, Run: runA, Round: 1, Reason: item.StopKilled}, app.RunStop{Item: b, Run: runB, Round: 1, Reason: item.StopKilled},
	)}, out)
	for _, id := range []item.ID{a, b} {
		requireKilled(t, h, id)
	}
	assert.False(t, h.c.Lifecycle().KillSwitchUnsettled, "its answer settles the view")
}

// A read after an unknown outcome that fails is tried again after the retry
// wait, each read that fails arming the next (Decision 5, step 1): two items
// in round 1; the on commits as row 1, its answer is lost, and the read after
// it fails, as does that read's first retry, while the runs go on. The second
// retry reads row 1: the view turns on, and both runs are halted killed,
// logged "kill switch set" with the cause reread (Contract C3).
func TestAReadAfterAnUnknownOutcomeThatFailsIsTriedAgain(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	a, b := h.open("checkout fails"), h.open("payments fail")
	unknownOn(t, h, true)
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, 2, h.controls.readsMade(), "the read, then its first retry, failed")
	require.Nil(t, h.state(a).Run.Stopped, "the run goes on while no read succeeds")

	h.controls.failReads(false)
	h.clock.Advance(h.opts.Retry)

	assert.Equal(t, 3, h.controls.readsMade(), "the second retry read the switch")
	for _, id := range []item.ID{a, b} {
		requireKilled(t, h, id)
	}
	assert.True(t, h.c.Lifecycle().KillSwitch.On, "the view turns on")
	lines := logged(t, logs.String(), "kill switch set")
	require.Len(t, lines, 1)
	assert.Equal(t, map[string]any{"on": true, "seq": 1.0, "onSeq": 1.0, "cause": "reread", "stopped": 2.0},
		pick(lines[0], "on", "seq", "onSeq", "cause", "stopped"))
}

// A kill switch row is written though its client leaves, once its request
// reached the coordinator's lock still live (Decision 5, step 1):
//   - a request whose context ends while the store writes: the row lands, as
//     row 1, written under the coordinator's own context, which carries its
//     holding's epoch, and both runs are halted killed;
//   - a request whose context ended before it reached the coordinator's lock
//     writes nothing: no row, no write, and the runs go on.
func TestAKillSwitchRowIsWrittenThoughItsClientLeavesOnceItReachedTheCoordinator(t *testing.T) {
	t.Run("its client leaves while the store writes", func(t *testing.T) {
		h := newHarness(t)
		a, b := h.open("checkout fails"), h.open("payments fail")
		request, cancel := context.WithCancel(t.Context())
		defer cancel()
		h.controls.onCommit(cancel)

		out, err := h.c.SetKillSwitch(request, true, killing(0))

		require.NoError(t, err)
		require.Error(t, request.Err(), "the request ended while the store wrote")
		assert.True(t, out.Changed)
		assert.Equal(t, int64(1), h.controls.rows(t), "the row lands")
		assert.Equal(t, []int64{h.epoch}, h.controls.writes.noted(), "under the coordinator's context, which carries its holding's epoch")
		for _, id := range []item.ID{a, b} {
			requireKilled(t, h, id)
		}
	})
	t.Run("its client left before it reached the lock", func(t *testing.T) {
		h := newHarness(t)
		a, b := h.open("checkout fails"), h.open("payments fail")
		request, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := h.c.SetKillSwitch(request, true, killing(0))

		assert.Zero(t, h.controls.rows(t), "no row is written")
		assert.Empty(t, h.controls.writes.noted(), "nothing is written")
		require.ErrorIs(t, err, context.Canceled)
		for _, id := range []item.ID{a, b} {
			assert.Nil(t, h.state(id).Run.Stopped, "%s's run goes on", id)
		}
		assert.Equal(t, app.KillSwitch{}, h.c.Lifecycle().KillSwitch, "the view stays as it was")
	})
}

// An off whose commit answer is lost still lets items be staffed (Decision 5,
// step 1): the switch on, as row 1; the off commits as row 2, and its answer
// is lost. The coordinator reads the switch again and finds row 2: the view
// turns off, and the act answers off, at row 2, changed. An item opened after
// it starts, and its round's turns start.
func TestAnOffWhoseCommitAnswerIsLostStillLetsItemsBeStaffed(t *testing.T) {
	h := newHarness(t)
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	h.controls.loseAnswers(true)

	out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))

	h.controls.loseAnswers(false)
	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{Seq: 2, Changed: true}, out)
	assert.Equal(t, app.Lifecycle{KillSwitch: tableView(t, h), Limit: 3}, h.c.Lifecycle(), "the view turns off, at row 2, settled")
	id := h.open("checkout fails")
	assert.Equal(t, startedKinds, h.kinds(id), "it starts")
	assert.Len(t, h.turns.started, 2, "its round's turns start")
}

// An off that reveals a lost on halts every run that owes it (Decision 5, the
// two rules; R37): an item in round 1; the on commits as row 1, its answer is
// lost, and the read after it fails, so the act answers an error the admin
// API answers 503, and the view stays off, unsettled. An off naming row 1, as
// someone who read the table sends it, lands as row 2: its answer raises the
// view's latest on-row to 1, the state off. The run, which owes row 1, is
// halted killed, by the on-row 1, then the off's steps run: the answer lists
// the item among those that stayed unstaffed. The answer settles the view:
// "kill switch settled" is logged, then "kill switch set" (Contract C3), and
// the read's retry is cancelled, so that the item's auto-close alone is
// armed. Fails first: the lost answer marks nothing, so the view is not
// unsettled, and the off's answer logs no "kill switch settled". (The halt
// and the off's steps of an off that reveals an on are Tasks 12 and 13's,
// which TestAnOffThatRevealsAnOnTheViewLackedRunsTheOnsStepsThenTheOffs
// pins.)
func TestAnOffThatRevealsALostOnHaltsEveryRunThatOwesIt(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	unknownOn(t, h, true)
	h.controls.failReads(false)
	require.Equal(t, int64(1), h.controls.rows(t), "the on committed as row 1")
	assert.Equal(t, app.Lifecycle{KillSwitchUnsettled: true, Limit: 3, Investigating: 1}, h.c.Lifecycle(), "the view stays off, unsettled")

	out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))

	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{
		Seq: 2, Changed: true, Stopped: []app.RunStop{{Item: id, Run: run, Round: 1, Reason: item.StopKilled}},
		StayedUnstaffed: []app.UnstaffedItem{{Item: id, State: item.StatusAwaitingClose}},
	}, out)
	requireKilled(t, h, id)
	assert.Equal(t, app.Lifecycle{KillSwitch: tableView(t, h), Limit: 3}, h.c.Lifecycle(), "the view follows the answer, settled")
	settled := logged(t, logs.String(), "kill switch settled")
	require.Len(t, settled, 1, "the answer settles the view")
	assert.Equal(t, map[string]any{"level": "INFO", "on": false, "seq": 2.0, "epoch": float64(h.epoch)}, pick(settled[0], "level", "on", "seq", "epoch"))
	set := logged(t, logs.String(), "kill switch set")
	require.Len(t, set, 1)
	assert.Equal(t, map[string]any{"on": false, "seq": 2.0, "onSeq": 1.0, "cause": "act", "stopped": 1.0}, pick(set[0], "on", "seq", "onSeq", "cause", "stopped"))
	assert.Less(t, strings.Index(logs.String(), `"msg":"kill switch settled"`), strings.Index(logs.String(), `"msg":"kill switch set"`), "settled, then set")
	assert.Equal(t, []time.Time{h.state(id).AwaitingSince.Add(closesAfter)}, h.clock.armed(), "its auto-close alone: the read's retry is cancelled")
}

// A kill switch write that the store's guard refuses fences the coordinator
// and answers ErrNotHeld (Decision 5, step 1): the lease is another
// holding's, so the store refuses the on with an error that wraps
// ErrLeaseLost. That is no unknown outcome, as the guard wrote nothing: the
// coordinator is fenced, as a write that found the lease gone fences it,
// every turn stopped, and its leader is told, with the holding's epoch; the
// act answers ErrNotHeld; and no second read is made, nor any retry armed.
func TestAKillSwitchWriteTheGuardRefusesFencesAndAnswersNotHeld(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	var lost []int64
	h.c.OnLost(func(epoch int64) { lost = append(lost, epoch) })
	stops := h.turns.all
	h.controls.lease.Store(h.epoch + 1)

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Equal(t, stops+1, h.turns.all, "the fence stops every turn")
	assert.Equal(t, app.Lifecycle{}, h.c.Lifecycle(), "the coordinator is fenced")
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
	h.clock.Advance(0)
	assert.Equal(t, []int64{h.epoch}, lost, "its leader is told")
	assert.Zero(t, h.controls.readsMade(), "no second read")
	assert.Zero(t, h.controls.rows(t), "no row")
	assert.Zero(t, h.clock.pending(), "nothing is tried again")
}

// A second read after an unknown outcome answers as it finds the switch,
// against the state asked and after, the latest row the sender saw
// (Decision 5, step 1), each time the write's answer lost: its row, if any,
// committed, or, where none lands, the write failed before it. Each asks the
// switch on:
//   - the state asked, by a row after after: changed;
//   - the state asked, with no row after after: unchanged, as it held;
//   - not the state asked, with no row after after: the row did not land, and
//     nothing changed, so the act answers an error the admin API answers 503,
//     and the request may be sent again as it was;
//   - not the state asked, with a row after after: ErrStale, as a newer row
//     stands; here another instance wrote rows 1, on, and 2, off.
//
// Each read settles the view, which follows the table.
func TestASecondReadAfterAnUnknownOutcomeAnswersAsItFinds(t *testing.T) {
	for _, c := range []struct {
		name string
		// before sets the table up before the request, which names after.
		before func(t *testing.T, h *harness)
		after  int64
		// lands has the write run, its answer lost; otherwise it fails before
		// its row.
		lands bool
		// rows is the table's latest row after the request.
		rows int64
		// changed is the success answer's; stale and unavailable say the act
		// answers ErrStale, or an error the admin API answers 503.
		changed, stale, unavailable bool
	}{
		{name: "the state asked, by a row after after", lands: true, rows: 1, changed: true},
		{name: "the state asked, with no row after after", before: func(t *testing.T, h *harness) {
			_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
			require.NoError(t, err)
		}, after: 1, lands: true, rows: 1},
		{name: "not the state asked, with no row after after", rows: 0, unavailable: true},
		{name: "not the state asked, with a row after after", before: func(t *testing.T, h *harness) {
			writeRows(t, h, true, false)
		}, lands: true, rows: 2, stale: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			if c.before != nil {
				c.before(t, h)
			}
			reads := h.controls.readsMade()
			h.controls.loseAnswers(c.lands)
			h.controls.failWrites(!c.lands)

			out, err := h.c.SetKillSwitch(t.Context(), true, killing(c.after))

			h.controls.loseAnswers(false)
			h.controls.failWrites(false)
			assert.Equal(t, reads+1, h.controls.readsMade(), "a second read")
			switch {
			case c.stale:
				require.ErrorIs(t, err, app.ErrStale)
			case c.unavailable:
				requireUnavailable(t, err)
			default:
				require.NoError(t, err)
				assert.Equal(t, c.changed, out.Changed)
				assert.True(t, out.On)
				assert.Equal(t, c.rows, out.Seq)
			}
			assert.Equal(t, c.rows, h.controls.rows(t))
			assert.Equal(t, tableView(t, h), h.c.Lifecycle().KillSwitch, "the view follows the table")
			assert.False(t, h.c.Lifecycle().KillSwitchUnsettled, "the read settles the view")
		})
	}
}

// A stale kill switch off after a newer on is refused (Decision 5, step 1,
// and its idempotency): this instance turns the switch on, as row 1; another
// instance turns it off, as row 2, and on again, as row 3, rows this
// instance's view lacks. A delayed off, whose sender saw row 1, answers
// ErrStale and writes no row: the switch stays on. The store's answer
// carries the switch's state, which the view follows, logged with the cause
// answer (Contract C3).
func TestAStaleKillSwitchOffAfterANewerOnIsRefused(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	writeRows(t, h, false, true)

	_, err = h.c.SetKillSwitch(t.Context(), false, turningOff(1))

	require.ErrorIs(t, err, app.ErrStale)
	assert.Equal(t, int64(3), h.controls.rows(t), "no row is written")
	table := tableView(t, h)
	assert.True(t, table.On, "the switch stays on")
	assert.Equal(t, table, h.c.Lifecycle().KillSwitch, "the view follows the state the answer carries")
	lines := logged(t, logs.String(), "kill switch set")
	require.Len(t, lines, 2, "the on, then the stale answer that moved the view")
	assert.Equal(t, map[string]any{"on": true, "seq": 3.0, "onSeq": 3.0, "cause": "answer"}, pick(lines[1], "on", "seq", "onSeq", "cause"))
}

// No turn starts while a kill switch write's outcome is unknown (the
// controller's ruling, R37): A's turns cannot start, their retries armed; B's
// round 1 is played to its end, which waits for its grace, and continues to a
// next round. The store commits the on as row 1, then answers an error, and
// the read after it fails: the act answers an error the admin API answers
// 503, and the snapshot shows the view unsettled, still off. Until the
// retry's read succeeds: A's launch retries call no Start, and arm their
// retries again; B, whose round ended, opens no next round, and no session is
// issued or moved; and an item opened meanwhile records its item_opened alone
// and waits queued, though a slot is free. The retry's read finds row 1: the
// view turns on, A's and B's runs are halted killed, by the on-row 1, and no
// Start, Issue or Move came after the row's commit. Fails first: the view
// stays off, and A's launch retry starts its turn after the row committed.
func TestNoTurnStartsWhileAKillSwitchWritesOutcomeIsUnknown(t *testing.T) {
	h := newHarness(t)
	order := noteWork(t, h)
	start := h.clock.Now()
	h.turns.fail = func(*app.Turn) error { return errEngine }
	a := h.open("checkout fails")
	h.turns.fail = nil
	h.clock.Advance(time.Second)
	b := h.open("payments fail")
	h.scripts[release.Name] = postsATask
	require.Len(t, h.play(), 2, "B's turns end; A's never started")
	h.clock.Advance(time.Second)
	unknownOn(t, h, true)
	lc := h.c.Lifecycle()
	assert.True(t, lc.KillSwitchUnsettled, "its outcome is unknown")
	assert.False(t, lc.KillSwitch.On, "the view is still off")

	// A's launch retries come, then B's round's end, then the failed read's
	// retry, which fails too.
	h.clock.Advance(h.opts.Retry)
	assert.Contains(t, h.clock.armed(), start.Add(2*h.opts.Retry), "A's launch retries are armed again")
	s := h.state(b)
	require.NotNil(t, s.Run.Rounds[0].Ended, "B's round ended")
	assert.Len(t, s.Run.Rounds, 1, "B opens no next round")
	opened := h.open("search fails")
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(opened), "an opening records its item_opened alone")
	assert.Equal(t, []item.ID{opened}, h.c.Lifecycle().Waiting, "it waits queued, though a slot is free")
	assert.Empty(t, workAfterCommit(t, order), "no turn started, no session issued or moved, while the outcome is unknown")

	h.controls.failReads(false)
	h.clock.Advance(h.opts.Retry)

	assert.True(t, h.c.Lifecycle().KillSwitch.On, "the retry's read finds row 1")
	requireKilled(t, h, a)
	s = h.state(b)
	require.NotNil(t, s.Run.Stopped, "B's run is halted")
	assert.Equal(t, item.StopKilled, s.Run.Stopped.Reason)
	assert.Equal(t, int64(1), s.Run.Stopped.Killed)
	assert.Len(t, s.Run.Rounds, 1, "B never opened its next round")
	assert.Contains(t, order.seen(), "halt "+string(a))
	assert.Contains(t, order.seen(), "halt "+string(b))
	assert.Empty(t, workAfterCommit(t, order), "no Start, Issue or Move after the row's commit")
}

// A failed kill switch write that did not commit resumes staffing once a read
// settles it (the controller's ruling, R37): with a limit of 2, A's turns
// cannot start, their retries armed, B investigates, and Q waits queued. The
// store fails the on before its row, and the read after it fails: the view
// is unsettled, still off. B's run stops quiet, which frees a slot: Q is not
// admitted, and A's launch retries start no turn. The retry's read finds the
// switch off, as it was: the mark clears, "kill switch settled" is logged,
// and no "kill switch set", as the view did not change. Every active item is
// advanced and the walk runs: Q starts, and A's turns start on their next
// retry. Nothing was recorded on the mark's account: neither A nor Q records
// item_awaiting_close or run_stopped. Fails first: staffing never pauses,
// and Q starts while the outcome is unknown.
func TestAFailedKillSwitchWriteThatDidNotCommitResumesStaffingOnceARereadSettlesIt(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, limit(2), logTo(logs))
	h.turns.fail = func(*app.Turn) error { return errEngine }
	a := h.open("checkout fails")
	h.turns.fail = nil
	b := h.open("payments fail")
	q := openLater(h, "search fails")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q), "Q waits queued")
	unknownOn(t, h, false)
	require.Zero(t, h.controls.rows(t), "no row committed")

	stopQuiet(h, b)

	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q), "a slot is free, but Q is not admitted")
	assert.Equal(t, app.Lifecycle{KillSwitchUnsettled: true, Limit: 2, Investigating: 1, Waiting: []item.ID{q}}, h.c.Lifecycle())
	require.Len(t, h.turns.started, 2, "B's turns alone: A's launch retries start no turn")
	unsettled := logged(t, logs.String(), "kill switch unsettled")
	require.Len(t, unsettled, 1)
	assert.Equal(t, map[string]any{"level": "WARN", "on": false, "seq": 0.0, "epoch": float64(h.epoch)}, pick(unsettled[0], "level", "on", "seq", "epoch"))

	h.controls.failReads(false)
	h.clock.Advance(h.opts.Retry)

	assert.Equal(t, startedKinds, h.kinds(q), "Q starts")
	assert.Equal(t, app.Lifecycle{Limit: 2, Investigating: 2}, h.c.Lifecycle(), "the view settled, off")
	settled := logged(t, logs.String(), "kill switch settled")
	require.Len(t, settled, 1)
	assert.Equal(t, map[string]any{"level": "INFO", "on": false, "seq": 0.0, "epoch": float64(h.epoch)}, pick(settled[0], "level", "on", "seq", "epoch"))
	assert.Empty(t, logged(t, logs.String(), "kill switch set"), "the view did not change")
	runA := h.state(a).Run.ID
	h.clock.Advance(h.opts.Retry)
	var startedOfA []string
	for _, turn := range h.turns.started {
		if turn.Run == runA {
			startedOfA = append(startedOfA, turn.Agent)
		}
	}
	assert.ElementsMatch(t, []string{release.Name, runtime.Name}, startedOfA, "A's turns start on their next retry")
	for _, id := range []item.ID{a, q} {
		assert.NotContains(t, h.kinds(id), item.KindAwaitingClose, id)
		assert.NotContains(t, h.kinds(id), item.KindRunStopped, id)
	}
}

// A promotion while a kill switch write's outcome is unknown answers
// unavailable (R37; R23's order): with a limit of 1, one item investigates,
// Q waits queued, and a third item is closed. The store fails the on before
// its row, and the read after it fails: the view is unsettled, still off.
// Q's promotion answers ErrUnsettled, which the admin API answers 503, and
// records nothing; the closed item's promotion still answers closed. Once a
// read settles the switch off, Q's promotion, sent again under its key,
// starts Q. (The plan's.)
func TestAPromotionWhileAKillSwitchWritesOutcomeIsUnknownAnswersUnavailable(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	q := openLater(h, "payments fail")
	closed := openLater(h, "orders fail")
	_, err := h.c.Close(t.Context(), closed, closing("close-1"))
	require.NoError(t, err)
	unknownOn(t, h, false)
	before := h.carried()

	_, err = h.c.Promote(t.Context(), q, promoting("promote-1"))

	require.ErrorIs(t, err, app.ErrUnsettled)
	assert.Equal(t, before, h.carried(), "it records nothing")
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q))
	_, err = h.c.Promote(t.Context(), closed, promoting("promote-2"))
	assert.Equal(t, app.CodeClosed, refusal(err), "a closed item's promotion is refused as closed first")
	h.controls.failReads(false)
	h.clock.Advance(h.opts.Retry)
	require.False(t, h.c.Lifecycle().KillSwitchUnsettled, "a read settles the switch off")

	out, err := h.c.Promote(t.Context(), q, promoting("promote-1"))

	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.Equal(t, promotedKinds, h.kinds(q), "the promotion starts Q")
}

// No item closes by itself while a kill switch write's outcome is unknown
// (Decision 8; R37): an item awaits its close, its deadline a second away,
// when the store fails the on before its row, and the read after it fails.
// Its deadline passes while the view is unsettled: no item_closed. Then a
// read settles the switch off: the advance that clears the mark arms the
// item's timer again, and the item closes at once, automatic, due at its
// deadline, which has passed. (The plan's.)
func TestNoItemClosesByItselfWhileAKillSwitchWritesOutcomeIsUnknown(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	due := h.state(id).AwaitingSince.Add(closesAfter)
	advanceTo(h, due.Add(-time.Second))
	unknownOn(t, h, false)

	advanceTo(h, due.Add(time.Second))

	assert.Zero(t, countOf(h, id, item.KindClosed), "no item_closed while the outcome is unknown")
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
	h.controls.failReads(false)
	h.clock.Advance(h.opts.Retry)
	closed, recorded := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed, "it closes at once once a read settles the switch off")
	assert.False(t, recorded.Before(due))
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
}

// A kill switch write that fails once the coordinator's holding ended
// answers ErrNotHeld, with no second read, and its outcome unknown starts no
// work until the fence (R37): A's turns cannot start, their retries armed.
// The on commits as row 1, and the commit ends the holding's context, as a
// drain's first moment does (Decision 12), before the store's answer is lost.
// The act answers ErrNotHeld and reads nothing; the coordinator, still held,
// shows the view unsettled, still off. A's launch retries, which a timer
// fires before the drain's fence, start no turn. The fence clears the mark
// with the view.
func TestAKillSwitchWriteThatFailsOnceTheHoldingEndedAnswersNotHeld(t *testing.T) {
	h := newHarness(t)
	endHolding := endingHolding(t, h)
	h.turns.fail = func(*app.Turn) error { return errEngine }
	h.open("checkout fails")
	h.turns.fail = nil
	h.controls.onCommit(endHolding)
	h.controls.loseAnswers(true)

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	h.controls.loseAnswers(false)
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Zero(t, h.controls.readsMade(), "no second read")
	assert.Equal(t, int64(1), h.controls.rows(t), "the on committed as row 1")
	lc := h.c.Lifecycle()
	assert.True(t, lc.KillSwitchUnsettled, "the outcome is unknown")
	assert.False(t, lc.KillSwitch.On)
	h.clock.Advance(h.opts.Retry)
	assert.Empty(t, h.turns.started, "A's launch retries start no turn")
	assert.Zero(t, h.controls.readsMade(), "nothing reads the switch again")
	h.c.Fence()
	require.NoError(t, h.c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	assert.False(t, h.c.Lifecycle().KillSwitchUnsettled, "the fence cleared the mark with the view")
}

// No item starts, and no run is created, while a kill switch write's outcome
// is unknown (R37; Decision 12, rule 4): the step waits where it would start
// an item or create its run, and records nothing on that account.
//   - With a limit of 1, an item the walk admitted, whose start the records
//     store refused, its retry armed: the store takes starts again, then the
//     on fails before its row, and the read after it fails. The item's retry
//     starts nothing. Once a read settles the switch off, the item starts.
//   - An item that started, whose run's creation failed, as the revision it
//     locked could not be read, its retry armed: the revision can be read
//     again, then the on fails before its row, and the read after it fails.
//     The item's retry creates no run. Once a read settles the switch off,
//     the item's run is created and its first round opens.
func TestNoItemStartsAndNoRunIsCreatedWhileAKillSwitchWritesOutcomeIsUnknown(t *testing.T) {
	t.Run("an admitted item's start", func(t *testing.T) {
		h := newHarness(t, limit(1))
		q := admittedUnstarted(h)
		h.records.refuses(nil)
		unknownOn(t, h, false)

		h.clock.Advance(h.opts.Retry)

		assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q), "its retry starts nothing")
		h.controls.failReads(false)
		h.clock.Advance(h.opts.Retry)
		assert.Equal(t, startedKinds, h.kinds(q), "it starts once a read settles the switch off")
	})
	t.Run("a started item's run", func(t *testing.T) {
		h := newHarness(t)
		id, restore := withoutItsRun(h)
		restore()
		unknownOn(t, h, false)

		h.clock.Advance(h.opts.Retry)

		assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted}, h.kinds(id), "its retry creates no run")
		h.controls.failReads(false)
		h.clock.Advance(h.opts.Retry)
		assert.Equal(t, startedKinds, h.kinds(id), "its run is created once a read settles the switch off")
	})
}

// A promotion's preview goes on while a kill switch write's outcome is
// unknown (R37: "close, reopen, previews and reads go on"; the controller's
// ruling of Task 14's fix round): with a limit of 1, one item investigates
// and Q waits queued. The store fails the on before its row, and the read
// after it fails: the view is unsettled, still off. A preview of Q's
// promotion answers its consequences as with the view settled: Q among the
// starts, two items investigating, and the limit of 1; it records nothing.
// The promotion itself answers ErrUnsettled, and records nothing either.
func TestAPromotionsPreviewGoesOnWhileAKillSwitchWritesOutcomeIsUnknown(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	q := openLater(h, "payments fail")
	unknownOn(t, h, false)
	before := h.carried()

	preview, err := h.c.Promote(t.Context(), q, previewing())

	require.NoError(t, err, "the preview goes on")
	assert.Equal(t, app.Outcome{Consequences: app.Consequences{Starts: []item.ID{q}, Investigating: 2, Limit: 1}}, preview)
	assert.Equal(t, before, h.carried(), "the preview records nothing")
	_, err = h.c.Promote(t.Context(), q, promoting("promote-1"))
	require.ErrorIs(t, err, app.ErrUnsettled, "the promotion itself does not go on")
	assert.Equal(t, before, h.carried(), "the promotion records nothing")
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q))
}

// A kill switch act whose second read fails as the coordinator's holding
// ends answers ErrNotHeld (Decision 13; R37; the controller's ruling of Task
// 14's fix round): the on commits as row 1 and its answer is lost while the
// holding lasts, so the coordinator reads the switch again; that read ends
// the holding's context, as a drain's first moment does (Decision 12), and
// fails for it. The act answers ErrNotHeld, not unavailable; no retry of the
// read is armed, and nothing reads the switch again. The view stays
// unsettled, still off, until the fence, which clears the mark.
func TestAKillSwitchActWhoseReadMeetsAnEndedHoldingAnswersNotHeld(t *testing.T) {
	h := newHarness(t)
	endHolding := endingHolding(t, h)
	h.controls.loseAnswers(true)
	h.controls.onRead(endHolding)

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	h.controls.loseAnswers(false)
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Equal(t, int64(1), h.controls.rows(t), "the on committed as row 1")
	assert.Equal(t, 1, h.controls.readsMade(), "the second read, which failed as the holding ended")
	assert.Zero(t, h.clock.pending(), "no retry of the read is armed")
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, 1, h.controls.readsMade(), "nothing reads the switch again")
	assert.Equal(t, app.Lifecycle{KillSwitchUnsettled: true, Limit: 3}, h.c.Lifecycle(), "the view stays unsettled, still off")
	h.c.Fence()
	assert.Equal(t, app.Lifecycle{}, h.c.Lifecycle(), "the fence clears the mark with the view")
}

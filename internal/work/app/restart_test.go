package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// An instance that takes over a round in progress stops the turns and ends
// the sessions it finds, then starts fresh ones for the agents that have
// not ended their turn on the board: a token of the last instance is
// unknown to the new one.
func TestATakeoverStartsFreshTurnsForTheRoundInProgress(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	first := h.turns.take()
	h.turns.queued = append(h.turns.queued, first[0])
	h.play()

	h.crash()
	require.NoError(t, h.boot())

	restarted := h.turns.started
	require.Len(t, restarted, 1, "the agent that ended its turn gets none")
	assert.Equal(t, runtime.Name, restarted[0].Agent)
	assert.Equal(t, 1, restarted[0].Round)
	assert.NotEqual(t, first[1].MCP.Reveal(), restarted[0].MCP.Reveal())
	_, err := h.registry.MCP().Session(t.Context(), first[1].MCP.Reveal())
	require.ErrorIs(t, err, actionsapp.ErrNoSession)
	_, err = h.registry.MCP().Session(t.Context(), restarted[0].MCP.Reveal())
	require.NoError(t, err)
	h.round()
	s := h.state(id)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, s.Run.Rounds[0].Ended.Turns)
	assert.Len(t, roundEntries(h.events(s.Run.ID)), 1, "the board's round entry once")
}

// A round whose deadline passed while no instance held the lease ends by
// an outage: no turn counts, it counts toward no budget, it is never quiet,
// and the next round opens.
func TestARoundWhoseDeadlinePassedWithoutAHolderEndsByOutage(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.crash()
	h.clock.Advance(3 * time.Minute)

	require.NoError(t, h.boot())

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByOutage, ended.EndedBy)
	assert.Empty(t, ended.Turns)
	assert.Zero(t, ended.Rounds)
	assert.Zero(t, ended.WallClock)
	assert.False(t, ended.Quiet)
	assert.Len(t, h.turns.started, 2)
	assert.Equal(t, 2, h.turns.started[0].Round)
	assert.Contains(t, h.turns.started[0].Prompt, "Round 2 of this investigation: at most 2 more rounds follow it", "the outage round counts toward no budget")
}

// A round whose deadline passed while its holder still held the lease, as
// when the holder died in its write calls' grace, ends by its deadline on
// the next instance: the turns that ended on the board ended, the others
// were interrupted, and the round counts toward the budget.
func TestARoundWhoseHolderDiedAfterItsDeadlineEndsByItsDeadline(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending("")
	id := h.open("checkout fails")
	h.play()
	h.clock.Advance(2*time.Minute + 2*time.Second)
	h.crash()
	h.clock.Advance(time.Minute)

	require.NoError(t, h.boot())

	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnInterrupted}, ended.Turns)
	assert.Equal(t, 1, ended.Rounds)
	assert.Equal(t, 2*time.Minute, ended.WallClock)
	assert.True(t, ended.Unseen, "this instance did not see its turns")
	assert.False(t, ended.Quiet)
}

// A round no holding acted on, whose holding lost the lease only after its
// deadline, ends by its deadline on the next instance, but is never quiet:
// the instance that ends it did not see its turns, which here never ran, so
// the run goes on. The board's store failed from the round's entry on.
func TestARoundNoHoldingActedOnIsNeverQuiet(t *testing.T) {
	h := newHarness(t)
	h.store.set(func(n int) error {
		if n >= 2 {
			return errors.New("the board's store is down")
		}
		return nil
	})
	id := h.open("checkout fails")
	h.clock.Advance(3 * time.Minute)
	h.crash()
	h.store.set(nil)
	h.clock.Advance(time.Minute)

	require.NoError(t, h.boot())

	s := h.state(id)
	ended := s.Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.True(t, ended.Unseen)
	assert.False(t, ended.Quiet)
	assert.Nil(t, s.Run.Stopped, "the run goes on")
	require.Len(t, s.Run.Rounds, 2)
	assert.Len(t, h.turns.started, 2, "round 2's turns started")
}

// heldWithTimeLeft opens an incident whose first round cannot be held while
// the board's store is down, and has the store back with left of the round's
// two minutes to run: the first retry after that holds the round and starts
// its turns. The retries come every retry wait, so left is a multiple of it.
func heldWithTimeLeft(t *testing.T, left time.Duration) (*harness, item.ID) {
	t.Helper()
	h := newHarness(t)
	h.store.set(func(n int) error {
		if n >= 2 {
			return errors.New("the board's store is down")
		}
		return nil
	})
	id := h.open("checkout fails")
	h.clock.Advance(2*time.Minute - left - h.opts.Retry)
	require.Empty(t, h.turns.started, "the round is not held while the store is down")
	h.store.set(nil)
	h.clock.Advance(h.opts.Retry)
	require.Len(t, h.turns.started, 2, "the retry held the round")
	require.Equal(t, left, h.state(id).Run.Latest().Opened.Deadline.Sub(h.clock.Now()))
	return h, id
}

// A round that this instance can act on only when less than a third of its
// time is left is never quiet, however it ends: its turns start, but this
// instance cannot tell a round whose agents found nothing from one they had
// no time for, so the round is unseen and the run goes on. The board's store
// was down from the round's entry on, and came back shortly before the
// deadline.
func TestARoundHeldWithLessThanAThirdOfItsTimeLeftIsNeverQuiet(t *testing.T) {
	for name, c := range map[string]struct {
		left time.Duration
		by   item.EndedBy
		turn item.TurnClass
	}{
		"seconds left, the deadline interrupts the turns":       {left: 5 * time.Second, by: item.EndedByDeadline, turn: item.TurnInterrupted},
		"seconds left, the turns end":                           {left: 5 * time.Second, by: item.EndedByTurns, turn: item.TurnEnded},
		"just under a third left, the deadline interrupts them": {left: 35 * time.Second, by: item.EndedByDeadline, turn: item.TurnInterrupted},
	} {
		h, id := heldWithTimeLeft(t, c.left)

		if c.by == item.EndedByTurns {
			h.round()
		} else {
			h.clock.Advance(c.left + h.opts.Grace)
		}

		s := h.state(id)
		ended := s.Run.Rounds[0].Ended
		require.NotNil(t, ended, name)
		assert.Equal(t, c.by, ended.EndedBy, name)
		assert.Equal(t, map[string]item.TurnClass{release.Name: c.turn, runtime.Name: c.turn}, ended.Turns, name)
		assert.True(t, ended.Unseen, name)
		assert.False(t, ended.Quiet, name)
		assert.Equal(t, item.Decision{Next: item.NextRound}, ended.Decision, name)
		assert.Nil(t, s.Run.Stopped, "%s: the run goes on", name)
		assert.Len(t, s.Run.Rounds, 2, name)
	}
}

// A round that a takeover finds with less than a third of its time left is
// unseen too, though its agents had ended their turns on the board: the round
// ends by its turns at once, as the takeover holds it, and is never quiet.
func TestARoundTakenOverWithLessThanAThirdLeftIsNeverQuietThoughItsTurnsEnded(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.play()
	h.crash()
	h.clock.Advance(2*time.Minute - 5*time.Second)

	require.NoError(t, h.boot())
	h.clock.Advance(h.opts.Grace)

	s := h.state(id)
	ended := s.Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByTurns, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
	assert.True(t, ended.Unseen)
	assert.False(t, ended.Quiet)
	assert.Nil(t, s.Run.Stopped, "the run goes on")
}

// A round that this instance acts on with a third of its time left or more is
// seen, as it was: the deadline interrupts its turns, nothing was posted, so
// the round is quiet and the run stops quiet.
func TestARoundHeldWithAThirdOfItsTimeLeftOrMoreIsSeen(t *testing.T) {
	for name, left := range map[string]time.Duration{
		"most of the time left":      85 * time.Second,
		"exactly a third of it left": 40 * time.Second,
	} {
		h, id := heldWithTimeLeft(t, left)

		h.clock.Advance(left + h.opts.Grace)

		s := h.state(id)
		ended := s.Run.Rounds[0].Ended
		require.NotNil(t, ended, name)
		assert.Equal(t, item.EndedByDeadline, ended.EndedBy, name)
		assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnInterrupted, runtime.Name: item.TurnInterrupted}, ended.Turns, name)
		assert.False(t, ended.Unseen, name)
		assert.True(t, ended.Quiet, name)
		require.NotNil(t, s.Run.Stopped, name)
		assert.Equal(t, item.StopQuiet, s.Run.Stopped.Reason, name)
	}
}

// startedWithTimeLeft opens an incident whose runner cannot be reached from
// the round's start, and has it back with left of the round's two minutes to
// run: the first retry after that starts the turns. The retries come every
// retry wait, so left is a multiple of it.
func startedWithTimeLeft(t *testing.T, left time.Duration) (*harness, item.ID) {
	t.Helper()
	h := newHarness(t)
	h.turns.fail = func(*app.Turn) error { return errors.New("the runner cannot be reached") }
	id := h.open("checkout fails")
	h.clock.Advance(2*time.Minute - left - h.opts.Retry)
	require.Empty(t, h.turns.started, "no turn starts while the runner is out of reach")
	h.turns.fail = nil
	h.clock.Advance(h.opts.Retry)
	require.Len(t, h.turns.started, 2, "the retry starts the turns")
	require.Equal(t, left, h.state(id).Run.Latest().Opened.Deadline.Sub(h.clock.Now()))
	return h, id
}

// A turn that the runner could not start until less than a third of the
// round was left counts as not started if it still runs at the deadline: it
// had no time to look, and an interrupted turn would pass for one that
// looked and found nothing. So the round is not quiet and the run goes on,
// though the round was held in time. The round is not unseen, so that a
// wrap-up round of this kind uses up a wrap-up. The next round, whose turns
// start in time, counts its turns as usual.
func TestATurnStartedWithLessThanAThirdOfTheRoundLeftCountsAsNotStarted(t *testing.T) {
	for name, left := range map[string]time.Duration{
		"seconds left":                  5 * time.Second,
		"just under a third of it left": 35 * time.Second,
	} {
		h, id := startedWithTimeLeft(t, left)

		h.clock.Advance(left + h.opts.Grace)

		s := h.state(id)
		ended := s.Run.Rounds[0].Ended
		require.NotNil(t, ended, name)
		assert.Equal(t, item.EndedByDeadline, ended.EndedBy, name)
		assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnUnstarted, runtime.Name: item.TurnUnstarted}, ended.Turns, name)
		assert.False(t, ended.Unseen, name)
		assert.False(t, ended.Quiet, name)
		assert.Equal(t, item.Decision{Next: item.NextRound}, ended.Decision, name)
		assert.Nil(t, s.Run.Stopped, "%s: the run goes on", name)
		require.Len(t, s.Run.Rounds, 2, name)
		h.clock.Advance(2*time.Minute + h.opts.Grace)
		next := h.state(id).Run.Rounds[1].Ended
		require.NotNil(t, next, name)
		assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnInterrupted, runtime.Name: item.TurnInterrupted}, next.Turns, name)
	}
}

// A turn that the runner started with a third of the round left or more, and
// that still runs at the deadline, counts as interrupted, as it did: it had
// time, and found nothing, so the round is quiet and the run stops quiet.
func TestATurnStartedWithAThirdOfTheRoundLeftOrMoreIsInterrupted(t *testing.T) {
	for name, left := range map[string]time.Duration{
		"most of the time left":      85 * time.Second,
		"exactly a third of it left": 40 * time.Second,
	} {
		h, id := startedWithTimeLeft(t, left)

		h.clock.Advance(left + h.opts.Grace)

		s := h.state(id)
		ended := s.Run.Rounds[0].Ended
		require.NotNil(t, ended, name)
		assert.Equal(t, item.EndedByDeadline, ended.EndedBy, name)
		assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnInterrupted, runtime.Name: item.TurnInterrupted}, ended.Turns, name)
		assert.False(t, ended.Unseen, name)
		assert.True(t, ended.Quiet, name)
		require.NotNil(t, s.Run.Stopped, name)
		assert.Equal(t, item.StopQuiet, s.Run.Stopped.Reason, name)
	}
}

// A turn started late that ends by itself before the deadline keeps the class
// it reports: only a turn still running at the deadline counts as not
// started.
func TestATurnStartedLateThatEndsBeforeTheDeadlineKeepsItsClass(t *testing.T) {
	h, id := startedWithTimeLeft(t, 5*time.Second)
	h.scripts[release.Name], h.scripts[runtime.Name] = endTurn, ending("")

	h.play()
	h.clock.Advance(5*time.Second + h.opts.Grace)

	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnUnstarted}, ended.Turns)
	assert.False(t, ended.Quiet, "the turn still running counts as not started")
}

// A round whose deadline falls on the last holding's expiry ended under that
// holding, by its deadline; one whose deadline falls on this holding's start
// ended while no instance held the lease, by an outage.
func TestAMissedRoundOnTheEdgesOfTheHoldings(t *testing.T) {
	onExpiry := newHarness(t)
	id := onExpiry.open("checkout fails")
	onExpiry.clock.Advance(2 * time.Minute)
	onExpiry.crash()
	onExpiry.clock.Advance(time.Minute)
	require.NoError(t, onExpiry.boot())
	onStart := newHarness(t)
	other := onStart.open("checkout fails")
	onStart.clock.Advance(time.Minute)
	onStart.crash()
	onStart.clock.Advance(time.Minute)

	require.NoError(t, onStart.boot())

	assert.Equal(t, item.EndedByDeadline, onExpiry.state(id).Run.Rounds[0].Ended.EndedBy)
	assert.Equal(t, item.EndedByOutage, onStart.state(other).Run.Rounds[0].Ended.EndedBy)
}

// A takeover whose holding ended before it began, as when the leader lost the
// lease while the coordinator waited for its lock, acts on nothing, and the
// coordinator stays fenced.
func TestATakeoverWhoseHoldingEndedActsOnNothing(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.crash()
	started := len(h.turns.started)
	ended, cancel := context.WithCancel(t.Context())
	cancel()

	err := h.c.TakeOver(ended, app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()})

	require.ErrorIs(t, err, context.Canceled)
	_, err = h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Len(t, h.turns.started, started)
}

// An item's opening that waits on a store gives way once the holding ends,
// so that the fence waits for nothing: the instance's sessions and turns end
// with the lease, though the request goes on.
func TestAnOpeningGivesWayOnceTheHoldingEnds(t *testing.T) {
	h := newHarness(t)
	h.crash()
	holding, release := context.WithCancel(t.Context())
	require.NoError(t, h.c.TakeOver(holding, app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()}))
	letGo := h.records.hang()
	t.Cleanup(letGo)
	opened := make(chan error, 1)
	go func() {
		_, err := h.c.Open(context.WithoutCancel(t.Context()), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
		opened <- err
	}()
	require.Eventually(t, func() bool { return h.records.waiting.Load() == 1 }, 5*time.Second, time.Millisecond)

	release()

	select {
	case err := <-opened:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("the opening waited for the store once the holding ended")
	}
	h.c.Fence()
}

// A round whose board entry never landed before its holder died, and whose
// deadline then passed, gets its entry before it ends, so that the board
// takes the next round's entry after it.
func TestARoundEndedByAnOutageGetsItsBoardEntryFirst(t *testing.T) {
	h := newHarness(t)
	h.store.set(func(n int) error {
		if n == 2 {
			return errors.New("the instance died")
		}
		return nil
	})
	id := h.open("checkout fails")
	h.crash()
	h.store.set(nil)
	h.clock.Advance(3 * time.Minute)

	require.NoError(t, h.boot())

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	assert.Equal(t, item.EndedByOutage, s.Run.Rounds[0].Ended.EndedBy)
	assert.Len(t, roundEntries(h.events(s.Run.ID)), 2, "both rounds' entries, in order")
	assert.Len(t, h.turns.started, 2, "round 2's turns started")
}

// A round whose deadline passes while this instance holds the lease but
// cannot act on it, as when the board's store is down, ends by its deadline
// once the store is back: its turns never started, and it counts toward the
// budget, so that a long outage of the board ends the run by its budget,
// not by rounds that cost nothing, for ever.
func TestARoundThisHoldingCouldNotActOnEndsByItsDeadline(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.crash()
	h.clock.Advance(time.Second)
	h.store.set(func(int) error { return errors.New("the board's store is down") })
	require.NoError(t, h.boot())
	h.clock.Advance(3 * time.Minute)
	h.store.set(nil)

	h.clock.Advance(h.opts.Retry)

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnUnstarted, runtime.Name: item.TurnUnstarted}, ended.Turns)
	assert.Equal(t, 1, ended.Rounds)
}

// A round ended by an outage whose end could not be recorded at first is
// ended on the next try.
func TestAnOutageRoundWhoseEndFailsIsEndedOnTheRetry(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.crash()
	h.clock.Advance(3 * time.Minute)
	h.records.set(func(n int) error {
		if n == 5 {
			return errors.New("the database blinked")
		}
		return nil
	})
	require.NoError(t, h.boot())
	require.Len(t, h.state(id).Run.Rounds, 1)

	h.clock.Advance(h.opts.Retry)

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	assert.Equal(t, item.EndedByOutage, s.Run.Rounds[0].Ended.EndedBy)
	assert.Len(t, h.turns.started, 2)
}

// A takeover that finds the lease gone, at any item, acts on no item after
// it: it stops, fenced, and says so. A step that wrote nothing, as a
// round's entry the board holds already, would otherwise start turns for
// an instance that holds no lease.
func TestATakeoverActsOnNothingOnceItFindsTheLeaseGone(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.open("payments fail")
	h.crash()
	found := false
	h.store.set(func(int) error {
		if !found {
			found = true
			return fmt.Errorf("append: %w", app.ErrLeaseLost)
		}
		return nil
	})

	err := h.boot()

	require.ErrorIs(t, err, app.ErrLeaseLost)
	assert.Empty(t, h.turns.started, "no turn starts once the lease is found gone")
	assert.Zero(t, h.clock.pending())
}

// A turn's end reported late, by a runner whose turn was stopped before
// the instance took the lease again and started a new turn for the same
// agent, changes nothing: the new turn's end is the one that counts.
func TestAnExitOfAnEarlierTurnChangesNothing(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	earlier := h.turns.take()
	h.c.Fence()
	require.NoError(t, h.c.TakeOver(t.Context(), app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()}))
	fresh := h.turns.take()
	require.Len(t, fresh, 2)

	for _, turn := range earlier {
		h.c.Exited(t.Context(), app.Exit{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: item.TurnCrashed})
	}
	h.turns.queued = fresh
	h.round()

	ended := h.state(id).Run.Rounds[0].Ended
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
}

// Timers that run on goroutines of their own, as Plan 7d's will, while
// turns end and deadlines pass at once: each round ends once, by its turns
// or by its deadline, whichever comes first, the run stops by its budget,
// and a fenced coordinator acts no more. Each round has an entry on the
// board, so that none is quiet.
func TestARealClocksTimersEndEachRoundOnce(t *testing.T) {
	h := newHarness(t)
	g := &goTimers{c: h.clock}
	h.timers = g
	h.crash()
	require.NoError(t, h.boot())
	id := h.open("checkout fails")

	for i := 0; i < 10 && h.state(id).Status() != item.StatusAwaitingClose; i++ {
		if s := h.state(id); s.Run.Latest().Ended == nil {
			round := s.Run.Latest().Opened.Round
			_, err := h.boards.PostTask(t.Context(), s.Run.ID, round, release, &board.TaskInput{Title: fmt.Sprintf("check round %d", round)})
			require.NoError(t, err)
		}
		var wg sync.WaitGroup
		for _, turn := range h.turns.take() {
			wg.Go(func() {
				h.c.Exited(t.Context(), app.Exit{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: item.TurnRefused})
			})
		}
		h.clock.Advance(2 * time.Minute)
		wg.Wait()
		g.wait()
		h.clock.Advance(h.opts.Grace)
		g.wait()
	}

	s := h.state(id)
	require.Equal(t, item.StatusAwaitingClose, s.Status())
	assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
	assert.Len(t, s.Run.Rounds, 3)
	other := h.open("payments fail")
	var wg sync.WaitGroup
	wg.Go(func() { h.clock.Advance(2 * time.Minute) })
	h.c.Fence()
	fenced := len(h.kinds(other))
	wg.Wait()
	g.wait()
	h.clock.Advance(10 * time.Minute)
	g.wait()
	assert.Len(t, h.kinds(other), fenced, "nothing is recorded once the coordinator is fenced")
}

// A crash at any step of an item's life leaves the item to the next
// instance, which carries it on from the records: the item ends as it
// would have, the board holds each round's entry and the stop entry once,
// and nothing after the stop.
func TestACrashAtAnyStepIsCarriedOnFromTheRecords(t *testing.T) {
	for _, c := range []struct {
		store string
		at    int
	}{
		{"records", 2}, {"records", 3}, {"records", 4}, {"records", 5}, {"records", 6}, {"records", 7},
		{"board", 1}, {"board", 2}, {"board", 3}, {"board", 5},
	} {
		name := fmt.Sprintf("%s append %d", c.store, c.at)
		h := newHarness(t)
		crashed := false
		fail := func(n int) error {
			if n == c.at && !crashed {
				crashed = true
				return errors.New("the instance died")
			}
			return nil
		}
		if c.store == "records" {
			h.records.fail = fail
		} else {
			h.store.fail = fail
		}
		id := h.open("checkout fails")
		for range 8 {
			if crashed && h.epoch == 1 {
				h.crash()
				require.NoError(t, h.boot(), name)
			}
			h.round()
		}

		s := h.state(id)
		require.True(t, crashed, name)
		assert.Equal(t, item.StatusAwaitingClose, s.Status(), name)
		assert.Equal(t, item.StopQuiet, s.Closing.Reason, name)
		events := h.events(s.Run.ID)
		assert.Len(t, roundEntries(events), len(s.Run.Rounds), name)
		_, stopped := lastStop(events)
		assert.True(t, stopped, name)
		assert.NotNil(t, events[len(events)-1].Body.Stop, "%s: nothing after the stop entry", name)
		evidence := 0
		for i := range events {
			if events[i].Body.Evidence != nil {
				evidence++
			}
		}
		assert.Equal(t, 1, evidence, "%s: the evidence the item opened with, once", name)
	}
}

// An instance never acts on records it cannot read: it refuses to take
// over, and acts on nothing.
func TestATakeoverRefusesRecordsItCannotRead(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	_, err := h.records.Records.Append(t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "from-a-later-version", Item: id, Incident: history[0].Incident, Body: item.Body{V: item.Version + 1}}}, nil
	})
	require.NoError(t, err)
	h.crash()

	err = h.boot()

	require.ErrorIs(t, err, item.ErrVersion)
	assert.Empty(t, h.turns.started)
	_, err = h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
}

// errBlinked is what a store that blinks answers.
var errBlinked = errors.New("the database blinked")

// blinking is the records store whose next load of an item fails, as a
// database does when it blinks.
type blinking struct {
	*failing
	blinkMu sync.Mutex
	once    map[item.ID]bool
}

func (b *blinking) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
	b.blinkMu.Lock()
	blink := b.once[id]
	delete(b.once, id)
	b.blinkMu.Unlock()
	if blink {
		return nil, errBlinked
	}
	return b.failing.Load(ctx, id)
}

// appendRaw commits a record to an item as it is, without the rules the
// coordinator checks a record against first, as a store holds a record of a
// later version, or one a bug wrote out of order.
func appendRaw(t *testing.T, h *harness, id item.ID, body item.Body) {
	t.Helper()
	_, err := h.records.Records.Append(t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: h.ids.NewID(), Item: id, Incident: history[0].Incident, Body: body}}, nil
	})
	require.NoError(t, err)
}

// A takeover that cannot load the records of an item that does not await its
// close refuses the lease and holds nothing, so that its leader takes the
// lease again and reads them: a record of a later version may be among those
// it could not load.
func TestATakeoverThatCannotLoadAnItemsRecordsRefusesTheLease(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	appendRaw(t, h, id, item.Body{V: item.Version + 1})
	h.crash()
	ports := h.ports()
	ports.Records = &blinking{failing: h.records, once: map[item.ID]bool{id: true}}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	started := len(h.turns.started)

	err = c.TakeOver(t.Context(), app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()})

	require.ErrorIs(t, err, errBlinked)
	_, err = c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Len(t, h.turns.started, started, "no turn starts")
	assert.Zero(t, h.clock.pending(), "no step is tried again")
}

// unreadable is the records store whose load of some items always fails, with
// the error given for each.
type unreadable struct {
	*failing
	loads map[item.ID]error
}

func (u *unreadable) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
	if err := u.loads[id]; err != nil {
		return nil, err
	}
	return u.failing.Load(ctx, id)
}

// A takeover carries on when the store reports that the records of one item
// do not decode, as the Postgres store does for a row of this version whose
// body does not: that item's steps fail and are tried again, and the other
// items are acted on, so that one corrupt row cannot block every takeover. The
// corrupt item comes first.
func TestATakeoverCarriesOnPastAnItemWhoseRecordsDoNotDecode(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.open("payments fail")
	active, err := h.records.Active(t.Context())
	require.NoError(t, err)
	require.Len(t, active, 2)
	corrupt, other := active[0], active[1]
	h.crash()
	ports := h.ports()
	ports.Records = &unreadable{failing: h.records, loads: map[item.ID]error{
		corrupt: fmt.Errorf("record 1: %w: %w", item.ErrInvalid, errors.New("cannot unmarshal a string into an opening")),
	}}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	before := len(h.turns.started)

	err = c.TakeOver(t.Context(), app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()})

	require.NoError(t, err)
	started := h.turns.started[before:]
	require.Len(t, started, 2, "a turn for each agent of the other item's round")
	for _, turn := range started {
		assert.Equal(t, h.state(other).Run.ID, turn.Run)
	}
}

// A takeover carries on when the records of one item fold into an invalid
// state, as a record out of the lifecycle's order does: that item's steps
// fail and are tried again, and the other items are acted on, so that one
// corrupt item cannot block every takeover. The corrupt item comes first.
func TestATakeoverCarriesOnPastAnItemWhoseRecordsAreInvalid(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.open("payments fail")
	active, err := h.records.Active(t.Context())
	require.NoError(t, err)
	require.Len(t, active, 2)
	corrupt, other := active[0], active[1]
	appendRaw(t, h, corrupt, item.Body{V: item.Version, Started: &item.Started{Commit: commit}})
	h.crash()

	require.NoError(t, h.boot())

	started := h.turns.started
	require.Len(t, started, 2, "a turn for each agent of the other item's round")
	for _, turn := range started {
		assert.Equal(t, h.state(other).Run.ID, turn.Run)
	}
}

// A coordinator that is fenced acts no more: its turns stop, its sessions
// end, its timers are cancelled, and it opens nothing and decides nothing
// until it takes over again.
func TestAFencedCoordinatorActsNoMore(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	played := h.turns.take()
	kinds := h.kinds(id)
	stops := h.turns.all

	h.c.Fence()
	for i := range played {
		h.c.Exited(t.Context(), app.Exit{Run: played[i].Run, Agent: played[i].Agent, Round: 1, Class: item.TurnEnded})
	}
	h.clock.Advance(10 * time.Minute)
	_, err := h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})

	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Equal(t, kinds, h.kinds(id))
	assert.Equal(t, stops+1, h.turns.all, "every turn stopped")
	_, err = h.registry.MCP().Session(t.Context(), played[0].MCP.Reveal())
	require.ErrorIs(t, err, actionsapp.ErrNoSession)
	assert.Zero(t, h.clock.pending())
}

// A write that finds the lease gone fences the coordinator at once, and
// tells its leader, which takes the lease again.
func TestAWriteThatFindsTheLeaseGoneFencesTheCoordinator(t *testing.T) {
	h := newHarness(t)
	lost := 0
	var epoch int64
	h.c.OnLost(func(e int64) { lost, epoch = lost+1, e })
	h.records.fail = func(n int) error {
		if n == 5 {
			return fmt.Errorf("record: %w", app.ErrLeaseLost)
		}
		return nil
	}
	h.open("checkout fails")
	played := h.play()
	stops := h.turns.all

	h.clock.Advance(h.opts.Grace)

	assert.Equal(t, 1, lost)
	assert.Equal(t, h.epoch, epoch, "the holding it acted under")
	assert.Equal(t, stops+1, h.turns.all)
	_, err := h.registry.LLM().Session(t.Context(), played[0].LLM.Reveal())
	require.Error(t, err)
	_, err = h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Zero(t, h.clock.pending(), "no retry of a fenced coordinator's step")
}

// A board append that finds the lease gone fences the coordinator as a
// record does.
func TestABoardAppendThatFindsTheLeaseGoneFencesTheCoordinator(t *testing.T) {
	h := newHarness(t)
	h.store.fail = func(n int) error {
		if n == 2 {
			return fmt.Errorf("append: %w", app.ErrLeaseLost)
		}
		return nil
	}

	_, err := h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})

	require.NoError(t, err, "the item's opening was recorded")
	_, err = h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Empty(t, h.turns.started)
	assert.Zero(t, h.clock.pending(), "no retry of a fenced coordinator's step")
}

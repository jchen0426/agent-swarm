package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	realclock "github.com/jchen0426/agent-swarm/internal/clock"
	"github.com/jchen0426/agent-swarm/internal/holding"
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

// A turn started late that the runner reports interrupted, as when it kills
// the sandbox at the deadline and its report comes a moment before this
// instance's own deadline does, counts as not started, as the deadline would
// count it: the round is not quiet, and the run goes on. A turn started late
// that the runner reports ending any other way keeps the class it reports.
func TestATurnStartedLateThatTheRunnerReportsInterruptedCountsAsNotStarted(t *testing.T) {
	for reported, counted := range map[item.TurnClass]item.TurnClass{
		item.TurnInterrupted: item.TurnUnstarted,
		item.TurnEnded:       item.TurnEnded,
		item.TurnFinished:    item.TurnFinished,
		item.TurnRefused:     item.TurnRefused,
		item.TurnVendor:      item.TurnVendor,
		item.TurnCrashed:     item.TurnCrashed,
	} {
		h, id := startedWithTimeLeft(t, 5*time.Second)
		h.clock.Advance(5*time.Second - time.Millisecond)

		for _, turn := range h.turns.take() {
			h.c.Exited(t.Context(), app.Exit{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: reported})
		}
		h.clock.Advance(time.Millisecond + h.opts.Grace)

		s := h.state(id)
		ended := s.Run.Rounds[0].Ended
		require.NotNil(t, ended, reported)
		assert.Equal(t, map[string]item.TurnClass{release.Name: counted, runtime.Name: counted}, ended.Turns, reported)
		if reported == item.TurnInterrupted {
			assert.Equal(t, item.EndedByTurns, ended.EndedBy)
			assert.False(t, ended.Quiet, "turns started with 5 s left had no time to look")
			assert.Nil(t, s.Run.Stopped, "the run goes on")
		}
	}
}

// A turn whose Start failed, though the runner had it after all, and that
// the runner then reports interrupted, as when it kills the sandbox at the
// deadline, counts as not started: this instance does not know when it
// started, and an interrupted turn would pass for one that looked and found
// nothing.
func TestATurnWhoseStartFailedThatTheRunnerReportsInterruptedCountsAsNotStarted(t *testing.T) {
	h := newHarness(t)
	failed := false
	h.turns.fail = func(turn *app.Turn) error {
		if turn.Agent != runtime.Name || failed {
			return nil
		}
		failed = true
		h.turns.queued, h.turns.started = append(h.turns.queued, *turn), append(h.turns.started, *turn)
		return errors.New("the runner started the turn, but its answer was lost")
	}
	h.scripts[runtime.Name] = ending(item.TurnInterrupted)
	id := h.open("checkout fails")

	h.round()

	s := h.state(id)
	ended := s.Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnUnstarted}, ended.Turns)
	assert.False(t, ended.Quiet)
	assert.Nil(t, s.Run.Stopped, "the run goes on")
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
	held, release := context.WithCancel(t.Context())
	require.NoError(t, h.c.TakeOver(held, app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()}))
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

// The production clocks and timers are the coordinator's and the leader's
// ports.
var (
	_ app.Clock        = (*realclock.DB)(nil)
	_ app.ClockSampler = (*realclock.DB)(nil)
	_ app.Clock        = realclock.Monotonic{}
	_ app.Timers       = (*realclock.Timers)(nil)
)

// await polls cond, in the test's goroutine, until it holds, for five
// seconds at most.
func await(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("waited in vain until " + what)
		}
	}
}

// Plan 7d's timers (clock.Timers), on a clock that runs, whose functions run
// on goroutines of their own, while turns end and deadlines pass at once:
// each round ends once, by its turns or by its deadline, whichever comes
// first, the run stops by its budget, and a fenced coordinator acts no more.
// Each round has an entry on the board, so that none is quiet. Rounds last
// a second here, ample for the post that keeps a round from being quiet on a
// busy machine, and their ends wait a fiftieth of a second for write calls.
func TestARealClocksTimersEndEachRoundOnce(t *testing.T) {
	h := newHarness(t, func(o *app.Options) {
		o.Grace, o.SettlePoll, o.Retry = 20*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond
	})
	h.revs.set(h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		files["work-item-types/incident.yaml"] = bytes.Replace(files["work-item-types/incident.yaml"], []byte("timeLimit: 2m"), []byte("timeLimit: 1s"), 1)
	}))
	h.clock.run()
	h.timers = realclock.NewTimers(h.clock)
	h.crash()
	require.NoError(t, h.boot())
	id := h.open("checkout fails")

	for round := 1; round <= 3; round++ {
		var opened *item.RoundOpened
		var run board.RunID
		await(t, func() bool {
			s := h.state(id)
			l := s.Run.Latest()
			if l == nil || l.Opened.Round != round || l.Ended != nil {
				return false
			}
			opened, run = &l.Opened, s.Run.ID
			return true
		}, fmt.Sprintf("round %d opens", round))
		// As an agent's post through the gateway does, it acts for the
		// holding that issued the agent's session.
		_, err := h.boards.PostTask(holding.WithEpoch(t.Context(), h.epoch), run, round, release, &board.TaskInput{Title: fmt.Sprintf("check round %d", round)})
		require.NoError(t, err)
		var turns []app.Turn
		await(t, func() bool {
			turns = append(turns, h.turns.take()...)
			return len(turns) == 2
		}, fmt.Sprintf("round %d's turns start", round))
		// The turns end as the deadline passes.
		time.Sleep(time.Until(opened.Deadline))
		var wg sync.WaitGroup
		for _, turn := range turns {
			wg.Go(func() {
				h.c.Exited(t.Context(), app.Exit{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: item.TurnRefused})
			})
		}
		wg.Wait()
	}
	await(t, func() bool { return h.state(id).Status() == item.StatusAwaitingClose }, "the run stops")

	s := h.state(id)
	assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
	assert.Len(t, s.Run.Rounds, 3)
	ends := 0
	for _, kind := range h.kinds(id) {
		if kind == item.KindRoundEnded {
			ends++
		}
	}
	assert.Equal(t, 3, ends, "each round ended once")
	other := h.open("payments fail")
	time.Sleep(time.Until(h.state(other).Run.Latest().Opened.Deadline))
	h.c.Fence()
	fenced := len(h.kinds(other))
	// Every timer armed before the fence has come due by now: the deadline,
	// the grace after it, and the settling.
	time.Sleep(200 * time.Millisecond)
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

// logTo has the coordinator log JSON lines, as serve's log does, into logs.
func logTo(logs *syncBuffer) func(*app.Options) {
	return func(o *app.Options) { o.Log = slog.New(slog.NewJSONHandler(logs, nil)) }
}

// logged returns the lines of a JSON log whose message is msg, in order.
func logged(t *testing.T, log, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
		if line == "" {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &fields), line)
		if fields["msg"] == msg {
			out = append(out, fields)
		}
	}
	return out
}

// requireSkippedOnce fails unless the log holds one "item skipped" line, for
// the item, with a reason that names the version (Contract C3).
func requireSkippedOnce(t *testing.T, logs *syncBuffer, id item.ID) {
	t.Helper()
	lines := logged(t, logs.String(), "item skipped")
	require.Len(t, lines, 1, "logged once")
	assert.Equal(t, string(id), lines[0]["item"])
	assert.Contains(t, lines[0]["reason"], "version")
}

// postsATask posts a task, then ends the turn: a round in which an agent
// plays it is not quiet.
func postsATask(h *harness, t *app.Turn) item.TurnClass {
	r := h.call(t, "post_task", fmt.Sprintf(`{"title":"check round %d"}`, t.Round))
	require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
	return endTurn(h, t)
}

// appendLaterEvent commits to a run's board, without the board's rules, an
// event of a version this build cannot read, as a later build writes one.
func appendLaterEvent(t *testing.T, h *harness, run board.RunID) {
	t.Helper()
	_, err := h.store.Store.Append(t.Context(), run, func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{{ID: "from-a-later-build", Run: run, Round: 1, Author: board.Coordinator, Body: board.Body{V: board.Version + 1}}}, nil
	})
	require.NoError(t, err)
}

// A takeover skips an item whose records hold one of a later version, as a
// later build writes it, and carries the other items on (Decision 19): the
// skipped item is logged once, Skipped names it, and nothing is written to it
// or started for it, while the other item's round goes on.
func TestATakeoverSkipsAnItemWithALaterRecordAndCarriesTheOthersOn(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	later := h.open("checkout fails")
	other := h.open("payments fail")
	appendRaw(t, h, later, item.Body{V: item.Version + 1})
	kinds := h.kinds(later)
	h.crash()

	require.NoError(t, h.boot())

	assert.Equal(t, []item.ID{later}, h.c.Skipped())
	run := h.state(other).Run.ID
	require.Len(t, h.turns.started, 2, "fresh turns for the other item's round, and none for the skipped one")
	for _, turn := range h.turns.started {
		assert.Equal(t, run, turn.Run)
	}
	h.round()
	assert.NotNil(t, h.state(other).Run.Rounds[0].Ended, "the other item's round went on to its end")
	h.clock.Advance(10 * time.Minute)
	assert.Equal(t, kinds, h.kinds(later), "nothing is written to the skipped item")
	requireSkippedOnce(t, logs, later)
}

// A takeover skips an item whose run's board holds an event of a later
// version, before it acts on any item (Decision 19). Here the item's round
// ended, and its next step, which opens its second round, writes a record
// before it reads the board: nothing is written to it all the same, and no
// turn starts for it. The other item goes on.
func TestATakeoverSkipsAnItemWhoseBoardHoldsALaterEvent(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	h.scripts[release.Name] = postsATask
	later := h.open("checkout fails")
	h.records.set(func(n int) error {
		if n == 6 {
			return errors.New("the instance died before the second round opened")
		}
		return nil
	})
	h.round()
	h.records.set(nil)
	kinds := h.kinds(later)
	require.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened, item.KindRoundEnded}, kinds)
	require.Equal(t, item.NextRound, h.state(later).Run.Rounds[0].Ended.Decision.Next)
	other := h.open("payments fail")
	h.crash()
	appendLaterEvent(t, h, h.state(later).Run.ID)

	require.NoError(t, h.boot())

	assert.Equal(t, []item.ID{later}, h.c.Skipped())
	assert.Equal(t, kinds, h.kinds(later), "the second round is not opened")
	run := h.state(other).Run.ID
	require.Len(t, h.turns.started, 2, "fresh turns for the other item's round, and none for the skipped one")
	for _, turn := range h.turns.started {
		assert.Equal(t, run, turn.Run)
	}
	h.clock.Advance(10 * time.Minute)
	assert.Equal(t, kinds, h.kinds(later), "nothing is written to the skipped item")
	requireSkippedOnce(t, logs, later)
}

// An item skipped at a takeover is skipped again at the next, by the same
// coordinator, and logged again, once per holding: skipped items stay held
// until a build that can read them leads (Decision 19). A fenced coordinator
// skips nothing, as it holds nothing.
func TestASkippedItemIsSkippedAgainAtTheNextTakeover(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	id := h.open("checkout fails")
	appendRaw(t, h, id, item.Body{V: item.Version + 1})
	kinds := h.kinds(id)
	h.crash()
	require.NoError(t, h.boot())
	require.Equal(t, []item.ID{id}, h.c.Skipped())
	h.c.Fence()
	fenced := h.c.Skipped()
	h.epoch++

	require.NoError(t, h.c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))

	assert.Empty(t, fenced, "a fenced coordinator skips nothing")
	assert.Equal(t, []item.ID{id}, h.c.Skipped())
	lines := logged(t, logs.String(), "item skipped")
	require.Len(t, lines, 2, "once per holding")
	for _, line := range lines {
		assert.Equal(t, string(id), line["item"])
	}
	assert.Empty(t, h.turns.started)
	h.clock.Advance(10 * time.Minute)
	assert.Equal(t, kinds, h.kinds(id), "nothing is written to it")
}

// An opening by the key of an item held by a later build is refused, whatever
// the request, and nothing is written (Decision 19). Such is an item this
// holding skips, here as its board holds an event a later build wrote, though
// its records read; and an item that awaited its close when this holding
// began, which its takeover did not read, and to which a later build wrote a
// record after its close.
func TestAnOpeningOfASkippedItemIsRefused(t *testing.T) {
	h := newHarness(t)
	in := byKey("item-by-key", "sha256:request", "checkout fails")
	id, err := h.c.Open(t.Context(), in)
	require.NoError(t, err)
	appendLaterEvent(t, h, h.state(id).Run.ID)
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
	closed := byKey("closed-by-key", "sha256:closed", "payments fail")
	_, err = h.c.Open(t.Context(), closed)
	require.NoError(t, err)
	require.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(closed.ID), "it awaits its close, not staffed")
	appendRaw(t, h, closed.ID, item.Body{V: item.Version + 1})
	h.crash()
	require.NoError(t, h.boot())
	require.Equal(t, []item.ID{id}, h.c.Skipped())
	before := h.carried()

	_, skipped := h.c.Open(t.Context(), in)
	_, afterItsClose := h.c.Open(t.Context(), closed)

	require.ErrorIs(t, skipped, app.ErrSkipped)
	require.ErrorIs(t, afterItsClose, app.ErrSkipped)
	assert.Equal(t, before, h.carried(), "no record or board entry is written, and no turn starts")
	assert.Equal(t, []item.ID{id}, activeItems(h), "no second item")
}

// answerLost is the records store whose appends commit, and then fail, as
// when the database's answer is lost on its way back: the caller sees an
// error, though its records landed.
type answerLost struct{ *failing }

func (a answerLost) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	if _, err := a.failing.Append(ctx, id, decide); err != nil {
		return nil, err
	}
	return nil, errors.New("the database's answer was lost")
}

// A person who retries an opening after a timeout, while the lease moved to
// another instance, gets the item the first try opened, and no second
// (Decision 14). The first try's opening landed under the last holding,
// though the database's answer was lost, so that holding never started the
// item. The retry, by the same key and request under the next holding,
// returns the item and starts it. Here that holding's takeover could not
// start the item either, as the database blinked: the retry starts it,
// before the takeover's own retry would.
func TestARetriedOpeningAfterATakeoverAdvancesAnItemWhoseOpeningLanded(t *testing.T) {
	h := newHarness(t)
	ports := h.ports()
	ports.Records = answerLost{h.records}
	first, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, first.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = first
	in := byKey("item-by-key", "sha256:request", "checkout fails")
	_, err = h.c.Open(t.Context(), in)
	require.Error(t, err, "the person sees the opening fail")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(in.ID), "though it landed")
	h.crash()
	h.records.set(func(n int) error {
		if n == 2 {
			return errBlinked
		}
		return nil
	})
	require.NoError(t, h.boot())
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(in.ID), "the takeover could not start it")

	id, err := h.c.Open(t.Context(), in)

	require.NoError(t, err)
	assert.Equal(t, in.ID, id)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}, h.kinds(id), "the retry started it")
	assert.Equal(t, []item.ID{id}, activeItems(h), "no second item")
	assert.Len(t, h.turns.started, 2, "its round's turns started")
	assert.Equal(t, []int64{1, 2, 2, 2, 2}, h.records.writes.noted(), "its opening landed under epoch 1, and its start under epoch 2")
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

// unreadableBoard is the boards' store, whose reads of one run fail with err,
// as a database's do: its loads, and its appends, which read the run's events
// first. With once set, only the first read fails, as a database that blinks.
type unreadableBoard struct {
	*failingStore
	run  board.RunID
	err  error
	once bool

	mu   sync.Mutex
	read bool
}

func (u *unreadableBoard) fails(run board.RunID) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if run != u.run || u.once && u.read {
		return nil
	}
	u.read = true
	return u.err
}

func (u *unreadableBoard) Load(ctx context.Context, run board.RunID) ([]board.Event, error) {
	if err := u.fails(run); err != nil {
		return nil, err
	}
	return u.failingStore.Load(ctx, run)
}

func (u *unreadableBoard) Append(ctx context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) ([]board.Event, error) {
	if err := u.fails(run); err != nil {
		return nil, err
	}
	return u.failingStore.Append(ctx, run, decide)
}

// overBoard is the coordinator of the harness's next instance, over the
// harness's ports, but whose boards read through store.
func overBoard(t *testing.T, h *harness, store app.EventStore) *app.Coordinator {
	t.Helper()
	ports := h.ports()
	ports.Boards = app.NewBoards(store, h.audit, h.records, h.ids)
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	return c
}

// A takeover that cannot read the board of an item that does not await its
// close refuses the lease and holds nothing, as one that cannot read its
// records does: an event of a later version may be among those it could not
// load, and the item's next step may write a record before it reads its board.
func TestATakeoverThatCannotLoadAnItemsBoardRefusesTheLease(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.crash()
	c := overBoard(t, h, &unreadableBoard{failingStore: h.store, run: h.state(id).Run.ID, err: errBlinked, once: true})
	started := len(h.turns.started)

	err := c.TakeOver(t.Context(), app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()})

	require.ErrorIs(t, err, errBlinked)
	_, err = c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Len(t, h.turns.started, started, "no turn starts")
	assert.Zero(t, h.clock.pending(), "no step is tried again")
}

// A takeover carries on when the store reports that the board of one item
// does not decode, as the Postgres store does for an event whose body does
// not: that item's steps fail and are tried again, and the other items are
// acted on, so that one corrupt event cannot block every takeover. The
// corrupt item comes first.
func TestATakeoverCarriesOnPastAnItemWhoseBoardDoesNotDecode(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.open("payments fail")
	active, err := h.records.Active(t.Context())
	require.NoError(t, err)
	require.Len(t, active, 2)
	corrupt, other := active[0], active[1]
	h.crash()
	c := overBoard(t, h, &unreadableBoard{failingStore: h.store, run: h.state(corrupt).Run.ID,
		err: fmt.Errorf("entry 2: %w: %w", board.ErrInvalid, errors.New("cannot unmarshal a string into a task"))})
	before := len(h.turns.started)

	err = c.TakeOver(t.Context(), app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()})

	require.NoError(t, err)
	assert.Empty(t, c.Skipped(), "a corrupt board is no later build's")
	started := h.turns.started[before:]
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

// Every write of the coordinator carries the epoch of the term it acts under:
// those of an opening, of the item's start, of a round's opening, of its end
// by its timer, and of the retries after a store failed, as well as the
// agents' posts through the gateway, for the sessions the coordinator issued.
// After a fence and a takeover under a later term, they carry the later
// epoch, the takeover's own included.
func TestEveryWriteOfTheCoordinatorCarriesItsTermsEpoch(t *testing.T) {
	h := newHarness(t)
	h.crash()
	h.epoch = 2
	require.NoError(t, h.boot(), "a takeover at epoch 3")
	h.scripts[release.Name], h.scripts[runtime.Name] = ending(""), ending("")
	var boardFailed, recordFailed bool
	h.store.set(func(n int) error {
		if n == 2 {
			boardFailed = true
			return errors.New("the board's store blinked")
		}
		return nil
	})
	h.records.set(func(n int) error {
		if n == 5 {
			recordFailed = true
			return errors.New("the database blinked")
		}
		return nil
	})

	first := h.open("checkout fails")
	h.clock.Advance(h.opts.Retry)
	require.Len(t, h.turns.started, 2, "the retry held the round")
	h.play()
	h.clock.Advance(2*time.Minute + h.opts.Grace)
	ended := h.state(first).Run.Rounds[0].Ended
	require.NotNil(t, ended, "the round's end, recorded on the retry")
	second := h.open("payments fail")
	records, entries := len(h.records.writes.noted()), len(h.store.writes.noted())
	h.c.Fence()
	h.epoch++
	require.NoError(t, h.c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.scripts[release.Name], h.scripts[runtime.Name] = endTurn, endTurn
	h.round()

	require.True(t, boardFailed && recordFailed, "a step was tried again after each store failed")
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	require.NotNil(t, h.state(second).Run.Rounds[0].Ended, "the later term ended the second item's round")
	for name, w := range map[string]struct {
		noted  []int64
		before int
	}{"records": {h.records.writes.noted(), records}, "board entries": {h.store.writes.noted(), entries}} {
		require.Greater(t, len(w.noted), w.before, "%s were written under the later term", name)
		for i, epoch := range w.noted {
			want := int64(3)
			if i >= w.before {
				want = 4
			}
			assert.Equal(t, want, epoch, "%s write %d", name, i)
		}
	}
}

// syncBuffer holds what a log writes, from any goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A write refused because its context carries no epoch is a bug, not a lost
// lease: the coordinator logs it as a write without an epoch, stays held, and
// tries the step again.
func TestAWriteWithoutAnEpochIsLoggedAndDoesNotFence(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, func(o *app.Options) { o.Log = slog.New(slog.NewTextHandler(logs, nil)) })
	lost := false
	h.c.OnLost(func(int64) { lost = true })
	h.records.set(func(n int) error {
		if n == 2 {
			return fmt.Errorf("record: %w", holding.ErrNoEpoch)
		}
		return nil
	})
	stops := h.turns.all

	id := h.open("checkout fails")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(id), "its start was refused")
	h.clock.Advance(h.opts.Retry)

	assert.Contains(t, logs.String(), "write without an epoch")
	assert.False(t, lost, "its leader is told of no loss")
	assert.Equal(t, stops, h.turns.all, "no turn was stopped")
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}, h.kinds(id), "the step was tried again")
	require.NoError(t, opens(h), "the coordinator is held")
}

// carried is what a takeover had carried on at a moment: the turns its
// instance started, and the records and board entries written so far.
type carried struct{ turns, records, entries int }

func (h *harness) carried() carried {
	return carried{len(h.turns.started), len(h.records.writes.noted()), len(h.store.writes.noted())}
}

// At each takeover, before it carries any item on, the coordinator has the
// LLM gateway sweep the model calls without an outcome that began before the
// new holding (Decision 7): once per takeover, with before at the holding's
// start, under its epoch, and with a reason that names who stopped first:
// another instance, by the name the take read, or this instance's earlier
// holding. The lease's first holding follows no holder.
func TestATakeoverSweepsTheCallsTheLastHoldingLeft(t *testing.T) {
	h := newHarness(t)
	first := h.sweeper.noted()
	h.open("checkout fails")
	for _, c := range []struct{ previous, reason string }{
		{"instance-b", "the instance instance-b stopped before the call's outcome was recorded"},
		{holder, "this instance's earlier holding ended before the call's outcome was recorded"},
	} {
		h.crash()
		h.previous = c.previous
		// The new holding starts a while after the last one ended.
		h.clock.Advance(10 * time.Second)
		sweeps, at, before := len(h.sweeper.noted()), h.clock.Now(), h.carried()
		var atSweep carried
		h.sweeper.set(func(s *sweeper) { s.at = func() { atSweep = h.carried() } })

		require.NoError(t, h.boot())

		noted := h.sweeper.noted()
		require.Len(t, noted, sweeps+1, "%s: one sweep per takeover", c.previous)
		assert.Equal(t, sweep{before: at, reason: c.reason, epoch: h.epoch}, noted[sweeps], c.previous)
		assert.Equal(t, carried{0, before.records, before.entries}, atSweep, "%s: nothing was carried on before the sweep", c.previous)
		assert.Len(t, h.turns.started, 2, "%s: the round's turns started afresh after it", c.previous)
	}
	require.Len(t, first, 1)
	assert.Equal(t, "an earlier holding ended before the call's outcome was recorded", first[0].reason, "the lease's first holding")
}

// A sweep that finds the lease gone stops the takeover (Decision 7): the
// takeover fails wrapping ErrLeaseLost, so that its leader ends the holding,
// holds nothing, and carries no item on.
func TestASweepThatFindsTheLeaseGoneStopsTheTakeover(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.crash()
	kinds, before := h.kinds(id), h.carried()
	h.sweeper.set(func(s *sweeper) { s.err = fmt.Errorf("sweep model call m-1: %w", app.ErrLeaseLost) })

	err := h.boot()

	require.ErrorIs(t, err, app.ErrLeaseLost)
	assert.Equal(t, carried{0, before.records, before.entries}, h.carried(), "no turn started, and nothing written")
	assert.Equal(t, kinds, h.kinds(id))
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
	assert.Zero(t, h.clock.pending(), "no step is tried again")
}

// A sweep is best effort (Decision 7): one that fails otherwise than by
// finding the lease gone is logged, and the takeover carries the items on.
func TestASweepThatFailsOtherwiseIsLoggedAndTheTakeoverGoesOn(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	h.open("checkout fails")
	h.crash()
	h.sweeper.set(func(s *sweeper) { s.err = errors.New("the model calls could not be read") })

	require.NoError(t, h.boot())

	assert.Len(t, h.turns.started, 2, "the round's turns started afresh")
	require.NoError(t, opens(h), "the coordinator holds the lease")
	lines := logged(t, logs.String(), "the model calls left without an outcome could not be swept")
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0]["err"], "the model calls could not be read")
}

// A sweep that fails because the holding ended, before the takeover or
// during it, as at a drain, is no failure to log: the takeover fails, as it
// does when the holding ends, and the leader logs the holding's end.
func TestASweepUnderAHoldingThatEndedLogsNoFailure(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	h.crash()
	c, err := app.NewCoordinator(h.ports(), &h.opts)
	require.NoError(t, err)
	ended, end := context.WithCancel(t.Context())
	end()

	err = c.TakeOver(ended, app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.expired, PreviousHolder: holder})

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, logged(t, logs.String(), "the model calls left without an outcome could not be swept"))
}

// A coordinator needs the sweeper, as it needs every port, and its
// instance's name, by which a takeover tells its own earlier holding from
// another instance's.
func TestACoordinatorNeedsTheSweeperAndItsInstancesName(t *testing.T) {
	h := newHarness(t)
	ports := h.ports()
	ports.Sweeper = nil
	opts := h.opts
	opts.Holder = ""

	_, noSweeper := app.NewCoordinator(ports, &h.opts)
	_, noName := app.NewCoordinator(h.ports(), &opts)

	require.Error(t, noSweeper)
	require.Error(t, noName)
}

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

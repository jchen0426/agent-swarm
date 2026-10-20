package item_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// A round whose agents all ended their own turns on the board, and left
// nothing else on it, is quiet, and the run stops quiet: the round's end
// records the turn ends it rests on.
func TestARoundWithNothingOnTheBoardStopsTheRunQuiet(t *testing.T) {
	l := newLife(t, created())
	l.open()

	ended := l.end(time.Minute, quiet())

	assert.True(t, ended.Quiet)
	assert.Equal(t, []string{release, runtime}, ended.TurnEnds)
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopQuiet}, ended.Decision)
}

// A round is never quiet when it left an entry, when an agent's turn crashed,
// was refused, failed at the model vendor, or finished or was interrupted
// without the agent's end_turn, so that it left no turn end, when no instance
// held the lease at its deadline, when its model calls were still in flight
// once the wait for them ended, or when a model call was noticed in it as
// refused, failed or of unknown outcome.
func TestARoundWithEntriesOrTroubleIsNeverQuiet(t *testing.T) {
	for name, change := range map[string]func(*item.Ending){
		"an entry":         func(e *item.Ending) { e.Report.Facts.Entries = 1 },
		"a crash":          func(e *item.Ending) { e.Turns[runtime], e.TurnEnds = item.TurnCrashed, []string{release} },
		"a refused call":   func(e *item.Ending) { e.Turns[runtime], e.TurnEnds = item.TurnRefused, []string{release} },
		"a vendor failure": func(e *item.Ending) { e.Turns, e.TurnEnds = all(item.TurnVendor, release, runtime), nil },
		"turns that finished or were interrupted": func(e *item.Ending) {
			e.Turns, e.TurnEnds = map[string]item.TurnClass{release: item.TurnFinished, runtime: item.TurnInterrupted}, nil
		},
		"an outage":            func(e *item.Ending) { e.EndedBy, e.Turns = item.EndedByOutage, nil },
		"calls in flight":      func(e *item.Ending) { e.Settled = false },
		"a model call noticed": func(e *item.Ending) { e.FailedCalls = 1 },
	} {
		l := newLife(t, created())
		l.open()
		e := quiet()
		change(e)

		ended := l.end(time.Minute, e)

		assert.False(t, ended.Quiet, name)
		assert.Equal(t, item.Decision{Next: item.NextRound}, ended.Decision, name)
	}
}

// A round is quiet only when every agent of it ended its own turn on the board
// (Decision 9): of two agents, one turn end leaves it not quiet, though the
// runner reported both turns ended; the turn ends of both make it quiet.
func TestARoundIsQuietOnlyWhenEveryAgentEndedItsOwnTurn(t *testing.T) {
	for _, c := range []struct {
		ends     []string
		quiet    bool
		decision item.Decision
	}{
		{[]string{release}, false, item.Decision{Next: item.NextRound}},
		{[]string{release, runtime}, true, item.Decision{Next: item.NextStop, Stop: item.StopQuiet}},
	} {
		l := newLife(t, created())
		l.open()
		e := quiet()
		e.TurnEnds = c.ends

		ended := l.end(time.Minute, e)

		assert.Equal(t, c.quiet, ended.Quiet, "the turn ends of %v", c.ends)
		assert.Equal(t, c.decision, ended.Decision, "the turn ends of %v", c.ends)
	}
}

// A round with no agent in it is never quiet (Decision 9): that every agent of
// it ended its own turn says nothing of a round that had none.
func TestARoundWithNoAgentIsNeverQuiet(t *testing.T) {
	rc := created()
	rc.Agents = []string{}
	l := newLife(t, rc)
	require.Empty(t, l.open().Agents)
	e := quiet()
	e.Turns, e.TurnEnds = map[string]item.TurnClass{}, nil

	ended := l.end(time.Minute, e)

	assert.False(t, ended.Quiet)
}

// A round that left a failed tool call is never quiet (Decision 9, R2),
// though its agents ended their turns with nothing on the board: a read, a
// write, a board call or a listing that failed on the server's side or the
// platform's may have kept them from what they would have found.
func TestARoundWithAFailedToolCallIsNeverQuiet(t *testing.T) {
	l := newLife(t, created())
	l.open()
	e := quiet()
	e.FailedToolCalls = 1

	ended := l.end(time.Minute, e)

	assert.False(t, ended.Quiet)
	assert.Equal(t, item.Decision{Next: item.NextRound}, ended.Decision)
}

// A converged hypothesis gets one more round, the wrap-up round, in which it
// may be refuted; if it is still converged after it, and the agents ended
// their turns on the board in it, the run stops converged (spec Section 5.6).
func TestAConvergedHypothesisGetsAWrapUpRoundThenTheRunStops(t *testing.T) {
	l := newLife(t, created())
	l.open()

	first := l.end(time.Minute, converging(hyp, other))
	wrapUp := l.open()
	last := l.end(time.Minute, converging(hyp))

	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, first.Decision, "the top hypothesis")
	assert.Equal(t, hyp, wrapUp.WrapUp)
	assert.Equal(t, 2, wrapUp.Round)
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopConverged}, last.Decision)
	assert.Equal(t, []string{release, runtime}, last.TurnEnds, "the turn ends it concluded on")
	assert.Equal(t, 1, last.Rounds, "the wrap-up round counts toward no budget")
	assert.Equal(t, time.Minute, last.WallClock)
}

// A refutation in the wrap-up round revokes the convergence: normal rounds
// resume while the budget lasts. A run has at most two wrap-up rounds.
func TestARevokedConvergenceResumesNormalRoundsAndWrapUpsAreCapped(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 6
	l := newLife(t, rc)
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()

	revoked := l.end(time.Minute, busy())
	l.open()
	again := l.end(time.Minute, converging(hyp))
	l.open()
	l.end(time.Minute, busy())
	l.open()
	capped := l.end(time.Minute, converging(hyp))

	assert.Equal(t, item.Decision{Next: item.NextRound}, revoked.Decision)
	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, again.Decision)
	assert.Equal(t, item.Decision{Next: item.NextRound}, capped.Decision, "two wrap-ups were had")
}

// The wrap-up round follows a convergence even when a budget is spent, and
// its own result decides; a revoked convergence then stops the run on its
// budget.
func TestTheWrapUpRoundFollowsAnExhaustedBudget(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 1
	l := newLife(t, rc)
	l.open()

	converged := l.end(time.Minute, converging(hyp))
	l.open()
	revoked := l.end(time.Minute, busy())

	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, converged.Decision)
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopBudgetExhausted}, revoked.Decision)
}

// The precedence of a round's end: the wrap-up round's result, the daily
// cap, a resolved trigger, no agents left, a convergence, the budget, then
// a quiet round.
func TestTheRoundEndsDecisionsInOrder(t *testing.T) {
	cut := func(e *item.Ending) *item.Ending { e.DaySpent = e.DailyCap; return e }
	resolved := func(e *item.Ending) *item.Ending { e.TriggerResolved = true; return e }

	l := newLife(t, created())
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()
	assert.Equal(t, item.StopConverged, l.end(time.Minute, cut(converging(hyp))).Decision.Stop, "the wrap-up's result first")

	l = newLife(t, created())
	l.open()
	assert.Equal(t, item.StopCut, l.end(time.Minute, resolved(cut(converging(hyp)))).Decision.Stop, "the cap before a resolved trigger")

	l = newLife(t, created())
	l.open()
	assert.Equal(t, item.StopTriggerResolved, l.end(time.Minute, resolved(converging(hyp))).Decision.Stop, "a resolved trigger before a convergence")

	l = newLife(t, created())
	l.open()
	crashed := busy()
	crashed.Turns = all(item.TurnCrashed, release, runtime)
	l.end(time.Minute, crashed)
	l.open()
	crashedConverging := converging(hyp)
	crashedConverging.Turns = all(item.TurnCrashed, release, runtime)
	gone := l.end(time.Minute, crashedConverging)
	assert.Equal(t, item.StopNotStaffed, gone.Decision.Stop, "no agents left before a convergence")
	assert.Equal(t, []string{release, runtime}, gone.Removed)

	rc := created()
	rc.Budget.Tokens = 100
	l = newLife(t, rc)
	l.open()
	spent := quiet()
	spent.Spent = 100
	assert.Equal(t, item.StopBudgetExhausted, l.end(time.Minute, spent).Decision.Stop, "the budget before a quiet round")
}

// A run stops when it used its budget: its rounds, its wall clock, so much
// of it that less than a third of a round's limit is left, or its tokens.
func TestARunStopsWhenItsBudgetIsUsed(t *testing.T) {
	for name, c := range map[string]struct {
		rounds int
		after  []time.Duration
		spent  int64
	}{
		"rounds":            {rounds: 2, after: []time.Duration{time.Second, time.Second}},
		"wall clock":        {rounds: 9, after: []time.Duration{2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute}},
		"a third of round":  {rounds: 9, after: []time.Duration{2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 81 * time.Second}},
		"tokens":            {rounds: 9, after: []time.Duration{time.Second}, spent: 90_000},
		"tokens, past them": {rounds: 9, after: []time.Duration{time.Second}, spent: 95_000},
	} {
		rc := created()
		rc.Budget.Rounds = c.rounds
		l := newLife(t, rc)
		var last item.RoundEnded
		for i, d := range c.after {
			if i > 0 {
				require.Equal(t, item.NextRound, last.Decision.Next, name)
			}
			l.open()
			e := busy()
			e.Spent = c.spent
			last = l.end(d, e)
		}

		assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopBudgetExhausted}, last.Decision, name)
	}
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	for range 4 {
		l.open()
		l.end(2*time.Minute, busy())
	}
	l.open()
	assert.Equal(t, item.NextRound, l.end(79*time.Second, busy()).Decision.Next, "41 s left of a 2-minute round's limit is more than a third")
}

// A round that ended by an outage counts toward neither the rounds nor the
// wall clock, and its turns are not counted.
func TestAnOutageRoundCountsTowardNoBudget(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 1
	l := newLife(t, rc)
	l.open()

	outage := l.end(2*time.Minute, &item.Ending{EndedBy: item.EndedByOutage, DailyCap: 1_000_000})
	l.open()
	normal := l.end(time.Minute, busy())

	assert.Equal(t, 0, outage.Rounds)
	assert.Zero(t, outage.WallClock)
	assert.Empty(t, outage.Turns)
	assert.Equal(t, item.Decision{Next: item.NextRound}, outage.Decision)
	assert.Equal(t, 1, normal.Rounds)
	assert.Equal(t, time.Minute, normal.WallClock)
}

// A wrap-up round counts as it ends: one that revoked its hypothesis while
// another converged uses up a wrap-up itself, so that the run's second
// wrap-up round is its last.
func TestAWrapUpRoundCountsAsItEnds(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()
	first := l.end(time.Minute, converging(other))
	l.open()

	second := l.end(time.Minute, converging(hyp))

	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: other}, first.Decision, "the first wrap-up round counts one")
	assert.Equal(t, item.Decision{Next: item.NextRound}, second.Decision, "the second counts two as it ends")
}

// A wrap-up round that ended by an outage revokes nothing, so it uses up no
// wrap-up, neither when its own end is decided nor later: a hypothesis still
// converged gets another. Here the first wrap-up round revoked the
// convergence, and the second is lost to an outage.
func TestAWrapUpRoundLostToAnOutageUsesUpNoWrapUp(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	lost := converging(hyp)
	lost.EndedBy, lost.Turns = item.EndedByOutage, nil
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()
	require.Equal(t, item.Decision{Next: item.NextRound}, l.end(time.Minute, busy()).Decision, "the first wrap-up revoked it")
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()

	ended := l.end(2*time.Minute, lost)

	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, ended.Decision)
	l.open()
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopConverged}, l.end(time.Minute, converging(hyp)).Decision)
}

// A round the instance that ended it did not see, as one a takeover found
// past its deadline, is never quiet, though the board holds its agents' turn
// ends: its turns are what the board and the deadline say, and the instance
// cannot tell whether its agents had their chance (Decision 9).
func TestARoundTheCoordinatorDidNotSeeIsNeverQuiet(t *testing.T) {
	l := newLife(t, created())
	l.open()
	unseen := quiet()
	unseen.EndedBy, unseen.Unseen = item.EndedByDeadline, true

	ended := l.end(2*time.Minute, unseen)

	assert.True(t, ended.Unseen)
	assert.False(t, ended.Quiet)
	assert.Equal(t, item.Decision{Next: item.NextRound}, ended.Decision)
	assert.Equal(t, 1, ended.Rounds, "it counts toward the budget")
}

// A wrap-up round in which no agent took part as the platform's records tell
// it, because no turn started, so that no agent ended its turn on the board,
// or because the instance that ended it did not see it, though the board holds
// its agents' turn ends, concludes nothing: a hypothesis still converged gets
// another wrap-up round, as after an outage. The unseen one, like one lost to
// an outage, uses up no wrap-up; the one no turn started in does. The last,
// whose agents ended their turns on the board, concludes.
func TestAWrapUpRoundNoAgentTookPartInConcludesNothing(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	unstarted := converging(hyp)
	unstarted.EndedBy, unstarted.Turns, unstarted.TurnEnds = item.EndedByDeadline, all(item.TurnUnstarted, release, runtime), nil
	unseen := converging(hyp)
	unseen.EndedBy, unseen.Unseen = item.EndedByDeadline, true
	l.open()
	l.end(time.Minute, converging(hyp))

	for _, e := range []*item.Ending{unstarted, unseen} {
		l.open()
		ended := l.end(2*time.Minute, e)
		require.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, ended.Decision)
	}
	l.open()

	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopConverged}, l.end(time.Minute, converging(hyp)).Decision)
}

// A wrap-up round none of whose turns started concludes nothing, but uses up
// a wrap-up all the same: while the runner stays out of reach and a
// hypothesis stays converged, the run gets its wrap-up rounds and no more,
// and its budget then stops it, rather than one more wrap-up round every
// time (spec Section 5.6).
func TestAWrapUpRoundNoTurnStartedInUsesUpAWrapUp(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 1
	l := newLife(t, rc)
	unstarted := func() *item.Ending {
		e := converging(hyp)
		e.EndedBy, e.Turns, e.TurnEnds = item.EndedByDeadline, all(item.TurnUnstarted, release, runtime), nil
		return e
	}
	wrapUp := item.Decision{Next: item.NextWrapUp, WrapUp: hyp}
	l.open()
	require.Equal(t, wrapUp, l.end(time.Minute, converging(hyp)).Decision)
	l.open()
	first := l.end(2*time.Minute, unstarted())
	l.open()

	second := l.end(2*time.Minute, unstarted())

	assert.Equal(t, wrapUp, first.Decision, "the first wrap-up round uses up one of two")
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopBudgetExhausted}, second.Decision, "the second uses up the other, and the budget's one round was used")
}

// An interrupted turn never takes part (Decision 9; Plan 7a counted it as
// taking part): it left no turn end on the board. A wrap-up round whose turns
// were all interrupted concludes nothing, though its hypothesis is still
// converged: it uses up a wrap-up, so that the next wrap-up round is the run's
// last, and with the day's cap reached the run stops cut. The turn end of one
// agent is enough for the round to conclude, the other's turn interrupted.
func TestAnInterruptedTurnNeverTakesPart(t *testing.T) {
	for _, c := range []struct {
		turns map[string]item.TurnClass
		ends  []string
		took  bool
	}{
		{all(item.TurnInterrupted, release, runtime), nil, false},
		{map[string]item.TurnClass{release: item.TurnEnded, runtime: item.TurnInterrupted}, []string{release}, true},
	} {
		for _, capped := range []bool{false, true} {
			name := fmt.Sprintf("%v, the cap reached %t", c.turns, capped)
			l := newLife(t, created())
			l.open()
			l.end(time.Minute, converging(hyp))
			l.open()
			e := converging(hyp)
			e.EndedBy, e.Turns, e.TurnEnds = item.EndedByDeadline, c.turns, c.ends
			if capped {
				e.DaySpent = e.DailyCap
			}

			ended := l.end(2*time.Minute, e)

			switch {
			case c.took:
				assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopConverged}, ended.Decision, name)
			case capped:
				assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopCut}, ended.Decision, name)
			case assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, ended.Decision, name):
				l.open()
				again := converging(hyp)
				again.EndedBy, again.Turns, again.TurnEnds = item.EndedByDeadline, all(item.TurnInterrupted, release, runtime), nil
				assert.Equal(t, item.Decision{Next: item.NextRound}, l.end(2*time.Minute, again).Decision, "%s: it used up a wrap-up, so the next was the last", name)
			}
		}
	}
}

// A wrap-up round concludes only when at least one agent ended its own turn on
// the board in it, and it left no failure (Decision 9): a turn end and no
// failure concludes; a turn end with a failed tool call, a failed model call,
// or model calls still in flight once the wait for them ended concludes
// nothing, and so does a round with no turn end, whose turns finished without
// end_turn or were interrupted. Each that concludes nothing uses up a wrap-up,
// so that the next wrap-up round, which concludes nothing either, is the
// run's last.
func TestAWrapUpRoundConcludesOnlyWithATurnEndAndNoFailure(t *testing.T) {
	wrapUp := item.Decision{Next: item.NextWrapUp, WrapUp: hyp}
	for name, c := range map[string]struct {
		change    func(*item.Ending)
		concludes bool
	}{
		"a turn end and no failure":          {func(*item.Ending) {}, true},
		"a turn end and a failed tool call":  {func(e *item.Ending) { e.FailedToolCalls = 1 }, false},
		"a turn end and a failed model call": {func(e *item.Ending) { e.FailedCalls = 1 }, false},
		"a turn end and calls unsettled":     {func(e *item.Ending) { e.Settled = false }, false},
		"no turn end":                        {func(e *item.Ending) { e.Turns[runtime], e.TurnEnds = item.TurnFinished, nil }, false},
	} {
		rc := created()
		rc.Budget.Rounds = 9
		l := newLife(t, rc)
		l.open()
		require.Equal(t, wrapUp, l.end(time.Minute, converging(hyp)).Decision, name)
		l.open()
		e := converging(hyp)
		e.Turns[release], e.TurnEnds = item.TurnInterrupted, []string{runtime}
		c.change(e)

		ended := l.end(time.Minute, e)

		if c.concludes {
			assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopConverged}, ended.Decision, name)
			continue
		}
		assert.Equal(t, wrapUp, ended.Decision, "%s: it concludes nothing", name)
		l.open()
		again := converging(hyp)
		again.Turns, again.TurnEnds = all(item.TurnFinished, release, runtime), nil
		assert.Equal(t, item.Decision{Next: item.NextRound}, l.end(time.Minute, again).Decision, "%s: it used up a wrap-up, so the next was the last", name)
	}
}

// A round's end records the evidence its decision read (Decision 9), in a
// body of version 1 (R12): the agents of the round whose own turn end the
// board holds, in the round's order, the failed model calls and failed tool
// calls it counted, and whether the round's model calls settled. The classes
// the runner reported are kept beside the turn ends, though they disagree.
func TestRoundEndedRecordsTheEvidenceItDecidedOn(t *testing.T) {
	l := newLife(t, created())
	l.open()
	e := quiet()
	e.Turns = map[string]item.TurnClass{release: item.TurnInterrupted, runtime: item.TurnEnded}
	e.TurnEnds = []string{runtime, "capacity-investigator", release}
	e.FailedCalls, e.FailedToolCalls, e.Settled = 2, 3, false

	ended := l.end(time.Minute, e)

	assert.Equal(t, []string{release, runtime}, ended.TurnEnds, "the round's agents with a turn end, in the round's order")
	assert.Equal(t, 2, ended.FailedCalls)
	assert.Equal(t, 3, ended.FailedToolCalls)
	assert.False(t, ended.Settled)
	assert.Equal(t, item.TurnInterrupted, ended.Turns[release], "the runner's class, beside the board's turn end")
	assert.False(t, ended.Quiet, "the failures it recorded")
	raw, err := json.Marshal(item.Body{V: item.Version, RoundEnded: &ended})
	require.NoError(t, err)
	var stored item.Body
	require.NoError(t, json.Unmarshal(raw, &stored))
	assert.Equal(t, 1, stored.V)
	assert.Equal(t, &ended, stored.RoundEnded, "the stored record keeps its evidence")
}

// Wrap-up rounds that use up no wrap-up, those that end unseen, as after a
// late takeover, or by an outage, are capped too: a run opens at most twice
// MaxWrapUps wrap-up rounds in all, after which a converged hypothesis gets
// no more, and the budget and quiet rules decide. Each wrap-up round raises
// the token allowance by one round's share of the budget, so a platform that
// kept failing in wrap-up rounds would otherwise hold the run in them, its
// allowance growing, until the daily cap.
func TestARunOpensAtMostTwiceItsWrapUpsInWrapUpRounds(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	wrapUp := item.Decision{Next: item.NextWrapUp, WrapUp: hyp}
	l.open()
	require.Equal(t, wrapUp, l.end(time.Minute, converging(hyp)).Decision)
	lost := []func(*item.Ending){
		func(e *item.Ending) { e.Unseen = true },
		func(e *item.Ending) { e.EndedBy, e.Turns = item.EndedByOutage, nil },
	}
	var decisions []item.Decision
	for i := range 2 * item.MaxWrapUps {
		require.Equal(t, hyp, l.open().WrapUp, "wrap-up round %d", i+1)
		e := converging(hyp)
		e.EndedBy = item.EndedByDeadline
		lost[i%2](e)
		decisions = append(decisions, l.end(2*time.Minute, e).Decision)
	}

	next := l.open()

	assert.Equal(t, []item.Decision{wrapUp, wrapUp, wrapUp, {Next: item.NextRound}}, decisions)
	assert.Empty(t, next.WrapUp, "a normal round follows the last wrap-up round")
}

// A turn that never started, as when the runner could not be reached all
// round, is neither quiet nor a crash: it left no turn end on the board, and
// it leaves the agent's count of crashes as it was, as a round that ended by
// an outage does.
func TestATurnThatNeverStartedIsNeitherQuietNorACrash(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	crash := func() *item.Ending {
		e := busy()
		e.Turns[runtime] = item.TurnCrashed
		return e
	}
	l.open()
	l.end(time.Second, crash())
	l.open()
	unstarted := quiet()
	unstarted.EndedBy, unstarted.Turns, unstarted.TurnEnds = item.EndedByDeadline, all(item.TurnUnstarted, release, runtime), nil
	never := l.end(2*time.Minute, unstarted)
	l.open()

	second := l.end(time.Second, crash())

	assert.False(t, never.Quiet)
	assert.Empty(t, never.Removed)
	assert.Equal(t, item.Decision{Next: item.NextRound}, never.Decision)
	assert.Equal(t, []string{runtime}, second.Removed)
}

// Two consecutive crashed turns remove an agent after the round; any other
// turn in between resets its count, and a round without counted turns
// leaves it as it was.
func TestTwoConsecutiveCrashesRemoveAnAgent(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	crash := func() *item.Ending {
		e := busy()
		e.Turns[runtime] = item.TurnCrashed
		return e
	}
	l.open()
	first := l.end(time.Second, crash())
	l.open()
	l.end(time.Second, busy())
	l.open()
	l.end(time.Second, crash())
	l.open()
	l.end(time.Second, &item.Ending{EndedBy: item.EndedByOutage, DailyCap: 1_000_000})
	l.open()

	second := l.end(time.Second, crash())
	next := l.open()

	assert.Empty(t, first.Removed)
	assert.Equal(t, []string{runtime}, second.Removed)
	assert.Equal(t, []string{release}, next.Agents)
	refolded, err := item.Fold(l.records)
	require.NoError(t, err)
	assert.Equal(t, []string{release}, refolded.Run.Agents(), "a restart keeps the count: it is in the records")
}

// A turn the coordinator never heard from counts as interrupted. Its class
// does not decide whether the round is quiet, its agent's turn end does
// (Decision 9): the round is quiet when the board holds that turn end, as
// when the runner's report was lost, and not when it holds none.
func TestATurnNeverHeardFromCountsAsInterrupted(t *testing.T) {
	for _, c := range []struct {
		ends  []string
		quiet bool
	}{
		{[]string{release, runtime}, true},
		{[]string{release}, false},
	} {
		l := newLife(t, created())
		l.open()
		e := quiet()
		delete(e.Turns, runtime)
		e.TurnEnds = c.ends

		ended := l.end(time.Minute, e)

		assert.Equal(t, item.TurnInterrupted, ended.Turns[runtime], "the turn ends of %v", c.ends)
		assert.Equal(t, c.quiet, ended.Quiet, "the turn ends of %v", c.ends)
	}
}

// An unavailable turn is no crash (Decision 13): the platform could not serve
// it, so it never counts toward its agent's removal. Two rounds whose turns
// all ended unavailable remove no agent, where two of crashed turns would; a
// crash after them is the agents' first, and an unavailable turn after that
// crash is no second.
func TestAnUnavailableTurnIsNoCrash(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	ending := func(class item.TurnClass) *item.Ending {
		e := busy()
		e.Turns, e.TurnEnds = all(class, release, runtime), nil
		return e
	}
	var ended []item.RoundEnded
	for _, class := range []item.TurnClass{item.TurnUnavailable, item.TurnUnavailable, item.TurnCrashed, item.TurnUnavailable} {
		l.open()
		ended = append(ended, l.end(time.Second, ending(class)))
	}

	next := l.open()

	for i, e := range ended {
		assert.Empty(t, e.Removed, "round %d", i+1)
		assert.Equal(t, item.Decision{Next: item.NextRound}, e.Decision, "round %d", i+1)
	}
	assert.Equal(t, []string{release, runtime}, next.Agents)
}

// A normal round's deadline is its limit, cut to the wall clock left; a
// wrap-up round has its whole limit, and a token allowance of one round's
// share of the budget above the spend at its opening.
func TestTheNextRoundsDeadlineAndBudget(t *testing.T) {
	rc := created()
	rc.Budget.WallClock = 5 * time.Minute
	rc.Budget.Rounds = 9
	l := newLife(t, rc)

	first := l.open()
	l.end(2*time.Minute, busy())
	l.open()
	l.end(2*time.Minute, busy())
	short := l.open()
	e := converging(hyp)
	e.Spent = 89_000
	l.end(30*time.Second, e)
	wrapUp := l.open()

	assert.Equal(t, t0.Add(2*time.Minute), first.Deadline)
	assert.Equal(t, int64(90_000), first.Budget)
	assert.Equal(t, l.now.Add(-30*time.Second).Add(time.Minute), short.Deadline, "a minute of wall clock was left")
	assert.Equal(t, l.now.Add(2*time.Minute), wrapUp.Deadline)
	assert.Equal(t, int64(89_000+10_000), wrapUp.Budget, "one round's share, 90,000 over 9 rounds, above the spend")
	assert.Equal(t, l.now, wrapUp.OpenedAt)
}

// A run's stop record carries the budget its last round says it used.
func TestARunsStopRecordCarriesTheBudgetUsed(t *testing.T) {
	l := newLife(t, created())
	l.open()
	e := quiet()
	e.Spent = 500
	l.end(time.Minute, e)

	stopped := l.s.Run.Stop("hyp-9")

	assert.Equal(t, item.RunStopped{Reason: item.StopQuiet, Rounds: 1, WallClock: time.Minute, Spent: 500, Settled: true, Conclusion: "hyp-9"}, stopped)
}

// A converged run's conclusion is the hypothesis of its wrap-up round, which
// stayed converged through it, its agents having ended their turns on the
// board in it, whatever the board's conclusion is when the run stops, as when
// two hypotheses tie. The fold refuses another.
func TestAConvergedRunConcludesWithItsWrapUpHypothesis(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, converging(hyp, other))
	l.open()
	wrapUp := l.end(time.Minute, converging(other, hyp))
	require.Equal(t, []string{release, runtime}, wrapUp.TurnEnds, "its agents took part")

	stopped := l.s.Run.Stop("")

	assert.Equal(t, hyp, stopped.Conclusion)
	stopped.Conclusion = other
	require.ErrorIs(t, l.try("run-1", item.Body{RunStopped: &stopped}), item.ErrInvalid)
}

// With no round lost to an outage or a takeover, a run stops within its
// budget's rounds and the wrap-up rounds a convergence may add, however its
// turns ended, and whatever its turn ends and failures: every round counts
// toward the budget or uses up a wrap-up, a wrap-up round no agent took part
// in, or that left a failure, included, so that a runner, a model vendor or a
// platform out of reach ends a run by its budget rather than holding it for
// ever (Decision 9). The runs are drawn from fixed seeds, the turn ends apart
// from the classes, with no turn started in a third of their rounds.
func TestARunStopsWithinItsRoundsAndItsWrapUps(t *testing.T) {
	for seed := range uint64(500) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 0))
			rc := created()
			rc.Budget.Rounds = 1 + rng.IntN(4)
			l := newLife(t, rc)
			var spent int64
			stopped := false

			for range rc.Budget.Rounds + item.MaxWrapUps {
				o := l.open()
				spent += 1 + rng.Int64N(9_999)
				limit := int(o.Deadline.Sub(o.OpenedAt) / time.Second)
				d := time.Duration(1+rng.IntN(limit)) * time.Second
				if stopped = l.end(d, drawnEnding(rng, o.Agents, spent)).Decision.Next == item.NextStop; stopped {
					break
				}
			}

			assert.True(t, stopped, "the run went on past its budget's rounds and its wrap-up rounds")
		})
	}
}

// A run stops within its budget's rounds and twice MaxWrapUps wrap-up rounds,
// however its rounds end, those the instance ending them did not see
// included, and wrap-up rounds lost to an outage, which count toward no budget
// and use up no wrap-up. Only an outage of a normal round, which counts toward
// no budget either, may hold a run longer, and the daily cap bounds what it
// spends (spec Section 8.2). The runs are drawn from fixed seeds.
func TestARunStopsWithinItsRoundsAndAllItsWrapUpRounds(t *testing.T) {
	for seed := range uint64(500) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 1))
			rc := created()
			rc.Budget.Rounds = 1 + rng.IntN(4)
			l := newLife(t, rc)
			var spent int64
			stopped := false

			for range rc.Budget.Rounds + 2*item.MaxWrapUps {
				o := l.open()
				spent += 1 + rng.Int64N(9_999)
				limit := int(o.Deadline.Sub(o.OpenedAt) / time.Second)
				d := time.Duration(1+rng.IntN(limit)) * time.Second
				e := drawnEnding(rng, o.Agents, spent)
				switch rng.IntN(3) {
				case 0:
					e.Unseen = true
				case 1:
					if o.WrapUp != "" {
						e.EndedBy, e.Turns = item.EndedByOutage, nil
					}
				}
				if stopped = l.end(d, e).Decision.Next == item.NextStop; stopped {
					break
				}
			}

			assert.True(t, stopped, "the run went on past its budget's rounds and all its wrap-up rounds")
		})
	}
}

// Quiet and taking part follow the platform's records, whatever the classes
// the runner reported (Decision 9). In rounds drawn from fixed seeds, of none
// to three agents, with the turn ends drawn apart from the classes,
// unavailable among them, a round is quiet exactly when the instance saw it,
// it left none of the failures and no entry, at least one agent was in it,
// and every agent of it ended its own turn on the board; and a wrap-up round
// concludes exactly when the instance saw it, it left none of the failures,
// and at least one agent of it ended its own turn on the board. Each round's
// end records the evidence it decided on.
func TestQuietAndTakingPartFollowTheTurnEndsWhateverTheClasses(t *testing.T) {
	pool := []string{"capacity-investigator", release, runtime}
	quietRounds, concluding := 0, 0
	for seed := range uint64(500) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 2))
			rc := created()
			rc.Budget.Rounds = 9
			rc.Agents = []string{}
			for _, a := range pool {
				if rng.IntN(4) != 0 {
					rc.Agents = append(rc.Agents, a)
				}
			}
			l := newLife(t, rc)
			o := l.open()
			e := drawnRound(rng, o.Agents)

			ended := l.end(time.Minute, e)

			assert.Equal(t, quietBy(o.Agents, e), ended.Quiet, "quiet")
			assert.Equal(t, endsOf(o.Agents, e), ended.TurnEnds, "the turn ends it recorded")
			assert.Equal(t, []any{e.Settled, e.FailedCalls, e.FailedToolCalls}, []any{ended.Settled, ended.FailedCalls, ended.FailedToolCalls}, "the failures it recorded")
			if ended.Quiet {
				quietRounds++
			}
			if len(o.Agents) == 0 {
				return
			}
			w := newLife(t, rc)
			w.open()
			require.Equal(t, item.NextWrapUp, w.end(time.Minute, converging(hyp)).Decision.Next)
			wo := w.open()
			we := drawnRound(rng, wo.Agents)
			we.Report.Assessment = converging(hyp).Report.Assessment

			concludes := w.end(time.Minute, we).Decision == item.Decision{Next: item.NextStop, Stop: item.StopConverged}

			assert.Equal(t, tookPartBy(wo.Agents, we), concludes, "concludes")
			if concludes {
				concluding++
			}
		})
	}
	assert.Positive(t, quietRounds, "no round drawn was quiet")
	assert.Positive(t, concluding, "no wrap-up round drawn concluded")
}

// seenWithoutFailure reports whether the instance ending a round saw it, and
// whether it left none of the failures the gateways' records hold, as e
// gives them: the outcome of Decision 9's rule shared by quiet and taking
// part.
func seenWithoutFailure(e *item.Ending) bool {
	return e.EndedBy != item.EndedByOutage && !e.Unseen && e.Settled && e.FailedCalls == 0 && e.FailedToolCalls == 0
}

// quietBy is Decision 9's quiet, of a round of agents that ended as e says.
func quietBy(agents []string, e *item.Ending) bool {
	return seenWithoutFailure(e) && e.Report.Facts.Entries == 0 && len(agents) > 0 && len(endsOf(agents, e)) == len(agents)
}

// tookPartBy is Decision 9's taking part, of a round of agents that ended as
// e says.
func tookPartBy(agents []string, e *item.Ending) bool {
	return seenWithoutFailure(e) && len(endsOf(agents, e)) > 0
}

// endsOf returns, in the round's order, the agents of the round whose own
// turn end e holds.
func endsOf(agents []string, e *item.Ending) []string {
	ends := []string{}
	for _, a := range agents {
		if slices.Contains(e.TurnEnds, a) {
			ends = append(ends, a)
		}
	}
	return ends
}

// classes are the turn classes the package defines.
var classes = []item.TurnClass{
	item.TurnEnded, item.TurnFinished, item.TurnInterrupted, item.TurnRefused, item.TurnVendor, item.TurnUnavailable, item.TurnCrashed,
	item.TurnUnstarted,
}

// drawnTurns draws how each agent's turn ended, of any class the package
// defines, and, apart from its class, whether the agent ended its own turn on
// the board, in three draws of four.
func drawnTurns(rng *rand.Rand, agents []string) (turns map[string]item.TurnClass, ends []string) {
	turns, ends = map[string]item.TurnClass{}, []string{}
	for _, a := range agents {
		turns[a] = classes[rng.IntN(len(classes))]
		if rng.IntN(4) != 0 {
			ends = append(ends, a)
		}
	}
	return turns, ends
}

// drawnFailures draws the failures a round can leave (Decision 9): its model
// calls still in flight once the wait for them ended or not, and failed model
// calls and failed tool calls or none, each in a draw of five.
func drawnFailures(rng *rand.Rand) (settled bool, failedCalls, failedToolCalls int) {
	failed := func() int {
		if rng.IntN(5) == 0 {
			return 1 + rng.IntN(3)
		}
		return 0
	}
	return rng.IntN(5) != 0, failed(), failed()
}

// drawnRound is the end of a round of agents as chance has it, the turn ends
// apart from the classes: by its turns, its deadline or an outage, seen by
// the instance that ended it or not; its turns and its turn ends as
// drawnTurns draws them, now and then with the turn end of a stranger to the
// round; its failures as drawnFailures draws them; and no entry or a few.
func drawnRound(rng *rand.Rand, agents []string) *item.Ending {
	e := quiet()
	e.EndedBy = []item.EndedBy{item.EndedByTurns, item.EndedByDeadline, item.EndedByOutage}[rng.IntN(3)]
	e.Unseen = rng.IntN(4) == 0
	e.Turns, e.TurnEnds = drawnTurns(rng, agents)
	if e.EndedBy == item.EndedByOutage {
		e.Turns = nil
	}
	if rng.IntN(5) == 0 {
		e.TurnEnds = append(e.TurnEnds, "a-stranger")
	}
	e.Settled, e.FailedCalls, e.FailedToolCalls = drawnFailures(rng)
	if rng.IntN(2) == 0 {
		e.Report.Facts.Entries = 1 + rng.IntN(3)
	}
	return e
}

// drawnEnding is the end of a round as chance has it, with no outage and no
// takeover: by its turns or its deadline; its turns and its turn ends as
// drawnTurns draws them, the turn ends apart from the classes (none of the
// turns started, and none ended on the board, in a third of the rounds); its
// failures as drawnFailures draws them; no entry or a few, each of the two
// hypotheses converged or not, and a spend within the run's token budget and
// the day's cap.
func drawnEnding(rng *rand.Rand, agents []string, spent int64) *item.Ending {
	var converged []board.EntryID
	for _, h := range []board.EntryID{hyp, other} {
		if rng.IntN(2) == 0 {
			converged = append(converged, h)
		}
	}
	rng.Shuffle(len(converged), func(i, j int) { converged[i], converged[j] = converged[j], converged[i] })
	e := converging(converged...)
	e.EndedBy = item.EndedByTurns
	if rng.IntN(2) == 0 {
		e.EndedBy = item.EndedByDeadline
	}
	e.Turns, e.TurnEnds = drawnTurns(rng, agents)
	if rng.IntN(3) == 0 {
		e.Turns, e.TurnEnds = all(item.TurnUnstarted, agents...), nil
	}
	e.Settled, e.FailedCalls, e.FailedToolCalls = drawnFailures(rng)
	e.Report.Facts.Entries = rng.IntN(4)
	e.Spent = spent
	e.DaySpent = spent + rng.Int64N(500_000)
	return e
}

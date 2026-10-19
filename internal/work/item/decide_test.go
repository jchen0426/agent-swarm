package item_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// A round whose turns all ended with nothing on the board is quiet, and the
// run stops quiet.
func TestARoundWithNothingOnTheBoardStopsTheRunQuiet(t *testing.T) {
	l := newLife(t, created())
	l.open()

	ended := l.end(time.Minute, quiet())

	assert.True(t, ended.Quiet)
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopQuiet}, ended.Decision)
}

// A round is never quiet when it left an entry, when a turn failed or was
// refused, when no instance held the lease at its deadline, when its model
// calls were still in flight once the wait for them ended, or when a model
// call was noticed in it as refused, failed or of unknown outcome.
func TestARoundWithEntriesOrTroubleIsNeverQuiet(t *testing.T) {
	for name, change := range map[string]func(*item.Ending){
		"an entry":             func(e *item.Ending) { e.Report.Facts.Entries = 1 },
		"a crash":              func(e *item.Ending) { e.Turns[runtime] = item.TurnCrashed },
		"a refused call":       func(e *item.Ending) { e.Turns[runtime] = item.TurnRefused },
		"a vendor failure":     func(e *item.Ending) { e.Turns = all(item.TurnVendor, release, runtime) },
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
	l := newLife(t, created())
	l.open()
	e := quiet()
	e.Turns = map[string]item.TurnClass{release: item.TurnFinished, runtime: item.TurnInterrupted}
	assert.True(t, l.end(time.Minute, e).Quiet, "turns that finished without end_turn, or were interrupted, are quiet")
}

// A converged hypothesis gets one more round, the wrap-up round, in which it
// may be refuted; if it is still converged after it, the run stops
// converged (spec Section 5.6).
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
// past its deadline, is never quiet: its turns are what the board and the
// deadline say, which cannot tell a round no agent ran from one that found
// nothing.
func TestARoundTheCoordinatorDidNotSeeIsNeverQuiet(t *testing.T) {
	l := newLife(t, created())
	l.open()
	unseen := quiet()
	unseen.EndedBy, unseen.Turns, unseen.Report.Facts.Ended, unseen.Unseen = item.EndedByDeadline, all(item.TurnInterrupted, release, runtime), nil, true

	ended := l.end(2*time.Minute, unseen)

	assert.True(t, ended.Unseen)
	assert.False(t, ended.Quiet)
	assert.Equal(t, item.Decision{Next: item.NextRound}, ended.Decision)
	assert.Equal(t, 1, ended.Rounds, "it counts toward the budget")
}

// A wrap-up round in which no agent took part as the coordinator saw it,
// because no turn started, or because the instance that ended it did not see
// it, concludes nothing: a hypothesis still converged gets another wrap-up
// round, as after an outage. The unseen one, like one lost to an outage, uses
// up no wrap-up; the one no turn started in does.
func TestAWrapUpRoundNoAgentTookPartInConcludesNothing(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	unstarted := converging(hyp)
	unstarted.EndedBy, unstarted.Turns = item.EndedByDeadline, all(item.TurnUnstarted, release, runtime)
	unseen := converging(hyp)
	unseen.EndedBy, unseen.Turns, unseen.Unseen = item.EndedByDeadline, all(item.TurnInterrupted, release, runtime), true
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
		e.EndedBy, e.Turns = item.EndedByDeadline, all(item.TurnUnstarted, release, runtime)
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

// A turn took part in its round only if it ended, finished or was
// interrupted. A wrap-up round whose turns were all refused, failed at the
// model vendor, crashed or did not start in time concludes nothing, though its
// hypothesis is still converged: it uses up a wrap-up, and with the day's cap
// reached the run stops cut. One turn that took part is enough for the wrap-up
// round to conclude.
func TestOnlyATurnThatEndedFinishedOrWasInterruptedTookPart(t *testing.T) {
	for _, c := range []struct {
		turns map[string]item.TurnClass
		took  bool
	}{
		{all(item.TurnEnded, release, runtime), true},
		{all(item.TurnFinished, release, runtime), true},
		{all(item.TurnInterrupted, release, runtime), true},
		{map[string]item.TurnClass{release: item.TurnInterrupted, runtime: item.TurnVendor}, true},
		{all(item.TurnRefused, release, runtime), false},
		{all(item.TurnVendor, release, runtime), false},
		{all(item.TurnCrashed, release, runtime), false},
		{all(item.TurnUnstarted, release, runtime), false},
		{map[string]item.TurnClass{release: item.TurnRefused, runtime: item.TurnCrashed}, false},
	} {
		for _, capped := range []bool{false, true} {
			name := fmt.Sprintf("%v, the cap reached %t", c.turns, capped)
			l := newLife(t, created())
			l.open()
			l.end(time.Minute, converging(hyp))
			l.open()
			e := converging(hyp)
			e.EndedBy, e.Turns = item.EndedByDeadline, c.turns
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
				vendor := converging(hyp)
				vendor.EndedBy, vendor.Turns = item.EndedByDeadline, all(item.TurnVendor, release, runtime)
				assert.Equal(t, item.Decision{Next: item.NextRound}, l.end(2*time.Minute, vendor).Decision, "%s: it used up a wrap-up, so the next was the last", name)
			}
		}
	}
}

// A turn that never started, as when the runner could not be reached all
// round, is neither quiet nor a crash: it leaves the agent's count of
// crashes as it was, as a round that ended by an outage does.
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
	unstarted.EndedBy, unstarted.Turns, unstarted.Report.Facts.Ended = item.EndedByDeadline, all(item.TurnUnstarted, release, runtime), nil
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

// A turn the coordinator never heard from counts as interrupted.
func TestATurnNeverHeardFromCountsAsInterrupted(t *testing.T) {
	l := newLife(t, created())
	l.open()
	e := quiet()
	delete(e.Turns, runtime)

	ended := l.end(time.Minute, e)

	assert.Equal(t, item.TurnInterrupted, ended.Turns[runtime])
	assert.True(t, ended.Quiet)
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
// stayed converged through it, whatever the board's conclusion is when the
// run stops, as when two hypotheses tie. The fold refuses another.
func TestAConvergedRunConcludesWithItsWrapUpHypothesis(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, converging(hyp, other))
	l.open()
	l.end(time.Minute, converging(other, hyp))

	stopped := l.s.Run.Stop("")

	assert.Equal(t, hyp, stopped.Conclusion)
	stopped.Conclusion = other
	require.ErrorIs(t, l.try("run-1", item.Body{RunStopped: &stopped}), item.ErrInvalid)
}

// With no round lost to an outage or a takeover, a run stops within its
// budget's rounds and the wrap-up rounds a convergence may add, however its
// turns ended: every round counts toward the budget or uses up a wrap-up, a
// wrap-up round no agent took part in included, so that a runner out of reach
// ends a run by its budget rather than holding it for ever. The runs are
// drawn from fixed seeds, with no turn started in a third of their rounds.
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

// drawnEnding is the end of a round as chance has it, with no outage and no
// takeover: by its turns or its deadline, each turn of any class the package
// defines (none of them started in a third of the rounds), no entry or a few,
// each of the two hypotheses converged or not, and a spend within the run's
// token budget and the day's cap.
func drawnEnding(rng *rand.Rand, agents []string, spent int64) *item.Ending {
	classes := []item.TurnClass{
		item.TurnEnded, item.TurnFinished, item.TurnInterrupted, item.TurnRefused, item.TurnVendor, item.TurnCrashed, item.TurnUnstarted,
	}
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
	e.Turns = map[string]item.TurnClass{}
	for _, a := range agents {
		e.Turns[a] = classes[rng.IntN(len(classes))]
	}
	if rng.IntN(3) == 0 {
		e.Turns = all(item.TurnUnstarted, agents...)
	}
	e.Report.Facts.Entries = rng.IntN(4)
	e.Spent = spent
	e.DaySpent = spent + rng.Int64N(500_000)
	return e
}

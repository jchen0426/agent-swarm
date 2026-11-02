package item_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// A halt with a round open ends the round stopped, then stops the run for the
// halt's reason (Decision 3). The stopped round records what the instance
// knew at the stop, the turn classes and turn ends e gives, an agent without
// a class interrupted, and the run's spend; it reads nothing more, so it has
// no failed call and never settled, though e says otherwise, and it is not
// quiet, converges nothing and removes no agent. A normal round counts toward
// the rounds and the wall clock, to the stop. The run's stop has the round's
// usage, the run's spend at the halt, unsettled, and the board's conclusion.
// Both records fold.
func TestAHaltWithARoundOpenEndsTheRoundStoppedThenStopsTheRun(t *testing.T) {
	rc := created()
	rc.Agents = []string{"alpha", "beta"}
	l := newLife(t, rc)
	o := l.open()
	e := &item.Ending{
		EndedAt: o.OpenedAt.Add(90 * time.Second), Turns: map[string]item.TurnClass{"alpha": item.TurnEnded}, TurnEnds: []string{"alpha"},
		Spent: 500, Report: board.Report{Assessment: board.Assessment{Conclusion: "h-1"}},
	}
	// What the halt does not read: whether the round's model calls settled,
	// its failed calls, and which hypotheses converged.
	e.Settled, e.FailedCalls, e.FailedToolCalls = true, 1, 1
	e.Report.Assessment.Hypotheses = []board.HypothesisView{{ID: "h-1", Converged: true}}

	bodies := l.s.Run.Halt(item.StopClosed, e)

	require.Len(t, bodies, 2)
	assert.Equal(t, &item.RoundEnded{
		Round: 1, EndedBy: item.EndedByStopped, EndedAt: e.EndedAt,
		Turns: map[string]item.TurnClass{"alpha": item.TurnEnded, "beta": item.TurnInterrupted}, TurnEnds: []string{"alpha"},
		Spent: 500, Rounds: 1, WallClock: 90 * time.Second, Decision: item.Decision{Next: item.NextStop, Stop: item.StopClosed},
	}, bodies[0].RoundEnded)
	assert.Equal(t, &item.RunStopped{Reason: item.StopClosed, Rounds: 1, WallClock: 90 * time.Second, Spent: 500, Conclusion: "h-1"}, bodies[1].RunStopped)
	l.write("run-1", bodies[0])
	l.write("run-1", bodies[1])
	assert.Equal(t, item.StopClosed, l.s.Run.Stopped.Reason)
}

// A halt before the run's first round stops the run alone, for the halt's
// reason; a run killed names the kill switch's on-row (Decision 3).
func TestAHaltBeforeTheFirstRoundStopsTheRun(t *testing.T) {
	l := newLife(t, created())

	bodies := l.s.Run.Halt(item.StopKilled, &item.Ending{KilledBy: 3})

	require.Len(t, bodies, 1)
	assert.Equal(t, item.KindRunStopped, bodies[0].Kind())
	assert.Equal(t, &item.RunStopped{Reason: item.StopKilled, Killed: 3}, bodies[0].RunStopped)
	l.write("run-1", bodies[0])
	assert.Equal(t, item.StopKilled, l.s.Run.Stopped.Reason)
}

// A halt after a round that decided its stop, the stop not recorded yet,
// keeps the decided stop, as Run.Stop gives it, whatever the halt's reason:
// the round's own decision is what measurement reads (Decision 3).
func TestAHaltAfterADecidedStopKeepsTheDecidedReason(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, quiet())

	bodies := l.s.Run.Halt(item.StopClosed, &item.Ending{Spent: 999, Report: board.Report{Assessment: board.Assessment{Conclusion: hyp}}})

	want := l.s.Run.Stop(hyp)
	require.Len(t, bodies, 1)
	assert.Equal(t, &want, bodies[0].RunStopped)
	assert.Equal(t, item.StopQuiet, bodies[0].RunStopped.Reason)
	l.write("run-1", bodies[0])
}

// A normal round stopped is never quiet, though every agent ended its own turn
// on the board and left nothing else, and it counts among the rounds used,
// its time to the stop in the wall clock; so does the run's stop (Decision 3).
func TestAStoppedRoundIsNeverQuietAndCountsAsUsed(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, busy())
	o := l.open()
	e := quiet()
	e.EndedAt = o.OpenedAt.Add(40 * time.Second)

	bodies := l.s.Run.Halt(item.StopKilled, e)

	require.Len(t, bodies, 2)
	ended, stopped := bodies[0].RoundEnded, bodies[1].RunStopped
	assert.False(t, ended.Quiet)
	assert.Equal(t, 2, ended.Rounds)
	assert.Equal(t, time.Minute+40*time.Second, ended.WallClock)
	assert.Equal(t, 2, stopped.Rounds)
	assert.Equal(t, time.Minute+40*time.Second, stopped.WallClock)
}

// A run killed leaves its item not staffed (Decision 3): after the halt's
// records, the item awaits its close not staffed, with the on-row that left
// it so, not killed; a run closed leaves it closed.
func TestAKilledRunLeavesItsItemNotStaffed(t *testing.T) {
	l := newLife(t, created())
	l.open()
	for _, b := range l.s.Run.Halt(item.StopKilled, &item.Ending{EndedAt: l.now.Add(time.Minute), KilledBy: 3}) {
		l.write("run-1", b)
	}

	require.ErrorIs(t, l.try("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopKilled}}), item.ErrInvalid)
	require.NoError(t, l.try("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed, UnstaffedBy: 3}}))
	assert.Equal(t, item.StopNotStaffed, item.ItemReason(item.StopKilled))
	assert.Equal(t, item.StopClosed, item.ItemReason(item.StopClosed))
}

package item

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A wrap-up round stopped uses up a wrap-up, and counts among the wrap-up
// rounds opened, as a wrap-up round no agent took part in does; it counts
// toward neither the rounds nor the wall clock, which stay as the round before
// left them, in its end and in the run's stop (Decision 3).
func TestAStoppedWrapUpRoundUsesUpAWrapUp(t *testing.T) {
	opened := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	first := RoundEnded{
		Round: 1, EndedBy: EndedByTurns, EndedAt: opened.Add(time.Minute), Rounds: 1, WallClock: time.Minute,
		Decision: Decision{Next: NextWrapUp, WrapUp: "hyp-1"},
	}
	r := &Run{
		ID:      "run-1",
		Created: RunCreated{Budget: Budget{WallClock: 10 * time.Minute, Tokens: 90_000, Rounds: 3}, RoundLimit: 2 * time.Minute, Agents: []string{"alpha"}},
		Rounds: []Round{
			{Opened: RoundOpened{Round: 1, OpenedAt: opened, Deadline: opened.Add(2 * time.Minute), Agents: []string{"alpha"}}, Ended: &first},
			{Opened: RoundOpened{Round: 2, WrapUp: "hyp-1", OpenedAt: opened.Add(time.Minute), Deadline: opened.Add(3 * time.Minute), Agents: []string{"alpha"}}},
		},
	}

	for _, b := range r.Halt(StopClosed, &Ending{EndedAt: opened.Add(90 * time.Second)}) {
		require.NoError(t, r.apply(&b))
	}

	stopped := r.Rounds[1].Ended
	require.NotNil(t, stopped)
	assert.Equal(t, EndedByStopped, stopped.EndedBy)
	assert.Equal(t, 1, stopped.Rounds, "the rounds as round 1 left them")
	assert.Equal(t, time.Minute, stopped.WallClock, "the wall clock as round 1 left it")
	assert.Equal(t, 1, r.wrapUps(nil), "the stopped wrap-up round used up a wrap-up")
	assert.Equal(t, 1, r.wrapUpRounds())
	assert.Equal(t, 1, r.Stopped.Rounds)
	assert.Equal(t, time.Minute, r.Stopped.WallClock)
}

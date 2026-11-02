package board_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// killSwitch is the notice that the kill switch stopped the run.
func killSwitch() *board.Notice {
	return &board.Notice{Code: board.NoticeKillSwitch}
}

// A kill_switch notice says that the kill switch stopped the run, and names
// nothing (Decision 5, C18): posting refuses one that names an agent, a call
// or a reason. The board's rule for repeats keeps it to one per round: a
// second in the same round repeats the first entry, and the next round takes
// one of its own. Each refusal is on a board of its own: a repeat is found
// before the rules, so a notice that also named an agent would return the
// first unchecked. (The plan's; R21.)
func TestAKillSwitchNoticeNamesNothingAndIsPostedOncePerRound(t *testing.T) {
	f := newFixture(t)

	first, err := f.notify(board.Coordinator, killSwitch())
	require.NoError(t, err)
	again, err := f.notify(board.Coordinator, killSwitch())
	require.NoError(t, err)
	f.round(2)
	next, err := f.notify(board.Coordinator, killSwitch())
	require.NoError(t, err)

	raw, err := json.Marshal(first.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v": 1, "notice": {"code": "kill_switch"}}`, string(raw))
	assert.Equal(t, first.ID, again.ID, "a second in round 1 repeats the first")
	assert.NotEqual(t, first.ID, next.ID, "round 2 takes one of its own")
	assert.Equal(t, 2, next.Round)
	assert.Len(t, f.events, 2)

	for name, n := range map[string]*board.Notice{
		"naming an agent": {Code: board.NoticeKillSwitch, Agent: "release-investigator"},
		"naming a call":   {Code: board.NoticeKillSwitch, CallID: "call-1"},
		"giving a reason": {Code: board.NoticeKillSwitch, Reason: "engine"},
		"naming a tool":   {Code: board.NoticeKillSwitch, Tool: "logs__search"},
	} {
		g := newFixture(t)

		_, err := g.notify(board.Coordinator, n)

		require.ErrorIs(t, err, board.ErrInvalid, name)
		assert.Empty(t, g.events, name)
	}
}

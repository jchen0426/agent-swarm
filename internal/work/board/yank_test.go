package board_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// yanked is the notice that agent pins skill, name@version, a version the
// registry's current revision yanks.
func yanked(agent, skill string) *board.Notice {
	return &board.Notice{Code: board.NoticeSkillYanked, Agent: agent, Skill: skill}
}

// A skill_yanked notice names the agent that pins a yanked skill version, and
// that version, and nothing else (Decision 10, C18): posting refuses one
// without an agent or a skill, and one that gives a tool, a call or a reason.
// It is posted once per run, agent and version, whatever the round: the same
// agent and skill in a later round repeat the first entry, and another
// version, or another agent, gets an entry of its own. Each refusal is on a
// board of its own: a repeat is found before the rules, so a notice of
// alpha's and its skill that also gave a tool would return the first
// unchecked.
func TestASkillYankedNoticeIsPostedOncePerRunAgentAndVersion(t *testing.T) {
	f := newFixture(t)

	first, err := f.notify(board.Coordinator, yanked("alpha", "diagnosis@1.0.0"))
	require.NoError(t, err)
	f.round(2)
	again, err := f.notify(board.Coordinator, yanked("alpha", "diagnosis@1.0.0"))
	require.NoError(t, err)
	version, err := f.notify(board.Coordinator, yanked("alpha", "diagnosis@1.1.0"))
	require.NoError(t, err)
	beta, err := f.notify(board.Coordinator, yanked("beta", "diagnosis@1.0.0"))
	require.NoError(t, err)

	raw, err := json.Marshal(first.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v": 1, "notice": {"code": "skill_yanked", "agent": "alpha", "skill": "diagnosis@1.0.0"}}`, string(raw))
	assert.Equal(t, first.ID, again.ID, "round 2's notice repeats round 1's")
	assert.Equal(t, 1, again.Round, "the first entry, of round 1")
	assert.NotEqual(t, first.ID, version.ID, "another version gets its own")
	assert.NotEqual(t, first.ID, beta.ID, "another agent gets its own")
	assert.Len(t, f.events, 3)

	for name, n := range map[string]*board.Notice{
		"without an agent": yanked("", "diagnosis@1.0.0"),
		"without a skill":  yanked("alpha", ""),
		"with a tool":      {Code: board.NoticeSkillYanked, Agent: "alpha", Skill: "diagnosis@1.0.0", Tool: "logs__search"},
		"with a call":      {Code: board.NoticeSkillYanked, Agent: "alpha", Skill: "diagnosis@1.0.0", CallID: "call-1"},
		"with a reason":    {Code: board.NoticeSkillYanked, Agent: "alpha", Skill: "diagnosis@1.0.0", Reason: "engine"},
	} {
		g := newFixture(t)

		_, err := g.notify(board.Coordinator, n)

		require.ErrorIs(t, err, board.ErrInvalid, name)
		assert.Empty(t, g.events, name)
	}
}

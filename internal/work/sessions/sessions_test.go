package sessions_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/sessions"
)

var _ app.Sessions = (*sessions.Registry)(nil)

var deadline = time.Date(2026, 10, 18, 9, 2, 30, 0, time.UTC)

func issue(t *testing.T, r *sessions.Registry, run board.RunID, agent string) app.Tokens {
	t.Helper()
	return r.Issue(&app.Session{Run: run, Agent: agent, Commit: "c1", Item: map[string]string{"service": "checkout"}})
}

// A session is known to the gateways only once its round opened: from Move
// on, until it ends. Each gateway sees it as its own Session.
func TestASessionIsKnownOnceItsRoundOpened(t *testing.T) {
	r := sessions.New()
	tok := issue(t, r, "run-1", "release-investigator")
	ctx := t.Context()

	_, beforeMCP := r.MCP().Session(ctx, tok.MCP.Reveal())
	_, beforeLLM := r.LLM().Session(ctx, tok.LLM.Reveal())
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 90_000))
	mcp, err := r.MCP().Session(ctx, tok.MCP.Reveal())
	require.NoError(t, err)
	llm, err := r.LLM().Session(ctx, tok.LLM.Reveal())
	require.NoError(t, err)

	require.ErrorIs(t, beforeMCP, actionsapp.ErrNoSession)
	require.ErrorIs(t, beforeLLM, llmapp.ErrNoSession)
	done := mcp.Done
	mcp.Done = nil
	assert.Equal(t, actionsapp.Session{
		Run: "run-1", Agent: "release-investigator", Commit: "c1", Round: 1, Deadline: deadline, Class: limits.Primary,
		Item: map[string]string{"service": "checkout"},
	}, mcp)
	assert.Equal(t, done, llm.Done, "one channel for both gateways")
	llm.Done = nil
	assert.Equal(t, llmapp.Session{
		Run: "run-1", Agent: "release-investigator", Commit: "c1", Round: 1, Deadline: deadline, Class: cost.Primary, Budget: 90_000,
	}, llm)
}

// A token of one gateway is unknown to the other, and a token no session
// holds is unknown to both.
func TestATokenOfOneGatewayIsUnknownToTheOther(t *testing.T) {
	r := sessions.New()
	tok := issue(t, r, "run-1", "release-investigator")
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 1))

	_, mcpAtLLM := r.LLM().Session(t.Context(), tok.MCP.Reveal())
	_, llmAtMCP := r.MCP().Session(t.Context(), tok.LLM.Reveal())
	_, none := r.MCP().Session(t.Context(), "aswm_"+string(make([]byte, 43)))

	require.ErrorIs(t, mcpAtLLM, llmapp.ErrNoSession)
	require.ErrorIs(t, llmAtMCP, actionsapp.ErrNoSession)
	require.ErrorIs(t, none, actionsapp.ErrNoSession)
}

// A session moves from round to round with the same channel; once it ends,
// its tokens are unknown and its channel is closed, which ends its calls.
func TestASessionMovesFromRoundToRoundUntilItEnds(t *testing.T) {
	r := sessions.New()
	tok := issue(t, r, "run-1", "release-investigator")
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 1))
	first, err := r.MCP().Session(t.Context(), tok.MCP.Reveal())
	require.NoError(t, err)

	require.NoError(t, r.Move("run-1", "release-investigator", 2, deadline.Add(time.Minute), 2))
	second, err := r.LLM().Session(t.Context(), tok.LLM.Reveal())
	require.NoError(t, err)
	r.End("run-1", "release-investigator")
	_, gone := r.MCP().Session(t.Context(), tok.MCP.Reveal())

	assert.Equal(t, 2, second.Round)
	assert.Equal(t, int64(2), second.Budget)
	assert.Equal(t, first.Done, second.Done)
	require.ErrorIs(t, gone, actionsapp.ErrNoSession)
	assert.True(t, closed(first.Done))
	require.Error(t, r.Move("run-1", "release-investigator", 3, deadline, 1), "an ended session moves no more")
	r.End("run-1", "release-investigator")
}

// Issuing a session's tokens again, as the coordinator does for each round,
// forgets the earlier ones: a turn of an earlier round still running cannot
// act in the next. The new tokens are known once Move puts the session in
// its round, and the session keeps its channel, which ends the calls of
// either tokens when the session ends.
func TestIssuingAgainGivesFreshTokensAndKeepsTheChannel(t *testing.T) {
	r := sessions.New()
	old := issue(t, r, "run-1", "release-investigator")
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 1))
	before, err := r.MCP().Session(t.Context(), old.MCP.Reveal())
	require.NoError(t, err)

	fresh := issue(t, r, "run-1", "release-investigator")
	_, early := r.MCP().Session(t.Context(), fresh.MCP.Reveal())
	require.NoError(t, r.Move("run-1", "release-investigator", 2, deadline.Add(time.Minute), 1))

	_, err = r.MCP().Session(t.Context(), old.MCP.Reveal())
	require.ErrorIs(t, err, actionsapp.ErrNoSession)
	_, err = r.LLM().Session(t.Context(), old.LLM.Reveal())
	require.ErrorIs(t, err, llmapp.ErrNoSession)
	require.ErrorIs(t, early, actionsapp.ErrNoSession, "known once Move puts the session in its round")
	after, err := r.MCP().Session(t.Context(), fresh.MCP.Reveal())
	require.NoError(t, err)
	assert.Equal(t, 2, after.Round)
	assert.NotEqual(t, old.MCP.Reveal(), fresh.MCP.Reveal())
	assert.False(t, closed(before.Done), "the earlier tokens' calls in flight go on")
	assert.Equal(t, before.Done, after.Done)
	r.End("run-1", "release-investigator")
	assert.True(t, closed(before.Done))
}

// A run's sessions end with it; EndAll ends every session, as a holder that
// lost its lease does.
func TestARunsSessionsEndWithItAndAllEndAtOnce(t *testing.T) {
	r := sessions.New()
	a := issue(t, r, "run-1", "release-investigator")
	b := issue(t, r, "run-1", "runtime-investigator")
	c := issue(t, r, "run-2", "release-investigator")
	for _, s := range []struct {
		run   board.RunID
		agent string
	}{{"run-1", "release-investigator"}, {"run-1", "runtime-investigator"}, {"run-2", "release-investigator"}} {
		require.NoError(t, r.Move(s.run, s.agent, 1, deadline, 1))
	}

	r.EndRun("run-1")
	_, errA := r.MCP().Session(t.Context(), a.MCP.Reveal())
	_, errB := r.LLM().Session(t.Context(), b.LLM.Reveal())
	kept, errC := r.MCP().Session(t.Context(), c.MCP.Reveal())
	r.EndAll()
	_, afterAll := r.MCP().Session(t.Context(), c.MCP.Reveal())

	require.ErrorIs(t, errA, actionsapp.ErrNoSession)
	require.ErrorIs(t, errB, llmapp.ErrNoSession)
	require.NoError(t, errC)
	require.ErrorIs(t, afterAll, actionsapp.ErrNoSession)
	assert.True(t, closed(kept.Done))
}

// Lookups run while the coordinator moves and ends sessions.
func TestLookupsRunWhileSessionsChange(t *testing.T) {
	r := sessions.New()
	tok := issue(t, r, "run-1", "release-investigator")
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 1))
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				_, _ = r.MCP().Session(t.Context(), tok.MCP.Reveal())
				_, _ = r.LLM().Session(t.Context(), tok.LLM.Reveal())
			}
		})
	}
	for round := 2; round < 200; round++ {
		_ = r.Move("run-1", "release-investigator", round, deadline, 1)
	}
	r.EndAll()
	wg.Wait()
}

func closed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

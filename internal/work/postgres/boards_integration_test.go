//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/ids"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

type staticCalls map[string]board.ReadCall

func (s staticCalls) ReadCall(_ context.Context, id string) (board.ReadCall, error) {
	c, ok := s[id]
	if !ok {
		return board.ReadCall{}, app.ErrCallNotFound
	}
	return c, nil
}

type staticRuns map[board.RunID]board.Context

func (s staticRuns) BoardContext(_ context.Context, run board.RunID) (board.Context, error) {
	return s[run], nil
}

// TestBoardsOverPostgres runs the board use cases against the real event
// store: two agents converge, and the conclusion holds after a reload.
func TestBoardsOverPostgres(t *testing.T) {
	ctx := t.Context()
	runtime := board.Author{Kind: board.AuthorAgent, Name: "runtime-investigator"}
	run := board.RunID("boards-over-postgres")
	calls := staticCalls{
		"c1": {ID: "c1", Run: run, Agent: release.Name, Server: "cd", Tool: "get_history", Output: []byte("checkout v42 deployed at 10:02")},
		"c2": {ID: "c2", Run: run, Agent: runtime.Name, Server: "logs", Tool: "search", Output: []byte("NullPointerException in CheckoutHandler")},
	}
	runs := staticRuns{run: {Run: run, Round: 1, Settings: board.Settings{
		Schema:      key.Schema{Fields: []key.Field{{Name: "service", Values: []string{"checkout"}}, {Name: "failureMode", Values: []string{"bad-deploy"}}}},
		Convergence: board.DefaultConvergence,
	}}}
	boards := app.NewBoards(postgres.NewEventStore(pgtest.Pool(t), "tenant-a"), calls, runs, ids.UUIDv7{})

	e1, err := boards.PostEvidence(ctx, run, release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42 deployed"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, run, release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.ID},
	})
	require.NoError(t, err)
	_, err = boards.PostEvidence(ctx, run, runtime, &board.EvidenceInput{
		Claim: "the handler throws", CallID: "c2", Quote: "NullPointerException", Hypothesis: h.ID, Stance: board.Supports,
	})
	require.NoError(t, err)

	a, err := boards.Assess(ctx, run, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, h.ID, a.Conclusion)
	assert.True(t, a.Hypotheses[0].Converged)
}

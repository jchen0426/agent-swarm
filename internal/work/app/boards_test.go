package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

var (
	release = board.Author{Kind: board.AuthorAgent, Name: "release-investigator"}
	runtime = board.Author{Kind: board.AuthorAgent, Name: "runtime-investigator"}
	alice   = board.Author{Kind: board.AuthorHuman, Name: "alice@example.com"}
)

func newBoards(t *testing.T) (*app.Boards, *memStore) {
	t.Helper()
	store := newMemStore()
	calls := memCalls{
		"c1": {ID: "c1", Run: "run-1", Agent: release.Name, Server: "cd", Tool: "get_history", Output: []byte("checkout v42 deployed at 10:02")},
		"c2": {ID: "c2", Run: "run-1", Agent: runtime.Name, Server: "logs", Tool: "search", Output: []byte(`{"msg":"NullPointerException in CheckoutHandler"}`)},
	}
	runs := memRuns{"run-1": {
		Run: "run-1", Round: 1, HumanEntries: true,
		Settings: board.Settings{
			Schema:      key.Schema{Fields: []key.Field{{Name: "service", Values: []string{"checkout"}}, {Name: "failureMode", Values: []string{"bad-deploy"}}}},
			Convergence: board.DefaultConvergence,
		},
	}}
	return app.NewBoards(store, calls, runs, &seqIDs{}), store
}

func TestBoardsReachAConclusion(t *testing.T) {
	ctx := context.Background()
	boards, _ := newBoards(t)

	e1, err := boards.PostEvidence(ctx, "run-1", release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42 deployed"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, "run-1", release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.ID},
	})
	require.NoError(t, err)
	_, err = boards.PostEvidence(ctx, "run-1", runtime, &board.EvidenceInput{
		Claim: "the handler throws", CallID: "c2", Quote: "NullPointerException in CheckoutHandler", Hypothesis: h.ID, Stance: board.Supports,
	})
	require.NoError(t, err)

	a, err := boards.Assess(ctx, "run-1", time.Time{})
	require.NoError(t, err)
	assert.Equal(t, h.ID, a.Conclusion)
	assert.True(t, a.Hypotheses[0].Converged)

	early, err := boards.Assess(ctx, "run-1", h.At)
	require.NoError(t, err)
	assert.False(t, early.Hypotheses[0].Converged, "as of the hypothesis, only its author supported it")
}

func TestPostEvidenceWithUnknownCallFailsProvenance(t *testing.T) {
	boards, store := newBoards(t)

	_, err := boards.PostEvidence(context.Background(), "run-1", release, &board.EvidenceInput{Claim: "x", CallID: "c404", Quote: "v42"})

	require.ErrorIs(t, err, board.ErrProvenance)
	events, _ := store.Load(context.Background(), "run-1")
	assert.Empty(t, events, "a refused post commits nothing")
}

func TestRetractTwiceReturnsTheFirstRetraction(t *testing.T) {
	ctx := context.Background()
	boards, store := newBoards(t)
	e1, err := boards.PostEvidence(ctx, "run-1", release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42"})
	require.NoError(t, err)

	first, err := boards.Retract(ctx, "run-1", release, e1.ID)
	require.NoError(t, err)
	again, err := boards.Retract(ctx, "run-1", release, e1.ID)
	require.NoError(t, err)

	assert.Equal(t, first, again)
	events, _ := store.Load(ctx, "run-1")
	assert.Len(t, events, 2, "the repeat committed nothing")

	_, err = boards.Retract(ctx, "run-1", runtime, e1.ID)
	require.ErrorIs(t, err, board.ErrNotAllowed, "a repeat by someone else is still refused")
}

func TestRuleRejectsAHypothesis(t *testing.T) {
	ctx := context.Background()
	boards, _ := newBoards(t)
	e1, err := boards.PostEvidence(ctx, "run-1", release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, "run-1", release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.ID},
	})
	require.NoError(t, err)

	_, err = boards.Rule(ctx, "run-1", alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h.ID, Reason: &board.Reason{Code: "wrong-cause"}})
	require.NoError(t, err)

	a, err := boards.Assess(ctx, "run-1", time.Time{})
	require.NoError(t, err)
	assert.True(t, a.Hypotheses[0].Rejected)
	assert.Empty(t, a.Conclusion)
}

func TestUnknownRunIsAnError(t *testing.T) {
	boards, _ := newBoards(t)

	_, err := boards.Assess(context.Background(), "run-404", time.Time{})

	require.Error(t, err)
}

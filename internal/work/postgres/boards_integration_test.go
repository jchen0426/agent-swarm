//go:build integration

package postgres_test

import (
	"context"
	"strings"
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

	e1, err := boards.PostEvidence(ctx, run, 1, release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42 deployed"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, run, 1, release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.Event.ID},
	})
	require.NoError(t, err)
	_, err = boards.PostEvidence(ctx, run, 1, runtime, &board.EvidenceInput{
		Claim: "the handler throws", CallID: "c2", Quote: "NullPointerException", Hypothesis: h.Event.ID, Stance: board.Supports,
	})
	require.NoError(t, err)

	a, err := boards.Assess(ctx, run, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, h.Event.ID, a.Conclusion)
	assert.True(t, a.Hypotheses[0].Converged)
}

// Round entries, tasks, claims, completions, proposals and notices survive
// a reload, and a proposal is still found by its repeat. Values from
// servers are stored as text, which jsonb keeps byte for byte, where nested
// JSON would be rewritten.
func TestNewEntriesSurviveAReloadAndStillRepeat(t *testing.T) {
	ctx := t.Context()
	run := board.RunID("new-entries")
	calls := staticCalls{"c1": {ID: "c1", Run: run, Agent: release.Name, Server: "cd", Tool: "get_history", Output: []byte("checkout v42 deployed")}}
	runs := staticRuns{run: {Run: run, Round: 1, Settings: board.Settings{
		Schema:      key.Schema{Fields: []key.Field{{Name: "service", Values: []string{"checkout"}}}},
		Convergence: board.DefaultConvergence,
	}}}
	pool := pgtest.Pool(t)
	boards := app.NewBoards(postgres.NewEventStore(pool, "tenant-a"), calls, runs, ids.UUIDv7{})
	_, err := boards.StartRound(ctx, run, &board.RoundStart{Round: 1})
	require.NoError(t, err)
	e1, err := boards.PostEvidence(ctx, run, 1, release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42 deployed"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, run, 1, release, &board.HypothesisInput{Key: key.Key{"service": "checkout"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.Event.ID}})
	require.NoError(t, err)
	task, err := boards.PostTask(ctx, run, 1, release, &board.TaskInput{Title: "check the pool", Tags: []string{"db"}})
	require.NoError(t, err)
	_, err = boards.ClaimTask(ctx, run, 1, release, task.Event.ID)
	require.NoError(t, err)
	_, err = boards.CompleteTask(ctx, run, 1, release, &board.CompletionInput{Task: task.Event.ID, Evidence: []board.EntryID{e1.Event.ID}})
	require.NoError(t, err)
	p := &board.Proposal{
		Proposer: release.Name, Tool: "deploys.rollback", SchemaHash: "sha256:" + strings.Repeat("a", 64), ArgsHash: "sha256:" + strings.Repeat("b", 64),
		Hypothesis: h.Event.ID, Target: []board.TargetValue{{Arg: "service", Value: "checkout"}},
		Summary: []board.SummaryPart{{Text: "Roll back "}, {Text: "checkout", Value: true}},
		Facts:   map[string]board.Fact{"current": {Values: []string{`{"tag":"v42",  "at":1.50}`}, CallID: "c9"}}, CallID: "c8",
	}
	first, err := boards.Propose(ctx, run, 1, p)
	require.NoError(t, err)
	_, err = boards.Notice(ctx, run, board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name, Tool: "post_task", Rule: "limit"})
	require.NoError(t, err)

	reloaded := app.NewBoards(postgres.NewEventStore(pool, "tenant-a"), calls, runs, ids.UUIDv7{})
	again, err := reloaded.Proposable(ctx, run, 1, p)
	require.NoError(t, err)
	require.NotNil(t, again.Earlier)
	assert.Equal(t, first.Event.ID, again.Earlier.ID)
	assert.Equal(t, p.Facts, again.Earlier.Body.Proposal.Facts, "values from servers kept byte for byte")
	v, err := reloaded.View(ctx, run, release.Name, 0)
	require.NoError(t, err)
	require.Len(t, v.Own, 6, "evidence, hypothesis, task, claim, completion and proposal")
	assert.Equal(t, task.Event.ID, v.Own[4].Body.Completion.Task)
	assert.Equal(t, []string{`{"tag":"v42",  "at":1.50}`}, v.Own[5].Body.Proposal.Facts["current"].Values)

	opened, err := boards.StartRound(ctx, run, &board.RoundStart{Round: 2, WrapUp: h.Event.ID})
	require.NoError(t, err)
	bc, err := app.NewBoards(postgres.NewEventStore(pool, "tenant-a"), calls, runs, ids.UUIDv7{}).Context(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, 2, bc.Round)
	assert.Equal(t, h.Event.ID, bc.WrapUp)
	assert.Equal(t, opened.Event.Seq-1, bc.Snapshot, "the round's snapshot holds every entry before its round entry")
}

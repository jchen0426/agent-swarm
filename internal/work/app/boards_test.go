package app_test

import (
	"context"
	"sync"
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

	e1, err := boards.PostEvidence(ctx, "run-1", 1, release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42 deployed"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, "run-1", 1, release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.Event.ID},
	})
	require.NoError(t, err)
	_, err = boards.PostEvidence(ctx, "run-1", 1, runtime, &board.EvidenceInput{
		Claim: "the handler throws", CallID: "c2", Quote: "NullPointerException in CheckoutHandler", Hypothesis: h.Event.ID, Stance: board.Supports,
	})
	require.NoError(t, err)

	a, err := boards.Assess(ctx, "run-1", time.Time{})
	require.NoError(t, err)
	assert.Equal(t, h.Event.ID, a.Conclusion)
	assert.True(t, a.Hypotheses[0].Converged)

	early, err := boards.Assess(ctx, "run-1", h.Event.At)
	require.NoError(t, err)
	assert.False(t, early.Hypotheses[0].Converged, "as of the hypothesis, only its author supported it")
}

func TestPostEvidenceWithUnknownCallFailsProvenance(t *testing.T) {
	boards, store := newBoards(t)

	_, err := boards.PostEvidence(context.Background(), "run-1", 1, release, &board.EvidenceInput{Claim: "x", CallID: "c404", Quote: "v42"})

	require.ErrorIs(t, err, board.ErrProvenance)
	events, _ := store.Load(context.Background(), "run-1")
	assert.Empty(t, events, "a refused post commits nothing")
}

func TestRetractTwiceReturnsTheFirstRetraction(t *testing.T) {
	ctx := context.Background()
	boards, store := newBoards(t)
	e1, err := boards.PostEvidence(ctx, "run-1", 1, release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42"})
	require.NoError(t, err)

	first, err := boards.Retract(ctx, "run-1", 1, release, e1.Event.ID)
	require.NoError(t, err)
	again, err := boards.Retract(ctx, "run-1", 1, release, e1.Event.ID)
	require.NoError(t, err)

	assert.Equal(t, first.Event, again.Event)
	assert.True(t, again.Repeat)
	events, _ := store.Load(ctx, "run-1")
	assert.Len(t, events, 2, "the repeat committed nothing")

	_, err = boards.Retract(ctx, "run-1", 1, runtime, e1.Event.ID)
	require.ErrorIs(t, err, board.ErrNotAllowed, "a repeat by someone else is still refused")
}

func TestRuleRejectsAHypothesis(t *testing.T) {
	ctx := context.Background()
	boards, _ := newBoards(t)
	e1, err := boards.PostEvidence(ctx, "run-1", 1, release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, "run-1", 1, release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.Event.ID},
	})
	require.NoError(t, err)

	_, err = boards.Rule(ctx, "run-1", alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h.Event.ID, Reason: &board.Reason{Code: "wrong-cause"}})
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

// Claims are atomic: of two agents that claim one task at once, one holds it
// and the other is told the task is held.
func TestTwoAgentsThatClaimOneTaskAtOnceGetOneHolder(t *testing.T) {
	ctx := context.Background()
	boards, store := newBoards(t)
	task, err := boards.PostTask(ctx, "run-1", 1, release, &board.TaskInput{Title: "check the pool"})
	require.NoError(t, err)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, agent := range []board.Author{release, runtime} {
		wg.Go(func() { _, errs[i] = boards.ClaimTask(ctx, "run-1", 1, agent, task.Event.ID) })
	}
	wg.Wait()

	held := 0
	for _, err := range errs {
		if err == nil {
			held++
			continue
		}
		require.ErrorIs(t, err, board.ErrHeld)
	}
	assert.Equal(t, 1, held)
	events, _ := store.Load(ctx, "run-1")
	assert.Len(t, events, 2, "the task and one claim")
}

// A repeated post is marked as a repeat and commits nothing.
func TestARepeatedPostIsMarkedAndCommitsNothing(t *testing.T) {
	ctx := context.Background()
	boards, store := newBoards(t)
	in := &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42 deployed"}
	first, err := boards.PostEvidence(ctx, "run-1", 1, release, in)
	require.NoError(t, err)

	again, err := boards.PostEvidence(ctx, "run-1", 1, release, in)

	require.NoError(t, err)
	assert.True(t, again.Repeat)
	assert.False(t, first.Repeat)
	assert.Equal(t, first.Event, again.Event)
	events, _ := store.Load(ctx, "run-1")
	assert.Len(t, events, 1)
}

// A post made in a round that ended is refused, never counted in the round
// after: a late end_turn would otherwise end the agent's next turn.
func TestAPostFromARoundThatEndedIsRefused(t *testing.T) {
	ctx := context.Background()
	runs := memRuns{"run-1": {Run: "run-1", Round: 2, Settings: board.Settings{Convergence: board.DefaultConvergence}}}
	boards := app.NewBoards(newMemStore(), memCalls{}, runs, &seqIDs{})

	_, err := boards.EndTurn(ctx, "run-1", 1, release)
	require.ErrorIs(t, err, board.ErrRoundEnded)
	ended, err := boards.EndTurn(ctx, "run-1", 2, release)
	require.NoError(t, err)
	assert.Equal(t, 2, ended.Event.Round)
}

// The run is read before a post takes the run's lock, never while it holds
// it: a Runs port on the board's own database pool would otherwise wait for
// a connection the posts queued behind the lock hold.
func TestTheRunIsReadOutsideTheBoardsLock(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	runs := unlockedRuns{memRuns: memRuns{"run-1": {Run: "run-1", Round: 1, Settings: board.Settings{Convergence: board.DefaultConvergence}}}, store: store}
	boards := app.NewBoards(store, memCalls{}, runs, &seqIDs{})

	_, err := boards.EndTurn(ctx, "run-1", 1, release)
	require.NoError(t, err)
	_, err = boards.Notice(ctx, "run-1", board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name})
	require.NoError(t, err)
}

// A post's round is checked under the run's lock, against the board's round
// entries: a post that waited for the lock while the coordinator opened the
// next round is refused, never counted in the round that ended.
func TestAPostThatWaitedWhileANewRoundOpenedIsRefused(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	runs := memRuns{"run-1": {Run: "run-1", Round: 1, Settings: board.Settings{Convergence: board.DefaultConvergence}}}
	boards := app.NewBoards(store, memCalls{}, runs, &seqIDs{})
	_, err := boards.StartRound(ctx, "run-1", &board.RoundStart{Round: 1})
	require.NoError(t, err)
	store.locked = func(run board.RunID) {
		store.locked = nil
		history := store.events[run]
		last := history[len(history)-1]
		store.events[run] = append(history, board.Event{
			Seq: last.Seq + 1, ID: "round-2", Run: run, Round: 2, Author: board.Coordinator, At: last.At.Add(time.Second),
			Body: board.Body{Round: &board.RoundStart{Round: 2}},
		})
	}

	_, err = boards.PostTask(ctx, "run-1", 1, release, &board.TaskInput{Title: "late"})

	require.ErrorIs(t, err, board.ErrRoundEnded)
	bc, err := boards.Context(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, 2, bc.Round, "the board's round entries say the round, whatever the run said before")
	assert.Equal(t, int64(1), bc.Snapshot)
}

// The coordinator posts the evidence an item opens with once, whatever
// happens after: a post repeated after a crash returns the first.
func TestPlatformEvidenceIsPostedOnce(t *testing.T) {
	ctx := context.Background()
	boards, store := newBoards(t)
	in := &board.PlatformEvidenceInput{Claim: "Opened by a person: checkout fails", Ref: "rec-1", Source: "person"}

	first, err := boards.PostPlatformEvidence(ctx, "run-1", in)
	require.NoError(t, err)
	again, err := boards.PostPlatformEvidence(ctx, "run-1", in)
	require.NoError(t, err)

	assert.Equal(t, board.Coordinator, first.Event.Author)
	assert.True(t, again.Repeat)
	assert.Equal(t, first.Event.ID, again.Event.ID)
	events, _ := store.Load(ctx, "run-1")
	assert.Len(t, events, 1)
}

// Once the coordinator stops a run, its board takes no post or notice, and
// people's rulings still land; the round's report says it stopped.
func TestAStoppedRunsBoardTakesRulingsOnly(t *testing.T) {
	ctx := context.Background()
	boards, _ := newBoards(t)
	e1, err := boards.PostEvidence(ctx, "run-1", 1, release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, "run-1", 1, release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.Event.ID},
	})
	require.NoError(t, err)

	stop, err := boards.Stop(ctx, "run-1", &board.StopInput{Reason: "quiet", Rounds: 1})
	require.NoError(t, err)
	again, err := boards.Stop(ctx, "run-1", &board.StopInput{Reason: "quiet", Rounds: 1})
	require.NoError(t, err)

	assert.Equal(t, []board.EntryID{h.Event.ID}, stop.Event.Body.Stop.Hypotheses)
	assert.True(t, again.Repeat)
	_, err = boards.EndTurn(ctx, "run-1", 1, release)
	require.ErrorIs(t, err, board.ErrStopped)
	_, err = boards.Notice(ctx, "run-1", board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name})
	require.ErrorIs(t, err, board.ErrStopped)
	_, err = boards.Rule(ctx, "run-1", alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h.Event.ID, Reason: &board.Reason{Code: "wrong-cause"}})
	require.NoError(t, err)
	report, err := boards.Report(ctx, "run-1", 1)
	require.NoError(t, err)
	assert.True(t, report.Stopped)
	assert.Equal(t, 3, report.Facts.Entries, "the evidence, the hypothesis and the ruling, made in the run's round")
}

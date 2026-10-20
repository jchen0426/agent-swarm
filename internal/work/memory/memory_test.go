package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

func draft(id string) board.Draft {
	return board.Draft{ID: board.EntryID(id), Run: "r1", Round: 1, Author: board.Author{Kind: board.AuthorAgent, Name: "a"},
		Body: board.Body{Retraction: &board.Retraction{Evidence: "e0"}}}
}

func TestAppendNumbersEventsAndNeverGoesBackInTime(t *testing.T) {
	clock := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	store := memory.NewStore(func() time.Time { return clock })
	ctx := context.Background()

	first, err := store.Append(ctx, "r1", func(history []board.Event) ([]board.Draft, error) {
		assert.Empty(t, history)
		return []board.Draft{draft("e1"), draft("e2")}, nil
	})
	require.NoError(t, err)
	clock = clock.Add(-time.Hour) // a clock that steps back
	second, err := store.Append(ctx, "r1", func(history []board.Event) ([]board.Draft, error) {
		assert.Len(t, history, 2)
		return []board.Draft{draft("e3")}, nil
	})
	require.NoError(t, err)

	events, err := store.Load(ctx, "r1")
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, []int64{1, 2, 3}, []int64{events[0].Seq, events[1].Seq, events[2].Seq})
	assert.True(t, first[1].At.After(first[0].At))
	assert.True(t, second[0].At.After(first[1].At), "commit times never go backwards within a run")
	assert.Equal(t, board.RunID("r1"), events[2].Run)
}

func TestAFailedDecisionCommitsNothing(t *testing.T) {
	store := memory.NewStore(time.Now)
	refused := errors.New("refused")

	_, err := store.Append(context.Background(), "r1", func([]board.Event) ([]board.Draft, error) { return nil, refused })

	require.ErrorIs(t, err, refused)
	events, err := store.Load(context.Background(), "r1")
	require.NoError(t, err)
	assert.Empty(t, events)
}

// A run whose board holds an event of a later version fails its load, and
// every append to it, with board.ErrVersion, as in the Postgres store: this
// build never acts on a board it cannot read.
func TestARunWithAnEventOfALaterVersionFailsItsLoadAndItsAppends(t *testing.T) {
	store := memory.NewStore(time.Now)
	ctx := context.Background()
	later := draft("e2")
	later.Body = board.Body{V: board.Version + 1}
	_, err := store.Append(ctx, "r1", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{draft("e1"), later}, nil
	})
	require.NoError(t, err)

	_, err = store.Load(ctx, "r1")

	require.ErrorIs(t, err, board.ErrVersion)
	_, err = store.Append(ctx, "r1", func([]board.Event) ([]board.Draft, error) { return []board.Draft{draft("e3")}, nil })
	require.ErrorIs(t, err, board.ErrVersion, "no append follows it")
	other, err := store.Load(ctx, "r2")
	require.NoError(t, err, "other runs are read as before")
	assert.Empty(t, other)
}

func TestCallsAndRunsAreFoundByID(t *testing.T) {
	calls := memory.NewCalls()
	calls.Add(&board.ReadCall{ID: "c1", Run: "r1", Agent: "a", Tool: "get_changes", Output: []byte("x")})
	runs := memory.NewRuns()
	runs.Add(&board.Context{Run: "r1", Round: 1})
	ctx := context.Background()

	call, err := calls.ReadCall(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, "get_changes", call.Tool)
	_, err = calls.ReadCall(ctx, "c2")
	require.ErrorIs(t, err, app.ErrCallNotFound)
	bc, err := runs.BoardContext(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 1, bc.Round)
	_, err = runs.BoardContext(ctx, "r2")
	require.Error(t, err)
}

func TestAllListsTheCallsInTheOrderTheyWereAdded(t *testing.T) {
	calls := memory.NewCalls()
	for _, id := range []string{"b-c1", "a-c1", "b-c2"} {
		calls.Add(&board.ReadCall{ID: id})
	}
	calls.Add(&board.ReadCall{ID: "a-c1", Tool: "again"})

	var ids []string
	for _, c := range calls.All() {
		ids = append(ids, c.ID)
	}

	assert.Equal(t, []string{"b-c1", "a-c1", "b-c2"}, ids)
	assert.Equal(t, "again", calls.All()[1].Tool, "adding an id again replaces the call in its place")
}

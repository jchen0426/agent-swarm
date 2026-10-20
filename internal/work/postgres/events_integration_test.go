//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/ids"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var release = board.Author{Kind: board.AuthorAgent, Name: "release-investigator"}

// evidenceDraft is an agent's evidence, of this build's version, as the
// board's rules draft it.
func evidenceDraft(run board.RunID, id board.EntryID, quote string) board.Draft {
	return board.Draft{ID: id, Run: run, Round: 1, Author: release, Body: board.Body{V: board.Version, Evidence: &board.Evidence{
		Claim:      "saw " + quote,
		Provenance: board.Provenance{Kind: board.FromReadCall, CallID: "c-" + string(id), Quote: quote, Source: "cd/get_history"},
	}}}
}

// insertEventRaw writes a board event by hand, as a store holds a row that no
// append of this build wrote: its body is the JSON given.
func insertEventRaw(t *testing.T, pool *pgxpool.Pool, tenant string, run board.RunID, seq int64, body string) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `INSERT INTO board_events (tenant_id, run_id, seq, id, round, kind, author_kind, author_name, committed_at, body)
		VALUES ($1, $2, $3, $4, 1, 'verdict', 'platform', 'coordinator', now(), $5::jsonb)`,
		tenant, string(run), seq, fmt.Sprintf("raw-%s-%d", run, seq), body)
	require.NoError(t, err)
}

// A board event of a later version, as a later build writes it, fails its
// run's load wrapped with board.ErrVersion, whatever its body holds, and so
// does an append to its run, which reads the run's events first: this build
// never acts on a board it cannot read (Decision 10).
func TestAnEventOfALaterVersionFailsItsRunsLoadWithErrVersion(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewEventStore(pool, "events-later")
	run := board.RunID("run-later")
	_, err := store.Append(t.Context(), run, func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft(run, "lt-1", "v42")}, nil
	})
	require.NoError(t, err)
	insertEventRaw(t, pool, "events-later", run, 2, `{"v": 2, "verdict": "a shape this build does not know"}`)

	_, err = store.Load(t.Context(), run)

	require.ErrorIs(t, err, board.ErrVersion)
	require.NotErrorIs(t, err, board.ErrInvalid)
	_, err = store.Append(t.Context(), run, func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft(run, "lt-3", "v43")}, nil
	})
	require.ErrorIs(t, err, board.ErrVersion, "no append follows it")
}

// A board event whose body does not decode is invalid, as a work record that
// does not decode is: Load reports it wrapped with board.ErrInvalid, and not
// as a later version, so that a takeover tells a corrupt row from a read that
// failed, and carries on past its item. Such is a body whose version is below
// 1, and one of this version that does not decode.
func TestAnEventThatDoesNotDecodeIsInvalid(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewEventStore(pool, "events-corrupt")
	for run, body := range map[board.RunID]string{
		"run-version-zero": `{"v": 0, "task": {"title": "check the pool"}}`,
		"run-not-a-task":   `{"v": 1, "task": "not a task"}`,
	} {
		insertEventRaw(t, pool, "events-corrupt", run, 1, body)

		_, err := store.Load(t.Context(), run)

		require.ErrorIs(t, err, board.ErrInvalid, body)
		assert.NotErrorIs(t, err, board.ErrVersion, body)
	}
}

// A read of a run's events that fails is not a corrupt row: Load does not
// report it as invalid, so that a takeover that could not read an item's board
// refuses the lease instead of carrying on past the item.
func TestAFailedReadOfEventsIsNotInvalid(t *testing.T) {
	store := postgres.NewEventStore(pgtest.Pool(t), "events-read")
	ended, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := store.Load(ended, "run-read")

	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, board.ErrInvalid)
}

func TestAppendAndLoadRoundTrip(t *testing.T) {
	ctx := t.Context()
	store := postgres.NewEventStore(pgtest.Pool(t), "tenant-a")
	run := board.RunID("round-trip")

	committed, err := store.Append(ctx, run, func(history []board.Event) ([]board.Draft, error) {
		assert.Empty(t, history)
		return []board.Draft{evidenceDraft(run, "rt-1", "v42"), evidenceDraft(run, "rt-2", "v43")}, nil
	})
	require.NoError(t, err)
	require.Len(t, committed, 2)
	assert.Equal(t, []int64{1, 2}, []int64{committed[0].Seq, committed[1].Seq})
	assert.False(t, committed[1].At.Before(committed[0].At))

	loaded, err := store.Load(ctx, run)
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	assert.Equal(t, committed[0].ID, loaded[0].ID)
	assert.Equal(t, release, loaded[0].Author)
	assert.Equal(t, run, loaded[0].Run)
	assert.Equal(t, committed[0].Body, loaded[0].Body)
	assert.True(t, committed[0].At.Equal(loaded[0].At))
}

func TestAppendCommitsNothingWhenDecideFails(t *testing.T) {
	ctx := t.Context()
	store := postgres.NewEventStore(pgtest.Pool(t), "tenant-a")
	run := board.RunID("decide-fails")
	refused := errors.New("refused by the rules")

	_, err := store.Append(ctx, run, func([]board.Event) ([]board.Draft, error) { return nil, refused })

	require.ErrorIs(t, err, refused)
	loaded, err := store.Load(ctx, run)
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

func TestConcurrentAppendsGetDistinctSequenceNumbers(t *testing.T) {
	ctx := t.Context()
	store := postgres.NewEventStore(pgtest.Pool(t), "tenant-a")
	run := board.RunID("concurrent")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, err := store.Append(ctx, run, func(history []board.Event) ([]board.Draft, error) {
				id := board.EntryID("cc-" + string(rune('a'+i)))
				return []board.Draft{evidenceDraft(run, id, "v42")}, nil
			})
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	loaded, err := store.Load(ctx, run)
	require.NoError(t, err)
	require.Len(t, loaded, 20)
	for i := range loaded {
		assert.Equal(t, int64(i+1), loaded[i].Seq)
		if i > 0 {
			assert.False(t, loaded[i].At.Before(loaded[i-1].At), "commit times never go backwards")
		}
	}
}

func TestTenantsAndRunsAreSeparate(t *testing.T) {
	ctx := t.Context()
	pool := pgtest.Pool(t)
	a := postgres.NewEventStore(pool, "tenant-a")
	b := postgres.NewEventStore(pool, "tenant-b")

	_, err := a.Append(ctx, "shared-run", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft("shared-run", "sep-1", "v42")}, nil
	})
	require.NoError(t, err)

	other, err := b.Load(ctx, "shared-run")
	require.NoError(t, err)
	assert.Empty(t, other)
	otherRun, err := a.Load(ctx, "another-run")
	require.NoError(t, err)
	assert.Empty(t, otherRun)
}

func TestAppendRefusesADraftForAnotherRun(t *testing.T) {
	store := postgres.NewEventStore(pgtest.Pool(t), "tenant-a")

	_, err := store.Append(t.Context(), "run-x", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft("run-y", "wrong-run-1", "v42")}, nil
	})

	require.ErrorContains(t, err, "belongs to run run-y")
}

func TestConcurrentAppendsUnderARepeatableReadDefault(t *testing.T) {
	// Under repeatable read, a transaction's snapshot predates its wait for the
	// run lock, so it would miss the entries of the writer it waited for. The
	// store must serialize appends whatever the database's default isolation is.
	ctx := t.Context()
	pool := pgtest.PoolWithParams(t, map[string]string{"default_transaction_isolation": "repeatable read"})
	store := postgres.NewEventStore(pool, "tenant-a")
	run := board.RunID("repeatable-read-" + ids.UUIDv7{}.NewID())

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, err := store.Append(ctx, run, func([]board.Event) ([]board.Draft, error) {
				return []board.Draft{evidenceDraft(run, board.EntryID(fmt.Sprintf("%s-%d", run, i)), "v42")}, nil
			})
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	loaded, err := store.Load(ctx, run)
	require.NoError(t, err)
	assert.Len(t, loaded, 20)
}

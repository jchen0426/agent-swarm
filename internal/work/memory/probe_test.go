package memory_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

var (
	_ app.Probe       = (*memory.Probe)(nil)
	_ app.EntryReader = (*memory.Store)(nil)
)

// The memory probe answers as the Postgres one (design D2; R138): each
// item's latest record, the run its latest run_created names, and that run's
// latest entry; an item queued has no run, an item whose run has no entry no
// entry, and an id with no record is absent. Fails first: a build failure.
func TestTheMemoryProbeAnswersAsThePostgresOne(t *testing.T) {
	records := memory.NewRecords(time.Now)
	events := memory.NewStore(time.Now)
	add := func(id item.ID, run board.RunID, b item.Body) item.Record {
		t.Helper()
		committed, err := records.Append(t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
			return []item.Draft{{ID: fmt.Sprintf("%s-%d", id, len(history)+1), Item: id, Incident: "inc", Run: run, Body: body(b)}}, nil
		})
		require.NoError(t, err)
		return committed[0]
	}
	started := item.Body{Started: &item.Started{Commit: "c1"}}
	created := item.Body{RunCreated: &item.RunCreated{Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: board.Settings{Convergence: board.DefaultConvergence}}}
	queued := add("item-queued", "", opening)
	add("item-run", "", opening)
	add("item-run", "", started)
	ran := add("item-run", "run-1", created)
	add("item-board", "", opening)
	add("item-board", "", started)
	add("item-board", "run-2", created)
	latest := add("item-board", "run-2", item.Body{RoundOpened: &item.RoundOpened{Round: 1, Agents: []string{"a"}}})
	_, err := events.Append(t.Context(), "run-2", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{draft("e1"), draft("e2")}, nil
	})
	require.NoError(t, err)

	got, err := memory.NewProbe(records, events).Versions(t.Context(), []item.ID{"item-queued", "item-run", "item-board", "item-unknown"})

	require.NoError(t, err)
	assert.Equal(t, map[item.ID]app.BoardVersion{
		"item-queued": {Record: app.Mark{ID: queued.ID, Seq: 1}},
		"item-run":    {Record: app.Mark{ID: ran.ID, Seq: 3}, Run: "run-1"},
		"item-board":  {Record: app.Mark{ID: latest.ID, Seq: 4}, Run: "run-2", Board: app.Mark{ID: "e2", Seq: 2}},
	}, got)
}

// An entry is read by its id, of any run (board_events' UNIQUE (tenant_id,
// id)): found, with its run and seq; absent; and one of a later version
// fails wrapping board.ErrVersion, as the Postgres store's read does. Fails
// first: a build failure.
func TestAnEntryIsReadByItsID(t *testing.T) {
	store := memory.NewStore(time.Now)
	_, err := store.Append(t.Context(), "r1", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{draft("e1"), draft("e2")}, nil
	})
	require.NoError(t, err)
	later := draft("e-later")
	later.Run, later.Body = "r2", board.Body{V: board.Version + 1}
	_, err = store.Append(t.Context(), "r2", func([]board.Event) ([]board.Draft, error) { return []board.Draft{later}, nil })
	require.NoError(t, err)

	found, ok, err := store.Entry(t.Context(), "e2")
	require.NoError(t, err)
	require.True(t, ok)
	_, absent, err := store.Entry(t.Context(), "e9")
	require.NoError(t, err)
	_, _, laterErr := store.Entry(t.Context(), "e-later")

	assert.Equal(t, board.EntryID("e2"), found.ID)
	assert.Equal(t, board.RunID("r1"), found.Run)
	assert.Equal(t, int64(2), found.Seq)
	assert.Equal(t, draft("e2").Body, found.Body)
	assert.False(t, absent, "no run holds it")
	require.ErrorIs(t, laterErr, board.ErrVersion)
}

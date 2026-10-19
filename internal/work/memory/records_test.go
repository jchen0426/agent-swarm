package memory_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

var _ app.Records = (*memory.Records)(nil)

func body(b item.Body) item.Body {
	b.V = item.Version
	return b
}

// An item's records come back numbered from 1, in commit order, with times
// that never go back; a decision that fails commits nothing.
func TestRecordsAreNumberedAndNeverGoBackInTime(t *testing.T) {
	clock := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	records := memory.NewRecords(func() time.Time { return clock })
	opened := item.Draft{ID: "r1", Item: "item-1", Incident: "inc-1", Body: body(item.Body{Opened: &item.Opened{Type: "incident"}})}

	first, err := records.Append(t.Context(), "item-1", func(history []item.Record) ([]item.Draft, error) {
		assert.Empty(t, history)
		return []item.Draft{opened}, nil
	})
	require.NoError(t, err)
	clock = clock.Add(-time.Hour)
	_, err = records.Append(t.Context(), "item-1", func(history []item.Record) ([]item.Draft, error) {
		assert.Len(t, history, 1)
		return nil, errors.New("refused")
	})
	require.Error(t, err)
	second, err := records.Append(t.Context(), "item-1", func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "r2", Item: "item-1", Incident: "inc-1", Body: body(item.Body{Started: &item.Started{}})}}, nil
	})
	require.NoError(t, err)

	loaded, err := records.Load(t.Context(), "item-1")
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, []int64{loaded[0].Seq, loaded[1].Seq})
	assert.True(t, second[0].At.After(first[0].At))
	_, err = records.Append(t.Context(), "item-1", func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "r3", Item: "item-2"}}, nil
	})
	require.Error(t, err, "a record of another item")
}

// The items that do not await their close are active, and a run's board
// context comes from its creation record.
func TestActiveItemsAndRunsComeFromTheRecords(t *testing.T) {
	records := memory.NewRecords(time.Now)
	add := func(id item.ID, run board.RunID, b item.Body) {
		_, err := records.Append(t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
			return []item.Draft{{ID: string(id) + "-" + string(rune('a'+len(history))), Item: id, Incident: "inc", Run: run, Body: body(b)}}, nil
		})
		require.NoError(t, err)
	}
	add("item-b", "", item.Body{Opened: &item.Opened{}})
	add("item-a", "", item.Body{Opened: &item.Opened{}})
	add("item-a", "run-a", item.Body{RunCreated: &item.RunCreated{Class: item.ClassPrimary, Mode: item.ModeVisible,
		Settings: board.Settings{Convergence: board.DefaultConvergence}}})
	add("item-c", "", item.Body{Opened: &item.Opened{}})
	add("item-c", "", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})

	active, err := records.Active(t.Context())
	require.NoError(t, err)
	bc, err := records.BoardContext(t.Context(), "run-a")
	require.NoError(t, err)
	_, unknown := records.BoardContext(t.Context(), "run-404")

	assert.Equal(t, []item.ID{"item-a", "item-b"}, active)
	assert.Equal(t, board.Context{Run: "run-a", HumanEntries: true, Settings: board.Settings{Convergence: board.DefaultConvergence}}, bc)
	require.Error(t, unknown)
}

// Active lists the opened items not awaiting their close: not an empty item, nor one of a later version.
func TestActiveItemsAreOpenedAndNotAwaitingClose(t *testing.T) {
	records := memory.NewRecords(time.Now)
	opened := item.Draft{ID: "a1", Item: "item-a", Incident: "inc", Body: body(item.Body{Opened: &item.Opened{Type: "incident"}})}
	_, err := records.Append(t.Context(), "item-a", func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{opened}, nil
	})
	require.NoError(t, err)
	_, err = records.Append(t.Context(), "item-empty", func([]item.Record) ([]item.Draft, error) {
		return nil, nil
	})
	require.NoError(t, err)
	later := item.Draft{ID: "l1", Item: "item-later", Incident: "inc", Body: item.Body{V: item.Version + 1}}
	_, err = records.Append(t.Context(), "item-later", func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{later}, nil
	})
	require.NoError(t, err)

	active, err := records.Active(t.Context())
	require.NoError(t, err)
	loaded, err := records.Load(t.Context(), "item-empty")
	require.NoError(t, err)

	assert.Equal(t, []item.ID{"item-a"}, active)
	assert.Empty(t, loaded)
}

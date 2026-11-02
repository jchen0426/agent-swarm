package memory_test

import (
	"errors"
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

var _ app.Records = (*memory.Records)(nil)

//nolint:gocritic // hugeParam: a test builds each body as a value, and body sets its copy's version.
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

// The items that are not closed are active, an item awaiting its close among
// them, and a run's board context comes from its creation record.
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

	assert.Equal(t, []item.ID{"item-a", "item-b", "item-c"}, active)
	assert.Equal(t, board.Context{Run: "run-a", HumanEntries: true, Settings: board.Settings{Convergence: board.DefaultConvergence}}, bc)
	require.Error(t, unknown)
}

// Active lists the opened items that are not closed: not an empty item, nor one of a later version.
func TestActiveItemsAreOpenedAndNotClosed(t *testing.T) {
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

// appendTo appends one record of each body to an item, in order, numbered from
// the item's history.
func appendTo(t *testing.T, records *memory.Records, id item.ID, bodies ...item.Body) error {
	t.Helper()
	for i := range bodies {
		_, err := records.Append(t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
			return []item.Draft{{ID: fmt.Sprintf("%s-%d", id, len(history)+1), Item: id, Incident: "inc", Body: body(bodies[i])}}, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// The bodies of an item's opening, its entry into awaiting_close not
// staffed, a person's close and a reopen.
var (
	opening   = item.Body{Opened: &item.Opened{Type: "incident"}}
	awaiting  = item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}}
	closing   = item.Body{Closed: &item.Closed{By: "admin", Request: &item.Request{Key: "sha256:k1", Digest: "sha256:d1"}}}
	reopening = item.Body{Reopened: &item.Reopened{By: "admin", Request: item.Request{Key: "sha256:k2", Digest: "sha256:d2"},
		AwaitingClose: item.AwaitingClose{Reason: item.StopNotStaffed}}}
)

// Active lists the items that are not closed, by id, as the Postgres store
// lists them (Decision 12; R16): an item opened, one awaiting its close, and
// one closed and reopened; not one closed, nor one closed again after a
// reopen, whose close no reopen of a later record follows.
func TestActiveItemsAreThoseNotClosed(t *testing.T) {
	records := memory.NewRecords(time.Now)
	require.NoError(t, appendTo(t, records, "item-opened", opening))
	require.NoError(t, appendTo(t, records, "item-awaiting", opening, awaiting))
	require.NoError(t, appendTo(t, records, "item-closed", opening, awaiting, closing))
	require.NoError(t, appendTo(t, records, "item-reopened", opening, awaiting, closing, reopening))
	require.NoError(t, appendTo(t, records, "item-closed-again", opening, awaiting, closing, reopening, closing))

	active, err := records.Active(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []item.ID{"item-awaiting", "item-opened", "item-reopened"}, active)
}

// The store refuses an automatic close that it would stamp before its due,
// with item.ErrNotDue, and writes nothing (R41): one due an hour after the
// store's clock is refused, and the item's records are as they were; one
// whose due has passed is taken; and a person's close, which has no due, is
// taken at any time. (The plan's.)
func TestTheRecordsRefuseAnAutomaticCloseStampedBeforeItsDue(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	records := memory.NewRecords(func() time.Time { return clock })
	for _, id := range []item.ID{"item-auto", "item-person"} {
		require.NoError(t, appendTo(t, records, id, opening, awaiting))
	}
	before, err := records.Load(t.Context(), "item-auto")
	require.NoError(t, err)

	early := appendTo(t, records, "item-auto", item.Body{Closed: &item.Closed{Auto: true, Due: clock.Add(time.Hour)}})

	require.ErrorIs(t, early, item.ErrNotDue)
	after, err := records.Load(t.Context(), "item-auto")
	require.NoError(t, err)
	assert.Equal(t, before, after, "the item's records are as they were")
	require.NoError(t, appendTo(t, records, "item-auto", item.Body{Closed: &item.Closed{Auto: true, Due: clock.Add(-time.Minute)}}), "a close whose due has passed")
	require.NoError(t, appendTo(t, records, "item-person", closing), "a person's close")
}

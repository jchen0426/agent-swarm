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

var (
	_ app.Records      = (*memory.Records)(nil)
	_ app.ClosedLister = (*memory.Records)(nil)
)

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

// ClosedSince lists the items whose latest close is within the window and not
// followed by a reopen, by id, as the Postgres store does. Item-old closed an
// hour ago and item-both closed an hour ago and, after a reopen, just now;
// item-new closed just now; item-reopened closed just now and was reopened;
// item-open never closed. A window of a minute finds item-both and item-new.
func TestTheMemoryStoresClosedSinceMatchesPostgres(t *testing.T) {
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	records := memory.NewRecords(func() time.Time { return now })
	add := func(id item.ID, b item.Body) {
		_, err := records.Append(t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
			return []item.Draft{{ID: fmt.Sprintf("%s-%d", id, len(history)), Item: id, Incident: "inc", Body: body(b)}}, nil
		})
		require.NoError(t, err)
	}
	closed := item.Body{Closed: &item.Closed{By: "admin"}}
	reopened := item.Body{Reopened: &item.Reopened{By: "admin"}}
	opened := item.Body{Opened: &item.Opened{Type: "incident"}}
	for _, id := range []item.ID{"item-old", "item-both", "item-new", "item-reopened", "item-open"} {
		add(id, opened)
	}
	add("item-old", closed)
	add("item-both", closed)
	now = now.Add(time.Hour)
	add("item-both", reopened)
	add("item-both", closed)
	add("item-new", closed)
	add("item-reopened", closed)
	add("item-reopened", reopened)

	found, err := records.ClosedSince(t.Context(), time.Minute)
	require.NoError(t, err)
	all, err := records.ClosedSince(t.Context(), 2*time.Hour)
	require.NoError(t, err)

	assert.Equal(t, []item.ID{"item-both", "item-new"}, found)
	assert.Equal(t, []item.ID{"item-both", "item-new", "item-old"}, all)
}

var _ app.Incidents = (*memory.Records)(nil)

// appendIn appends to an item one record of each body, each in the incident
// that its pair names, in order, as the Postgres twin does.
func appendIn(t *testing.T, records *memory.Records, id item.ID, in ...struct {
	incident item.IncidentID
	body     item.Body
},
) {
	t.Helper()
	for i := range in {
		_, err := records.Append(t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
			return []item.Draft{{ID: fmt.Sprintf("%s-%d", id, len(history)+1), Item: id, Incident: in[i].incident, Body: body(in[i].body)}}, nil
		})
		require.NoError(t, err)
	}
}

// The items of an incident are those whose latest record is in it, each once,
// sorted (Decision 18), as the Postgres store reads them: item-a opened in
// inc-1; item-c opened in inc-2 and linked into inc-1; item-e moved out of
// inc-1 and back; item-b unlinked out of inc-1 into inc-3; item-d recorded in
// inc-2 alone. An incident with no item has none: an empty list, not nil, as
// the Postgres store reads it, so that no view of it gives null.
func TestTheItemsOfAnIncident(t *testing.T) {
	records := memory.NewRecords(time.Now)
	type in = struct {
		incident item.IncidentID
		body     item.Body
	}
	linked := func(left item.IncidentID) item.Body {
		return item.Body{Linked: &item.Linked{Left: left, With: "item-a", By: "admin", Reason: &board.Reason{Code: "duplicate"}}}
	}
	unlinked := item.Body{Unlinked: &item.Unlinked{Left: "inc-1", By: "admin", Reason: &board.Reason{Code: "not_related"}}}
	appendIn(t, records, "item-c", in{"inc-2", opening}, in{"inc-1", linked("inc-2")})
	appendIn(t, records, "item-a", in{"inc-1", opening})
	appendIn(t, records, "item-e", in{"inc-1", opening}, in{"inc-2", linked("inc-1")}, in{"inc-1", linked("inc-2")})
	appendIn(t, records, "item-b", in{"inc-1", opening}, in{"inc-3", unlinked})
	appendIn(t, records, "item-d", in{"inc-2", opening}, in{"inc-2", awaiting})

	for incident, want := range map[item.IncidentID][]item.ID{
		"inc-1": {"item-a", "item-c", "item-e"}, "inc-2": {"item-d"}, "inc-3": {"item-b"}, "inc-none": {},
	} {
		got, err := records.IncidentItems(t.Context(), incident)

		require.NoError(t, err, incident)
		assert.Equal(t, want, got, incident)
	}
}

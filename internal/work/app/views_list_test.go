package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// viewsWith makes the views of the harness's items as viewsOf does, as change
// edits their ports.
func viewsWith(h *harness, change func(p *app.ViewPorts)) *app.Views {
	p := viewPorts(h)
	change(&p)
	return app.NewViews(h.records, h.boards, &p)
}

// rowsByID indexes rows by their items' ids, and fails on an id twice.
func rowsByID(t *testing.T, rows []app.Row) map[item.ID]app.Row {
	t.Helper()
	out := map[item.ID]app.Row{}
	for i := range rows {
		_, twice := out[rows[i].ID]
		require.False(t, twice, "%s is listed twice", rows[i].ID)
		out[rows[i].ID] = rows[i]
	}
	return out
}

// The list holds the items that are not closed and the items closed within
// the reopen window, each once: an item closed and reopened is listed once, as
// the item it is now; one closed longer ago than the window is not listed.
func TestListShowsActiveAndRecentlyClosedItemsOnce(t *testing.T) {
	h := newHarness(t)
	old := awaitingItsClose(h, "old close")
	_, err := h.c.Close(t.Context(), old, closing("close-old"))
	require.NoError(t, err)
	h.clock.Advance(h.opts.ReopenWindow + time.Hour)
	recent := awaitingItsClose(h, "recent close")
	_, err = h.c.Close(t.Context(), recent, closing("close-recent"))
	require.NoError(t, err)
	again := awaitingItsClose(h, "closed and reopened")
	_, err = h.c.Close(t.Context(), again, closing("close-again"))
	require.NoError(t, err)
	_, err = h.c.Reopen(t.Context(), again, reopening("reopen-again"))
	require.NoError(t, err)
	active, err := h.c.Open(t.Context(), &app.OpenInput{
		Actor: administrator, Type: "incident", Title: "still open", Source: "person", Routing: map[string]string{"service": "payments"},
		Principal: "alice", Responders: []string{"bob"},
	})
	require.NoError(t, err)

	rows, err := viewsOf(h).List(t.Context())

	require.NoError(t, err)
	byID := rowsByID(t, rows)
	assert.Len(t, rows, 3, "the item closed outside the window is not listed")
	assert.NotContains(t, byID, old)
	s := h.state(active)
	assert.Equal(t, app.Row{
		ID: active, Title: "still open", Service: "payments", Principal: s.Opened.Principal, Responders: []string{"bob"},
		State: "investigating", PositionKnown: true, Incident: s.Incident,
	}, byID[active])
	closedAt := h.state(recent).ClosedAt
	r := byID[recent]
	assert.Equal(t, []any{"closed", closedAt, closedAt.Add(h.opts.ReopenWindow), "checkout"},
		[]any{r.State, r.ClosedAt, r.ReopenableUntil, r.Service})
	assert.Equal(t, "awaiting_close", byID[again].State, "reopened, so listed as it is now")
	assert.Zero(t, byID[again].ClosedAt)
	assert.Equal(t, h.state(again).AwaitingSince.Add(closesAfter), byID[again].ClosesAt)
}

// switchReads counts the reads of the kill switch's table.
type switchReads struct {
	killSwitchReads
	n atomic.Int32
}

func (s *switchReads) KillSwitch(ctx context.Context) (app.KillSwitch, error) {
	s.n.Add(1)
	return s.killSwitchReads.KillSwitch(ctx)
}

// The list folds each item's records alone: it reads no board, and the kill
// switch's table once for the list, not once per row. The views here have no
// boards, so that a read of one would panic.
func TestListsLightFoldReadsNoBoardAndNoSwitchPerRow(t *testing.T) {
	h := newHarness(t)
	for _, title := range []string{"one", "two", "three"} {
		awaitingItsClose(h, title)
	}
	h.open("four")
	table := &switchReads{killSwitchReads: killSwitchReads{h.controls}}
	p := viewPorts(h)
	p.Controls = table
	views := app.NewViews(h.records, nil, &p)

	rows, err := views.List(t.Context())

	require.NoError(t, err)
	assert.Len(t, rows, 4)
	assert.Equal(t, int32(1), table.n.Load(), "the table is read once for the list")
	for i := range rows {
		if rows[i].State == "awaiting_close" {
			assert.False(t, rows[i].ClosesAt.IsZero(), rows[i].ID)
			assert.False(t, rows[i].AutoClosePaused || rows[i].AutoCloseUnread || rows[i].ClosesAtUnread, rows[i].ID)
		}
	}
}

// With the kill switch on, the rows of items awaiting their close say their
// auto-close is paused and give no time; with its table unreadable they say
// the table is unread, not that the delay is: the table is tried once either
// way.
func TestListsRowsShowThePausedAndTheUnreadSwitch(t *testing.T) {
	h := newHarness(t)
	one, two := awaitingItsClose(h, "one"), awaitingItsClose(h, "two")
	table := &switchReads{killSwitchReads: killSwitchReads{h.controls}}
	p := viewPorts(h)
	p.Controls = table
	views := app.NewViews(h.records, h.boards, &p)

	writeRows(t, h, true)
	paused, err := views.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int32(1), table.n.Load())
	h.controls.failReads(true)
	table.n.Store(0)
	unread, err := views.List(t.Context())
	require.NoError(t, err)

	assert.Equal(t, int32(1), table.n.Load(), "tried once")
	for _, id := range []item.ID{one, two} {
		p, u := rowsByID(t, paused)[id], rowsByID(t, unread)[id]
		assert.Equal(t, []any{true, false, false, true}, []any{p.AutoClosePaused, p.AutoCloseUnread, p.ClosesAtUnread, p.ClosesAt.IsZero()}, id)
		assert.Equal(t, []any{false, true, false, true}, []any{u.AutoClosePaused, u.AutoCloseUnread, u.ClosesAtUnread, u.ClosesAt.IsZero()}, id)
	}
}

// An item whose records are invalid is a row that says it is unreadable, and
// the other items are listed; the failure is logged once for the item. A list
// whose read fails as a whole fails.
func TestListMarksACorruptItemAndListsTheRest(t *testing.T) {
	h := newHarness(t)
	good, bad := h.open("good"), h.open("bad")
	h.records.loads(func(records []item.Record) error {
		if len(records) > 0 && records[0].Item == bad {
			return fmt.Errorf("decode: %w", item.ErrInvalid)
		}
		return nil
	})
	var logged bytes.Buffer
	views := viewsWith(h, func(p *app.ViewPorts) {
		p.Log = slog.New(slog.NewTextHandler(&logged, nil))
	})

	rows, err := views.List(t.Context())
	require.NoError(t, err)
	_, err = views.List(t.Context())
	require.NoError(t, err)

	byID := rowsByID(t, rows)
	assert.Equal(t, app.Row{ID: bad, Unreadable: true}, byID[bad])
	assert.Equal(t, "good", byID[good].Title)
	assert.Equal(t, 1, strings.Count(logged.String(), "records are invalid"), logged.String())

	h.records.loads(func([]item.Record) error { return errors.New("database down") })
	_, err = views.List(t.Context())
	require.Error(t, err)
}

// A standby that still has a snapshot, as an instance that has just lost the
// lease has until it is fenced, shows no position as known.
func TestAnInstanceThatIsNotHeldShowsPositionNotKnownWithASnapshot(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	waiting := openLater(h, "payments fail")
	require.Equal(t, []item.ID{waiting}, h.c.Lifecycle().Waiting)

	rows, err := viewsWith(h, func(p *app.ViewPorts) { p.Held = func() bool { return false } }).List(t.Context())

	require.NoError(t, err)
	require.Len(t, rows, 2)
	for i := range rows {
		assert.False(t, rows[i].PositionKnown, rows[i].ID)
		assert.Zero(t, rows[i].QueuePosition, rows[i].ID)
	}
}

// A standby's rows say that their queue position is not known, since its
// snapshot is empty; a leader's rows say it is known, for the item promoted
// or admitted and not yet started too, whose position is 0.
func TestAStandbysRowsShowPositionNotKnown(t *testing.T) {
	h := newHarness(t, limit(1))
	running := h.open("checkout fails")
	unstarted := admittedUnstartedAfter(h, running)
	waiting := openLater(h, "payments fail")

	leader, err := viewsOf(h).List(t.Context())
	require.NoError(t, err)
	byID := rowsByID(t, leader)
	for id := range byID {
		assert.True(t, byID[id].PositionKnown, id)
	}
	assert.Equal(t, 0, byID[unstarted].QueuePosition, "admitted, not waiting")
	assert.Equal(t, "queued", byID[unstarted].State)
	assert.Equal(t, 1, byID[waiting].QueuePosition)

	h.crash()
	standby, err := viewsWith(h, func(p *app.ViewPorts) { p.Held = func() bool { return false } }).List(t.Context())
	require.NoError(t, err)
	assert.Len(t, standby, 3)
	for i := range standby {
		assert.False(t, standby[i].PositionKnown, standby[i].ID)
		assert.Zero(t, standby[i].QueuePosition, standby[i].ID)
	}
}

// admittedUnstartedAfter has the walk admit the oldest waiting item once first
// stops, with its start refused: queued, admitted, counted, not waiting.
func admittedUnstartedAfter(h *harness, first item.ID) item.ID {
	h.t.Helper()
	q := openLater(h, "admitted soon")
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if id == q && startOf(drafts) {
			return errStartRefused
		}
		return nil
	})
	stopQuiet(h, first)
	return q
}

// An auto-close delay that cannot be read leaves the item's closing time out
// and says so, in its view and in its row, and the read is logged once for the
// item, not once per view (I15).
func TestAnUnreadAutoCloseDelayIsFlaggedAndLoggedOncePerItem(t *testing.T) {
	h := newHarness(t)
	zero := h.open("payments fail")
	_, err := h.c.Close(t.Context(), zero, closing("close-1"))
	require.NoError(t, err)
	loseCommit(h, commit)
	_, err = h.c.Reopen(t.Context(), zero, reopening("reopen-1"))
	require.NoError(t, err)
	require.Zero(t, h.state(zero).Closing.AutoClose)
	var logged bytes.Buffer
	views := viewsWith(h, func(p *app.ViewPorts) {
		p.Log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})

	view, err := views.Item(t.Context(), zero)
	require.NoError(t, err)
	_, err = views.Item(t.Context(), zero)
	require.NoError(t, err)
	rows, err := views.List(t.Context())
	require.NoError(t, err)
	_, err = views.List(t.Context())
	require.NoError(t, err)

	assert.True(t, view.ClosesAtUnread)
	assert.Zero(t, view.ClosesAt)
	assert.False(t, view.AutoCloseUnread, "the table was read")
	require.Len(t, rows, 1)
	assert.True(t, rows[0].ClosesAtUnread)
	assert.Zero(t, rows[0].ClosesAt)
	assert.Equal(t, 1, strings.Count(logged.String(), "auto-close delay could not be read"), logged.String())
}

// An item's view carries the run's stop entry, read from the board the view
// replays: nil while the run goes on, and for an item with no run.
func TestTheItemViewCarriesTheStopEntry(t *testing.T) {
	h := newHarness(t)
	stopped := awaitingItsClose(h, "checkout fails")
	running := h.open("payments fail")
	views := viewsOf(h)

	view, err := views.Item(t.Context(), stopped)
	require.NoError(t, err)
	live, err := views.Item(t.Context(), running)
	require.NoError(t, err)

	var want *board.Stop
	for _, e := range h.events(h.state(stopped).Run.ID) {
		if e.Body.Stop != nil {
			want = e.Body.Stop
		}
	}
	require.NotNil(t, want)
	require.NotNil(t, view.StopEntry)
	assert.Equal(t, want, view.StopEntry)
	assert.Equal(t, view.Stop, view.StopEntry.Reason)
	assert.Nil(t, live.StopEntry)
}
